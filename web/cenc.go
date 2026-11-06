package main

import (
	"bytes"
	"context"
	"crypto/aes"
	"crypto/cipher"
	"encoding/binary"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"
)

type mp4Box struct {
	boxType    string
	start      int
	headerSize int
	end        int
}

func (box mp4Box) payloadStart() int {
	return box.start + box.headerSize
}

type cencSubsample struct {
	clear     uint16
	protected uint32
}

type cencSampleAux struct {
	iv         [16]byte
	ivSize     int
	subsamples []cencSubsample
}

type cencSampleSpan struct {
	offset     int64
	size       int64
	iv         [16]byte
	subsamples []cencSubsample
}

type cencTrackConfig struct {
	trackID           uint32
	isProtected       bool
	perSampleIVSize   int
	constIV           [16]byte
	constIVSize       int
	defaultSampleSize uint32
}

func parseMP4Boxes(data []byte, start, end int) []mp4Box {
	var boxes []mp4Box
	offset := start
	for offset+8 <= end {
		size32 := binary.BigEndian.Uint32(data[offset : offset+4])
		boxType := string(data[offset+4 : offset+8])
		headerSize := 8
		boxEnd := 0
		switch size32 {
		case 1:
			if offset+16 > end {
				return boxes
			}
			size64 := binary.BigEndian.Uint64(data[offset+8 : offset+16])
			if size64 < 16 || uint64(offset)+size64 > uint64(end) {
				return boxes
			}
			headerSize = 16
			boxEnd = offset + int(size64)
		case 0:
			boxEnd = end
		default:
			if size32 < 8 || offset+int(size32) > end {
				return boxes
			}
			boxEnd = offset + int(size32)
		}
		boxes = append(boxes, mp4Box{
			boxType:    boxType,
			start:      offset,
			headerSize: headerSize,
			end:        boxEnd,
		})
		if boxEnd <= offset {
			break
		}
		offset = boxEnd
	}
	return boxes
}

func turnBoxIntoFree(data []byte, box mp4Box) {
	copy(data[box.start+4:box.start+8], "free")
	clear(data[box.payloadStart():box.end])
}

func parseTrackIDFromTKHD(data []byte, tkhd mp4Box) uint32 {
	payload := data[tkhd.payloadStart():tkhd.end]
	if len(payload) < 16 {
		return 0
	}
	version := payload[0]
	if version == 1 {
		if len(payload) < 24 {
			return 0
		}
		return binary.BigEndian.Uint32(payload[20:24])
	}
	return binary.BigEndian.Uint32(payload[12:16])
}

func parseAndUnwrapSTSD(data []byte, stsd mp4Box) (cencTrackConfig, bool) {
	var cfg cencTrackConfig
	payloadStart := stsd.payloadStart()
	if payloadStart+8 > stsd.end {
		return cfg, false
	}
	entryCount := int(binary.BigEndian.Uint32(data[payloadStart+4 : payloadStart+8]))
	entries := parseMP4Boxes(data, payloadStart+8, stsd.end)
	found := false
	for idx, entry := range entries {
		if idx >= entryCount {
			break
		}
		fixedLen := 0
		switch entry.boxType {
		case "encv":
			fixedLen = 78
		case "enca":
			if entry.payloadStart()+10 > entry.end {
				continue
			}
			audioVersion := binary.BigEndian.Uint16(data[entry.payloadStart()+8 : entry.payloadStart()+10])
			switch audioVersion {
			case 1:
				fixedLen = 44
			case 2:
				fixedLen = 64
			default:
				fixedLen = 28
			}
		default:
			continue
		}
		childStart := entry.payloadStart() + fixedLen
		if childStart > entry.end {
			continue
		}
		children := parseMP4Boxes(data, childStart, entry.end)
		for _, child := range children {
			if child.boxType != "sinf" {
				continue
			}
			origFormat := ""
			for _, sinfChild := range parseMP4Boxes(data, child.payloadStart(), child.end) {
				switch sinfChild.boxType {
				case "frma":
					if sinfChild.payloadStart()+4 <= sinfChild.end {
						origFormat = string(data[sinfChild.payloadStart() : sinfChild.payloadStart()+4])
					}
				case "schi":
					for _, schiChild := range parseMP4Boxes(data, sinfChild.payloadStart(), sinfChild.end) {
						if schiChild.boxType != "tenc" {
							continue
						}
						tp := data[schiChild.payloadStart():schiChild.end]
						if len(tp) < 24 {
							continue
						}
						cfg.isProtected = tp[6] != 0
						cfg.perSampleIVSize = int(tp[7])
						if cfg.isProtected && cfg.perSampleIVSize == 0 && len(tp) >= 25 {
							ivLen := int(tp[24])
							if len(tp) >= 25+ivLen && (ivLen == 8 || ivLen == 16) {
								cfg.constIVSize = ivLen
								copy(cfg.constIV[:ivLen], tp[25:25+ivLen])
							}
						}
						found = true
					}
				}
			}
			if len(origFormat) == 4 {
				copy(data[entry.start+4:entry.start+8], origFormat)
			}
			turnBoxIntoFree(data, child)
		}
	}
	return cfg, found
}

func parseSTSZ(data []byte, stsz mp4Box) []uint32 {
	p := data[stsz.payloadStart():stsz.end]
	if len(p) < 12 {
		return nil
	}
	fixedSize := binary.BigEndian.Uint32(p[4:8])
	count := int(binary.BigEndian.Uint32(p[8:12]))
	if count <= 0 || count > 2000000 {
		return nil
	}
	sizes := make([]uint32, count)
	if fixedSize != 0 {
		for i := range sizes {
			sizes[i] = fixedSize
		}
		return sizes
	}
	if len(p) < 12+count*4 {
		return nil
	}
	for i := 0; i < count; i++ {
		sizes[i] = binary.BigEndian.Uint32(p[12+i*4 : 16+i*4])
	}
	return sizes
}

