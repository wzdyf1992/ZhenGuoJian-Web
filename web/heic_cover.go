package main

import (
	"bytes"
	"context"
	"crypto/rand"
	"crypto/sha256"
	"duanjuapp/native/core"
	"encoding/binary"
	"encoding/hex"
	"errors"
	"fmt"
	"image/jpeg"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"
)

type heicConvertCall struct {
	done chan struct{}
	path string
	err  error
}

type heicUploadGrant struct {
	compatKey string
	expiresAt time.Time
}

type heicCompatManager struct {
	dir       string
	slots     chan struct{}
	mu        sync.Mutex
	inflight  map[string]*heicConvertCall
	grants    map[string]heicUploadGrant
	pruning   bool
	lastPrune time.Time
}

func newHEICCompatManager(dataDir string) *heicCompatManager {
	return &heicCompatManager{
		dir:      filepath.Join(dataDir, "covers-v1", "compatible-web-v1"),
		slots:    make(chan struct{}, 3),
		inflight: map[string]*heicConvertCall{},
		grants:   map[string]heicUploadGrant{},
	}
}

func (m *heicCompatManager) compatKeyForFile(origPath string) (string, error) {
	info, err := os.Stat(origPath)
	if err != nil || !info.Mode().IsRegular() || info.Size() <= 0 {
		return "", errors.New("海报缓存不存在")
	}
	raw := fmt.Sprintf("%s\x00%d\x00%d", filepath.Clean(origPath), info.Size(), info.ModTime().UnixNano())
	sum := sha256.Sum256([]byte(raw))
	return hex.EncodeToString(sum[:16]), nil
}

func (m *heicCompatManager) targetFile(compatKey string) string {
	return filepath.Join(m.dir, compatKey+".jpg")
}

func isValidJPEGFile(filePath string) bool {
	f, err := os.Open(filePath)
	if err != nil {
		return false
	}
	defer f.Close()
	info, err := f.Stat()
	if err != nil || !info.Mode().IsRegular() || info.Size() < 16 || info.Size() > 4<<20 {
		return false
	}
	var hdr [3]byte
	if n, rErr := f.Read(hdr[:]); rErr != nil || n != 3 {
		return false
	}
	return hdr[0] == 0xff && hdr[1] == 0xd8 && hdr[2] == 0xff
}

func (m *heicCompatManager) lookup(compatKey string) (string, bool) {
	if compatKey == "" {
		return "", false
	}
	p := m.targetFile(compatKey)
	if !isValidJPEGFile(p) {
		return "", false
	}
	now := time.Now()
	_ = os.Chtimes(p, now, now)
	return p, true
}

func (m *heicCompatManager) issueUploadToken(compatKey string) string {
	if compatKey == "" {
		return ""
	}
	var buf [16]byte
	if _, err := rand.Read(buf[:]); err != nil {
		return ""
	}
	tok := hex.EncodeToString(buf[:])
	now := time.Now()
	m.mu.Lock()
	for k, g := range m.grants {
		if now.After(g.expiresAt) {
			delete(m.grants, k)
		}
	}
	if len(m.grants) >= 512 {
		for k := range m.grants {
			delete(m.grants, k)
			break
		}
	}
	m.grants[tok] = heicUploadGrant{
		compatKey: compatKey,
		expiresAt: now.Add(15 * time.Minute),
	}
	m.mu.Unlock()
	return tok
}

func (m *heicCompatManager) saveUploadedJPEG(token string, data []byte) (string, error) {
	if token == "" || len(data) < 16 || len(data) > 4<<20 {
		return "", errors.New("无效的海报数据")
	}
	m.mu.Lock()
	grant, ok := m.grants[token]
	if ok && time.Now().After(grant.expiresAt) {
		delete(m.grants, token)
		ok = false
	}
	if ok {
		delete(m.grants, token)
	}
	m.mu.Unlock()
	if !ok || grant.compatKey == "" {
		return "", errors.New("海报缓存凭证无效或已过期")
	}
	cfg, err := jpeg.DecodeConfig(bytes.NewReader(data))
	if err != nil || cfg.Width <= 0 || cfg.Height <= 0 || cfg.Width > 4096 || cfg.Height > 4096 {
		return "", errors.New("无效的 JPEG 图片格式")
	}
	if err := os.MkdirAll(m.dir, 0700); err != nil {
		return "", err
	}
	dst := m.targetFile(grant.compatKey)
	tmp, err := os.CreateTemp(m.dir, ".upload-*.part")
	if err != nil {
		return "", err
	}
	tmpName := tmp.Name()
	_, wErr := tmp.Write(data)
	cErr := tmp.Close()
	if wErr != nil || cErr != nil {
		_ = os.Remove(tmpName)
		return "", errors.New("保存兼容海报失败")
	}
	if err := os.Rename(tmpName, dst); err != nil {
		_ = os.Remove(dst)
		if rErr := os.Rename(tmpName, dst); rErr != nil {
			_ = os.Remove(tmpName)
			return "", rErr
		}
	}
	m.schedulePrune(dst)
	return dst, nil
}

