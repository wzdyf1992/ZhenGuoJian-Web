package core

import (
	"bytes"
	"context"
	"encoding/binary"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"math/bits"
	"os"
	"path/filepath"
	"strings"
)

type HEICWebFrame struct {
	Width      uint32
	Height     uint32
	Transforms []string
	Codec      string
	HVCC       []byte
	Sample     []byte
}

func AllSourcesEnabled() bool {
	return buildAllSources == "true"
}

func StreamServerOrigin() string {
	nativeState.Lock()
	engine := nativeState.engine
	nativeState.Unlock()
	if engine == nil {
		return ""
	}
	engine.mu.Lock()
	defer engine.mu.Unlock()
	if engine.stream == nil {
		return ""
	}
	return engine.stream.address
}

func DiagnosticLogPath() string {
	nativeState.Lock()
	engine := nativeState.engine
	nativeState.Unlock()
	if engine == nil || engine.directory == "" {
		return ""
	}
	return filepath.Join(engine.directory, "logs", "app.log")
}

func LoadCoverFile(ctx context.Context, rawDrama []byte, force bool) (string, string, error) {
	var drama nativeDrama
	if err := json.Unmarshal(rawDrama, &drama); err != nil {
		return "", "", errors.New("剧集信息格式无效")
	}
	if !nativeDramaAvailable(drama) {
		return "", "", errNativeBuildSource
	}
	nativeState.Lock()
	engine := nativeState.engine
	nativeState.Unlock()
	if engine == nil {
		return "", "", errors.New("应用核心尚未就绪")
	}
	result, err := engine.loadCover(ctx, drama, force)
	if err != nil {
		return "", "", publicError(err)
	}
	path, _ := result["path"].(string)
	if path == "" {
		return "", "", errors.New("海报暂不可用")
	}
	file, err := os.Open(path)
	if err != nil {
		return "", "", errors.New("海报暂不可用")
	}
	defer file.Close()
	header := make([]byte, 256)
	count, _ := file.Read(header)
	mimeType := detectCoverMime(header[:count])
	if result["heic"] == true || isHEICImage(header[:count]) || mimeType == "image/heic" {
		return path, "image/heic", nil
	}
	return path, mimeType, nil
}

func PrepareHEICCover(ctx context.Context, rawDrama []byte) (string, string, string, bool, error) {
	var drama nativeDrama
	if err := json.Unmarshal(rawDrama, &drama); err != nil {
		return "", "", "", false, errors.New("剧集信息格式无效")
	}
	if !nativeDramaAvailable(drama) {
		return "", "", "", false, errNativeBuildSource
	}
	nativeState.Lock()
	engine := nativeState.engine
	nativeState.Unlock()
	if engine == nil {
		return "", "", "", false, errors.New("应用核心尚未就绪")
	}
	resAny, err := engine.prepareCover(ctx, drama)
	if err != nil {
		return "", "", "", false, publicError(err)
	}
	res, _ := resAny.(map[string]any)
	origPath, _ := res["path"].(string)
	isHEIC, _ := res["heic"].(bool)
	inputPath, _ := res["input"].(string)
	filters, _ := res["filters"].(string)
	return origPath, inputPath, filters, isHEIC, nil
}