type stscEntry struct {
	firstChunk      uint32
	samplesPerChunk uint32
}

func parseSTSC(data []byte, stsc mp4Box) []stscEntry {
	p := data[stsc.payloadStart():stsc.end]
	if len(p) < 8 {
		return nil
	}
	count := int(binary.BigEndian.Uint32(p[4:8]))
	if count <= 0 || len(p) < 8+count*12 {
		return nil
	}
	entries := make([]stscEntry, count)
	for i := 0; i < count; i++ {
		base := 8 + i*12
		entries[i] = stscEntry{
			firstChunk:      binary.BigEndian.Uint32(p[base : base+4]),
			samplesPerChunk: binary.BigEndian.Uint32(p[base+4 : base+8]),
		}
	}
	return entries
}

func parseChunkOffsets(data []byte, box mp4Box, bit64 bool) []int64 {
	p := data[box.payloadStart():box.end]
	if len(p) < 8 {
		return nil
	}
	count := int(binary.BigEndian.Uint32(p[4:8]))
	if count <= 0 || count > 2000000 {
		return nil
	}
	if bit64 {
		if len(p) < 8+count*8 {
			return nil
		}
		offsets := make([]int64, count)
		for i := 0; i < count; i++ {
			offsets[i] = int64(binary.BigEndian.Uint64(p[8+i*8 : 16+i*8]))
		}
		return offsets
	}
	if len(p) < 8+count*4 {
		return nil
	}
	offsets := make([]int64, count)
	for i := 0; i < count; i++ {
		offsets[i] = int64(binary.BigEndian.Uint32(p[8+i*4 : 12+i*4]))
	}
	return offsets
}

func computeSampleOffsets(sampleSizes []uint32, stsc []stscEntry, chunkOffsets []int64) ([]int64, [][]int) {
	if len(sampleSizes) == 0 || len(stsc) == 0 || len(chunkOffsets) == 0 {
		return nil, nil
	}
	offsets := make([]int64, len(sampleSizes))
	chunkSamples := make([][]int, len(chunkOffsets))
	sampleIdx := 0
	stscIdx := 0
	for chunkIdx := 0; chunkIdx < len(chunkOffsets) && sampleIdx < len(sampleSizes); chunkIdx++ {
		chunkNum := uint32(chunkIdx + 1)
		for stscIdx+1 < len(stsc) && chunkNum >= stsc[stscIdx+1].firstChunk {
			stscIdx++
		}
		perChunk := int(stsc[stscIdx].samplesPerChunk)
		cursor := chunkOffsets[chunkIdx]
		for s := 0; s < perChunk && sampleIdx < len(sampleSizes); s++ {
			offsets[sampleIdx] = cursor
			chunkSamples[chunkIdx] = append(chunkSamples[chunkIdx], sampleIdx)
			cursor += int64(sampleSizes[sampleIdx])
			sampleIdx++
		}
	}
	if sampleIdx != len(sampleSizes) {
		return nil, nil
	}
	return offsets, chunkSamples
}

func expandSampleIV(cfg cencTrackConfig, rawIV []byte) [16]byte {
	var iv16 [16]byte
	if cfg.perSampleIVSize == 0 && cfg.constIVSize > 0 {
		copy(iv16[:cfg.constIVSize], cfg.constIV[:cfg.constIVSize])
		return iv16
	}
	copy(iv16[:len(rawIV)], rawIV)
	return iv16
}

func parseSENC(data []byte, senc mp4Box, cfg cencTrackConfig) ([]cencSampleAux, bool) {
	p := data[senc.payloadStart():senc.end]
	if len(p) < 8 {
		return nil, false
	}
	flags := uint32(p[1])<<16 | uint32(p[2])<<8 | uint32(p[3])
	useSubsamples := (flags & 0x02) != 0
	count := int(binary.BigEndian.Uint32(p[4:8]))
	if count < 0 || count > 2000000 {
		return nil, false
	}
	aux := make([]cencSampleAux, count)
	pos := 8
	for i := 0; i < count; i++ {
		if pos+cfg.perSampleIVSize > len(p) {
			return nil, false
		}
		aux[i].iv = expandSampleIV(cfg, p[pos:pos+cfg.perSampleIVSize])
		aux[i].ivSize = cfg.perSampleIVSize
		pos += cfg.perSampleIVSize
		if useSubsamples {
			if pos+2 > len(p) {
				return nil, false
			}
			subCount := int(binary.BigEndian.Uint16(p[pos : pos+2]))
			pos += 2
			if pos+subCount*6 > len(p) {
				return nil, false
			}
			subs := make([]cencSubsample, subCount)
			for j := 0; j < subCount; j++ {
				subs[j] = cencSubsample{
					clear:     binary.BigEndian.Uint16(p[pos : pos+2]),
					protected: binary.BigEndian.Uint32(p[pos+2 : pos+6]),
				}
				pos += 6
			}
			aux[i].subsamples = subs
		}
	}
	return aux, true
}

