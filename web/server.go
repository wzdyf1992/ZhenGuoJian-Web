package main

import (
	"bytes"
	"context"
	"crypto/rand"
	"duanjuapp/native/core"
	"embed"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"mime"
	"net/http"
	"net/url"
	"os"
	"os/exec"
	"path"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"time"
)

//go:embed ui/*
var embeddedUIFS embed.FS

type cachedPlaybackPlan struct {
	rawURL      string
	keyHex      string
	streamToken string
	local       bool
	createdAt   time.Time
}

type webAppServer struct {
	version       string
	disableImages bool
	uiOverrideDir string
	tempDir       string
	store         *webStore
	diary         *webDiary
	cenc          *cencStreamManager
	heicCompat    *heicCompatManager
	converter     *conversionWorker
	streamClient  *http.Client
	planMu        sync.Mutex
	plans         map[string]cachedPlaybackPlan
	encryptedHLS  map[string]time.Time
	coverMu       sync.Mutex
	knownCovers   map[string]string
	rpcSeq        atomic.Int64
}

func newWebAppServer(version string, disableImages bool, uiOverrideDir, tempDir string, store *webStore, diary *webDiary) *webAppServer {
	cenc := newCENCStreamManager()
	srv := &webAppServer{
		version:       version,
		disableImages: disableImages,
		uiOverrideDir: uiOverrideDir,
		tempDir:       tempDir,
		store:         store,
		diary:         diary,
		cenc:          cenc,
		heicCompat:    newHEICCompatManager(filepath.Dir(tempDir)),
		converter:     newConversionWorker(diary),
		streamClient:  &http.Client{Timeout: 0, Transport: &http.Transport{Proxy: nil}},
		plans:         map[string]cachedPlaybackPlan{},
		encryptedHLS:  map[string]time.Time{},
		knownCovers:   map[string]string{},
	}
	srv.rpcSeq.Store(time.Now().UnixMicro())
	return srv
}

func (srv *webAppServer) routes() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("/api/bootstrap", srv.handleBootstrap)
	mux.HandleFunc("/api/rpc", srv.handleRPC)
	mux.HandleFunc("/api/cover", srv.handleCover)
	mux.HandleFunc("/api/cover-compatible", srv.handleCoverCompatible)
	mux.HandleFunc("/api/stream/", srv.handleStream)
	mux.HandleFunc("/api/local-media", srv.handleLocalMedia)
	mux.HandleFunc("/api/downloads/file/", srv.handleDownloadFile)
	mux.HandleFunc("/api/downloads/merge", srv.handleDownloadMerge)
	mux.HandleFunc("/api/store/state", srv.handleStoreState)
	mux.HandleFunc("/api/store/switch-profile", srv.handleSwitchProfile)
	mux.HandleFunc("/api/store/lock", srv.handleLockProfile)
	mux.HandleFunc("/api/store/profile", srv.handleManageProfile)
	mux.HandleFunc("/api/store/settings", srv.handleGlobalSettings)
	mux.HandleFunc("/api/store/user-settings", srv.handleUserSettings)
	mux.HandleFunc("/api/store/watch", srv.handleWatchProgress)
	mux.HandleFunc("/api/store/history-delete", srv.handleHistoryDelete)
	mux.HandleFunc("/api/store/follow", srv.handleFollow)
	mux.HandleFunc("/api/store/follow-check", srv.handleFollowCheck)
	mux.HandleFunc("/api/store/backup", srv.handleBackup)
	mux.HandleFunc("/api/logs", srv.handleLogs)
	mux.HandleFunc("/", srv.handleStaticUI)
	return mux
}

func (srv *webAppServer) browserToken(writer http.ResponseWriter, request *http.Request) string {
	if hdr := strings.TrimSpace(request.Header.Get("X-Session-Token")); len(hdr) >= 16 && len(hdr) <= 64 {
		return hdr
	}
	if cookie, err := request.Cookie("zgj_session"); err == nil && len(cookie.Value) >= 16 && len(cookie.Value) <= 64 {
		return cookie.Value
	}
	var b [16]byte
	_, _ = rand.Read(b[:])
	token := hex.EncodeToString(b[:])
	http.SetCookie(writer, &http.Cookie{
		Name:     "zgj_session",
		Value:    token,
		Path:     "/",
		HttpOnly: true,
		SameSite: http.SameSiteLaxMode,
		MaxAge:   30 * 24 * 3600,
	})
	return token
}

func writeJSONResponse(writer http.ResponseWriter, status int, payload any) {
	writer.Header().Set("Content-Type", "application/json; charset=utf-8")
	writer.Header().Set("Cache-Control", "no-store")
	writer.WriteHeader(status)
	_ = json.NewEncoder(writer).Encode(payload)
}

func writeAPIError(writer http.ResponseWriter, status int, err error) {
	msg := "请求失败，请重试"
	if err != nil && err.Error() != "" {
		msg = err.Error()
	}
	writeJSONResponse(writer, status, map[string]any{"ok": false, "error": msg})
}

func readJSONBody(request *http.Request, target any) error {
	defer request.Body.Close()
	data, err := io.ReadAll(io.LimitReader(request.Body, 8<<20+1024))
	if err != nil {
		return errors.New("无法读取请求内容")
	}
	if len(data) > 8<<20 {
		return errors.New("请求数据过大")
	}
	dec := json.NewDecoder(strings.NewReader(string(data)))
	dec.UseNumber()
	if err := dec.Decode(target); err != nil {
		return errors.New("请求 JSON 格式无效")
	}
	return nil
}

func (srv *webAppServer) handleBootstrap(writer http.ResponseWriter, request *http.Request) {
	if request.Method != http.MethodGet {
		writer.WriteHeader(http.StatusMethodNotAllowed)
		return
	}
	token := srv.browserToken(writer, request)
	state := srv.store.stateSnapshot(token)

	hongguoReady := true
	var hgEnv struct {
		OK   bool `json:"ok"`
		Data struct {
			Ready bool `json:"ready"`
		} `json:"data"`
	}
	if json.Unmarshal([]byte(core.NativeRequest(`{"action":"hongguoDevices"}`)), &hgEnv) == nil && hgEnv.OK {
		hongguoReady = hgEnv.Data.Ready
	}

	writeJSONResponse(writer, http.StatusOK, map[string]any{
		"ok":            true,
		"version":       srv.version,
		"disableImages": srv.disableImages,
		"hongguoReady":  hongguoReady,
		"ffmpegReady":   findFFmpegBinary() != "",
		"state":         state,
	})
}