func (m *heicCompatManager) convertWithFFmpeg(ctx context.Context, ffmpegBin string, rawDrama []byte, compatKey string) (string, error) {
	if existing, ok := m.lookup(compatKey); ok {
		return existing, nil
	}
	m.mu.Lock()
	call := m.inflight[compatKey]
	if call == nil {
		if len(m.inflight) >= 96 {
			m.mu.Unlock()
			return "", errors.New("海报正在转换，请稍后重试")
		}
		call = &heicConvertCall{done: make(chan struct{})}
		m.inflight[compatKey] = call
		go m.runFFmpegConvert(ffmpegBin, rawDrama, compatKey, call)
	}
	m.mu.Unlock()

	select {
	case <-ctx.Done():
		return "", ctx.Err()
	case <-call.done:
		return call.path, call.err
	}
}

func (m *heicCompatManager) runFFmpegConvert(ffmpegBin string, rawDrama []byte, compatKey string, call *heicConvertCall) {
	workCtx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()

	var outPath string
	var convErr error

	select {
	case m.slots <- struct{}{}:
		outPath, convErr = m.execFFmpeg(workCtx, ffmpegBin, rawDrama, compatKey)
		<-m.slots
	case <-workCtx.Done():
		convErr = workCtx.Err()
	}

	m.mu.Lock()
	call.path = outPath
	call.err = convErr
	delete(m.inflight, compatKey)
	close(call.done)
	m.mu.Unlock()
}

func (m *heicCompatManager) execFFmpeg(ctx context.Context, ffmpegBin string, rawDrama []byte, compatKey string) (string, error) {
	origPath, hevcInput, orientFilters, isHEIC, err := core.PrepareHEICCover(ctx, rawDrama)
	if err != nil {
		return "", err
	}
	if hevcInput != "" && hevcInput != origPath {
		defer os.Remove(hevcInput)
	}
	if !isHEIC {
		return origPath, nil
	}
	if hevcInput == "" {
		return "", errors.New("HEIC 海报提取失败")
	}
	if err := os.MkdirAll(m.dir, 0700); err != nil {
		return "", err
	}
	dst := m.targetFile(compatKey)
	tmpFile, err := os.CreateTemp(m.dir, ".ffconv-*.part")
	if err != nil {
		return "", err
	}
	partPath := tmpFile.Name()
	_ = tmpFile.Close()
	defer os.Remove(partPath)

	vfParts := []string{}
	if strings.TrimSpace(orientFilters) != "" {
		vfParts = append(vfParts, strings.TrimSpace(orientFilters))
	}
	vfParts = append(vfParts, "scale=w='min(800,iw)':h='min(800,ih)':force_original_aspect_ratio=decrease")

	args := []string{
		"-hide_banner",
		"-loglevel", "error",
		"-nostdin",
		"-y",
		"-max_alloc", "67108864",
		"-threads", "1",
		"-f", "hevc",
		"-i", hevcInput,
		"-frames:v", "1",
		"-an",
		"-sn",
		"-vf", strings.Join(vfParts, ","),
		"-filter_threads", "1",
		"-threads", "1",
		"-c:v", "mjpeg",
		"-pix_fmt", "yuvj420p",
		"-q:v", "3",
		"-f", "image2",
		partPath,
	}
	cmd := exec.CommandContext(ctx, ffmpegBin, args...)
	if err := cmd.Run(); err != nil {
		return "", errors.New("FFmpeg 转换 HEIC 海报失败")
	}
	if !isValidJPEGFile(partPath) {
		return "", errors.New("转换后的海报文件无效")
	}
	if err := os.Rename(partPath, dst); err != nil {
		_ = os.Remove(dst)
		if rErr := os.Rename(partPath, dst); rErr != nil {
			return "", rErr
		}
	}
	m.schedulePrune(dst)
	return dst, nil
}