func parseSAIZAndSAIO(fullFile []byte, moovData []byte, moovStart int64, saiz, saio mp4Box, cfg cencTrackConfig, chunkSamples [][]int, baseOffset int64) ([]cencSampleAux, bool) {
	zp := moovData[saiz.payloadStart():saiz.end]
	op := moovData[saio.payloadStart():saio.end]
	if len(zp) < 9 || len(op) < 8 {
		return nil, false
	}
	zFlags := uint32(zp[1])<<16 | uint32(zp[2])<<8 | uint32(zp[3])
	zPos := 4
	if (zFlags & 0x01) != 0 {
		zPos += 8
	}
	if zPos+5 > len(zp) {
		return nil, false
	}
	defaultInfoSize := zp[zPos]
	sampleCount := int(binary.BigEndian.Uint32(zp[zPos+1 : zPos+5]))
	zPos += 5
	if sampleCount <= 0 || sampleCount > 2000000 {
		return nil, false
	}
	infoSizes := make([]int, sampleCount)
	if defaultInfoSize != 0 {
		for i := range infoSizes {
			infoSizes[i] = int(defaultInfoSize)
		}
	} else {
		if zPos+sampleCount > len(zp) {
			return nil, false
		}
		for i := 0; i < sampleCount; i++ {
			infoSizes[i] = int(zp[zPos+i])
		}
	}

	oVersion := op[0]
	oFlags := uint32(op[1])<<16 | uint32(op[2])<<8 | uint32(op[3])
	oPos := 4
	if (oFlags & 0x01) != 0 {
		oPos += 8
	}
	if oPos+4 > len(op) {
		return nil, false
	}
	entryCount := int(binary.BigEndian.Uint32(op[oPos : oPos+4]))
	oPos += 4
	auxOffsets := make([]int64, entryCount)
	for i := 0; i < entryCount; i++ {
		if oVersion == 0 {
			if oPos+4 > len(op) {
				return nil, false
			}
			auxOffsets[i] = baseOffset + int64(binary.BigEndian.Uint32(op[oPos:oPos+4]))
			oPos += 4
		} else {
			if oPos+8 > len(op) {
				return nil, false
			}
			auxOffsets[i] = baseOffset + int64(binary.BigEndian.Uint64(op[oPos:oPos+8]))
			oPos += 8
		}
	}

	readRecord := func(filePos int64, infoLen int) (cencSampleAux, bool) {
		var record []byte
		if filePos >= moovStart && filePos+int64(infoLen) <= moovStart+int64(len(moovData)) {
			rel := int(filePos - moovStart)
			record = moovData[rel : rel+infoLen]
		} else if fullFile != nil && filePos >= 0 && filePos+int64(infoLen) <= int64(len(fullFile)) {
			record = fullFile[filePos : filePos+int64(infoLen)]
		} else {
			return cencSampleAux{}, false
		}
		if len(record) < cfg.perSampleIVSize {
			return cencSampleAux{}, false
		}
		item := cencSampleAux{
			iv:     expandSampleIV(cfg, record[:cfg.perSampleIVSize]),
			ivSize: cfg.perSampleIVSize,
		}
		rem := record[cfg.perSampleIVSize:]
		if len(rem) >= 2 {
			subCount := int(binary.BigEndian.Uint16(rem[:2]))
			if 2+subCount*6 <= len(rem) {
				subs := make([]cencSubsample, subCount)
				for j := 0; j < subCount; j++ {
					subs[j] = cencSubsample{
						clear:     binary.BigEndian.Uint16(rem[2+j*6 : 4+j*6]),
						protected: binary.BigEndian.Uint32(rem[4+j*6 : 8+j*6]),
					}
				}
				item.subsamples = subs
			}
		}
		return item, true
	}

	aux := make([]cencSampleAux, sampleCount)
	if entryCount == 1 {
		cursor := auxOffsets[0]
		for i := 0; i < sampleCount; i++ {
			item, ok := readRecord(cursor, infoSizes[i])
			if !ok {
				return nil, false
			}
			aux[i] = item
			cursor += int64(infoSizes[i])
		}
		return aux, true
	}
	if entryCount == len(chunkSamples) {
		for c, indices := range chunkSamples {
			cursor := auxOffsets[c]
			for _, idx := range indices {
				item, ok := readRecord(cursor, infoSizes[idx])
				if !ok {
					return nil, false
				}
				aux[idx] = item
				cursor += int64(infoSizes[idx])
			}
		}
		return aux, true
	}
	return nil, false
}

func parseMVEXTREX(moovData []byte, mvex mp4Box, tracks map[uint32]*cencTrackConfig) {
	for _, child := range parseMP4Boxes(moovData, mvex.payloadStart(), mvex.end) {
		if child.boxType != "trex" {
			continue
		}
		p := moovData[child.payloadStart():child.end]
		if len(p) < 20 {
			continue
		}
		trackID := binary.BigEndian.Uint32(p[4:8])
		defaultSize := binary.BigEndian.Uint32(p[16:20])
		if track := tracks[trackID]; track != nil {
			track.defaultSampleSize = defaultSize
		}
	}
}