func sourceForRankingBoard(board string) string {
	switch board {
	case "hongguo-hot", "hongguo-real", "hongguo-comic", "hongguo-ai":
		return "hongguo"
	case "huangdou-all", "huangdou-mogai", "huangdou-search", "huangdou-favorite", "huangdou-finish":
		return "huangdou"
	case "huangguo-hot", "huangguo-recommend", "huangguo-potential":
		return "huangguoai"
	}
	return ""
}

func (srv *webAppServer) rememberPlan(session string, plan cachedPlaybackPlan) {
	if session == "" {
		return
	}
	srv.planMu.Lock()
	defer srv.planMu.Unlock()
	for k, old := range srv.plans {
		if time.Since(old.createdAt) > 12*time.Hour {
			delete(srv.plans, k)
		}
	}
	if len(srv.plans) >= 64 {
		var oldestKey string
		for k, old := range srv.plans {
			if oldestKey == "" || old.createdAt.Before(srv.plans[oldestKey].createdAt) {
				oldestKey = k
			}
		}
		delete(srv.plans, oldestKey)
	}
	plan.createdAt = time.Now()
	srv.plans[session] = plan
}

func (srv *webAppServer) markEncryptedHLS(streamToken string) {
	if streamToken == "" {
		return
	}
	srv.planMu.Lock()
	defer srv.planMu.Unlock()
	for k, ts := range srv.encryptedHLS {
		if time.Since(ts) > 12*time.Hour {
			delete(srv.encryptedHLS, k)
		}
	}
	srv.encryptedHLS[streamToken] = time.Now()
}

func (srv *webAppServer) isEncryptedHLS(streamToken string) bool {
	if streamToken == "" {
		return false
	}
	srv.planMu.Lock()
	_, ok := srv.encryptedHLS[streamToken]
	srv.planMu.Unlock()
	return ok
}

func (srv *webAppServer) dropCachedPlan(session string) cachedPlaybackPlan {
	if session == "" {
		return cachedPlaybackPlan{}
	}
	srv.planMu.Lock()
	plan, ok := srv.plans[session]
	delete(srv.plans, session)
	if ok && plan.streamToken != "" {
		delete(srv.encryptedHLS, plan.streamToken)
	}
	srv.planMu.Unlock()
	if ok && plan.streamToken != "" {
		srv.cenc.releaseToken(plan.streamToken)
	}
	return plan
}

func (srv *webAppServer) rewritePlanForBrowser(planMap map[string]any, dramaID string, episodeIndex int) {
	rawURL, _ := planMap["url"].(string)
	isLocal, _ := planMap["local"].(bool)
	keyHex, _ := planMap["decryptionKey"].(string)
	session, _ := planMap["session"].(string)

	if isLocal {
		if session == "" {
			session = fmt.Sprintf("local:%s:%d", dramaID, episodeIndex)
			planMap["session"] = session
		}
		srv.rememberPlan(session, cachedPlaybackPlan{
			rawURL: rawURL,
			keyHex: keyHex,
			local:  true,
		})
		q := url.Values{}
		q.Set("id", dramaID)
		q.Set("index", strconv.Itoa(episodeIndex))
		planMap["url"] = "/api/local-media?" + q.Encode()
		planMap["decryptionKey"] = ""
		planMap["isHls"] = false
		return
	}

	origin := core.StreamServerOrigin()
	if origin != "" && strings.HasPrefix(rawURL, origin+"/") {
		rel := strings.TrimPrefix(rawURL, origin+"/")
		parts := strings.SplitN(rel, "/", 2)
		streamToken := parts[0]
		assetName := ""
		if len(parts) > 1 {
			assetName = parts[1]
		}
		isHLS := strings.HasSuffix(strings.ToLower(assetName), ".m3u8")
		if keyHex != "" {
			srv.cenc.registerKey(streamToken, keyHex)
			if !isHLS && assetName != "" {
				srv.cenc.warmMetadata(streamToken+"/"+assetName, origin+"/"+rel)
			}
		}
		srv.rememberPlan(session, cachedPlaybackPlan{
			rawURL:      rawURL,
			keyHex:      keyHex,
			streamToken: streamToken,
			local:       false,
		})
		planMap["url"] = "/api/stream/" + rel
		planMap["decryptionKey"] = ""
		planMap["isHls"] = isHLS
	}
}

