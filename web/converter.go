package main

import (
	"bytes"
	"context"
	"duanjuapp/native/core"
	"encoding/binary"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"
)

type conversionWorker struct {
	wake    chan struct{}
	diary   *webDiary
	mu      sync.Mutex
	skipped map[string]time.Time
}

func newConversionWorker(diary *webDiary) *conversionWorker {
	return &conversionWorker{
		wake:    make(chan struct{}, 1),
		diary:   diary,
		skipped: map[string]time.Time{},
	}
}

func (worker *conversionWorker) trigger() {
	select {
	case worker.wake <- struct{}{}:
	default:
	}
}

func (worker *conversionWorker) run(ctx context.Context) {
	ticker := time.NewTicker(1500 * time.Millisecond)
	defer ticker.Stop()
	for {
		worker.processPending(ctx)
		select {
		case <-ctx.Done():
			return
		case <-worker.wake:
		case <-ticker.C:
		}
	}
}

type rawJobItem struct {
	ID      string `json:"id"`
	Index   int    `json:"index"`
	State   string `json:"state"`
	Created int64  `json:"created"`
	Drama   struct {
		Title string `json:"title"`
	} `json:"drama"`
}

func (worker *conversionWorker) processPending(ctx context.Context) {
	for ctx.Err() == nil {
		respRaw := core.NativeRequest(`{"action":"downloads"}`)
		var env struct {
			OK   bool `json:"ok"`
			Data struct {
				Jobs []rawJobItem `json:"jobs"`
			} `json:"data"`
		}
		if json.Unmarshal([]byte(respRaw), &env) != nil || !env.OK {
			return
		}

		worker.mu.Lock()
		for id, until := range worker.skipped {
			if time.Now().After(until) {
				delete(worker.skipped, id)
			}
		}
		var chosen *rawJobItem
		for i := range env.Data.Jobs {
			job := &env.Data.Jobs[i]
			if job.State != "converting" {
				continue
			}
			if _, skip := worker.skipped[job.ID]; skip {
				continue
			}
			if chosen == nil || job.Created < chosen.Created || (job.Created == chosen.Created && job.Index < chosen.Index) {
				chosen = job
			}
		}
		worker.mu.Unlock()

		if chosen == nil {
			return
		}
		worker.convertSingle(ctx, *chosen)
	}
}

func findFFmpegBinary() string {
	if custom := strings.TrimSpace(os.Getenv("FFMPEG_PATH")); custom != "" {
		if info, err := os.Stat(custom); err == nil && !info.IsDir() {
			return custom
		}
	}
	if found, err := exec.LookPath("ffmpeg"); err == nil {
		return found
	}
	return ""
}

func runFFmpegConversion(ctx context.Context, ffmpegPath, inputPath, keyHex, outputPath string) error {
	var args []string
	args = append(args, "-v", "error", "-y")
	if keyHex != "" {
		args = append(args, "-decryption_key", keyHex)
	}
	if strings.EqualFold(filepath.Ext(inputPath), ".m3u8") {
		args = append(args, "-allowed_extensions", "ALL", "-extension_picky", "0")
	}
	args = append(args, "-i", inputPath, "-map", "0:v:0", "-map", "0:a:0?", "-c", "copy", "-movflags", "+faststart", outputPath)
	cmdCtx, cancel := context.WithTimeout(ctx, 10*time.Minute)
	defer cancel()
	cmd := exec.CommandContext(cmdCtx, ffmpegPath, args...)
	out, err := cmd.CombinedOutput()
	if err != nil {
		msg := strings.TrimSpace(string(out))
		if msg != "" {
			return fmt.Errorf("%w: %s", err, msg)
		}
		return err
	}
	return nil
}