func processMOOVBox(moovData []byte, moovFileOffset int64, fullFile []byte) ([]cencSampleSpan, map[uint32]*cencTrackConfig, bool) {
	tracks := map[uint32]*cencTrackConfig{}
	var spans []cencSampleSpan
	hasMVEX := false
	var mvexBox mp4Box

	for _, child := range parseMP4Boxes(moovData, 8, len(moovData)) {
		switch child.boxType {
		case "pssh":
			turnBoxIntoFree(moovData, child)
		case "mvex":
			hasMVEX = true
			mvexBox = child
		case "trak":
			var trackID uint32
			for _, trakChild := range parseMP4Boxes(moovData, child.payloadStart(), child.end) {
				if trakChild.boxType == "tkhd" {
					trackID = parseTrackIDFromTKHD(moovData, trakChild)
				}
				if trakChild.boxType != "mdia" {
					continue
				}
				for _, mdiaChild := range parseMP4Boxes(moovData, trakChild.payloadStart(), trakChild.end) {
					if mdiaChild.boxType != "minf" {
						continue
					}
					for _, minfChild := range parseMP4Boxes(moovData, mdiaChild.payloadStart(), mdiaChild.end) {
						if minfChild.boxType != "stbl" {
							continue
						}
						var (
							cfg     cencTrackConfig
							hasEnc  bool
							stszBox *mp4Box
							stscBox *mp4Box
							stcoBox *mp4Box
							co64Box *mp4Box
							sencBox *mp4Box
							saizBox *mp4Box
							saioBox *mp4Box
						)
						stblChildren := parseMP4Boxes(moovData, minfChild.payloadStart(), minfChild.end)
						for i := range stblChildren {
							b := &stblChildren[i]
							switch b.boxType {
							case "stsd":
								cfg, hasEnc = parseAndUnwrapSTSD(moovData, *b)
								cfg.trackID = trackID
							case "stsz":
								stszBox = b
							case "stsc":
								stscBox = b
							case "stco":
								stcoBox = b
							case "co64":
								co64Box = b
							case "senc":
								sencBox = b
							case "saiz":
								saizBox = b
							case "saio":
								saioBox = b
							}
						}
						if hasEnc {
							cfgCopy := cfg
							tracks[trackID] = &cfgCopy
						}
						if hasEnc && cfg.isProtected && stszBox != nil && stscBox != nil && (stcoBox != nil || co64Box != nil) {
							sizes := parseSTSZ(moovData, *stszBox)
							stsc := parseSTSC(moovData, *stscBox)
							var chunkOffsets []int64
							if co64Box != nil {
								chunkOffsets = parseChunkOffsets(moovData, *co64Box, true)
							} else {
								chunkOffsets = parseChunkOffsets(moovData, *stcoBox, false)
							}
							sampleOffsets, chunkSamples := computeSampleOffsets(sizes, stsc, chunkOffsets)
							var aux []cencSampleAux
							var ok bool
							if sencBox != nil {
								aux, ok = parseSENC(moovData, *sencBox, cfg)
							}
							if !ok && saizBox != nil && saioBox != nil {
								aux, ok = parseSAIZAndSAIO(fullFile, moovData, moovFileOffset, *saizBox, *saioBox, cfg, chunkSamples, 0)
							}
							if ok && len(aux) == len(sampleOffsets) {
								for sIdx := range sampleOffsets {
									if sizes[sIdx] == 0 {
										continue
									}
									spans = append(spans, cencSampleSpan{
										offset:     sampleOffsets[sIdx],
										size:       int64(sizes[sIdx]),
										iv:         aux[sIdx].iv,
										subsamples: aux[sIdx].subsamples,
									})
								}
							}
						}
						if sencBox != nil {
							turnBoxIntoFree(moovData, *sencBox)
						}
						if saizBox != nil {
							turnBoxIntoFree(moovData, *saizBox)
						}
						if saioBox != nil {
							turnBoxIntoFree(moovData, *saioBox)
						}
					}
				}
			}
		}
	}
	if hasMVEX {
		parseMVEXTREX(moovData, mvexBox, tracks)
	}
	sort.Slice(spans, func(i, j int) bool {
		return spans[i].offset < spans[j].offset
	})
	return spans, tracks, hasMVEX
}

func decryptSamplePrefix(block cipher.Block, span cencSampleSpan, samplePrefix []byte) {
	if len(samplePrefix) == 0 {
		return
	}
	stream := cipher.NewCTR(block, span.iv[:])
	if len(span.subsamples) == 0 {
		stream.XORKeyStream(samplePrefix, samplePrefix)
		return
	}
	pos := 0
	for _, sub := range span.subsamples {
		pos += int(sub.clear)
		if pos >= len(samplePrefix) {
			return
		}
		protLen := int(sub.protected)
		if protLen <= 0 {
			continue
		}
		end := pos + protLen
		if end > len(samplePrefix) {
			end = len(samplePrefix)
		}
		stream.XORKeyStream(samplePrefix[pos:end], samplePrefix[pos:end])
		pos = end
		if pos >= len(samplePrefix) {
			return
		}
	}
}

