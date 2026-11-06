package main

import (
	"bytes"
	"crypto/aes"
	"crypto/cipher"
	"encoding/binary"
	"encoding/hex"
	"errors"
	"io"
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
)

var hlsAttrPattern = regexp.MustCompile(`([A-Z0-9-]+)=("[^"]*"|[^,]*)`)

func parseHLSAttrs(line string) map[string]string {
	attrs := map[string]string{}
	for _, m := range hlsAttrPattern.FindAllStringSubmatch(line, -1) {
		attrs[m[1]] = strings.Trim(m[2], `"`)
	}
	return attrs
}

func pkcs7Strip(data []byte) []byte {
	if len(data) == 0 || len(data)%aes.BlockSize != 0 {
		return data
	}
	pad := int(data[len(data)-1])
	if pad <= 0 || pad > aes.BlockSize || pad > len(data) {
		return data
	}
	for i := len(data) - pad; i < len(data); i++ {
		if int(data[i]) != pad {
			return data
		}
	}
	return data[:len(data)-pad]
}

func decryptHLSSegment(raw []byte, key []byte, ivHex string, seq uint64) ([]byte, error) {
	if len(key) != 16 {
		return raw, nil
	}
	if len(raw) < aes.BlockSize || len(raw)%aes.BlockSize != 0 {
		return nil, errors.New("加密分片长度无效")
	}
	var iv [16]byte
	cleaned := strings.TrimPrefix(strings.TrimPrefix(strings.TrimSpace(ivHex), "0x"), "0X")
	if cleaned != "" {
		if len(cleaned)%2 == 1 {
			cleaned = "0" + cleaned
		}
		decoded, err := hex.DecodeString(cleaned)
		if err != nil || len(decoded) > 16 {
			return nil, errors.New("加密分片 IV 无效")
		}
		copy(iv[16-len(decoded):], decoded)
	} else {
		binary.BigEndian.PutUint64(iv[8:], seq)
	}
	block, err := aes.NewCipher(key)
	if err != nil {
		return nil, err
	}
	out := make([]byte, len(raw))
	cipher.NewCBCDecrypter(block, iv[:]).CryptBlocks(out, raw)
	return pkcs7Strip(out), nil
}

func playlistHasEncryptedKey(playlist string) bool {
	for _, raw := range strings.Split(strings.ReplaceAll(playlist, "\r\n", "\n"), "\n") {
		line := strings.TrimSpace(raw)
		if !strings.HasPrefix(line, "#EXT-X-KEY:") {
			continue
		}
		attrs := parseHLSAttrs(line)
		method := strings.ToUpper(strings.TrimSpace(attrs["METHOD"]))
		if method != "" && method != "NONE" {
			return true
		}
	}
	return false
}

func hasDisguisedMediaHeader(data []byte) bool {
	if len(data) < 4 {
		return false
	}
	return bytes.HasPrefix(data, []byte{0x89, 'P', 'N', 'G'}) ||
		bytes.HasPrefix(data, []byte{0xff, 0xd8, 0xff}) ||
		bytes.HasPrefix(data, []byte("GIF8")) ||
		bytes.HasPrefix(data, []byte("BM")) ||
		bytes.HasPrefix(data, []byte("RIFF")) ||
		bytes.HasPrefix(data, []byte("ID3")) ||
		bytes.HasPrefix(data, []byte{0xef, 0xbb, 0xbf})
}

func stripDisguisedTSPrefix(data []byte) []byte {
	if len(data) == 0 || data[0] == 0x47 {
		return data
	}
	maxScan := min(len(data), 4096)
	for offset := 1; offset+564 < len(data) && offset <= maxScan; offset++ {
		if data[offset] == 0x47 && data[offset+188] == 0x47 && data[offset+376] == 0x47 && data[offset+564] == 0x47 {
			return data[offset:]
		}
	}
	if hasDisguisedMediaHeader(data) {
		for offset := 1; offset+376 < len(data) && offset <= maxScan; offset++ {
			if data[offset] == 0x47 && data[offset+188] == 0x47 && data[offset+376] == 0x47 {
				return data[offset:]
			}
		}
		for offset := 1; offset+188 <= len(data) && offset <= maxScan; offset++ {
			if data[offset] == 0x47 && (offset+188 == len(data) || data[offset+188] == 0x47) {
				return data[offset:]
			}
		}
	}
	return data
}