func (m *heicCompatManager) schedulePrune(keepPath string) {
	m.mu.Lock()
	if m.pruning || time.Since(m.lastPrune) < 30*time.Second {
		m.mu.Unlock()
		return
	}
	m.pruning = true
	m.lastPrune = time.Now()
	m.mu.Unlock()

	go func() {
		defer func() {
			m.mu.Lock()
			m.pruning = false
			m.mu.Unlock()
		}()
		entries, err := os.ReadDir(m.dir)
		if err != nil {
			return
		}
		type item struct {
			path    string
			size    int64
			modTime time.Time
		}
		var jpgs []item
		var total int64
		now := time.Now()
		for _, ent := range entries {
			if ent.IsDir() {
				continue
			}
			full := filepath.Join(m.dir, ent.Name())
			info, sErr := ent.Info()
			if sErr != nil {
				continue
			}
			if strings.HasSuffix(ent.Name(), ".part") {
				if now.Sub(info.ModTime()) > time.Minute {
					_ = os.Remove(full)
				}
				continue
			}
			if strings.HasSuffix(ent.Name(), ".jpg") {
				jpgs = append(jpgs, item{path: full, size: info.Size(), modTime: info.ModTime()})
				total += info.Size()
			}
		}
		if len(jpgs) <= 1000 && total <= 96<<20 {
			return
		}
		sort.Slice(jpgs, func(i, j int) bool {
			return jpgs[i].modTime.Before(jpgs[j].modTime)
		})
		count := len(jpgs)
		for _, it := range jpgs {
			if count <= 1000 && total <= 96<<20 {
				break
			}
			if it.path == keepPath {
				continue
			}
			if err := os.Remove(it.path); err == nil || os.IsNotExist(err) {
				count--
				total -= it.size
			}
		}
	}()
}

func mp4MakeBox(boxType string, payloads ...[]byte) []byte {
	total := 8
	for _, p := range payloads {
		total += len(p)
	}
	out := make([]byte, 8, total)
	binary.BigEndian.PutUint32(out[0:4], uint32(total))
	copy(out[4:8], boxType)
	for _, p := range payloads {
		out = append(out, p...)
	}
	return out
}

func mp4MakeFullBox(boxType string, version byte, flags uint32, payloads ...[]byte) []byte {
	var vf [4]byte
	vf[0] = version
	vf[1] = byte((flags >> 16) & 0xff)
	vf[2] = byte((flags >> 8) & 0xff)
	vf[3] = byte(flags & 0xff)
	all := make([][]byte, 0, len(payloads)+1)
	all = append(all, vf[:])
	all = append(all, payloads...)
	return mp4MakeBox(boxType, all...)
}