func processMOOFFragments(fileData []byte, tracks map[uint32]*cencTrackConfig, block cipher.Block) {
	topBoxes := parseMP4Boxes(fileData, 0, len(fileData))
	for _, box := range topBoxes {
		if box.boxType != "moof" {
			continue
		}
		moofOffset := int64(box.start)
		prevTrafEnd := moofOffset
		for _, traf := range parseMP4Boxes(fileData, box.payloadStart(), box.end) {
			if traf.boxType != "traf" {
				if traf.boxType == "pssh" {
					turnBoxIntoFree(fileData, traf)
				}
				continue
			}
			var (
				trackID           uint32
				baseDataOffset    = moofOffset
				defaultSampleSize uint32
				trunBoxes         []mp4Box
				sencBox           *mp4Box
				saizBox           *mp4Box
				saioBox           *mp4Box
			)
			trafChildren := parseMP4Boxes(fileData, traf.payloadStart(), traf.end)
			for i := range trafChildren {
				c := &trafChildren[i]
				switch c.boxType {
				case "tfhd":
					p := fileData[c.payloadStart():c.end]
					if len(p) < 8 {
						continue
					}
					flags := uint32(p[1])<<16 | uint32(p[2])<<8 | uint32(p[3])
					trackID = binary.BigEndian.Uint32(p[4:8])
					if track := tracks[trackID]; track != nil {
						defaultSampleSize = track.defaultSampleSize
					}
					if (flags&0x020000) == 0 && prevTrafEnd > moofOffset && (flags&0x01) == 0 {
						baseDataOffset = prevTrafEnd
					}
					pos := 8
					if (flags&0x01) != 0 && pos+8 <= len(p) {
						baseDataOffset = int64(binary.BigEndian.Uint64(p[pos : pos+8]))
						pos += 8
					}
					if (flags & 0x02) != 0 {
						pos += 4
					}
					if (flags & 0x08) != 0 {
						pos += 4
					}
					if (flags&0x10) != 0 && pos+4 <= len(p) {
						defaultSampleSize = binary.BigEndian.Uint32(p[pos : pos+4])
					}
				case "trun":
					trunBoxes = append(trunBoxes, *c)
				case "senc":
					sencBox = c
				case "saiz":
					saizBox = c
				case "saio":
					saioBox = c
				}
			}

			track := tracks[trackID]
			if track != nil && track.isProtected && len(trunBoxes) > 0 {
				var sampleOffsets []int64
				var sampleSizes []uint32
				var chunkSamples [][]int
				cursor := baseDataOffset
				for _, trun := range trunBoxes {
					tp := fileData[trun.payloadStart():trun.end]
					if len(tp) < 8 {
						continue
					}
					flags := uint32(tp[1])<<16 | uint32(tp[2])<<8 | uint32(tp[3])
					count := int(binary.BigEndian.Uint32(tp[4:8]))
					pos := 8
					if (flags&0x01) != 0 && pos+4 <= len(tp) {
						dataOffset := int32(binary.BigEndian.Uint32(tp[pos : pos+4]))
						cursor = baseDataOffset + int64(dataOffset)
						pos += 4
					}
					if (flags & 0x04) != 0 {
						pos += 4
					}
					var indices []int
					for s := 0; s < count; s++ {
						if (flags & 0x100) != 0 {
							pos += 4
						}
						sz := defaultSampleSize
						if (flags&0x200) != 0 && pos+4 <= len(tp) {
							sz = binary.BigEndian.Uint32(tp[pos : pos+4])
							pos += 4
						}
						if (flags & 0x400) != 0 {
							pos += 4
						}
						if (flags & 0x800) != 0 {
							pos += 4
						}
						indices = append(indices, len(sampleOffsets))
						sampleOffsets = append(sampleOffsets, cursor)
						sampleSizes = append(sampleSizes, sz)
						cursor += int64(sz)
					}
					chunkSamples = append(chunkSamples, indices)
					prevTrafEnd = cursor
				}
				var aux []cencSampleAux
				var ok bool
				if sencBox != nil {
					aux, ok = parseSENC(fileData, *sencBox, *track)
				}
				if !ok && saizBox != nil && saioBox != nil {
					aux, ok = parseSAIZAndSAIO(fileData, fileData[box.start:box.end], moofOffset, mp4Box{
						boxType:    saizBox.boxType,
						start:      saizBox.start - box.start,
						headerSize: saizBox.headerSize,
						end:        saizBox.end - box.start,
					}, mp4Box{
						boxType:    saioBox.boxType,
						start:      saioBox.start - box.start,
						headerSize: saioBox.headerSize,
						end:        saioBox.end - box.start,
					}, *track, chunkSamples, baseDataOffset)
				}
				if ok && len(aux) == len(sampleOffsets) {
					for i, off := range sampleOffsets {
						sz := int64(sampleSizes[i])
						if off >= 0 && off+sz <= int64(len(fileData)) && sz > 0 {
							span := cencSampleSpan{
								offset:     off,
								size:       sz,
								iv:         aux[i].iv,
								subsamples: aux[i].subsamples,
							}
							decryptSamplePrefix(block, span, fileData[off:off+sz])
						}
					}
				}
			}
			if sencBox != nil {
				turnBoxIntoFree(fileData, *sencBox)
			}
			if saizBox != nil {
				turnBoxIntoFree(fileData, *saizBox)
			}
			if saioBox != nil {
				turnBoxIntoFree(fileData, *saioBox)
			}
		}
	}
}

func decryptCENCBytes(data []byte, key []byte) error {
	block, err := aes.NewCipher(key)
	if err != nil {
		return err
	}
	topBoxes := parseMP4Boxes(data, 0, len(data))
	for _, box := range topBoxes {
		if box.boxType != "moov" {
			continue
		}
		spans, tracks, hasMVEX := processMOOVBox(data[box.start:box.end], int64(box.start), data)
		for _, span := range spans {
			if span.offset >= 0 && span.offset+span.size <= int64(len(data)) {
				decryptSamplePrefix(block, span, data[span.offset:span.offset+span.size])
			}
		}
		if hasMVEX && len(spans) == 0 {
			processMOOFFragments(data, tracks, block)
		}
		return nil
	}
	return errors.New("未找到有效的 MP4 视频头")
}

func decryptCENCFile(inputPath string, keyHex string, outputPath string) error {
	key, err := hex.DecodeString(strings.TrimSpace(keyHex))
	if err != nil || len(key) != 16 {
		return errors.New("视频解密密钥无效")
	}
	data, err := os.ReadFile(inputPath)
	if err != nil {
		return err
	}
	if err := decryptCENCBytes(data, key); err != nil {
		return err
	}
	tmp := outputPath + ".tmp"
	if err := os.WriteFile(tmp, data, 0600); err != nil {
		_ = os.Remove(tmp)
		return err
	}
	return os.Rename(tmp, outputPath)
}

type cencStreamMetadata struct {
	upstreamURL string
	key         []byte
	block       cipher.Block
	totalSize   int64
	moovOffset  int64
	moovBytes   []byte
	spans       []cencSampleSpan
	headPlain   []byte
	fragmented  bool
	fullPlain   []byte
	lastUsed    time.Time
}

type cencStreamManager struct {
	mu       sync.Mutex
	client   *http.Client
	keys     map[string][]byte
	metadata map[string]*cencStreamMetadata
	inflight map[string]chan struct{}
}

func newCENCStreamManager() *cencStreamManager {
	return &cencStreamManager{
		client:   &http.Client{Timeout: 0, Transport: &http.Transport{Proxy: nil}},
		keys:     map[string][]byte{},
		metadata: map[string]*cencStreamMetadata{},
		inflight: map[string]chan struct{}{},
	}
}

func (manager *cencStreamManager) registerKey(streamToken string, keyHex string) {
	if streamToken == "" || keyHex == "" {
		return
	}
	key, err := hex.DecodeString(strings.TrimSpace(keyHex))
	if err != nil || len(key) != 16 {
		return
	}
	manager.mu.Lock()
	manager.keys[streamToken] = key
	if len(manager.keys) > 32 {
		for k := range manager.keys {
			if k != streamToken {
				delete(manager.keys, k)
				break
			}
		}
	}
	manager.mu.Unlock()
}