func convertLocalHLSToMP4(playlistPath string, outputPath string) error {
	dir := filepath.Dir(playlistPath)
	rawPlaylist, err := os.ReadFile(playlistPath)
	if err != nil {
		return err
	}
	lines := strings.Split(strings.ReplaceAll(string(rawPlaylist), "\r\n", "\n"), "\n")
	for i, line := range lines {
		if strings.HasPrefix(strings.TrimSpace(line), "#EXT-X-STREAM-INF:") {
			for next := i + 1; next < len(lines); next++ {
				uri := strings.TrimSpace(lines[next])
				if uri == "" || strings.HasPrefix(uri, "#") {
					continue
				}
				childBytes, childErr := os.ReadFile(filepath.Join(dir, filepath.FromSlash(uri)))
				if childErr != nil {
					return childErr
				}
				lines = strings.Split(strings.ReplaceAll(string(childBytes), "\r\n", "\n"), "\n")
				break
			}
			break
		}
	}

	var (
		mediaSeq  uint64
		segIndex  uint64
		activeKey []byte
		activeIV  string
		mapBytes  []byte
		segments  [][]byte
	)

	for _, line := range lines {
		text := strings.TrimSpace(line)
		if text == "" {
			continue
		}
		if strings.HasPrefix(text, "#EXT-X-MEDIA-SEQUENCE:") {
			val := strings.TrimSpace(strings.TrimPrefix(text, "#EXT-X-MEDIA-SEQUENCE:"))
			mediaSeq, _ = strconv.ParseUint(val, 10, 64)
			continue
		}
		if strings.HasPrefix(text, "#EXT-X-KEY:") {
			attrs := parseHLSAttrs(text)
			method := attrs["METHOD"]
			if method == "" || method == "NONE" {
				activeKey = nil
				activeIV = ""
				continue
			}
			if method != "AES-128" {
				return errors.New("不支持的 HLS 加密算法")
			}
			keyBytes, keyErr := os.ReadFile(filepath.Join(dir, filepath.FromSlash(attrs["URI"])))
			if keyErr != nil || len(keyBytes) != 16 {
				return errors.New("无法读取 HLS 密钥文件")
			}
			activeKey = keyBytes
			activeIV = attrs["IV"]
			continue
		}
		if strings.HasPrefix(text, "#EXT-X-MAP:") {
			attrs := parseHLSAttrs(text)
			if uri := attrs["URI"]; uri != "" {
				rawMap, mapErr := os.ReadFile(filepath.Join(dir, filepath.FromSlash(uri)))
				if mapErr != nil {
					return mapErr
				}
				if len(activeKey) == 16 {
					rawMap, mapErr = decryptHLSSegment(rawMap, activeKey, activeIV, mediaSeq)
					if mapErr != nil {
						return mapErr
					}
				}
				mapBytes = rawMap
			}
			continue
		}
		if strings.HasPrefix(text, "#") {
			continue
		}
		segRaw, segErr := os.ReadFile(filepath.Join(dir, filepath.FromSlash(text)))
		if segErr != nil {
			return segErr
		}
		if len(activeKey) == 16 {
			segRaw, segErr = decryptHLSSegment(segRaw, activeKey, activeIV, mediaSeq+segIndex)
			if segErr != nil {
				return segErr
			}
		}
		segIndex++
		segments = append(segments, segRaw)
	}

	if len(segments) == 0 {
		return errors.New("播放列表中没有有效媒体分片")
	}

	if len(mapBytes) > 0 || (len(segments[0]) >= 8 && (string(segments[0][4:8]) == "ftyp" || string(segments[0][4:8]) == "styp" || string(segments[0][4:8]) == "moof")) {
		tmp := outputPath + ".tmp"
		out, err := os.OpenFile(tmp, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, 0600)
		if err != nil {
			return err
		}
		if len(mapBytes) > 0 {
			if _, err := out.Write(mapBytes); err != nil {
				_ = out.Close()
				_ = os.Remove(tmp)
				return err
			}
		}
		for _, seg := range segments {
			if _, err := out.Write(seg); err != nil {
				_ = out.Close()
				_ = os.Remove(tmp)
				return err
			}
		}
		if err := out.Close(); err != nil {
			_ = os.Remove(tmp)
			return err
		}
		return os.Rename(tmp, outputPath)
	}

	return remuxMPEGTSDataToMP4(segments, outputPath)
}

type tsVideoSample struct {
	pts      int64
	dts      int64
	keyframe bool
	avccData []byte
}

type tsAudioSample struct {
	pts  int64
	data []byte
}

func parsePTSValue(p []byte) int64 {
	if len(p) < 5 {
		return 0
	}
	return (int64(p[0]>>1)&0x07)<<30 |
		int64(p[1])<<22 |
		(int64(p[2]>>1)&0x7f)<<15 |
		int64(p[3])<<7 |
		int64(p[4]>>1)
}