func (srv *webAppServer) handleRPC(writer http.ResponseWriter, request *http.Request) {
	if request.Method != http.MethodPost {
		writer.WriteHeader(http.StatusMethodNotAllowed)
		return
	}
	token := srv.browserToken(writer, request)
	var input map[string]any
	if err := readJSONBody(request, &input); err != nil {
		writeAPIError(writer, http.StatusBadRequest, err)
		return
	}
	action, _ := input["action"].(string)
	if action == "" || action == "initialize" || action == "beginConversion" || action == "finishConversion" {
		writeAPIError(writer, http.StatusBadRequest, errors.New("不支持的操作"))
		return
	}

	unrestricted := action == "release" || action == "cancelPlayback" || action == "cancelRead" || action == "hongguoDevices"
	if !unrestricted {
		needAdmin := action == "resourceSettings" || action == "saveResourceSettings" || action == "moveDownloads"
		needDownload := action == "downloads" || action == "enqueueDownloads" || action == "controlDownloads" ||
			action == "controlDownloadBatch" || action == "localPlayback" || action == "storage" ||
			action == "downloadDirectory" || action == "moveDownloads" || action == "workLease"
		var targetSource string
		switch action {
		case "catalog", "cached", "categories":
			targetSource, _ = input["source"].(string)
		case "recommendations", "cachedRecommendations", "suggestions", "danmaku":
			targetSource = "hongguo"
		case "rankings":
			board, _ := input["board"].(string)
			targetSource = sourceForRankingBoard(board)
		case "detail", "metadata", "followCheck", "resolve", "preload", "enqueueDownloads", "localPlayback":
			if dm, ok := input["drama"].(map[string]any); ok {
				targetSource, _ = dm["source"].(string)
			}
		}
		if err := srv.store.authorizeAction(token, targetSource, needDownload, needAdmin); err != nil {
			writeAPIError(writer, http.StatusForbidden, err)
			return
		}
	}

	if action == "resolve" {
		snap := srv.store.stateSnapshot(token)
		if canDL, _ := snap["canDownload"].(bool); !canDL {
			input["force"] = true
		}
	}

	switch action {
	case "resolve", "fallback", "cancelPlayback":
		input["sequence"] = srv.rpcSeq.Add(1)
	case "danmaku", "preload", "cancelRead", "catalog", "categories", "suggestions", "recommendations", "metadata":
		sessRaw, _ := input["session"].(string)
		if sessRaw == "" && (action == "danmaku" || action == "preload") {
			sessRaw = action
		}
		if sessRaw != "" {
			prefix := token
			if len(prefix) > 12 {
				prefix = prefix[:12]
			}
			cleanScope := strings.Map(func(r rune) rune {
				if (r >= '0' && r <= '9') || (r >= 'a' && r <= 'z') || (r >= 'A' && r <= 'Z') || strings.ContainsRune("-_:.", r) {
					return r
				}
				return '_'
			}, sessRaw)
			if len(cleanScope) > 96 {
				cleanScope = cleanScope[:96]
			}
			input["session"] = prefix + ":" + cleanScope
			input["sequence"] = srv.rpcSeq.Add(1)
		}
	}

	prevFallbackSession := ""
	if action == "fallback" {
		prevFallbackSession, _ = input["session"].(string)
	}

	if action == "release" {
		if sess, _ := input["session"].(string); sess != "" {
			srv.dropCachedPlan(sess)
		}
	}

	rawReq, err := json.Marshal(input)
	if err != nil {
		writeAPIError(writer, http.StatusBadRequest, errors.New("参数编码失败"))
		return
	}
	rawResp := core.NativeRequest(string(rawReq))

	var envelope map[string]any
	dec := json.NewDecoder(strings.NewReader(rawResp))
	dec.UseNumber()
	if err := dec.Decode(&envelope); err != nil {
		writeAPIError(writer, http.StatusInternalServerError, errors.New("核心返回数据解析失败"))
		return
	}
	if ok, _ := envelope["ok"].(bool); !ok {
		writeJSONResponse(writer, http.StatusOK, envelope)
		return
	}

	dataMap, _ := envelope["data"].(map[string]any)
	switch action {
	case "resolve", "fallback", "preload", "localPlayback":
		if dataMap != nil {
			dramaID := ""
			if dm, ok := input["drama"].(map[string]any); ok {
				dramaID, _ = dm["id"].(string)
			}
			idx := intFromAny(input["index"])
			srv.rewritePlanForBrowser(dataMap, dramaID, idx)
			if action == "fallback" && prevFallbackSession != "" {
				newSess, _ := dataMap["session"].(string)
				if newSess != "" && newSess != prevFallbackSession {
					srv.dropCachedPlan(prevFallbackSession)
					relBytes, _ := json.Marshal(map[string]any{"action": "release", "session": prevFallbackSession})
					_ = core.NativeRequest(string(relBytes))
				}
			}
		}
	case "detail":
		if dataMap != nil {
			if dm, ok := dataMap["drama"].(map[string]any); ok {
				srv.rememberDramaCover(dm)
				srv.store.observeDramaMetadata(dm, token)
			}
		}
	case "metadata":
		if dataMap != nil {
			if dm, ok := dataMap["drama"].(map[string]any); ok {
				srv.rememberDramaCover(dm)
				srv.store.observeDramaMetadata(dm, "")
			} else {
				srv.rememberDramaCover(dataMap)
				srv.store.observeDramaMetadata(dataMap, "")
			}
		}
	case "catalog", "recommendations", "cached", "cachedRecommendations":
		if dataMap != nil {
			if items, ok := dataMap["items"].([]any); ok {
				for _, item := range items {
					if dm, ok := item.(map[string]any); ok {
						srv.rememberDramaCover(dm)
						if action == "catalog" || action == "recommendations" {
							srv.store.observeDramaMetadata(dm, "")
						}
					}
				}
			}
		}
	case "rankings":
		if dataMap != nil {
			if items, ok := dataMap["items"].([]any); ok {
				for _, item := range items {
					if row, ok := item.(map[string]any); ok {
						if dm, ok := row["drama"].(map[string]any); ok {
							srv.rememberDramaCover(dm)
						}
					}
				}
			}
		}
	case "rankingBoards":
		if dataMap != nil {
			snap := srv.store.stateSnapshot(token)
			allowedSet := map[string]bool{}
			if srcs, ok := snap["sources"].([]sourceSiteInfo); ok {
				for _, s := range srcs {
					allowedSet[s.ID] = true
				}
			}
			if items, ok := dataMap["items"].([]any); ok {
				var filtered []any
				for _, row := range items {
					if m, ok := row.(map[string]any); ok {
						src, _ := m["source"].(string)
						if allowedSet[src] {
							filtered = append(filtered, m)
						}
					}
				}
				dataMap["items"] = filtered
			}
		}
	case "downloads":
		if dataMap != nil {
			snap := srv.store.stateSnapshot(token)
			allowedSet := map[string]bool{}
			if srcs, ok := snap["permittedSources"].([]sourceSiteInfo); ok {
				for _, s := range srcs {
					allowedSet[s.ID] = true
				}
			}
			if jobs, ok := dataMap["jobs"].([]any); ok {
				var filtered []any
				for _, row := range jobs {
					if m, ok := row.(map[string]any); ok {
						dm, _ := m["drama"].(map[string]any)
						src, _ := dm["source"].(string)
						if allowedSet[src] {
							filtered = append(filtered, m)
						}
					}
				}
				dataMap["jobs"] = filtered
			}
		}
	case "enqueueDownloads", "controlDownloads", "controlDownloadBatch":
		srv.converter.trigger()
	}

	writeJSONResponse(writer, http.StatusOK, envelope)
}

const syntheticPosterSVG = `<svg xmlns="http://www.w3.org/2000/svg" viewBox="0 0 360 480"><defs><linearGradient id="g" x1="0" y1="0" x2="1" y2="1"><stop offset="0%" stop-color="#2a3140"/><stop offset="100%" stop-color="#171b24"/></linearGradient></defs><rect width="360" height="480" fill="url(#g)"/><circle cx="180" cy="220" r="44" fill="#ffffff" fill-opacity="0.1"/><polygon points="168,198 168,242 204,220" fill="#ffffff" fill-opacity="0.4"/></svg>`