func (manager *cencStreamManager) keyForToken(streamToken string) []byte {
	manager.mu.Lock()
	defer manager.mu.Unlock()
	key := manager.keys[streamToken]
	if len(key) != 16 {
		return nil
	}
	out := make([]byte, 16)
	copy(out, key)
	return out
}

func (manager *cencStreamManager) releaseToken(streamToken string) {
	manager.mu.Lock()
	delete(manager.keys, streamToken)
	for cacheKey := range manager.metadata {
		if strings.HasPrefix(cacheKey, streamToken+"/") {
			delete(manager.metadata, cacheKey)
		}
	}
	manager.mu.Unlock()
}

func (manager *cencStreamManager) warmMetadata(cacheKey, upstreamURL string) {
	streamToken := cacheKey
	if idx := strings.IndexByte(cacheKey, '/'); idx >= 0 {
		streamToken = cacheKey[:idx]
	}
	key := manager.keyForToken(streamToken)
	if len(key) != 16 || cacheKey == "" || upstreamURL == "" {
		return
	}
	manager.mu.Lock()
	if manager.metadata[cacheKey] != nil || manager.inflight[cacheKey] != nil {
		manager.mu.Unlock()
		return
	}
	manager.mu.Unlock()
	go func() {
		ctx, cancel := context.WithTimeout(context.Background(), 12*time.Second)
		defer cancel()
		_, _ = manager.loadMetadata(ctx, cacheKey, upstreamURL, key)
	}()
}

func (manager *cencStreamManager) loadMetadata(ctx context.Context, cacheKey, upstreamURL string, key []byte) (*cencStreamMetadata, error) {
	for {
		manager.mu.Lock()
		if meta := manager.metadata[cacheKey]; meta != nil {
			meta.lastUsed = time.Now()
			manager.mu.Unlock()
			return meta, nil
		}
		wait := manager.inflight[cacheKey]
		if wait == nil {
			wait = make(chan struct{})
			manager.inflight[cacheKey] = wait
			manager.mu.Unlock()
			break
		}
		manager.mu.Unlock()
		select {
		case <-ctx.Done():
			return nil, ctx.Err()
		case <-wait:
		}
	}

	meta, err := manager.probeUpstreamMP4(ctx, upstreamURL, key)
	manager.mu.Lock()
	if err == nil && meta != nil {
		for k, old := range manager.metadata {
			if time.Since(old.lastUsed) > 15*time.Minute {
				delete(manager.metadata, k)
			}
		}
		if len(manager.metadata) >= 8 {
			oldestKey := ""
			for k, old := range manager.metadata {
				if oldestKey == "" || old.lastUsed.Before(manager.metadata[oldestKey].lastUsed) {
					oldestKey = k
				}
			}
			delete(manager.metadata, oldestKey)
		}
		meta.lastUsed = time.Now()
		manager.metadata[cacheKey] = meta
	}
	wait := manager.inflight[cacheKey]
	delete(manager.inflight, cacheKey)
	close(wait)
	manager.mu.Unlock()
	return meta, err
}

func parseTotalFromContentRange(header string) int64 {
	var start, end, total int64
	if n, err := fmt.Sscanf(header, "bytes %d-%d/%d", &start, &end, &total); err == nil && n == 3 && total > 0 {
		return total
	}
	return 0
}

func (manager *cencStreamManager) fetchRangeBytes(ctx context.Context, upstreamURL string, start, end int64) ([]byte, int64, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, upstreamURL, nil)
	if err != nil {
		return nil, 0, err
	}
	if end >= start {
		req.Header.Set("Range", fmt.Sprintf("bytes=%d-%d", start, end))
	} else {
		req.Header.Set("Range", fmt.Sprintf("bytes=%d-", start))
	}
	resp, err := manager.client.Do(req)
	if err != nil {
		return nil, 0, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK && resp.StatusCode != http.StatusPartialContent {
		return nil, 0, fmt.Errorf("上游媒体返回 HTTP %d", resp.StatusCode)
	}
	total := parseTotalFromContentRange(resp.Header.Get("Content-Range"))
	if resp.StatusCode == http.StatusOK && resp.ContentLength > 0 {
		total = resp.ContentLength
	}
	limit := int64(128 << 20)
	if end >= start {
		limit = end - start + 1
	}
	body, err := io.ReadAll(io.LimitReader(resp.Body, limit))
	if err != nil {
		return nil, 0, err
	}
	if resp.StatusCode == http.StatusOK && int64(len(body)) > start {
		if total == 0 {
			total = int64(len(body))
		}
		sliceEnd := int64(len(body))
		if end >= start && end+1 < sliceEnd {
			sliceEnd = end + 1
		}
		return body[start:sliceEnd], total, nil
	}
	return body, total, nil
}