func splitAnnexBNALUs(stream []byte) [][]byte {
	var nalus [][]byte
	n := len(stream)
	start := -1
	i := 0
	for i < n {
		if i+3 <= n && stream[i] == 0 && stream[i+1] == 0 && stream[i+2] == 1 {
			if start >= 0 {
				end := i
				if end > start && stream[end-1] == 0 {
					end--
				}
				if end > start {
					nalus = append(nalus, stream[start:end])
				}
			}
			start = i + 3
			i += 3
			continue
		}
		if i+4 <= n && stream[i] == 0 && stream[i+1] == 0 && stream[i+2] == 0 && stream[i+3] == 1 {
			if start >= 0 {
				end := i
				if end > start {
					nalus = append(nalus, stream[start:end])
				}
			}
			start = i + 4
			i += 4
			continue
		}
		i++
	}
	if start >= 0 && start < n {
		nalus = append(nalus, stream[start:n])
	}
	return nalus
}

func parseH264DimensionsFallback() (uint16, uint16) {
	return 1080, 1920
}

func remuxMPEGTSDataToMP4(segments [][]byte, outputPath string) error {
	pmtPID := -1
	videoPID := -1
	audioPID := -1

	var videoPES bytes.Buffer
	var audioPES bytes.Buffer

	var (
		videoSamples []tsVideoSample
		audioSamples []tsAudioSample
		sps          []byte
		pps          []byte
		audioObjType int
		audioFreqIdx int
		audioChanCfg int
	)

	flushVideoPES := func() {
		if videoPES.Len() < 9 {
			videoPES.Reset()
			return
		}
		raw := append([]byte(nil), videoPES.Bytes()...)
		videoPES.Reset()
		if raw[0] != 0 || raw[1] != 0 || raw[2] != 1 {
			return
		}
		flags := raw[7]
		hdrLen := int(raw[8])
		if 9+hdrLen > len(raw) {
			return
		}
		var pts, dts int64
		if (flags&0x80) != 0 && hdrLen >= 5 {
			pts = parsePTSValue(raw[9 : 9+5])
			dts = pts
		}
		if (flags&0xc0) == 0xc0 && hdrLen >= 10 {
			dts = parsePTSValue(raw[14 : 14+5])
		}
		es := raw[9+hdrLen:]
		nalus := splitAnnexBNALUs(es)
		var frameBuf bytes.Buffer
		keyframe := false
		for _, nalu := range nalus {
			if len(nalu) == 0 {
				continue
			}
			nalType := nalu[0] & 0x1f
			switch nalType {
			case 7:
				if len(sps) == 0 {
					sps = append([]byte(nil), nalu...)
				}
			case 8:
				if len(pps) == 0 {
					pps = append([]byte(nil), nalu...)
				}
			case 9:
				continue
			case 5:
				keyframe = true
				var lenBytes [4]byte
				binary.BigEndian.PutUint32(lenBytes[:], uint32(len(nalu)))
				frameBuf.Write(lenBytes[:])
				frameBuf.Write(nalu)
			default:
				var lenBytes [4]byte
				binary.BigEndian.PutUint32(lenBytes[:], uint32(len(nalu)))
				frameBuf.Write(lenBytes[:])
				frameBuf.Write(nalu)
			}
		}
		if frameBuf.Len() > 0 {
			videoSamples = append(videoSamples, tsVideoSample{
				pts:      pts,
				dts:      dts,
				keyframe: keyframe,
				avccData: frameBuf.Bytes(),
			})
		}
	}

	flushAudioPES := func() {
		if audioPES.Len() < 9 {
			audioPES.Reset()
			return
		}
		raw := append([]byte(nil), audioPES.Bytes()...)
		audioPES.Reset()
		if raw[0] != 0 || raw[1] != 0 || raw[2] != 1 {
			return
		}
		flags := raw[7]
		hdrLen := int(raw[8])
		if 9+hdrLen > len(raw) {
			return
		}
		var pts int64
		if (flags&0x80) != 0 && hdrLen >= 5 {
			pts = parsePTSValue(raw[9 : 9+5])
		}
		es := raw[9+hdrLen:]
		pos := 0
		frameIdx := int64(0)
		for pos+7 <= len(es) {
			if es[pos] != 0xff || (es[pos+1]&0xf0) != 0xf0 {
				pos++
				continue
			}
			noCRC := (es[pos+1] & 0x01) != 0
			adtsHdr := 7
			if !noCRC {
				adtsHdr = 9
			}
			profile := int((es[pos+2]>>6)&0x03) + 1
			freqIdx := int((es[pos+2] >> 2) & 0x0f)
			chanCfg := int((es[pos+2]&0x01)<<2 | (es[pos+3]>>6)&0x03)
			frameLen := int(es[pos+3]&0x03)<<11 | int(es[pos+4])<<3 | int(es[pos+5]>>5)
			if frameLen <= adtsHdr || pos+frameLen > len(es) {
				break
			}
			if audioObjType == 0 && freqIdx < 13 {
				audioObjType = profile
				audioFreqIdx = freqIdx
				audioChanCfg = chanCfg
			}
			aacRaw := append([]byte(nil), es[pos+adtsHdr:pos+frameLen]...)
			sampleRate := aacSampleRateByIndex(freqIdx)
			framePTS := pts
			if sampleRate > 0 {
				framePTS += frameIdx * 90000 * 1024 / int64(sampleRate)
			}
			audioSamples = append(audioSamples, tsAudioSample{
				pts:  framePTS,
				data: aacRaw,
			})
			pos += frameLen
			frameIdx++
		}
	}

	for _, seg := range segments {
		clean := stripDisguisedTSPrefix(seg)
		for offset := 0; offset+188 <= len(clean); offset += 188 {
			pkt := clean[offset : offset+188]
			if pkt[0] != 0x47 {
				continue
			}
			pusi := (pkt[1] & 0x40) != 0
			pid := int(pkt[1]&0x1f)<<8 | int(pkt[2])
			afc := (pkt[3] >> 4) & 0x03
			pos := 4
			if afc == 2 || afc == 0 {
				continue
			}
			if afc == 3 {
				if pos >= 188 {
					continue
				}
				pos += 1 + int(pkt[pos])
			}
			if pos >= 188 {
				continue
			}
			payload := pkt[pos:]

			if pid == 0 {
				if pusi && len(payload) > 0 {
					ptr := int(payload[0])
					if 1+ptr < len(payload) {
						payload = payload[1+ptr:]
					}
				}
				if len(payload) >= 12 && payload[0] == 0x00 {
					secLen := int(payload[1]&0x0f)<<8 | int(payload[2])
					end := min(len(payload), 3+secLen-4)
					for p := 8; p+4 <= end; p += 4 {
						progNum := int(payload[p])<<8 | int(payload[p+1])
						entryPID := int(payload[p+2]&0x1f)<<8 | int(payload[p+3])
						if progNum != 0 {
							pmtPID = entryPID
							break
						}
					}
				}
				continue
			}

			if pid == pmtPID && pmtPID > 0 {
				if pusi && len(payload) > 0 {
					ptr := int(payload[0])
					if 1+ptr < len(payload) {
						payload = payload[1+ptr:]
					}
				}
				if len(payload) >= 12 && payload[0] == 0x02 {
					secLen := int(payload[1]&0x0f)<<8 | int(payload[2])
					progInfoLen := int(payload[10]&0x0f)<<8 | int(payload[11])
					end := min(len(payload), 3+secLen-4)
					p := 12 + progInfoLen
					for p+5 <= end {
						streamType := payload[p]
						elemPID := int(payload[p+1]&0x1f)<<8 | int(payload[p+2])
						esInfoLen := int(payload[p+3]&0x0f)<<8 | int(payload[p+4])
						switch streamType {
						case 0x1b:
							if videoPID < 0 {
								videoPID = elemPID
							}
						case 0x0f:
							if audioPID < 0 {
								audioPID = elemPID
							}
						}
						p += 5 + esInfoLen
					}
				}
				continue
			}

			if pid == videoPID && videoPID > 0 {
				if pusi {
					flushVideoPES()
				}
				videoPES.Write(payload)
			} else if pid == audioPID && audioPID > 0 {
				if pusi {
					flushAudioPES()
				}
				audioPES.Write(payload)
			}
		}
	}
	flushVideoPES()
	flushAudioPES()

	if len(videoSamples) == 0 || len(sps) == 0 || len(pps) == 0 {
		return errors.New("暂无法无损合成该流格式，请在服务端环境安装 FFmpeg 后重试")
	}

	return writeSimpleMP4(outputPath, sps, pps, videoSamples, audioObjType, audioFreqIdx, audioChanCfg, audioSamples)
}