func (srv *webAppServer) rememberDramaCover(dm map[string]any) {
	if dm == nil {
		return
	}
	id, _ := dm["id"].(string)
	cov, _ := dm["cover"].(string)
	id = strings.TrimSpace(id)
	cov = strings.TrimSpace(cov)
	if id == "" || cov == "" {
		return
	}
	srv.coverMu.Lock()
	if len(srv.knownCovers) >= 4000 {
		for k := range srv.knownCovers {
			delete(srv.knownCovers, k)
			if len(srv.knownCovers) < 3500 {
				break
			}
		}
	}
	srv.knownCovers[id] = cov
	srv.coverMu.Unlock()
}

func (srv *webAppServer) lookupDramaCover(id string) string {
	if id == "" {
		return ""
	}
	srv.coverMu.Lock()
	cov := srv.knownCovers[id]
	srv.coverMu.Unlock()
	return cov
}

func serveImageFile(writer http.ResponseWriter, request *http.Request, filePath, contentType string) {
	f, err := os.Open(filePath)
	if err != nil {
		http.NotFound(writer, request)
		return
	}
	defer f.Close()
	info, err := f.Stat()
	if err != nil {
		http.NotFound(writer, request)
		return
	}
	writer.Header().Set("Content-Type", contentType)
	writer.Header().Set("Cache-Control", "private, max-age=86400")
	http.ServeContent(writer, request, filepath.Base(filePath), info.ModTime(), f)
}

func (srv *webAppServer) handleCover(writer http.ResponseWriter, request *http.Request) {
	if request.Method != http.MethodGet && request.Method != http.MethodHead {
		writer.WriteHeader(http.StatusMethodNotAllowed)
		return
	}
	if srv.disableImages || request.URL.Query().Get("placeholder") == "1" {
		writer.Header().Set("Content-Type", "image/svg+xml")
		writer.Header().Set("Cache-Control", "public, max-age=3600")
		_, _ = io.WriteString(writer, syntheticPosterSVG)
		return
	}
	token := srv.browserToken(writer, request)
	q := request.URL.Query()
	id := strings.TrimSpace(q.Get("id"))
	source := strings.TrimSpace(q.Get("source"))
	coverURL := strings.TrimSpace(q.Get("cover"))
	title := strings.TrimSpace(q.Get("title"))
	force := q.Get("force") == "1" || q.Get("force") == "true"
	reqFormat := strings.ToLower(strings.TrimSpace(q.Get("format")))
	isHydrator := request.Header.Get("X-Cover-Fetch") == "1" || reqFormat != ""

	if id == "" || source == "" {
		http.NotFound(writer, request)
		return
	}
	if err := srv.store.authorizeAction(token, source, false, false); err != nil {
		http.Error(writer, err.Error(), http.StatusForbidden)
		return
	}
	if coverURL == "" {
		coverURL = srv.lookupDramaCover(id)
	}

	rawDrama, _ := json.Marshal(map[string]any{
		"metadataSchema": 1,
		"id":             id,
		"source":         source,
		"title":          title,
		"cover":          coverURL,
	})
	ctx, cancel := context.WithTimeout(request.Context(), 28*time.Second)
	defer cancel()
	filePath, contentType, err := core.LoadCoverFile(ctx, rawDrama, force)
	if err != nil {
		if isHydrator {
			writeAPIError(writer, http.StatusBadGateway, err)
			return
		}
		writer.Header().Set("Content-Type", "image/svg+xml")
		writer.Header().Set("Cache-Control", "no-store")
		_, _ = io.WriteString(writer, syntheticPosterSVG)
		return
	}

	if contentType == "image/heic" {
		compatKey, _ := srv.heicCompat.compatKeyForFile(filePath)
		if !force && compatKey != "" {
			if jpgPath, ok := srv.heicCompat.lookup(compatKey); ok && reqFormat == "" {
				serveImageFile(writer, request, jpgPath, "image/jpeg")
				return
			}
		}
		if ffmpegBin := findFFmpegBinary(); ffmpegBin != "" && compatKey != "" && reqFormat == "" {
			if jpgPath, cErr := srv.heicCompat.convertWithFFmpeg(ctx, ffmpegBin, rawDrama, compatKey); cErr == nil && jpgPath != "" {
				serveImageFile(writer, request, jpgPath, "image/jpeg")
				return
			}
		}
		if reqFormat == "hevc-frame" {
			frame, fErr := core.ExtractHEICWebFrame(filePath)
			if fErr != nil {
				writeAPIError(writer, http.StatusUnprocessableEntity, fErr)
				return
			}
			writeJSONResponse(writer, http.StatusOK, map[string]any{
				"ok":          true,
				"width":       frame.Width,
				"height":      frame.Height,
				"transforms":  frame.Transforms,
				"codec":       frame.Codec,
				"hvcc":        base64.StdEncoding.EncodeToString(frame.HVCC),
				"sample":      base64.StdEncoding.EncodeToString(frame.Sample),
				"compatToken": srv.heicCompat.issueUploadToken(compatKey),
			})
			return
		}
		if reqFormat == "hevc-mp4" {
			frame, fErr := core.ExtractHEICWebFrame(filePath)
			if fErr != nil {
				writeAPIError(writer, http.StatusUnprocessableEntity, fErr)
				return
			}
			mp4Data := buildSingleFrameHEVCMP4(frame)
			if tok := srv.heicCompat.issueUploadToken(compatKey); tok != "" {
				writer.Header().Set("X-Cover-Compat-Token", tok)
			}
			if len(frame.Transforms) > 0 {
				writer.Header().Set("X-HEIC-Transforms", strings.Join(frame.Transforms, ","))
			}
			writer.Header().Set("Content-Type", "video/mp4")
			writer.Header().Set("Accept-Ranges", "bytes")
			writer.Header().Set("Cache-Control", "no-store")
			http.ServeContent(writer, request, "cover.mp4", time.Time{}, bytes.NewReader(mp4Data))
			return
		}
		if !isHydrator && !strings.Contains(strings.ToLower(request.Header.Get("Accept")), "image/heic") {
			writer.Header().Set("Content-Type", "image/svg+xml")
			writer.Header().Set("Cache-Control", "no-store")
			_, _ = io.WriteString(writer, syntheticPosterSVG)
			return
		}
		if tok := srv.heicCompat.issueUploadToken(compatKey); tok != "" {
			writer.Header().Set("X-Cover-Compat-Token", tok)
		}
		f, oErr := os.Open(filePath)
		if oErr != nil {
			http.NotFound(writer, request)
			return
		}
		defer f.Close()
		info, sErr := f.Stat()
		if sErr != nil {
			http.NotFound(writer, request)
			return
		}
		writer.Header().Set("Content-Type", "image/heic")
		writer.Header().Set("Cache-Control", "no-store")
		http.ServeContent(writer, request, filepath.Base(filePath), info.ModTime(), f)
		return
	}

	serveImageFile(writer, request, filePath, contentType)
}