func (manager *cencStreamManager) probeUpstreamMP4(ctx context.Context, upstreamURL string, key []byte) (*cencStreamMetadata, error) {
	block, err := aes.NewCipher(key)
	if err != nil {
		return nil, err
	}
	const initialProbe = 2 << 20
	head, totalSize, err := manager.fetchRangeBytes(ctx, upstreamURL, 0, initialProbe-1)
	if err != nil || len(head) < 16 {
		return nil, errors.New("无法读取加密视频头")
	}
	var moovOffset int64 = -1
	var moovSize int64
	cursor := int64(0)
	currentBuf := head
	currentBufStart := int64(0)

	for step := 0; step < 32 && (totalSize == 0 || cursor+8 <= totalSize); step++ {
		var hdr []byte
		if cursor >= currentBufStart && cursor+16 <= currentBufStart+int64(len(currentBuf)) {
			rel := cursor - currentBufStart
			hdr = currentBuf[rel : rel+16]
		} else {
			fetched, tot, fetchErr := manager.fetchRangeBytes(ctx, upstreamURL, cursor, cursor+63)
			if fetchErr != nil || len(fetched) < 8 {
				break
			}
			if tot > 0 {
				totalSize = tot
			}
			hdr = fetched
		}
		sz32 := binary.BigEndian.Uint32(hdr[0:4])
		bType := string(hdr[4:8])
		var boxSize int64
		switch sz32 {
		case 1:
			if len(hdr) < 16 {
				break
			}
			boxSize = int64(binary.BigEndian.Uint64(hdr[8:16]))
		case 0:
			if totalSize > cursor {
				boxSize = totalSize - cursor
			}
		default:
			boxSize = int64(sz32)
		}
		if boxSize < 8 {
			break
		}
		if bType == "moov" {
			moovOffset = cursor
			moovSize = boxSize
			break
		}
		cursor += boxSize
	}

	if moovOffset < 0 || moovSize < 8 || moovSize > 32<<20 {
		full, tot, fullErr := manager.fetchRangeBytes(ctx, upstreamURL, 0, -1)
		if fullErr != nil {
			return nil, fullErr
		}
		if err := decryptCENCBytes(full, key); err != nil {
			return nil, err
		}
		if tot == 0 {
			tot = int64(len(full))
		}
		return &cencStreamMetadata{
			upstreamURL: upstreamURL,
			key:         key,
			block:       block,
			totalSize:   int64(len(full)),
			fragmented:  true,
			fullPlain:   full,
		}, nil
	}

	var moovBytes []byte
	if moovOffset >= 0 && moovOffset+moovSize <= int64(len(head)) {
		moovBytes = append([]byte(nil), head[moovOffset:moovOffset+moovSize]...)
	} else {
		fetched, tot, fetchErr := manager.fetchRangeBytes(ctx, upstreamURL, moovOffset, moovOffset+moovSize-1)
		if fetchErr != nil || int64(len(fetched)) != moovSize {
			return nil, errors.New("无法完整读取 MP4 索引头")
		}
		if tot > 0 {
			totalSize = tot
		}
		moovBytes = fetched
	}

	spans, tracks, hasMVEX := processMOOVBox(moovBytes, moovOffset, nil)
	hasProtectedTrack := false
	for _, tr := range tracks {
		if tr != nil && tr.isProtected {
			hasProtectedTrack = true
			break
		}
	}
	if (hasMVEX || hasProtectedTrack) && len(spans) == 0 {
		full, _, fullErr := manager.fetchRangeBytes(ctx, upstreamURL, 0, -1)
		if fullErr != nil {
			return nil, fullErr
		}
		if err := decryptCENCBytes(full, key); err != nil {
			return nil, err
		}
		return &cencStreamMetadata{
			upstreamURL: upstreamURL,
			key:         key,
			block:       block,
			totalSize:   int64(len(full)),
			fragmented:  true,
			fullPlain:   full,
		}, nil
	}

	if totalSize <= 0 {
		return nil, errors.New("无法确定视频文件长度")
	}

	usableLen := min(int64(len(head)), totalSize)
	for _, sp := range spans {
		if sp.offset >= usableLen {
			break
		}
		if sp.offset+sp.size > usableLen {
			usableLen = sp.offset
			break
		}
	}
	var headPlain []byte
	if usableLen > 0 {
		headPlain = head[:usableLen]
		for _, sp := range spans {
			if sp.offset >= usableLen {
				break
			}
			if sp.offset >= 0 && sp.size > 0 {
				decryptSamplePrefix(block, sp, headPlain[sp.offset:sp.offset+sp.size])
			}
		}
		moovEnd := moovOffset + int64(len(moovBytes))
		if moovOffset < usableLen && moovEnd > 0 {
			ovStart := max(int64(0), moovOffset)
			ovEnd := min(usableLen, moovEnd)
			copy(headPlain[ovStart:ovEnd], moovBytes[ovStart-moovOffset:ovEnd-moovOffset])
		}
	}

	return &cencStreamMetadata{
		upstreamURL: upstreamURL,
		key:         key,
		block:       block,
		totalSize:   totalSize,
		moovOffset:  moovOffset,
		moovBytes:   moovBytes,
		spans:       spans,
		headPlain:   headPlain,
	}, nil
}

func parseSingleHTTPRange(rangeHeader string, total int64) (int64, int64, bool, error) {
	if rangeHeader == "" {
		return 0, total - 1, false, nil
	}
	if !strings.HasPrefix(rangeHeader, "bytes=") {
		return 0, 0, false, errors.New("invalid range")
	}
	spec := strings.TrimSpace(strings.TrimPrefix(rangeHeader, "bytes="))
	if idx := strings.IndexByte(spec, ','); idx >= 0 {
		spec = strings.TrimSpace(spec[:idx])
	}
	parts := strings.SplitN(spec, "-", 2)
	if len(parts) != 2 {
		return 0, 0, false, errors.New("invalid range")
	}
	if parts[0] == "" {
		suffix, err := strconv.ParseInt(parts[1], 10, 64)
		if err != nil || suffix <= 0 {
			return 0, 0, false, errors.New("invalid range")
		}
		if suffix > total {
			suffix = total
		}
		return total - suffix, total - 1, true, nil
	}
	start, err := strconv.ParseInt(parts[0], 10, 64)
	if err != nil || start < 0 || start >= total {
		return 0, 0, false, errors.New("range not satisfiable")
	}
	end := total - 1
	if parts[1] != "" {
		end, err = strconv.ParseInt(parts[1], 10, 64)
		if err != nil || end < start {
			return 0, 0, false, errors.New("invalid range")
		}
		if end >= total {
			end = total - 1
		}
	}
	return start, end, true, nil
}