func aacSampleRateByIndex(idx int) uint32 {
	rates := [...]uint32{96000, 88200, 64000, 48000, 44100, 32000, 24000, 22050, 16000, 12000, 11025, 8000, 7350}
	if idx >= 0 && idx < len(rates) {
		return rates[idx]
	}
	return 44100
}

func makeMP4Box(boxType string, payload []byte) []byte {
	out := make([]byte, 8+len(payload))
	binary.BigEndian.PutUint32(out[0:4], uint32(len(out)))
	copy(out[4:8], boxType)
	copy(out[8:], payload)
	return out
}

func makeFullBox(boxType string, version byte, flags uint32, body []byte) []byte {
	out := make([]byte, 12+len(body))
	binary.BigEndian.PutUint32(out[0:4], uint32(len(out)))
	copy(out[4:8], boxType)
	out[8] = version
	out[9] = byte(flags >> 16)
	out[10] = byte(flags >> 8)
	out[11] = byte(flags)
	copy(out[12:], body)
	return out
}

func writeSimpleMP4(outputPath string, sps, pps []byte, video []tsVideoSample, audioObj, audioFreqIdx, audioChan int, audio []tsAudioSample) error {
	width, height := parseH264DimensionsFallback()
	const videoTimescale = 90000

	videoDeltas := make([]uint32, len(video))
	videoCTTS := make([]int32, len(video))
	videoSizes := make([]uint32, len(video))
	var syncSamples []uint32
	var totalVideoDur uint64
	for i := range video {
		dur := uint32(3000)
		if i+1 < len(video) && video[i+1].dts > video[i].dts {
			dur = uint32(video[i+1].dts - video[i].dts)
		} else if i > 0 {
			dur = videoDeltas[i-1]
		}
		videoDeltas[i] = dur
		totalVideoDur += uint64(dur)
		videoCTTS[i] = int32(video[i].pts - video[i].dts)
		videoSizes[i] = uint32(len(video[i].avccData))
		if video[i].keyframe {
			syncSamples = append(syncSamples, uint32(i+1))
		}
	}

	audioRate := aacSampleRateByIndex(audioFreqIdx)
	hasAudio := len(audio) > 0 && audioObj > 0
	audioSizes := make([]uint32, len(audio))
	for i := range audio {
		audioSizes[i] = uint32(len(audio[i].data))
	}
	totalAudioDur := uint64(len(audio)) * 1024

	ftyp := makeMP4Box("ftyp", []byte("isom\x00\x00\x02\x00isomiso2avc1mp41"))

	buildMOOV := func(vChunkOffset, aChunkOffset uint64) []byte {
		var mvhdBody [96]byte
		binary.BigEndian.PutUint32(mvhdBody[8:12], videoTimescale)
		binary.BigEndian.PutUint32(mvhdBody[12:16], uint32(totalVideoDur))
		binary.BigEndian.PutUint32(mvhdBody[16:20], 0x00010000)
		binary.BigEndian.PutUint16(mvhdBody[20:22], 0x0100)
		binary.BigEndian.PutUint32(mvhdBody[32:36], 0x00010000)
		binary.BigEndian.PutUint32(mvhdBody[48:52], 0x00010000)
		binary.BigEndian.PutUint32(mvhdBody[64:68], 0x40000000)
		nextTrack := uint32(2)
		if hasAudio {
			nextTrack = 3
		}
		binary.BigEndian.PutUint32(mvhdBody[92:96], nextTrack)
		mvhd := makeFullBox("mvhd", 0, 0, mvhdBody[:])

		vTrak := buildVideoTrakBox(1, width, height, videoTimescale, uint32(totalVideoDur), sps, pps, videoDeltas, videoCTTS, syncSamples, videoSizes, vChunkOffset)
		var children bytes.Buffer
		children.Write(mvhd)
		children.Write(vTrak)
		if hasAudio {
			aTrak := buildAudioTrakBox(2, audioRate, uint32(totalAudioDur), uint16(max(1, audioChan)), audioObj, audioFreqIdx, audioChan, audioSizes, aChunkOffset)
			children.Write(aTrak)
		}
		return makeMP4Box("moov", children.Bytes())
	}

	var mdatPayloadLen uint64
	for _, v := range video {
		mdatPayloadLen += uint64(len(v.avccData))
	}
	var audioOffsetInMdat uint64 = mdatPayloadLen
	if hasAudio {
		for _, a := range audio {
			mdatPayloadLen += uint64(len(a.data))
		}
	}

	dummyMOOV := buildMOOV(0, 0)
	mdatHeaderLen := uint64(8)
	if mdatPayloadLen+8 > 0xffffffff {
		mdatHeaderLen = 16
	}
	vChunkStart := uint64(len(ftyp)+len(dummyMOOV)) + mdatHeaderLen
	aChunkStart := vChunkStart + audioOffsetInMdat
	realMOOV := buildMOOV(vChunkStart, aChunkStart)

	tmp := outputPath + ".tmp"
	f, err := os.OpenFile(tmp, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, 0600)
	if err != nil {
		return err
	}
	writeErr := func() error {
		if _, err := f.Write(ftyp); err != nil {
			return err
		}
		if _, err := f.Write(realMOOV); err != nil {
			return err
		}
		if mdatHeaderLen == 8 {
			var hdr [8]byte
			binary.BigEndian.PutUint32(hdr[0:4], uint32(mdatPayloadLen+8))
			copy(hdr[4:8], "mdat")
			if _, err := f.Write(hdr[:]); err != nil {
				return err
			}
		} else {
			var hdr [16]byte
			binary.BigEndian.PutUint32(hdr[0:4], 1)
			copy(hdr[4:8], "mdat")
			binary.BigEndian.PutUint64(hdr[8:16], mdatPayloadLen+16)
			if _, err := f.Write(hdr[:]); err != nil {
				return err
			}
		}
		for _, v := range video {
			if _, err := f.Write(v.avccData); err != nil {
				return err
			}
		}
		if hasAudio {
			for _, a := range audio {
				if _, err := f.Write(a.data); err != nil {
					return err
				}
			}
		}
		return nil
	}()
	closeErr := f.Close()
	if writeErr != nil {
		_ = os.Remove(tmp)
		return writeErr
	}
	if closeErr != nil {
		_ = os.Remove(tmp)
		return closeErr
	}
	return os.Rename(tmp, outputPath)
}