func (worker *conversionWorker) convertSingle(ctx context.Context, job rawJobItem) {
	reqBytes, _ := json.Marshal(map[string]any{"action": "beginConversion", "jobId": job.ID})
	var beginEnv struct {
		OK   bool `json:"ok"`
		Data struct {
			Input  string `json:"input"`
			Key    string `json:"key"`
			Output string `json:"output"`
		} `json:"data"`
	}
	if json.Unmarshal([]byte(core.NativeRequest(string(reqBytes))), &beginEnv) != nil || !beginEnv.OK {
		worker.mu.Lock()
		worker.skipped[job.ID] = time.Now().Add(8 * time.Second)
		worker.mu.Unlock()
		return
	}

	inputPath := beginEnv.Data.Input
	keyHex := strings.TrimSpace(beginEnv.Data.Key)
	outputPath := beginEnv.Data.Output

	result := "failed"
	var convErr error
	ext := strings.ToLower(filepath.Ext(inputPath))
	ffmpeg := findFFmpegBinary()

	switch {
	case ext == ".mp4" && keyHex != "":
		convErr = decryptCENCFile(inputPath, keyHex, outputPath)
		if convErr != nil && ffmpeg != "" {
			convErr = runFFmpegConversion(ctx, ffmpeg, inputPath, keyHex, outputPath)
		}
	case ext == ".mp4":
		if ffmpeg != "" {
			convErr = runFFmpegConversion(ctx, ffmpeg, inputPath, "", outputPath)
		}
		if ffmpeg == "" || convErr != nil {
			convErr = copyPlainFile(inputPath, outputPath)
		}
	case ext == ".m3u8":
		if ffmpeg != "" {
			convErr = runFFmpegConversion(ctx, ffmpeg, inputPath, keyHex, outputPath)
		} else {
			convErr = convertLocalHLSToMP4(inputPath, outputPath)
		}
	default:
		if ffmpeg != "" {
			convErr = runFFmpegConversion(ctx, ffmpeg, inputPath, keyHex, outputPath)
		} else {
			convErr = copyPlainFile(inputPath, outputPath)
		}
	}

	if ctx.Err() != nil {
		result = "cancel"
	} else if convErr == nil {
		if info, statErr := os.Stat(outputPath); statErr == nil && info.Size() > 0 {
			result = "done"
		} else {
			convErr = errors.New("输出文件为空")
		}
	}

	if result == "failed" && convErr != nil && worker.diary != nil {
		worker.diary.add(fmt.Sprintf("[下载] %s 第 %d 集转换失败：%v", job.Drama.Title, job.Index, convErr))
	}

	finBytes, _ := json.Marshal(map[string]any{
		"action":  "finishConversion",
		"jobId":   job.ID,
		"command": result,
	})
	_ = core.NativeRequest(string(finBytes))
}

type mp4VideoSignature struct {
	codec  string
	width  uint16
	height uint16
}

func inspectMP4Signature(filePath string) (mp4VideoSignature, error) {
	f, err := os.Open(filePath)
	if err != nil {
		return mp4VideoSignature{}, err
	}
	defer f.Close()
	info, err := f.Stat()
	if err != nil || info.Size() < 32 {
		return mp4VideoSignature{}, errors.New("视频文件无效")
	}

	readBoxAt := func(offset int64, maxLen int64) ([]byte, error) {
		if offset < 0 || offset >= info.Size() {
			return nil, io.EOF
		}
		sz := min(maxLen, info.Size()-offset)
		buf := make([]byte, sz)
		n, rErr := f.ReadAt(buf, offset)
		if n == 0 && rErr != nil {
			return nil, rErr
		}
		return buf[:n], nil
	}

	var cursor int64
	for step := 0; step < 32 && cursor+8 <= info.Size(); step++ {
		hdr, err := readBoxAt(cursor, 16)
		if err != nil || len(hdr) < 8 {
			break
		}
		sz32 := binary.BigEndian.Uint32(hdr[0:4])
		bType := string(hdr[4:8])
		var boxSize int64
		switch sz32 {
		case 1:
			if len(hdr) < 16 {
				return mp4VideoSignature{}, errors.New("视频头损坏")
			}
			boxSize = int64(binary.BigEndian.Uint64(hdr[8:16]))
		case 0:
			boxSize = info.Size() - cursor
		default:
			boxSize = int64(sz32)
		}
		if boxSize < 8 {
			break
		}
		if bType == "moov" && boxSize <= 32<<20 {
			moov, mErr := readBoxAt(cursor, boxSize)
			if mErr != nil {
				return mp4VideoSignature{}, mErr
			}
			for _, trak := range parseMP4Boxes(moov, 8, len(moov)) {
				if trak.boxType != "trak" {
					continue
				}
				for _, mdia := range parseMP4Boxes(moov, trak.payloadStart(), trak.end) {
					if mdia.boxType != "mdia" {
						continue
					}
					for _, minf := range parseMP4Boxes(moov, mdia.payloadStart(), mdia.end) {
						if minf.boxType != "minf" {
							continue
						}
						for _, stbl := range parseMP4Boxes(moov, minf.payloadStart(), minf.end) {
							if stbl.boxType != "stbl" {
								continue
							}
							for _, stsd := range parseMP4Boxes(moov, stbl.payloadStart(), stbl.end) {
								if stsd.boxType != "stsd" || stsd.payloadStart()+8 > stsd.end {
									continue
								}
								entries := parseMP4Boxes(moov, stsd.payloadStart()+8, stsd.end)
								for _, entry := range entries {
									switch entry.boxType {
									case "avc1", "avc3", "hvc1", "hev1", "mp4v", "vp09", "av01":
										if entry.payloadStart()+28 <= entry.end {
											w := binary.BigEndian.Uint16(moov[entry.payloadStart()+24 : entry.payloadStart()+26])
											h := binary.BigEndian.Uint16(moov[entry.payloadStart()+26 : entry.payloadStart()+28])
											return mp4VideoSignature{codec: entry.boxType, width: w, height: h}, nil
										}
									}
								}
							}
						}
					}
				}
			}
		}
		cursor += boxSize
	}
	return mp4VideoSignature{}, errors.New("无法识别视频编码格式")
}