func (srv *webAppServer) handleCoverCompatible(writer http.ResponseWriter, request *http.Request) {
	if request.Method != http.MethodPost {
		writer.WriteHeader(http.StatusMethodNotAllowed)
		return
	}
	token := srv.browserToken(writer, request)
	if err := srv.store.authorizeAction(token, "", false, false); err != nil {
		writeAPIError(writer, http.StatusForbidden, err)
		return
	}
	uploadToken := strings.TrimSpace(request.URL.Query().Get("token"))
	defer request.Body.Close()
	body, err := io.ReadAll(io.LimitReader(request.Body, 4<<20+1))
	if err != nil || len(body) > 4<<20 {
		writeAPIError(writer, http.StatusBadRequest, errors.New("海报图片过大或读取失败"))
		return
	}
	if _, err := srv.heicCompat.saveUploadedJPEG(uploadToken, body); err != nil {
		writeAPIError(writer, http.StatusBadRequest, err)
		return
	}
	writeJSONResponse(writer, http.StatusOK, map[string]any{"ok": true})
}

func (srv *webAppServer) handleStream(writer http.ResponseWriter, request *http.Request) {
	if request.Method != http.MethodGet && request.Method != http.MethodHead {
		writer.WriteHeader(http.StatusMethodNotAllowed)
		return
	}
	origin := core.StreamServerOrigin()
	if origin == "" {
		http.Error(writer, "播放流尚未初始化", http.StatusGone)
		return
	}
	rel := strings.TrimPrefix(request.URL.Path, "/api/stream/")
	parts := strings.Split(rel, "/")
	if len(parts) != 2 || parts[0] == "" || parts[1] == "" || strings.Contains(rel, "..") {
		http.NotFound(writer, request)
		return
	}
	streamToken, assetName := parts[0], parts[1]
	upstreamURL := origin + "/" + streamToken + "/" + assetName

	if request.URL.Query().Get("transcode") == "h264" {
		ffmpeg := findFFmpegBinary()
		if ffmpeg == "" {
			http.Error(writer, "当前环境未安装 FFmpeg，无法进行实时兼容转码", http.StatusNotImplemented)
			return
		}
		writer.Header().Set("Content-Type", "video/mp4")
		writer.Header().Set("Cache-Control", "no-store")
		if request.Method == http.MethodHead {
			writer.WriteHeader(http.StatusOK)
			return
		}
		var args []string
		args = append(args, "-v", "error")
		if key := srv.cenc.keyForToken(streamToken); len(key) == 16 {
			args = append(args, "-decryption_key", hex.EncodeToString(key))
		}
		if strings.HasSuffix(strings.ToLower(assetName), ".m3u8") {
			args = append(args, "-allowed_extensions", "ALL", "-extension_picky", "0")
		}
		if startSec, sErr := strconv.ParseFloat(request.URL.Query().Get("start"), 64); sErr == nil && startSec > 0.5 {
			args = append(args, "-ss", fmt.Sprintf("%.2f", startSec))
		}
		args = append(args,
			"-i", upstreamURL,
			"-map", "0:v:0",
			"-map", "0:a:0?",
			"-c:v", "libx264",
			"-preset", "ultrafast",
			"-crf", "23",
			"-pix_fmt", "yuv420p",
			"-c:a", "aac",
			"-b:a", "128k",
			"-movflags", "frag_keyframe+empty_moov+default_base_moof",
			"-f", "mp4",
			"pipe:1",
		)
		cmd := exec.CommandContext(request.Context(), ffmpeg, args...)
		cmd.Stdout = writer
		_ = cmd.Run()
		return
	}

	if key := srv.cenc.keyForToken(streamToken); len(key) == 16 && !strings.HasSuffix(strings.ToLower(assetName), ".m3u8") {
		srv.cenc.serveDecrypted(writer, request, streamToken+"/"+assetName, upstreamURL, key)
		return
	}

	upReq, err := http.NewRequestWithContext(request.Context(), request.Method, upstreamURL, nil)
	if err != nil {
		http.Error(writer, "无效的流请求", http.StatusBadGateway)
		return
	}
	for _, hdr := range []string{"Range", "If-Range"} {
		if v := request.Header.Get(hdr); v != "" {
			upReq.Header.Set(hdr, v)
		}
	}

	resp, err := srv.streamClient.Do(upReq)
	if err != nil {
		http.Error(writer, "连接本地播放代理失败", http.StatusBadGateway)
		return
	}
	defer resp.Body.Close()

	ct := strings.ToLower(resp.Header.Get("Content-Type"))
	isPlaylist := strings.Contains(ct, "mpegurl") || strings.HasSuffix(strings.ToLower(assetName), ".m3u8")
	if isPlaylist && resp.StatusCode == http.StatusOK && request.Method == http.MethodGet {
		body, rErr := io.ReadAll(io.LimitReader(resp.Body, 4<<20))
		if rErr != nil {
			http.Error(writer, "读取播放列表失败", http.StatusBadGateway)
			return
		}
		rewritten := strings.ReplaceAll(string(body), origin+"/", "/api/stream/")
		if playlistHasEncryptedKey(rewritten) {
			srv.markEncryptedHLS(streamToken)
		}
		writer.Header().Set("Content-Type", "application/vnd.apple.mpegurl")
		if strings.Contains(rewritten, "#EXT-X-ENDLIST") {
			writer.Header().Set("Cache-Control", "private, max-age=300")
		} else {
			writer.Header().Set("Cache-Control", "no-store")
		}
		writer.WriteHeader(http.StatusOK)
		_, _ = io.WriteString(writer, rewritten)
		return
	}

	if strings.HasSuffix(strings.ToLower(assetName), ".ts") && resp.StatusCode == http.StatusOK && request.Method == http.MethodGet {
		rawSeg, sErr := io.ReadAll(io.LimitReader(resp.Body, 32<<20))
		if sErr != nil {
			http.Error(writer, "读取媒体分片失败", http.StatusBadGateway)
			return
		}
		cleaned := rawSeg
		if !srv.isEncryptedHLS(streamToken) {
			cleaned = stripDisguisedTSPrefix(rawSeg)
		}
		writer.Header().Set("Content-Type", "video/mp2t")
		writer.Header().Set("Cache-Control", "private, max-age=600")
		writer.Header().Set("Content-Length", strconv.Itoa(len(cleaned)))
		writer.WriteHeader(http.StatusOK)
		_, _ = writer.Write(cleaned)
		return
	}

	writer.Header().Set("Cache-Control", "private, max-age=600")
	for _, hdr := range []string{"Content-Type", "Content-Length", "Content-Range", "Accept-Ranges", "ETag", "Last-Modified"} {
		if v := resp.Header.Get(hdr); v != "" {
			writer.Header().Set(hdr, v)
		}
	}
	writer.WriteHeader(resp.StatusCode)
	if request.Method == http.MethodGet {
		_, _ = io.Copy(writer, resp.Body)
	}
}