func buildVideoTrakBox(trackID uint32, width, height uint16, timescale, duration uint32, sps, pps []byte, deltas []uint32, ctts []int32, sync []uint32, sizes []uint32, chunkOffset uint64) []byte {
	var tkhdBody [80]byte
	binary.BigEndian.PutUint32(tkhdBody[8:12], trackID)
	binary.BigEndian.PutUint32(tkhdBody[16:20], duration)
	binary.BigEndian.PutUint32(tkhdBody[36:40], 0x00010000)
	binary.BigEndian.PutUint32(tkhdBody[52:56], 0x00010000)
	binary.BigEndian.PutUint32(tkhdBody[68:72], 0x40000000)
	binary.BigEndian.PutUint32(tkhdBody[72:76], uint32(width)<<16)
	binary.BigEndian.PutUint32(tkhdBody[76:80], uint32(height)<<16)
	tkhd := makeFullBox("tkhd", 0, 0x03, tkhdBody[:])

	var mdhdBody [20]byte
	binary.BigEndian.PutUint32(mdhdBody[8:12], timescale)
	binary.BigEndian.PutUint32(mdhdBody[12:16], duration)
	binary.BigEndian.PutUint16(mdhdBody[16:18], 0x55c4)
	mdhd := makeFullBox("mdhd", 0, 0, mdhdBody[:])

	var hdlrBody bytes.Buffer
	hdlrBody.Write(make([]byte, 4))
	hdlrBody.WriteString("vide")
	hdlrBody.Write(make([]byte, 12))
	hdlrBody.WriteString("VideoHandler\x00")
	hdlr := makeFullBox("hdlr", 0, 0, hdlrBody.Bytes())

	vmhd := makeFullBox("vmhd", 0, 1, make([]byte, 8))
	urlBox := makeFullBox("url ", 0, 1, nil)
	var drefBody bytes.Buffer
	drefBody.Write([]byte{0, 0, 0, 1})
	drefBody.Write(urlBox)
	dinf := makeMP4Box("dinf", makeFullBox("dref", 0, 0, drefBody.Bytes()))

	var avcC bytes.Buffer
	profile := byte(100)
	compat := byte(0)
	level := byte(40)
	if len(sps) >= 4 {
		profile, compat, level = sps[1], sps[2], sps[3]
	}
	avcC.Write([]byte{1, profile, compat, level, 0xff, 0xe1})
	var spsLen [2]byte
	binary.BigEndian.PutUint16(spsLen[:], uint16(len(sps)))
	avcC.Write(spsLen[:])
	avcC.Write(sps)
	avcC.WriteByte(1)
	var ppsLen [2]byte
	binary.BigEndian.PutUint16(ppsLen[:], uint16(len(pps)))
	avcC.Write(ppsLen[:])
	avcC.Write(pps)
	avcCBox := makeMP4Box("avcC", avcC.Bytes())

	var avc1Body bytes.Buffer
	avc1Body.Write(make([]byte, 6))
	avc1Body.Write([]byte{0, 1})
	avc1Body.Write(make([]byte, 16))
	var wh [4]byte
	binary.BigEndian.PutUint16(wh[0:2], width)
	binary.BigEndian.PutUint16(wh[2:4], height)
	avc1Body.Write(wh[:])
	avc1Body.Write([]byte{0, 0x48, 0, 0, 0, 0x48, 0, 0, 0, 0, 0, 0, 0, 1})
	avc1Body.Write(make([]byte, 32))
	avc1Body.Write([]byte{0, 0x18, 0xff, 0xff})
	avc1Body.Write(avcCBox)
	avc1Box := makeMP4Box("avc1", avc1Body.Bytes())

	var stsdBody bytes.Buffer
	stsdBody.Write([]byte{0, 0, 0, 1})
	stsdBody.Write(avc1Box)
	stsd := makeFullBox("stsd", 0, 0, stsdBody.Bytes())

	var sttsBody bytes.Buffer
	var num4 [4]byte
	binary.BigEndian.PutUint32(num4[:], uint32(len(deltas)))
	sttsBody.Write(num4[:])
	for _, d := range deltas {
		sttsBody.Write([]byte{0, 0, 0, 1})
		binary.BigEndian.PutUint32(num4[:], d)
		sttsBody.Write(num4[:])
	}
	stts := makeFullBox("stts", 0, 0, sttsBody.Bytes())

	var cttsBody bytes.Buffer
	binary.BigEndian.PutUint32(num4[:], uint32(len(ctts)))
	cttsBody.Write(num4[:])
	for _, c := range ctts {
		cttsBody.Write([]byte{0, 0, 0, 1})
		binary.BigEndian.PutUint32(num4[:], uint32(c))
		cttsBody.Write(num4[:])
	}
	cttsBox := makeFullBox("ctts", 1, 0, cttsBody.Bytes())

	var stssBody bytes.Buffer
	binary.BigEndian.PutUint32(num4[:], uint32(len(sync)))
	stssBody.Write(num4[:])
	for _, s := range sync {
		binary.BigEndian.PutUint32(num4[:], s)
		stssBody.Write(num4[:])
	}
	stss := makeFullBox("stss", 0, 0, stssBody.Bytes())

	var stscBody [16]byte
	binary.BigEndian.PutUint32(stscBody[0:4], 1)
	binary.BigEndian.PutUint32(stscBody[4:8], 1)
	binary.BigEndian.PutUint32(stscBody[8:12], uint32(len(sizes)))
	binary.BigEndian.PutUint32(stscBody[12:16], 1)
	stsc := makeFullBox("stsc", 0, 0, stscBody[:])

	var stszBody bytes.Buffer
	stszBody.Write([]byte{0, 0, 0, 0})
	binary.BigEndian.PutUint32(num4[:], uint32(len(sizes)))
	stszBody.Write(num4[:])
	for _, sz := range sizes {
		binary.BigEndian.PutUint32(num4[:], sz)
		stszBody.Write(num4[:])
	}
	stsz := makeFullBox("stsz", 0, 0, stszBody.Bytes())

	var co64Body [12]byte
	binary.BigEndian.PutUint32(co64Body[0:4], 1)
	binary.BigEndian.PutUint64(co64Body[4:12], chunkOffset)
	co64 := makeFullBox("co64", 0, 0, co64Body[:])

	var stbl bytes.Buffer
	stbl.Write(stsd)
	stbl.Write(stts)
	stbl.Write(cttsBox)
	stbl.Write(stss)
	stbl.Write(stsc)
	stbl.Write(stsz)
	stbl.Write(co64)

	var minf bytes.Buffer
	minf.Write(vmhd)
	minf.Write(dinf)
	minf.Write(makeMP4Box("stbl", stbl.Bytes()))

	var mdia bytes.Buffer
	mdia.Write(mdhd)
	mdia.Write(hdlr)
	mdia.Write(makeMP4Box("minf", minf.Bytes()))

	var trak bytes.Buffer
	trak.Write(tkhd)
	trak.Write(makeMP4Box("mdia", mdia.Bytes()))
	return makeMP4Box("trak", trak.Bytes())
}