func buildSingleFrameHEVCMP4(frame *core.HEICWebFrame) []byte {
	ftypPayload := []byte{
		'i', 's', 'o', 'm',
		0, 0, 2, 0,
		'i', 's', 'o', 'm',
		'i', 's', 'o', '2',
		'm', 'p', '4', '1',
	}
	ftyp := mp4MakeBox("ftyp", ftypPayload)

	buildMoov := func(sampleFileOffset uint32) []byte {
		sampleLen := uint32(len(frame.Sample))
		var mvhdBody [96]byte
		binary.BigEndian.PutUint32(mvhdBody[8:12], 1000)
		binary.BigEndian.PutUint32(mvhdBody[12:16], 200)
		binary.BigEndian.PutUint32(mvhdBody[16:20], 0x00010000)
		binary.BigEndian.PutUint16(mvhdBody[20:22], 0x0100)
		binary.BigEndian.PutUint32(mvhdBody[32:36], 0x00010000)
		binary.BigEndian.PutUint32(mvhdBody[48:52], 0x00010000)
		binary.BigEndian.PutUint32(mvhdBody[64:68], 0x40000000)
		binary.BigEndian.PutUint32(mvhdBody[92:96], 2)
		mvhd := mp4MakeFullBox("mvhd", 0, 0, mvhdBody[:])

		var tkhdBody [80]byte
		binary.BigEndian.PutUint32(tkhdBody[8:12], 1)
		binary.BigEndian.PutUint32(tkhdBody[16:20], 200)
		binary.BigEndian.PutUint32(tkhdBody[36:40], 0x00010000)
		binary.BigEndian.PutUint32(tkhdBody[52:56], 0x00010000)
		binary.BigEndian.PutUint32(tkhdBody[68:72], 0x40000000)
		binary.BigEndian.PutUint32(tkhdBody[72:76], frame.Width<<16)
		binary.BigEndian.PutUint32(tkhdBody[76:80], frame.Height<<16)
		tkhd := mp4MakeFullBox("tkhd", 0, 0x000003, tkhdBody[:])

		var mdhdBody [20]byte
		binary.BigEndian.PutUint32(mdhdBody[8:12], 1000)
		binary.BigEndian.PutUint32(mdhdBody[12:16], 200)
		binary.BigEndian.PutUint16(mdhdBody[16:18], 0x55c4)
		mdhd := mp4MakeFullBox("mdhd", 0, 0, mdhdBody[:])

		hdlrBody := append([]byte{
			0, 0, 0, 0,
			'v', 'i', 'd', 'e',
			0, 0, 0, 0,
			0, 0, 0, 0,
			0, 0, 0, 0,
		}, []byte("VideoHandler\x00")...)
		hdlr := mp4MakeFullBox("hdlr", 0, 0, hdlrBody)

		vmhd := mp4MakeFullBox("vmhd", 0, 1, make([]byte, 8))
		urlBox := mp4MakeFullBox("url ", 0, 1)
		var drefCount [4]byte
		binary.BigEndian.PutUint32(drefCount[:], 1)
		dref := mp4MakeFullBox("dref", 0, 0, drefCount[:], urlBox)
		dinf := mp4MakeBox("dinf", dref)

		hvccBox := mp4MakeBox("hvcC", frame.HVCC)
		var vseHeader [78]byte
		binary.BigEndian.PutUint16(vseHeader[6:8], 1)
		binary.BigEndian.PutUint16(vseHeader[24:26], uint16(frame.Width))
		binary.BigEndian.PutUint16(vseHeader[26:28], uint16(frame.Height))
		binary.BigEndian.PutUint32(vseHeader[28:32], 0x00480000)
		binary.BigEndian.PutUint32(vseHeader[32:36], 0x00480000)
		binary.BigEndian.PutUint16(vseHeader[40:42], 1)
		binary.BigEndian.PutUint16(vseHeader[74:76], 0x0018)
		binary.BigEndian.PutUint16(vseHeader[76:78], 0xffff)
		hvc1Entry := mp4MakeBox("hvc1", vseHeader[:], hvccBox)
		var stsdCount [4]byte
		binary.BigEndian.PutUint32(stsdCount[:], 1)
		stsd := mp4MakeFullBox("stsd", 0, 0, stsdCount[:], hvc1Entry)

		var sttsBody [12]byte
		binary.BigEndian.PutUint32(sttsBody[0:4], 1)
		binary.BigEndian.PutUint32(sttsBody[4:8], 2)
		binary.BigEndian.PutUint32(sttsBody[8:12], 100)
		stts := mp4MakeFullBox("stts", 0, 0, sttsBody[:])

		var stssBody [12]byte
		binary.BigEndian.PutUint32(stssBody[0:4], 2)
		binary.BigEndian.PutUint32(stssBody[4:8], 1)
		binary.BigEndian.PutUint32(stssBody[8:12], 2)
		stss := mp4MakeFullBox("stss", 0, 0, stssBody[:])

		var stscBody [16]byte
		binary.BigEndian.PutUint32(stscBody[0:4], 1)
		binary.BigEndian.PutUint32(stscBody[4:8], 1)
		binary.BigEndian.PutUint32(stscBody[8:12], 1)
		binary.BigEndian.PutUint32(stscBody[12:16], 1)
		stsc := mp4MakeFullBox("stsc", 0, 0, stscBody[:])

		var stszBody [8]byte
		binary.BigEndian.PutUint32(stszBody[0:4], sampleLen)
		binary.BigEndian.PutUint32(stszBody[4:8], 2)
		stsz := mp4MakeFullBox("stsz", 0, 0, stszBody[:])

		var stcoBody [12]byte
		binary.BigEndian.PutUint32(stcoBody[0:4], 2)
		binary.BigEndian.PutUint32(stcoBody[4:8], sampleFileOffset)
		binary.BigEndian.PutUint32(stcoBody[8:12], sampleFileOffset+sampleLen)
		stco := mp4MakeFullBox("stco", 0, 0, stcoBody[:])

		stbl := mp4MakeBox("stbl", stsd, stts, stss, stsc, stsz, stco)
		minf := mp4MakeBox("minf", vmhd, dinf, stbl)
		mdia := mp4MakeBox("mdia", mdhd, hdlr, minf)
		trak := mp4MakeBox("trak", tkhd, mdia)
		return mp4MakeBox("moov", mvhd, trak)
	}

	probeMoov := buildMoov(0)
	mdatPayloadOffset := uint32(len(ftyp) + len(probeMoov) + 8)
	finalMoov := buildMoov(mdatPayloadOffset)
	mdat := mp4MakeBox("mdat", frame.Sample, frame.Sample)

	out := make([]byte, 0, len(ftyp)+len(finalMoov)+len(mdat))
	out = append(out, ftyp...)
	out = append(out, finalMoov...)
	out = append(out, mdat...)
	return out
}