func ExtractHEICWebFrame(filePath string) (*HEICWebFrame, error) {
	f, err := os.Open(filePath)
	if err != nil {
		return nil, errors.New("海报文件不可读")
	}
	data, err := io.ReadAll(io.LimitReader(f, nativeCoverMaxBytes+1))
	_ = f.Close()
	if err != nil || len(data) == 0 || len(data) > nativeCoverMaxBytes {
		return nil, errors.New("海报文件长度无效")
	}
	hdrLen := len(data)
	if hdrLen > 256 {
		hdrLen = 256
	}
	if !isHEICImage(data) && detectCoverMime(data[:hdrLen]) != "image/heic" {
		return nil, errors.New("不是支持的 HEIC 封面")
	}
	boxes, err := heifBoxes(data)
	if err != nil {
		return nil, err
	}
	meta := heifBoxData(boxes, "meta")
	if len(meta) < 4 || meta[0] != 0 {
		return nil, errors.New("HEIC 缺少图片元数据")
	}
	children, err := heifBoxes(meta[4:])
	if err != nil {
		return nil, err
	}
	primary := &heifReader{data: heifBoxData(children, "pitm")}
	version := primary.uint(1)
	primary.take(3)
	idSize := 2
	if version == 1 {
		idSize = 4
	}
	itemID := primary.uint(idSize)
	if primary.err != nil || version > 1 || itemID == 0 {
		return nil, errors.New("HEIC 主图片标识无效")
	}
	if err := validateHEICItem(heifBoxData(children, "iinf"), itemID); err != nil {
		return nil, err
	}
	iprpBoxes, err := heifBoxes(heifBoxData(children, "iprp"))
	if err != nil {
		return nil, err
	}
	properties, err := heifBoxes(heifBoxData(iprpBoxes, "ipco"))
	if err != nil {
		return nil, err
	}
	reader := &heifReader{data: heifBoxData(iprpBoxes, "ipma")}
	ipmaVersion, ipmaFlags := reader.uint(1), reader.uint(3)
	entryCount := reader.uint(4)
	if ipmaVersion > 1 || entryCount > 4096 || reader.err != nil {
		return nil, errors.New("HEIC 图片属性无效")
	}
	var config []byte
	var transforms []string
	var width, height uint32
	for entry := uint64(0); entry < entryCount && reader.err == nil; entry++ {
		curIDSize := 2
		if ipmaVersion == 1 {
			curIDSize = 4
		}
		id := reader.uint(curIDSize)
		assocCount := reader.uint(1)
		for assoc := uint64(0); assoc < assocCount && reader.err == nil; assoc++ {
			idx := reader.uint(1) & 127
			if ipmaFlags&1 != 0 {
				idx = idx<<8 | reader.uint(1)
			}
			if id != itemID || idx == 0 {
				continue
			}
			if idx > uint64(len(properties)) {
				return nil, errors.New("HEIC 属性索引越界")
			}
			prop := properties[idx-1]
			switch prop.kind {
			case "hvcC":
				config = append([]byte(nil), prop.data...)
			case "ispe":
				if len(prop.data) != 12 {
					return nil, errors.New("HEIC 图片尺寸无效")
				}
				width = binary.BigEndian.Uint32(prop.data[4:8])
				height = binary.BigEndian.Uint32(prop.data[8:12])
			case "imir":
				if len(prop.data) != 1 {
					return nil, errors.New("HEIC 图片镜像无效")
				}
				if prop.data[0]&1 == 0 {
					transforms = append(transforms, "hflip")
				} else {
					transforms = append(transforms, "vflip")
				}
			case "irot":
				if len(prop.data) != 1 {
					return nil, errors.New("HEIC 图片旋转无效")
				}
				switch prop.data[0] & 3 {
				case 1:
					transforms = append(transforms, "rot90ccw")
				case 2:
					transforms = append(transforms, "rot180")
				case 3:
					transforms = append(transforms, "rot90cw")
				}
			}
		}
	}
	if reader.err != nil || len(config) < 23 || config[0] != 1 || width == 0 || height == 0 || width > 4096 || height > 4096 {
		return nil, errors.New("HEIC 编码参数无效")
	}
	rawSample, err := heifItemData(heifBoxData(children, "iloc"), itemID, data, heifBoxData(children, "idat"))
	if err != nil {
		return nil, err
	}
	lengthSize := int(config[21]&3) + 1
	var normalizedSample []byte
	if lengthSize == 4 {
		sampleReader := &heifReader{data: rawSample}
		for count := 0; len(sampleReader.data) > 0; count++ {
			nalLen := sampleReader.uint(4)
			if sampleReader.err != nil || nalLen < 2 || nalLen > uint64(len(sampleReader.data)) || count >= 4096 {
				return nil, errors.New("HEIC 帧分片不完整")
			}
			sampleReader.take(int(nalLen))
		}
		if sampleReader.err != nil {
			return nil, sampleReader.err
		}
		normalizedSample = rawSample
	} else {
		sampleReader := &heifReader{data: rawSample}
		for count := 0; len(sampleReader.data) > 0; count++ {
			nalLen := sampleReader.uint(lengthSize)
			if sampleReader.err != nil || nalLen < 2 || nalLen > uint64(len(sampleReader.data)) || count >= 4096 || int(nalLen) > nativeCoverMaxBytes-4-len(normalizedSample) {
				return nil, errors.New("HEIC 帧分片不完整")
			}
			var lenBuf [4]byte
			binary.BigEndian.PutUint32(lenBuf[:], uint32(nalLen))
			normalizedSample = append(normalizedSample, lenBuf[:]...)
			normalizedSample = append(normalizedSample, sampleReader.take(int(nalLen))...)
		}
		if sampleReader.err != nil || len(normalizedSample) == 0 {
			return nil, errors.New("HEIC 帧数据无效")
		}
		config[21] = (config[21] & 0xFC) | 0x03
	}
	return &HEICWebFrame{
		Width:      width,
		Height:     height,
		Transforms: transforms,
		Codec:      hevcCodecStringFromHVCC(config),
		HVCC:       config,
		Sample:     normalizedSample,
	}, nil
}