func (srv *webAppServer) handleLocalMedia(writer http.ResponseWriter, request *http.Request) {
	if request.Method != http.MethodGet && request.Method != http.MethodHead {
		writer.WriteHeader(http.StatusMethodNotAllowed)
		return
	}
	token := srv.browserToken(writer, request)
	dramaID := strings.TrimSpace(request.URL.Query().Get("id"))
	index, _ := strconv.Atoi(request.URL.Query().Get("index"))
	source := ""
	if idx := strings.IndexByte(dramaID, ':'); idx > 0 {
		source = dramaID[:idx]
	}
	if dramaID == "" || index < 1 || source == "" {
		http.NotFound(writer, request)
		return
	}
	if err := srv.store.authorizeAction(token, source, true, false); err != nil {
		http.Error(writer, err.Error(), http.StatusForbidden)
		return
	}
	filePath, _, _, found, err := core.LocalEpisodeFile(dramaID, index)
	if err != nil || !found || filePath == "" {
		http.NotFound(writer, request)
		return
	}
	f, err := os.Open(filePath)
	if err != nil {
		http.NotFound(writer, request)
		return
	}
	defer f.Close()
	info, err := f.Stat()
	if err != nil {
		http.NotFound(writer, request)
		return
	}
	writer.Header().Set("Content-Type", "video/mp4")
	writer.Header().Set("Accept-Ranges", "bytes")
	http.ServeContent(writer, request, filepath.Base(filePath), info.ModTime(), f)
}

func setAttachmentHeader(writer http.ResponseWriter, fileName string) {
	escaped := url.PathEscape(fileName)
	writer.Header().Set("Content-Disposition", fmt.Sprintf("attachment; filename=\"video.mp4\"; filename*=UTF-8''%s", escaped))
}

func (srv *webAppServer) handleDownloadFile(writer http.ResponseWriter, request *http.Request) {
	if request.Method != http.MethodGet && request.Method != http.MethodHead {
		writer.WriteHeader(http.StatusMethodNotAllowed)
		return
	}
	token := srv.browserToken(writer, request)
	jobID := strings.TrimPrefix(request.URL.Path, "/api/downloads/file/")
	if jobID == "" || strings.Contains(jobID, "/") {
		http.NotFound(writer, request)
		return
	}
	filePath, source, title, episode, err := core.CompletedDownloadFile(jobID)
	if err != nil {
		http.Error(writer, err.Error(), http.StatusNotFound)
		return
	}
	if err := srv.store.authorizeAction(token, source, true, false); err != nil {
		http.Error(writer, err.Error(), http.StatusForbidden)
		return
	}
	f, err := os.Open(filePath)
	if err != nil {
		http.NotFound(writer, request)
		return
	}
	defer f.Close()
	info, err := f.Stat()
	if err != nil {
		http.NotFound(writer, request)
		return
	}
	downloadName := fmt.Sprintf("%s-第%02d集.mp4", sanitizeAttachmentName(title), episode)
	writer.Header().Set("Content-Type", "video/mp4")
	setAttachmentHeader(writer, downloadName)
	http.ServeContent(writer, request, downloadName, info.ModTime(), f)
}

func (srv *webAppServer) handleDownloadMerge(writer http.ResponseWriter, request *http.Request) {
	if request.Method != http.MethodPost {
		writer.WriteHeader(http.StatusMethodNotAllowed)
		return
	}
	token := srv.browserToken(writer, request)
	var body struct {
		JobIDs []string `json:"jobIds"`
	}
	if err := readJSONBody(request, &body); err != nil {
		writeAPIError(writer, http.StatusBadRequest, err)
		return
	}
	if len(body.JobIDs) < 2 || len(body.JobIDs) > 200 {
		writeAPIError(writer, http.StatusBadRequest, errors.New("请选择 2 至 200 集已下载视频进行合并"))
		return
	}

	seen := map[string]bool{}
	var episodes []mergeInputEpisode
	for _, id := range body.JobIDs {
		if seen[id] {
			continue
		}
		seen[id] = true
		filePath, source, title, epNum, err := core.CompletedDownloadFile(id)
		if err != nil {
			writeAPIError(writer, http.StatusBadRequest, err)
			return
		}
		if err := srv.store.authorizeAction(token, source, true, false); err != nil {
			writeAPIError(writer, http.StatusForbidden, err)
			return
		}
		episodes = append(episodes, mergeInputEpisode{
			episode: epNum,
			title:   title,
			source:  source,
			path:    filePath,
		})
	}

	mergedPath, fileName, err := mergeEpisodesToTempMP4(request.Context(), filepath.Join(srv.tempDir, "merge"), episodes)
	if err != nil {
		writeAPIError(writer, http.StatusBadRequest, err)
		return
	}
	defer os.Remove(mergedPath)

	f, err := os.Open(mergedPath)
	if err != nil {
		writeAPIError(writer, http.StatusInternalServerError, errors.New("无法读取合并后的视频"))
		return
	}
	defer f.Close()
	info, err := f.Stat()
	if err != nil {
		writeAPIError(writer, http.StatusInternalServerError, errors.New("无法读取合并后的视频信息"))
		return
	}
	writer.Header().Set("Content-Type", "video/mp4")
	writer.Header().Set("Content-Length", strconv.FormatInt(info.Size(), 10))
	setAttachmentHeader(writer, fileName)
	writer.WriteHeader(http.StatusOK)
	_, _ = io.Copy(writer, f)
}