type mergeInputEpisode struct {
	episode int
	title   string
	source  string
	path    string
}

func mergeEpisodesToTempMP4(ctx context.Context, tempDir string, items []mergeInputEpisode) (string, string, error) {
	if len(items) < 2 {
		return "", "", errors.New("请至少选择 2 集进行合并")
	}
	sort.Slice(items, func(i, j int) bool {
		return items[i].episode < items[j].episode
	})

	baseSig, err := inspectMP4Signature(items[0].path)
	if err != nil {
		return "", "", fmt.Errorf("无法读取第 %d 集视频信息：%w", items[0].episode, err)
	}
	var mismatched []string
	for _, item := range items[1:] {
		sig, sigErr := inspectMP4Signature(item.path)
		if sigErr != nil || sig != baseSig {
			mismatched = append(mismatched, fmt.Sprintf("第%d集", item.episode))
		}
	}
	if len(mismatched) > 0 {
		return "", "", fmt.Errorf("所选分集的分辨率或编码与第 %d 集不一致（%s），无法无损合并", items[0].episode, strings.Join(mismatched, "、"))
	}

	ffmpeg := findFFmpegBinary()
	if ffmpeg == "" {
		return "", "", errors.New("无损合并多集视频需要服务端环境安装 FFmpeg（当前环境未检测到 ffmpeg）")
	}

	if err := os.MkdirAll(tempDir, 0700); err != nil {
		return "", "", errors.New("无法创建合并临时目录")
	}
	listFile, err := os.CreateTemp(tempDir, "concat-*.txt")
	if err != nil {
		return "", "", errors.New("无法创建合并列表")
	}
	listPath := listFile.Name()
	defer os.Remove(listPath)

	var listBuf bytes.Buffer
	for _, item := range items {
		escaped := strings.ReplaceAll(filepath.ToSlash(item.path), "'", "'\\''")
		listBuf.WriteString("file '")
		listBuf.WriteString(escaped)
		listBuf.WriteString("'\n")
	}
	if _, err := listFile.Write(listBuf.Bytes()); err != nil {
		_ = listFile.Close()
		return "", "", err
	}
	_ = listFile.Close()

	outFile, err := os.CreateTemp(tempDir, "merged-*.mp4")
	if err != nil {
		return "", "", err
	}
	outPath := outFile.Name()
	_ = outFile.Close()

	cmdCtx, cancel := context.WithTimeout(ctx, 15*time.Minute)
	defer cancel()
	cmd := exec.CommandContext(cmdCtx, ffmpeg,
		"-v", "error", "-y",
		"-f", "concat", "-safe", "0",
		"-i", listPath,
		"-c", "copy",
		"-movflags", "+faststart",
		outPath,
	)
	out, runErr := cmd.CombinedOutput()
	if runErr != nil {
		_ = os.Remove(outPath)
		msg := strings.TrimSpace(string(out))
		if msg != "" {
			return "", "", fmt.Errorf("合并失败：%s", msg)
		}
		return "", "", errors.New("合并视频失败，请重试")
	}
	fileName := fmt.Sprintf("%s-第%d-%d集.mp4", sanitizeAttachmentName(items[0].title), items[0].episode, items[len(items)-1].episode)
	return outPath, fileName, nil
}

func sanitizeAttachmentName(title string) string {
	runes := []rune(strings.TrimSpace(title))
	for i, r := range runes {
		if r < 0x20 || r == 0x7f || strings.ContainsRune(`<>:"/\|?*`, r) {
			runes[i] = '_'
		}
	}
	if len(runes) == 0 {
		return "短剧"
	}
	if len(runes) > 60 {
		runes = runes[:60]
	}
	return string(runes)
}