func hevcCodecStringFromHVCC(config []byte) string {
	if len(config) < 13 {
		return "hvc1.1.6.L93.B0"
	}
	spaceIdx := (config[1] >> 6) & 0x03
	spacePrefix := ""
	switch spaceIdx {
	case 1:
		spacePrefix = "A"
	case 2:
		spacePrefix = "B"
	case 3:
		spacePrefix = "C"
	}
	tierChar := 'L'
	if (config[1]>>5)&0x01 != 0 {
		tierChar = 'H'
	}
	profileIDC := config[1] & 0x1F
	compat := binary.BigEndian.Uint32(config[2:6])
	revCompat := bits.Reverse32(compat)
	levelIDC := config[12]

	var b strings.Builder
	fmt.Fprintf(&b, "hvc1.%s%d.%X.%c%d", spacePrefix, profileIDC, revCompat, tierChar, levelIDC)
	constraints := config[6:12]
	lastNonZero := -1
	for i := len(constraints) - 1; i >= 0; i-- {
		if constraints[i] != 0 {
			lastNonZero = i
			break
		}
	}
	for i := 0; i <= lastNonZero; i++ {
		fmt.Fprintf(&b, ".%X", constraints[i])
	}
	return b.String()
}

func detectCoverMime(header []byte) string {
	if bytes.HasPrefix(header, []byte{0x89, 'P', 'N', 'G', '\r', '\n', 0x1a, '\n'}) {
		return "image/png"
	}
	if bytes.HasPrefix(header, []byte{0xff, 0xd8, 0xff}) {
		return "image/jpeg"
	}
	if bytes.HasPrefix(header, []byte("GIF87a")) || bytes.HasPrefix(header, []byte("GIF89a")) {
		return "image/gif"
	}
	if len(header) >= 12 && string(header[:4]) == "RIFF" && string(header[8:12]) == "WEBP" {
		return "image/webp"
	}
	if len(header) >= 12 && string(header[4:8]) == "ftyp" {
		ftypSize := int(binary.BigEndian.Uint32(header[:4]))
		if ftypSize < 12 || ftypSize > len(header) {
			ftypSize = len(header)
		}
		for off := 8; off+4 <= ftypSize; off += 4 {
			if off == 12 {
				continue
			}
			switch string(header[off : off+4]) {
			case "avif", "avis":
				return "image/avif"
			case "heic", "heix", "hevc", "hevx":
				return "image/heic"
			}
		}
		if string(header[8:12]) == "mif1" {
			return "image/heic"
		}
	}
	return "application/octet-stream"
}

func CompletedDownloadFile(jobID string) (string, string, string, int, error) {
	nativeState.Lock()
	engine := nativeState.engine
	nativeState.Unlock()
	if engine == nil || engine.downloads == nil {
		return "", "", "", 0, errors.New("下载服务尚未就绪")
	}
	manager := engine.downloads
	manager.mu.Lock()
	defer manager.mu.Unlock()
	record := manager.jobs[jobID]
	if record == nil || record.State != "completed" {
		return "", "", "", 0, errors.New("下载文件不存在或尚未完成")
	}
	if !nativeDownloadAvailable(record.nativeDownloadJob) {
		return "", "", "", 0, errNativeBuildSource
	}
	if !manager.localFilesValid(*record) {
		return "", "", "", 0, errNativeLocalFile
	}
	path := filepath.Join(manager.jobDirectory(record.nativeDownloadJob), record.File)
	if record.Output != "" {
		path = manager.outputFile(record.Output)
	}
	return path, record.Drama.Source, record.Drama.Title, record.Index, nil
}

func LocalEpisodeFile(dramaID string, index int) (string, string, int, bool, error) {
	nativeState.Lock()
	engine := nativeState.engine
	nativeState.Unlock()
	if engine == nil || engine.downloads == nil {
		return "", "", 0, false, nil
	}
	plan, found, err := engine.downloads.localPlan(dramaID, index)
	if err != nil {
		return "", "", 0, found, publicError(err)
	}
	return plan.URL, plan.Key, plan.Quality, found, nil
}

func ShutdownEngine() {
	nativeState.Lock()
	engine := nativeState.engine
	nativeState.Unlock()
	if engine == nil {
		return
	}
	if engine.downloads != nil {
		engine.downloads.close()
	}
	engine.mu.Lock()
	stream := engine.stream
	engine.mu.Unlock()
	if stream != nil && stream.server != nil {
		_ = stream.server.Close()
	}
}