func buildAudioTrakBox(trackID, sampleRate, duration uint32, channels uint16, objType, freqIdx, chanCfg int, sizes []uint32, chunkOffset uint64) []byte {
	movieDur := uint32(uint64(duration) * 90000 / uint64(max(1, sampleRate)))
	var tkhdBody [80]byte
	binary.BigEndian.PutUint32(tkhdBody[8:12], trackID)
	binary.BigEndian.PutUint32(tkhdBody[16:20], movieDur)
	binary.BigEndian.PutUint16(tkhdBody[32:34], 0x0100)
	binary.BigEndian.PutUint32(tkhdBody[36:40], 0x00010000)
	binary.BigEndian.PutUint32(tkhdBody[52:56], 0x00010000)
	binary.BigEndian.PutUint32(tkhdBody[68:72], 0x40000000)
	tkhd := makeFullBox("tkhd", 0, 0x03, tkhdBody[:])

	var mdhdBody [20]byte
	binary.BigEndian.PutUint32(mdhdBody[8:12], sampleRate)
	binary.BigEndian.PutUint32(mdhdBody[12:16], duration)
	binary.BigEndian.PutUint16(mdhdBody[16:18], 0x55c4)
	mdhd := makeFullBox("mdhd", 0, 0, mdhdBody[:])

	var hdlrBody bytes.Buffer
	hdlrBody.Write(make([]byte, 4))
	hdlrBody.WriteString("soun")
	hdlrBody.Write(make([]byte, 12))
	hdlrBody.WriteString("SoundHandler\x00")
	hdlr := makeFullBox("hdlr", 0, 0, hdlrBody.Bytes())

	smhd := makeFullBox("smhd", 0, 0, make([]byte, 4))
	urlBox := makeFullBox("url ", 0, 1, nil)
	var drefBody bytes.Buffer
	drefBody.Write([]byte{0, 0, 0, 1})
	drefBody.Write(urlBox)
	dinf := makeMP4Box("dinf", makeFullBox("dref", 0, 0, drefBody.Bytes()))

	asc0 := byte((objType << 3) | ((freqIdx >> 1) & 0x07))
	asc1 := byte(((freqIdx & 0x01) << 7) | ((chanCfg & 0x0f) << 3))
	esdsPayload := []byte{
		0x03, 0x19, 0x00, 0x02, 0x00,
		0x04, 0x11, 0x40, 0x15, 0x00, 0x00, 0x00, 0x00, 0x01, 0xf4, 0x00, 0x00, 0x01, 0xf4, 0x00,
		0x05, 0x02, asc0, asc1,
		0x06, 0x01, 0x02,
	}
	esds := makeFullBox("esds", 0, 0, esdsPayload)

	var mp4aBody bytes.Buffer
	mp4aBody.Write(make([]byte, 6))
	mp4aBody.Write([]byte{0, 1})
	mp4aBody.Write(make([]byte, 8))
	var chs [4]byte
	binary.BigEndian.PutUint16(chs[0:2], channels)
	binary.BigEndian.PutUint16(chs[2:4], 16)
	mp4aBody.Write(chs[:])
	mp4aBody.Write(make([]byte, 4))
	var sr [4]byte
	binary.BigEndian.PutUint32(sr[:], sampleRate<<16)
	mp4aBody.Write(sr[:])
	mp4aBody.Write(esds)
	mp4a := makeMP4Box("mp4a", mp4aBody.Bytes())

	var stsdBody bytes.Buffer
	stsdBody.Write([]byte{0, 0, 0, 1})
	stsdBody.Write(mp4a)
	stsd := makeFullBox("stsd", 0, 0, stsdBody.Bytes())

	var sttsBody [12]byte
	binary.BigEndian.PutUint32(sttsBody[0:4], 1)
	binary.BigEndian.PutUint32(sttsBody[4:8], uint32(len(sizes)))
	binary.BigEndian.PutUint32(sttsBody[8:12], 1024)
	stts := makeFullBox("stts", 0, 0, sttsBody[:])

	var stscBody [16]byte
	binary.BigEndian.PutUint32(stscBody[0:4], 1)
	binary.BigEndian.PutUint32(stscBody[4:8], 1)
	binary.BigEndian.PutUint32(stscBody[8:12], uint32(len(sizes)))
	binary.BigEndian.PutUint32(stscBody[12:16], 1)
	stsc := makeFullBox("stsc", 0, 0, stscBody[:])

	var stszBody bytes.Buffer
	var num4 [4]byte
	stszBody.Write([]byte{0, 0, 0, 0})
	binary.BigEndian.PutUint32(num4[:], uint32(len(sizes)))
	stszBody.Write(num4[:])
	for _, sz := range sizes {
		binary.BigEndian.PutUint32(num4[:], sz)
		stszBody.Write(num4[:])
	}
	stsz := makeFullBox("stsz", 0, 0, stszBody.Bytes())

	var co64Body [12]byte
	binary.BigEndian.PutUint32(co64Body[0:4], 1)
	binary.BigEndian.PutUint64(co64Body[4:12], chunkOffset)
	co64 := makeFullBox("co64", 0, 0, co64Body[:])

	var stbl bytes.Buffer
	stbl.Write(stsd)
	stbl.Write(stts)
	stbl.Write(stsc)
	stbl.Write(stsz)
	stbl.Write(co64)

	var minf bytes.Buffer
	minf.Write(smhd)
	minf.Write(dinf)
	minf.Write(makeMP4Box("stbl", stbl.Bytes()))

	var mdia bytes.Buffer
	mdia.Write(mdhd)
	mdia.Write(hdlr)
	mdia.Write(makeMP4Box("minf", minf.Bytes()))

	var trak bytes.Buffer
	trak.Write(tkhd)
	trak.Write(makeMP4Box("mdia", mdia.Bytes()))
	return makeMP4Box("trak", trak.Bytes())
}

func copyPlainFile(src, dst string) error {
	in, err := os.Open(src)
	if err != nil {
		return err
	}
	defer in.Close()
	tmp := dst + ".tmp"
	out, err := os.OpenFile(tmp, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, 0600)
	if err != nil {
		return err
	}
	_, copyErr := io.Copy(out, in)
	closeErr := out.Close()
	if copyErr != nil {
		_ = os.Remove(tmp)
		return copyErr
	}
	if closeErr != nil {
		_ = os.Remove(tmp)
		return closeErr
	}
	return os.Rename(tmp, dst)
}