func (srv *webAppServer) handleStoreState(writer http.ResponseWriter, request *http.Request) {
	token := srv.browserToken(writer, request)
	writeJSONResponse(writer, http.StatusOK, map[string]any{"ok": true, "state": srv.store.stateSnapshot(token)})
}

func (srv *webAppServer) handleSwitchProfile(writer http.ResponseWriter, request *http.Request) {
	if request.Method != http.MethodPost {
		writer.WriteHeader(http.StatusMethodNotAllowed)
		return
	}
	token := srv.browserToken(writer, request)
	var body struct {
		ID  string `json:"id"`
		Pin string `json:"pin"`
	}
	if err := readJSONBody(request, &body); err != nil {
		writeAPIError(writer, http.StatusBadRequest, err)
		return
	}
	state, err := srv.store.switchProfile(token, body.ID, body.Pin)
	if err != nil {
		writeAPIError(writer, http.StatusBadRequest, err)
		return
	}
	writeJSONResponse(writer, http.StatusOK, map[string]any{"ok": true, "state": state})
}

func (srv *webAppServer) handleLockProfile(writer http.ResponseWriter, request *http.Request) {
	if request.Method != http.MethodPost {
		writer.WriteHeader(http.StatusMethodNotAllowed)
		return
	}
	token := srv.browserToken(writer, request)
	writeJSONResponse(writer, http.StatusOK, map[string]any{"ok": true, "state": srv.store.lockProfile(token)})
}

func (srv *webAppServer) handleManageProfile(writer http.ResponseWriter, request *http.Request) {
	token := srv.browserToken(writer, request)
	switch request.Method {
	case http.MethodPost:
		var body struct {
			ID       string   `json:"id"`
			Name     string   `json:"name"`
			Sources  []string `json:"sources"`
			Download bool     `json:"download"`
			Pin      *string  `json:"pin"`
		}
		if err := readJSONBody(request, &body); err != nil {
			writeAPIError(writer, http.StatusBadRequest, err)
			return
		}
		state, err := srv.store.saveProfile(token, body)
		if err != nil {
			writeAPIError(writer, http.StatusBadRequest, err)
			return
		}
		writeJSONResponse(writer, http.StatusOK, map[string]any{"ok": true, "state": state})
	case http.MethodDelete:
		id := strings.TrimSpace(request.URL.Query().Get("id"))
		state, err := srv.store.deleteProfile(token, id)
		if err != nil {
			writeAPIError(writer, http.StatusBadRequest, err)
			return
		}
		writeJSONResponse(writer, http.StatusOK, map[string]any{"ok": true, "state": state})
	default:
		writer.WriteHeader(http.StatusMethodNotAllowed)
	}
}

func (srv *webAppServer) handleGlobalSettings(writer http.ResponseWriter, request *http.Request) {
	if request.Method != http.MethodPost {
		writer.WriteHeader(http.StatusMethodNotAllowed)
		return
	}
	token := srv.browserToken(writer, request)
	var patch map[string]any
	if err := readJSONBody(request, &patch); err != nil {
		writeAPIError(writer, http.StatusBadRequest, err)
		return
	}
	state, err := srv.store.updateGlobalSettings(token, patch)
	if err != nil {
		writeAPIError(writer, http.StatusBadRequest, err)
		return
	}
	writeJSONResponse(writer, http.StatusOK, map[string]any{"ok": true, "state": state})
}

func (srv *webAppServer) handleUserSettings(writer http.ResponseWriter, request *http.Request) {
	if request.Method != http.MethodPost {
		writer.WriteHeader(http.StatusMethodNotAllowed)
		return
	}
	token := srv.browserToken(writer, request)
	var patch map[string]any
	if err := readJSONBody(request, &patch); err != nil {
		writeAPIError(writer, http.StatusBadRequest, err)
		return
	}
	state, err := srv.store.updateUserSettings(token, patch)
	if err != nil {
		writeAPIError(writer, http.StatusBadRequest, err)
		return
	}
	writeJSONResponse(writer, http.StatusOK, map[string]any{"ok": true, "state": state})
}

func (srv *webAppServer) handleWatchProgress(writer http.ResponseWriter, request *http.Request) {
	if request.Method != http.MethodPost {
		writer.WriteHeader(http.StatusMethodNotAllowed)
		return
	}
	token := srv.browserToken(writer, request)
	var entry map[string]any
	if err := readJSONBody(request, &entry); err != nil {
		writeAPIError(writer, http.StatusBadRequest, err)
		return
	}
	state, err := srv.store.saveWatchProgress(token, entry)
	if err != nil {
		writeAPIError(writer, http.StatusBadRequest, err)
		return
	}
	writeJSONResponse(writer, http.StatusOK, map[string]any{"ok": true, "state": state})
}

func (srv *webAppServer) handleHistoryDelete(writer http.ResponseWriter, request *http.Request) {
	if request.Method != http.MethodPost {
		writer.WriteHeader(http.StatusMethodNotAllowed)
		return
	}
	token := srv.browserToken(writer, request)
	var body struct {
		IDs []string `json:"ids"`
	}
	if err := readJSONBody(request, &body); err != nil {
		writeAPIError(writer, http.StatusBadRequest, err)
		return
	}
	state, err := srv.store.deleteHistoryItems(token, body.IDs)
	if err != nil {
		writeAPIError(writer, http.StatusBadRequest, err)
		return
	}
	writeJSONResponse(writer, http.StatusOK, map[string]any{"ok": true, "state": state})
}