func (manager *cencStreamManager) serveDecrypted(writer http.ResponseWriter, request *http.Request, cacheKey, upstreamURL string, key []byte) {
	meta, err := manager.loadMetadata(request.Context(), cacheKey, upstreamURL, key)
	if err != nil {
		http.Error(writer, err.Error(), http.StatusBadGateway)
		return
	}
	writer.Header().Set("Content-Type", "video/mp4")
	writer.Header().Set("Accept-Ranges", "bytes")
	writer.Header().Set("Cache-Control", "private, max-age=600")

	if meta.fragmented && len(meta.fullPlain) > 0 {
		http.ServeContent(writer, request, "video.mp4", time.Time{}, bytes.NewReader(meta.fullPlain))
		return
	}

	reqStart, reqEnd, isPartial, rangeErr := parseSingleHTTPRange(request.Header.Get("Range"), meta.totalSize)
	if rangeErr != nil {
		writer.Header().Set("Content-Range", fmt.Sprintf("bytes */%d", meta.totalSize))
		writer.WriteHeader(http.StatusRequestedRangeNotSatisfiable)
		return
	}
	contentLen := reqEnd - reqStart + 1
	writer.Header().Set("Content-Length", strconv.FormatInt(contentLen, 10))
	if isPartial {
		writer.Header().Set("Content-Range", fmt.Sprintf("bytes %d-%d/%d", reqStart, reqEnd, meta.totalSize))
		writer.WriteHeader(http.StatusPartialContent)
	} else {
		writer.WriteHeader(http.StatusOK)
	}
	if request.Method == http.MethodHead {
		return
	}

	if headLen := int64(len(meta.headPlain)); headLen > 0 && reqStart < headLen {
		sliceEnd := min(reqEnd+1, headLen)
		if _, wErr := writer.Write(meta.headPlain[reqStart:sliceEnd]); wErr != nil {
			return
		}
		if flusher, ok := writer.(http.Flusher); ok {
			flusher.Flush()
		}
		if reqEnd < headLen {
			return
		}
		reqStart = headLen
	}

	firstIdx := sort.Search(len(meta.spans), func(i int) bool {
		return meta.spans[i].offset+meta.spans[i].size > reqStart
	})
	fetchStart := reqStart
	if firstIdx < len(meta.spans) && meta.spans[firstIdx].offset < reqStart {
		fetchStart = meta.spans[firstIdx].offset
	}

	upReq, err := http.NewRequestWithContext(request.Context(), http.MethodGet, upstreamURL, nil)
	if err != nil {
		return
	}
	upReq.Header.Set("Range", fmt.Sprintf("bytes=%d-%d", fetchStart, reqEnd))
	resp, err := manager.client.Do(upReq)
	if err != nil {
		return
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK && resp.StatusCode != http.StatusPartialContent {
		return
	}

	reader := resp.Body
	if resp.StatusCode == http.StatusOK && fetchStart > 0 {
		if _, err := io.CopyN(io.Discard, reader, fetchStart); err != nil {
			return
		}
	}

	emit := func(chunkOffset int64, chunk []byte) error {
		cStart := chunkOffset
		cEnd := chunkOffset + int64(len(chunk))
		if cEnd <= reqStart || cStart > reqEnd {
			return nil
		}
		moovStart := meta.moovOffset
		moovEnd := meta.moovOffset + int64(len(meta.moovBytes))
		if cStart < moovEnd && cEnd > moovStart {
			ovStart := max(cStart, moovStart)
			ovEnd := min(cEnd, moovEnd)
			copy(chunk[ovStart-cStart:ovEnd-cStart], meta.moovBytes[ovStart-moovStart:ovEnd-moovStart])
		}
		outStart := max(cStart, reqStart)
		outEnd := min(cEnd, reqEnd+1)
		_, wErr := writer.Write(chunk[outStart-cStart : outEnd-cStart])
		return wErr
	}

	pos := fetchStart
	spanIdx := firstIdx
	plainBuf := make([]byte, 64*1024)
	var sampleBuf []byte

	for pos <= reqEnd {
		for spanIdx < len(meta.spans) && meta.spans[spanIdx].offset+meta.spans[spanIdx].size <= pos {
			spanIdx++
		}
		nextSpanStart := reqEnd + 1
		if spanIdx < len(meta.spans) && meta.spans[spanIdx].offset <= reqEnd {
			nextSpanStart = meta.spans[spanIdx].offset
		}
		if pos < nextSpanStart {
			gap := nextSpanStart - pos
			for gap > 0 {
				toRead := int64(len(plainBuf))
				if gap < toRead {
					toRead = gap
				}
				n, rErr := io.ReadFull(reader, plainBuf[:toRead])
				if n > 0 {
					if err := emit(pos, plainBuf[:n]); err != nil {
						return
					}
					pos += int64(n)
					gap -= int64(n)
				}
				if rErr != nil {
					return
				}
			}
		}
		if pos > reqEnd || spanIdx >= len(meta.spans) {
			break
		}
		span := meta.spans[spanIdx]
		if span.offset != pos {
			break
		}
		readLen := span.size
		if pos+readLen > reqEnd+1 {
			readLen = reqEnd + 1 - pos
		}
		if readLen <= 0 {
			break
		}
		if int64(cap(sampleBuf)) < readLen {
			sampleBuf = make([]byte, readLen)
		} else {
			sampleBuf = sampleBuf[:readLen]
		}
		n, rErr := io.ReadFull(reader, sampleBuf)
		if n > 0 {
			decryptSamplePrefix(meta.block, span, sampleBuf[:n])
			if err := emit(pos, sampleBuf[:n]); err != nil {
				return
			}
			pos += int64(n)
		}
		if rErr != nil || int64(n) < readLen {
			return
		}
		spanIdx++
	}
}