func (srv *webAppServer) handleFollow(writer http.ResponseWriter, request *http.Request) {
	if request.Method != http.MethodPost {
		writer.WriteHeader(http.StatusMethodNotAllowed)
		return
	}
	token := srv.browserToken(writer, request)
	var body struct {
		Drama    map[string]any `json:"drama"`
		Follow   bool           `json:"follow"`
		BatchIDs []string       `json:"ids"`
		MarkRead string         `json:"markRead"`
		Episodes int            `json:"episodes"`
	}
	if err := readJSONBody(request, &body); err != nil {
		writeAPIError(writer, http.StatusBadRequest, err)
		return
	}
	if body.MarkRead != "" {
		srv.store.markFollowRead(token, body.MarkRead, body.Episodes)
		writeJSONResponse(writer, http.StatusOK, map[string]any{"ok": true, "state": srv.store.stateSnapshot(token)})
		return
	}
	if len(body.BatchIDs) > 0 {
		state, err := srv.store.unfollowBatch(token, body.BatchIDs)
		if err != nil {
			writeAPIError(writer, http.StatusBadRequest, err)
			return
		}
		writeJSONResponse(writer, http.StatusOK, map[string]any{"ok": true, "state": state})
		return
	}
	state, err := srv.store.toggleFollow(token, body.Drama, body.Follow)
	if err != nil {
		writeAPIError(writer, http.StatusBadRequest, err)
		return
	}
	writeJSONResponse(writer, http.StatusOK, map[string]any{"ok": true, "state": state})
}

func (srv *webAppServer) handleFollowCheck(writer http.ResponseWriter, request *http.Request) {
	if request.Method != http.MethodPost {
		writer.WriteHeader(http.StatusMethodNotAllowed)
		return
	}
	token := srv.browserToken(writer, request)
	ctx, cancel := context.WithTimeout(request.Context(), 90*time.Second)
	defer cancel()
	updated, err := srv.store.checkFollowUpdatesNow(ctx, token, 30)
	if err != nil {
		writeAPIError(writer, http.StatusBadRequest, err)
		return
	}
	writeJSONResponse(writer, http.StatusOK, map[string]any{
		"ok":      true,
		"updated": updated,
		"state":   srv.store.stateSnapshot(token),
	})
}

func (srv *webAppServer) handleBackup(writer http.ResponseWriter, request *http.Request) {
	token := srv.browserToken(writer, request)
	switch request.Method {
	case http.MethodGet:
		data, err := srv.store.exportBackup(token)
		if err != nil {
			writeAPIError(writer, http.StatusForbidden, err)
			return
		}
		fileName := fmt.Sprintf("zhenguojian-backup-%s.json", time.Now().Format("20060102-1504"))
		writer.Header().Set("Content-Type", "application/json; charset=utf-8")
		writer.Header().Set("Content-Disposition", fmt.Sprintf("attachment; filename=\"%s\"", fileName))
		writer.WriteHeader(http.StatusOK)
		_, _ = writer.Write(data)
	case http.MethodPost:
		defer request.Body.Close()
		raw, err := io.ReadAll(io.LimitReader(request.Body, 8<<20+1024))
		if err != nil {
			writeAPIError(writer, http.StatusBadRequest, errors.New("无法读取备份文件"))
			return
		}
		state, err := srv.store.importBackup(token, raw)
		if err != nil {
			writeAPIError(writer, http.StatusBadRequest, err)
			return
		}
		writeJSONResponse(writer, http.StatusOK, map[string]any{"ok": true, "state": state})
	default:
		writer.WriteHeader(http.StatusMethodNotAllowed)
	}
}

func (srv *webAppServer) handleLogs(writer http.ResponseWriter, request *http.Request) {
	token := srv.browserToken(writer, request)
	if err := srv.store.authorizeAction(token, "", false, false); err != nil {
		writeAPIError(writer, http.StatusForbidden, err)
		return
	}
	switch request.Method {
	case http.MethodGet:
		writeJSONResponse(writer, http.StatusOK, map[string]any{
			"ok":    true,
			"lines": srv.diary.snapshot(),
		})
	case http.MethodPost:
		var body struct {
			Message string `json:"message"`
			Clear   bool   `json:"clear"`
		}
		if err := readJSONBody(request, &body); err != nil {
			writeAPIError(writer, http.StatusBadRequest, err)
			return
		}
		if body.Clear {
			srv.diary.clearMemory()
		} else if strings.TrimSpace(body.Message) != "" {
			srv.diary.add(body.Message)
		}
		writeJSONResponse(writer, http.StatusOK, map[string]any{"ok": true})
	default:
		writer.WriteHeader(http.StatusMethodNotAllowed)
	}
}

func (srv *webAppServer) handleStaticUI(writer http.ResponseWriter, request *http.Request) {
	if strings.HasPrefix(request.URL.Path, "/api/") {
		http.NotFound(writer, request)
		return
	}
	cleanPath := path.Clean(strings.TrimPrefix(request.URL.Path, "/"))
	if cleanPath == "." || cleanPath == "" {
		cleanPath = "index.html"
	}

	var fileSystem fs.FS
	if srv.uiOverrideDir != "" {
		fileSystem = os.DirFS(srv.uiOverrideDir)
	} else {
		sub, err := fs.Sub(embeddedUIFS, "ui")
		if err != nil {
			http.Error(writer, "UI 资源加载失败", http.StatusInternalServerError)
			return
		}
		fileSystem = sub
	}

	data, err := fs.ReadFile(fileSystem, cleanPath)
	if err != nil {
		data, err = fs.ReadFile(fileSystem, "index.html")
		if err != nil {
			http.NotFound(writer, request)
			return
		}
		cleanPath = "index.html"
	}

	ext := strings.ToLower(path.Ext(cleanPath))
	ct := mime.TypeByExtension(ext)
	switch ext {
	case ".html":
		ct = "text/html; charset=utf-8"
		writer.Header().Set("Cache-Control", "no-cache")
	case ".css":
		ct = "text/css; charset=utf-8"
		writer.Header().Set("Cache-Control", "no-cache")
	case ".js":
		ct = "application/javascript; charset=utf-8"
		writer.Header().Set("Cache-Control", "no-cache")
		if cleanPath == "sw.js" {
			writer.Header().Set("Service-Worker-Allowed", "/")
		}
	case ".svg":
		ct = "image/svg+xml"
		writer.Header().Set("Cache-Control", "public, max-age=86400")
	case ".png":
		ct = "image/png"
		writer.Header().Set("Cache-Control", "public, max-age=86400")
	case ".json":
		ct = "application/manifest+json; charset=utf-8"
		writer.Header().Set("Cache-Control", "no-cache")
		if cleanPath == "manifest.json" && !core.AllSourcesEnabled() {
			data = bytes.ReplaceAll(data, []byte("真果鉴"), []byte("红果鉴"))
		}
	}
	if ct != "" {
		writer.Header().Set("Content-Type", ct)
	}
	writer.WriteHeader(http.StatusOK)
	if request.Method != http.MethodHead {
		_, _ = writer.Write(data)
	}
}
