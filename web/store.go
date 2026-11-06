package main

import (
	"context"
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha256"
	"crypto/subtle"
	"duanjuapp/native/core"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"sync"
	"time"
	"unicode/utf8"
)

type sourceSiteInfo struct {
	ID                string `json:"id"`
	Name              string `json:"name"`
	Description       string `json:"description"`
	GroupID           string `json:"groupId"`
	GroupName         string `json:"groupName"`
	EntryName         string `json:"entryName"`
	Restricted        bool   `json:"restricted"`
	OnlineSearch      bool   `json:"onlineSearch"`
	SearchSuggestions bool   `json:"searchSuggestions"`
}

var allKnownSources = []sourceSiteInfo{
	{ID: "hongguo", Name: "红果", Description: "短剧 · 漫剧 · AI 剧", GroupID: "hongguo", GroupName: "红果", EntryName: "红果", Restricted: false, OnlineSearch: true, SearchSuggestions: true},
	{ID: "hanxiaoquan", Name: "韩小圈", Description: "韩剧 · 韩国电影 · 综艺动漫", GroupID: "hanxiaoquan", GroupName: "韩小圈", EntryName: "韩小圈", Restricted: false, OnlineSearch: true},
	{ID: "guipian", Name: "鬼片", Description: "鬼片 · 电视剧 · 动漫", GroupID: "guipian", GroupName: "鬼片", EntryName: "鬼片", Restricted: false, OnlineSearch: true},
	{ID: "sorani", Name: "青空", Description: "番剧 · 剧场动画 · 特摄", GroupID: "sorani", GroupName: "青空", EntryName: "青空", Restricted: false, OnlineSearch: true},
	{ID: "huangdou", Name: "黄豆", Description: "精选短剧", GroupID: "huangdou", GroupName: "黄豆", EntryName: "黄豆", Restricted: true, OnlineSearch: false},
	{ID: "huangju", Name: "剧果", Description: "热门 · 最新 · 分类短剧", GroupID: "huangju", GroupName: "剧果", EntryName: "剧果", Restricted: true, OnlineSearch: true},
	{ID: "yeguo", Name: "野果", Description: "分类短剧 · 在线搜索", GroupID: "yeguo", GroupName: "野果", EntryName: "野果", Restricted: true, OnlineSearch: true},
	{ID: "dsd", Name: "帝果", Description: "分类视频 · 在线搜索", GroupID: "dsd", GroupName: "帝果", EntryName: "帝果", Restricted: true, OnlineSearch: true},
	{ID: "huangguo-video", Name: "黄果视频", Description: "视频剧集", GroupID: "huangguo", GroupName: "黄果", EntryName: "视频", Restricted: true, OnlineSearch: false},
	{ID: "huangguoai", Name: "黄果 AI", Description: "AI 短剧", GroupID: "huangguo", GroupName: "黄果", EntryName: "AI", Restricted: true, OnlineSearch: true},
	{ID: "cloudfront", Name: "黄果旧版", Description: "旧 API 剧库", GroupID: "huangguo", GroupName: "黄果", EntryName: "旧版", Restricted: true, OnlineSearch: false},
}

func availableSourceSites() []sourceSiteInfo {
	if core.AllSourcesEnabled() {
		out := make([]sourceSiteInfo, len(allKnownSources))
		copy(out, allKnownSources)
		return out
	}
	return []sourceSiteInfo{allKnownSources[0]}
}

func isKnownSourceID(id string) bool {
	for _, s := range allKnownSources {
		if s.ID == id {
			return true
		}
	}
	return false
}

func isAvailableSourceID(id string) bool {
	for _, s := range availableSourceSites() {
		if s.ID == id {
			return true
		}
	}
	return false
}

func isRestrictedSourceID(id string) bool {
	for _, s := range allKnownSources {
		if s.ID == id {
			return s.Restricted
		}
	}
	return false
}

type webDiary struct {
	mu      sync.Mutex
	entries []string
}

func newWebDiary() *webDiary {
	return &webDiary{}
}

func (d *webDiary) add(message string) {
	stamp := time.Now().Format("15:04:05.000")
	line := fmt.Sprintf("[%s] %s", stamp, strings.TrimSpace(message))
	d.mu.Lock()
	d.entries = append(d.entries, line)
	if len(d.entries) > 500 {
		d.entries = append([]string(nil), d.entries[len(d.entries)-500:]...)
	}
	d.mu.Unlock()
}

func (d *webDiary) snapshot() []string {
	d.mu.Lock()
	mem := append([]string(nil), d.entries...)
	d.mu.Unlock()

	logPath := core.DiagnosticLogPath()
	if logPath == "" {
		return mem
	}
	data, err := os.ReadFile(logPath)
	if err != nil || len(data) == 0 {
		return mem
	}
	if len(data) > 128<<10 {
		data = data[len(data)-(128<<10):]
	}
	lines := strings.Split(strings.ReplaceAll(string(data), "\r\n", "\n"), "\n")
	var out []string
	for _, line := range lines {
		trimmed := strings.TrimSpace(line)
		if trimmed != "" {
			out = append(out, "[Core] "+trimmed)
		}
	}
	if len(out) > 300 {
		out = out[len(out)-300:]
	}
	return append(out, mem...)
}

func (d *webDiary) clearMemory() {
	d.mu.Lock()
	d.entries = nil
	d.mu.Unlock()
}

type localFileStore struct {
	root string
}

func newLocalFileStore(root string) *localFileStore {
	return &localFileStore{root: root}
}

func (fs *localFileStore) complete(dir string) bool {
	for _, name := range []string{"users.json", "users.json.bak"} {
		if info, err := os.Stat(filepath.Join(dir, name)); err == nil && !info.IsDir() {
			return true
		}
	}
	return false
}

func (fs *localFileStore) ensureReady() bool {
	staging := fs.root + ".new"
	retired := fs.root + ".old"
	if !fs.complete(fs.root) {
		for _, candidate := range []string{staging, retired} {
			if !fs.complete(candidate) {
				continue
			}
			_ = os.RemoveAll(fs.root)
			_ = os.Rename(candidate, fs.root)
			break
		}
	}
	_ = os.RemoveAll(staging)
	_ = os.RemoveAll(retired)
	return fs.complete(fs.root)
}

func (fs *localFileStore) readJSON(relPath string) (any, bool, error) {
	primary := filepath.Join(fs.root, filepath.FromSlash(relPath))
	backup := primary + ".bak"
	found := false
	for _, candidate := range []string{primary, backup} {
		data, err := os.ReadFile(candidate)
		if os.IsNotExist(err) {
			continue
		}
		found = true
		if err != nil {
			continue
		}
		var val any
		dec := json.NewDecoder(strings.NewReader(string(data)))
		if dec.Decode(&val) == nil {
			return val, true, nil
		}
	}
	if found {
		return nil, true, errors.New(relPath + " 无法读取")
	}
	return nil, false, nil
}

func (fs *localFileStore) writeToDir(dir, relPath string, value any) error {
	target := filepath.Join(dir, filepath.FromSlash(relPath))
	if err := os.MkdirAll(filepath.Dir(target), 0700); err != nil {
		return err
	}
	encoded, err := json.Marshal(value)
	if err != nil {
		return err
	}
	tmp := target + ".tmp"
	if err := os.WriteFile(tmp, encoded, 0600); err != nil {
		_ = os.Remove(tmp)
		return err
	}
	if _, statErr := os.Stat(target); statErr == nil {
		_ = os.Remove(target + ".bak")
		_ = os.Rename(target, target+".bak")
	}
	return os.Rename(tmp, target)
}

func (fs *localFileStore) writeJSON(relPath string, value any) error {
	return fs.writeToDir(fs.root, relPath, value)
}

func (fs *localFileStore) replaceAll(contents map[string]any) error {
	staging := fs.root + ".new"
	retired := fs.root + ".old"
	_ = os.RemoveAll(staging)
	for relPath, val := range contents {
		if relPath == "users.json" {
			continue
		}
		if err := fs.writeToDir(staging, relPath, val); err != nil {
			return err
		}
	}
	if err := fs.writeToDir(staging, "users.json", contents["users.json"]); err != nil {
		return err
	}
	_ = os.RemoveAll(retired)
	if _, err := os.Stat(fs.root); err == nil {
		if err := os.Rename(fs.root, retired); err != nil {
			return err
		}
	}
	if err := os.Rename(staging, fs.root); err != nil {
		_ = os.Rename(retired, fs.root)
		return err
	}
	_ = os.RemoveAll(retired)
	return nil
}

func (fs *localFileStore) removeSubdir(relDir string) {
	_ = os.RemoveAll(filepath.Join(fs.root, filepath.FromSlash(relDir)))
}

type userProfile struct {
	ID       string   `json:"id"`
	Name     string   `json:"name"`
	Admin    bool     `json:"admin"`
	Sources  []string `json:"sources"`
	Download bool     `json:"download"`
	Salt     string   `json:"salt"`
	PinHash  string   `json:"pinHash"`
}

func (p userProfile) protected() bool {
	return p.Salt != "" && p.PinHash != ""
}

func (p userProfile) allows(source string) bool {
	if p.Admin {
		return true
	}
	for _, s := range p.Sources {
		if s == source {
			return true
		}
	}
	return false
}

func (p userProfile) publicMap() map[string]any {
	return map[string]any{
		"id":        p.ID,
		"name":      p.Name,
		"admin":     p.Admin,
		"sources":   p.Sources,
		"download":  p.Download,
		"protected": p.protected(),
	}
}

var (
	profileIDPattern = regexp.MustCompile(`^[a-zA-Z0-9_-]{1,64}$`)
	saltPattern      = regexp.MustCompile(`^[a-f0-9]{32}$`)
	pinHashPattern   = regexp.MustCompile(`^[a-f0-9]{64}$`)
	validDisplayMode = map[string]bool{"auto": true, "television": true, "phone": true, "pad": true, "window": true}
	validThemeMode   = map[string]bool{"light": true, "dark": true, "system": true}
	validThemeColors = map[string]bool{"orange": true, "rose": true, "amber": true, "teal": true, "blue": true, "violet": true}
	backupBoolFlags  = []string{"checkUpdates", "shareUsage", "exportAfterDownload", "deleteAfterAutoExport", "deleteAfterManualExport", "televisionKeyboard"}
)

func randomHex16() string {
	var b [16]byte
	_, _ = rand.Read(b[:])
	return hex.EncodeToString(b[:])
}

func deriveProfilePin(pin, salt string) string {
	mac := hmac.New(sha256.New, []byte(pin))
	seed := make([]byte, len(salt)+4)
	copy(seed, salt)
	seed[len(salt)+3] = 1
	mac.Write(seed)
	u := mac.Sum(nil)
	out := make([]byte, len(u))
	copy(out, u)
	for i := 1; i < 120000; i++ {
		mac.Reset()
		mac.Write(u)
		u = mac.Sum(u[:0])
		for j := range out {
			out[j] ^= u[j]
		}
	}
	return hex.EncodeToString(out)
}

func verifyProfilePin(profile userProfile, pin string) bool {
	if !profile.protected() {
		return true
	}
	computed := deriveProfilePin(pin, profile.Salt)
	return subtle.ConstantTimeCompare([]byte(computed), []byte(profile.PinHash)) == 1
}

type followStateRecord struct {
	KnownEpisodes int            `json:"knownEpisodes"`
	ReadEpisodes  *int           `json:"readEpisodes"`
	SeriesSeasons map[string]any `json:"seriesSeasons,omitempty"`
}

type userLibrary struct {
	history      []map[string]any
	favorites    []map[string]any
	followStates map[string]followStateRecord
	followChecks map[string][2]int64
	settings     map[string]any
}

type browserSession struct {
	profileID string
	unlocked  bool
	updatedAt time.Time
}

type webStore struct {
	mu           sync.Mutex
	files        *localFileStore
	diary        *webDiary
	profiles     []userProfile
	defaultID    string
	forceLogin   *bool
	settings     map[string]any
	libraries    map[string]*userLibrary
	sessions     map[string]*browserSession
	pinFailures  int
	pinRetryTime time.Time
}

func openWebStore(dataDir string, diary *webDiary) (*webStore, error) {
	fs := newLocalFileStore(filepath.Join(dataDir, "store"))
	ws := &webStore{
		files:     fs,
		diary:     diary,
		settings:  map[string]any{},
		libraries: map[string]*userLibrary{},
		sessions:  map[string]*browserSession{},
	}
	if err := ws.initialize(); err != nil {
		return nil, err
	}
	return ws, nil
}

func parseAndValidateProfiles(raw any) ([]userProfile, error) {
	data, err := json.Marshal(raw)
	if err != nil {
		return nil, errors.New("用户配置无效")
	}
	var rows []map[string]any
	if err := json.Unmarshal(data, &rows); err != nil || len(rows) == 0 || len(rows) > 20 {
		return nil, errors.New("用户配置无效")
	}
	seen := map[string]bool{}
	adminCount := 0
	var adminProtected bool
	var out []userProfile

	for _, row := range rows {
		id, okID := row["id"].(string)
		nameRaw, okName := row["name"].(string)
		admin, okAdmin := row["admin"].(bool)
		if !okID || !okName || !okAdmin {
			return nil, errors.New("用户配置无效")
		}
		name := strings.TrimSpace(nameRaw)
		if !profileIDPattern.MatchString(id) || name == "" || utf8.RuneCountInString(name) > 40 || admin != (id == "default") || seen[id] {
			return nil, errors.New("用户配置无效")
		}
		seen[id] = true
		download := true
		if dlRaw, exists := row["download"]; exists {
			dlBool, ok := dlRaw.(bool)
			if !ok {
				return nil, errors.New("用户配置无效")
			}
			download = dlBool
		}
		srcList, okSrc := row["sources"].([]any)
		if !okSrc {
			srcList = nil
		}
		srcSeen := map[string]bool{}
		sources := []string{}
		for _, item := range srcList {
			s, ok := item.(string)
			if !ok || !isKnownSourceID(s) {
				return nil, errors.New("站源配置无效")
			}
			if !srcSeen[s] {
				srcSeen[s] = true
				sources = append(sources, s)
			}
		}
		salt, _ := row["salt"].(string)
		pinHash, _ := row["pinHash"].(string)
		if (salt == "") != (pinHash == "") {
			return nil, errors.New("用户密码配置无效")
		}
		if salt != "" && (!saltPattern.MatchString(salt) || !pinHashPattern.MatchString(pinHash)) {
			return nil, errors.New("用户密码配置无效")
		}
		prof := userProfile{
			ID:       id,
			Name:     name,
			Admin:    admin,
			Sources:  sources,
			Download: download,
			Salt:     salt,
			PinHash:  pinHash,
		}
		if admin {
			adminCount++
			adminProtected = prof.protected()
		}
		out = append(out, prof)
	}
	if adminCount != 1 || (len(out) > 1 && !adminProtected) {
		return nil, errors.New("管理员配置无效")
	}
	return out, nil
}

func (ws *webStore) initialize() error {
	if !ws.files.ensureReady() {
		defaultAdmin := userProfile{ID: "default", Name: "管理员", Admin: true, Sources: []string{}, Download: true}
		initial := map[string]any{
			"settings.json": map[string]any{
				"displayMode":       "auto",
				"themeMode":         "system",
				"followSystemColor": false,
				"themeColor":        "orange",
				"sourceSwitches":    map[string]any{},
			},
			"users.json": map[string]any{
				"profiles": []any{defaultAdmin},
				"active":   "default",
			},
			"users/default/history.json":      []any{},
			"users/default/favorites.json":    []any{},
			"users/default/followStates.json": map[string]any{},
			"users/default/followChecks.json": map[string]any{},
			"users/default/settings.json": map[string]any{
				"source":              "hongguo",
				"playback":            map[string]any{},
				"downloadPreferences": map[string]any{},
				"recentSearches":      []any{},
			},
		}
		if err := ws.files.replaceAll(initial); err != nil {
			return err
		}
	}

	rawSettings, _, _ := ws.files.readJSON("settings.json")
	if m, ok := rawSettings.(map[string]any); ok {
		ws.settings = m
	} else {
		ws.settings = map[string]any{}
	}

	rawUsers, found, err := ws.files.readJSON("users.json")
	if err != nil || !found {
		return errors.New("无法读取本地用户配置")
	}
	usersMap, ok := rawUsers.(map[string]any)
	if !ok {
		return errors.New("本地用户配置格式无效")
	}
	profiles, err := parseAndValidateProfiles(usersMap["profiles"])
	if err != nil {
		return err
	}
	ws.profiles = profiles
	active, _ := usersMap["active"].(string)
	if active == "" || ws.findProfileLocked(active) == nil {
		active = "default"
	}
	ws.defaultID = active
	if fl, ok := usersMap["forceLogin"].(bool); ok {
		ws.forceLogin = &fl
	} else {
		ws.forceLogin = nil
	}

	ws.libraries = map[string]*userLibrary{}
	for _, prof := range ws.profiles {
		ws.libraries[prof.ID] = ws.loadUserLibraryLocked(prof.ID)
	}
	return nil
}

func (ws *webStore) loadUserLibraryLocked(id string) *userLibrary {
	lib := &userLibrary{
		history:      []map[string]any{},
		favorites:    []map[string]any{},
		followStates: map[string]followStateRecord{},
		followChecks: map[string][2]int64{},
		settings:     map[string]any{},
	}
	if v, ok, _ := ws.files.readJSON(fmt.Sprintf("users/%s/settings.json", id)); ok {
		if m, ok := v.(map[string]any); ok {
			lib.settings = m
		}
	}
	if v, ok, _ := ws.files.readJSON(fmt.Sprintf("users/%s/history.json", id)); ok {
		if arr, ok := v.([]any); ok {
			for _, row := range arr {
				if m, ok := row.(map[string]any); ok {
					lib.history = append(lib.history, m)
				}
			}
		}
	}
	if v, ok, _ := ws.files.readJSON(fmt.Sprintf("users/%s/favorites.json", id)); ok {
		if arr, ok := v.([]any); ok {
			for _, row := range arr {
				if m, ok := row.(map[string]any); ok {
					lib.favorites = append(lib.favorites, m)
				}
			}
		}
	}
	if v, ok, _ := ws.files.readJSON(fmt.Sprintf("users/%s/followStates.json", id)); ok {
		if b, mErr := json.Marshal(v); mErr == nil {
			_ = json.Unmarshal(b, &lib.followStates)
		}
	}
	if v, ok, _ := ws.files.readJSON(fmt.Sprintf("users/%s/followChecks.json", id)); ok {
		if b, mErr := json.Marshal(v); mErr == nil {
			_ = json.Unmarshal(b, &lib.followChecks)
		}
	}
	return lib
}

func (ws *webStore) findProfileLocked(id string) *userProfile {
	for i := range ws.profiles {
		if ws.profiles[i].ID == id {
			return &ws.profiles[i]
		}
	}
	return nil
}

func (ws *webStore) forceLoginEnabledLocked() bool {
	if ws.forceLogin != nil {
		return *ws.forceLogin
	}
	admin := ws.findProfileLocked("default")
	return admin != nil && admin.protected()
}

func (ws *webStore) resolveSessionLocked(token string) (*browserSession, *userProfile) {
	now := time.Now()
	for k, s := range ws.sessions {
		if now.Sub(s.updatedAt) > 30*24*time.Hour {
			delete(ws.sessions, k)
		}
	}
	sess := ws.sessions[token]
	if sess != nil {
		if prof := ws.findProfileLocked(sess.profileID); prof != nil {
			sess.updatedAt = now
			return sess, prof
		}
		delete(ws.sessions, token)
	}
	prof := ws.findProfileLocked(ws.defaultID)
	if prof == nil {
		prof = &ws.profiles[0]
	}
	unlocked := !(ws.forceLoginEnabledLocked() && prof.protected())
	sess = &browserSession{
		profileID: prof.ID,
		unlocked:  unlocked,
		updatedAt: now,
	}
	if token != "" && len(ws.sessions) < 512 {
		ws.sessions[token] = sess
	}
	return sess, prof
}

func (ws *webStore) sourceEnabledLocked(source string) bool {
	if sw, ok := ws.settings["sourceSwitches"].(map[string]any); ok {
		if val, ok := sw[source].(bool); ok {
			return val
		}
	}
	return !isRestrictedSourceID(source)
}

func (ws *webStore) allowsSourceLocked(sess *browserSession, prof *userProfile, source string) bool {
	return sess.unlocked && isAvailableSourceID(source) && prof.allows(source) && ws.sourceEnabledLocked(source)
}

func (ws *webStore) checkPinLocked(prof userProfile, pin string) error {
	if !prof.protected() {
		return nil
	}
	now := time.Now()
	if now.Before(ws.pinRetryTime) {
		secs := max(1, int(ws.pinRetryTime.Sub(now).Seconds()+0.9))
		return fmt.Errorf("尝试过多，请 %d 秒后再试", secs)
	}
	if !verifyProfilePin(prof, pin) {
		ws.pinFailures++
		if ws.pinFailures >= 5 {
			delay := min(60, 1<<min(6, ws.pinFailures-4))
			ws.pinRetryTime = now.Add(time.Duration(delay) * time.Second)
		}
		return errors.New("密码不正确")
	}
	ws.pinFailures = 0
	return nil
}

func boolSetting(m map[string]any, key string, fallback bool) bool {
	if v, ok := m[key].(bool); ok {
		return v
	}
	return fallback
}

func stringSetting(m map[string]any, key string, valid map[string]bool, fallback string) string {
	if v, ok := m[key].(string); ok && (valid == nil || valid[v]) {
		return v
	}
	return fallback
}

func (ws *webStore) stateSnapshot(token string) map[string]any {
	ws.mu.Lock()
	defer ws.mu.Unlock()
	return ws.stateSnapshotLocked(token)
}

func (ws *webStore) stateSnapshotLocked(token string) map[string]any {
	sess, prof := ws.resolveSessionLocked(token)
	locked := !sess.unlocked

	avail := availableSourceSites()
	var activeSources []sourceSiteInfo
	var permittedSources []sourceSiteInfo
	switches := map[string]bool{}
	for _, site := range avail {
		switches[site.ID] = ws.sourceEnabledLocked(site.ID)
		if !locked && prof.allows(site.ID) {
			permittedSources = append(permittedSources, site)
			if switches[site.ID] {
				activeSources = append(activeSources, site)
			}
		}
	}

	profilesOut := make([]map[string]any, 0, len(ws.profiles))
	for _, p := range ws.profiles {
		profilesOut = append(profilesOut, p.publicMap())
	}

	lib := ws.libraries[prof.ID]
	if lib == nil {
		lib = &userLibrary{settings: map[string]any{}, followStates: map[string]followStateRecord{}}
	}

	currentSource := ""
	if !locked {
		if savedSrc, ok := lib.settings["source"].(string); ok {
			for _, s := range activeSources {
				if s.ID == savedSrc {
					currentSource = savedSrc
					break
				}
			}
		}
		if currentSource == "" && len(activeSources) > 0 {
			currentSource = activeSources[0].ID
		}
	}

	historyOut := []map[string]any{}
	favoritesOut := []map[string]any{}
	followStatesOut := map[string]followStateRecord{}
	recentSearches := []string{}
	playbackPrefs := map[string]any{}
	downloadPrefs := map[string]any{}

	if !locked {
		for _, entry := range lib.history {
			dramaMap, _ := entry["drama"].(map[string]any)
			src, _ := dramaMap["source"].(string)
			if ws.allowsSourceLocked(sess, prof, src) {
				historyOut = append(historyOut, entry)
			}
		}
		sort.SliceStable(historyOut, func(i, j int) bool {
			ti, _ := historyOut[i]["updatedAt"].(string)
			tj, _ := historyOut[j]["updatedAt"].(string)
			return ti > tj
		})
		for idx := len(lib.favorites) - 1; idx >= 0; idx-- {
			drama := lib.favorites[idx]
			src, _ := drama["source"].(string)
			id, _ := drama["id"].(string)
			if ws.allowsSourceLocked(sess, prof, src) {
				favoritesOut = append(favoritesOut, drama)
				if st, ok := lib.followStates[id]; ok {
					followStatesOut[id] = st
				}
			}
		}
		if arr, ok := lib.settings["recentSearches"].([]any); ok {
			for _, item := range arr {
				if s, ok := item.(string); ok && len(recentSearches) < 20 {
					recentSearches = append(recentSearches, s)
				}
			}
		}
		if m, ok := lib.settings["playback"].(map[string]any); ok {
			playbackPrefs = m
		}
		if m, ok := lib.settings["downloadPreferences"].(map[string]any); ok {
			downloadPrefs = m
		}
	}

	return map[string]any{
		"allSources":              core.AllSourcesEnabled(),
		"appName":                 appDisplayName(),
		"knownSources":            avail,
		"sources":                 activeSources,
		"permittedSources":        permittedSources,
		"sourceSwitches":          switches,
		"profiles":                profilesOut,
		"profile":                 prof.publicMap(),
		"locked":                  locked,
		"canDownload":             !locked && (prof.Admin || prof.Download),
		"forceLogin":              ws.forceLoginEnabledLocked(),
		"displayMode":             stringSetting(ws.settings, "displayMode", validDisplayMode, "auto"),
		"themeMode":               stringSetting(ws.settings, "themeMode", validThemeMode, "system"),
		"themeColor":              stringSetting(ws.settings, "themeColor", validThemeColors, "orange"),
		"followSystemColor":       boolSetting(ws.settings, "followSystemColor", false),
		"televisionKeyboard":      boolSetting(ws.settings, "televisionKeyboard", false),
		"checkUpdates":            boolSetting(ws.settings, "checkUpdates", true),
		"shareUsage":              boolSetting(ws.settings, "shareUsage", true),
		"exportAfterDownload":     boolSetting(ws.settings, "exportAfterDownload", false),
		"deleteAfterAutoExport":   boolSetting(ws.settings, "deleteAfterAutoExport", false),
		"deleteAfterManualExport": boolSetting(ws.settings, "deleteAfterManualExport", false),
		"ignoredUpdate":           stringSetting(ws.settings, "ignoredUpdate", nil, ""),
		"source":                  currentSource,
		"history":                 historyOut,
		"favorites":               favoritesOut,
		"followStates":            followStatesOut,
		"recentSearches":          recentSearches,
		"playback":                playbackPrefs,
		"downloadPreferences":     downloadPrefs,
	}
}

func appDisplayName() string {
	if core.AllSourcesEnabled() {
		return "真果鉴"
	}
	return "红果鉴"
}

func (ws *webStore) authorizeAction(token, source string, needDownload, needAdmin bool) error {
	ws.mu.Lock()
	defer ws.mu.Unlock()
	sess, prof := ws.resolveSessionLocked(token)
	if !sess.unlocked {
		return errors.New("请先解锁当前用户")
	}
	if needAdmin && !prof.Admin {
		return errors.New("仅管理员可执行此操作")
	}
	if needDownload && !(prof.Admin || prof.Download) {
		return errors.New("当前用户仅支持在线观看")
	}
	if source != "" && !ws.allowsSourceLocked(sess, prof, source) {
		return errors.New("当前用户没有此站源权限或站源未开启")
	}
	return nil
}

func (ws *webStore) switchProfile(token, targetID, pin string) (map[string]any, error) {
	ws.mu.Lock()
	defer ws.mu.Unlock()
	target := ws.findProfileLocked(targetID)
	if target == nil {
		return nil, errors.New("用户不存在")
	}
	if err := ws.checkPinLocked(*target, pin); err != nil {
		return nil, err
	}
	ws.defaultID = target.ID
	if err := ws.saveUsersFileLocked(); err != nil {
		return nil, err
	}
	ws.sessions[token] = &browserSession{
		profileID: target.ID,
		unlocked:  true,
		updatedAt: time.Now(),
	}
	return ws.stateSnapshotLocked(token), nil
}

func (ws *webStore) lockProfile(token string) map[string]any {
	ws.mu.Lock()
	defer ws.mu.Unlock()
	sess, prof := ws.resolveSessionLocked(token)
	if prof.protected() {
		sess.unlocked = false
	}
	return ws.stateSnapshotLocked(token)
}

func (ws *webStore) saveUsersFileLocked() error {
	payload := map[string]any{
		"profiles": ws.profiles,
		"active":   ws.defaultID,
	}
	if ws.forceLogin != nil {
		payload["forceLogin"] = *ws.forceLogin
	}
	return ws.files.writeJSON("users.json", payload)
}

func (ws *webStore) saveProfile(token string, input struct {
	ID       string   `json:"id"`
	Name     string   `json:"name"`
	Sources  []string `json:"sources"`
	Download bool     `json:"download"`
	Pin      *string  `json:"pin"`
}) (map[string]any, error) {
	ws.mu.Lock()
	defer ws.mu.Unlock()
	sess, actor := ws.resolveSessionLocked(token)
	if !sess.unlocked || !actor.Admin {
		return nil, errors.New("仅管理员可管理用户")
	}
	old := ws.findProfileLocked(input.ID)
	targetID := input.ID
	if old == nil {
		targetID = randomHex16()
	}
	cleanName := strings.TrimSpace(input.Name)
	if cleanName == "" || utf8.RuneCountInString(cleanName) > 40 {
		return nil, errors.New("用户名需要 1 至 40 个字符")
	}
	srcSet := map[string]bool{}
	var cleanSources []string
	for _, s := range input.Sources {
		if !isKnownSourceID(s) {
			return nil, errors.New("站源无效")
		}
		if !srcSet[s] {
			srcSet[s] = true
			cleanSources = append(cleanSources, s)
		}
	}
	adminProf := ws.findProfileLocked("default")
	if targetID != "default" && (adminProf == nil || !adminProf.protected()) {
		return nil, errors.New("请先为管理员设置密码，再创建或修改其他用户")
	}
	if old == nil && len(ws.profiles) >= 20 {
		return nil, errors.New("最多支持 20 个本地用户")
	}

	salt := ""
	hash := ""
	if old != nil {
		salt, hash = old.Salt, old.PinHash
	}
	if input.Pin != nil {
		pin := *input.Pin
		if pin == "" {
			if targetID == "default" && len(ws.profiles) > 1 {
				return nil, errors.New("存在其他用户时不能取消管理员密码")
			}
			salt, hash = "", ""
		} else {
			pinLen := utf8.RuneCountInString(pin)
			if pinLen < 3 || pinLen > 32 {
				return nil, errors.New("密码需要 3 至 32 个字符")
			}
			salt = randomHex16()
			hash = deriveProfilePin(pin, salt)
		}
	}

	updated := userProfile{
		ID:       targetID,
		Name:     cleanName,
		Admin:    targetID == "default",
		Sources:  cleanSources,
		Download: targetID == "default" || input.Download,
		Salt:     salt,
		PinHash:  hash,
	}

	next := make([]userProfile, 0, len(ws.profiles)+1)
	for _, p := range ws.profiles {
		if p.ID != targetID {
			next = append(next, p)
		}
	}
	next = append(next, updated)
	sort.Slice(next, func(i, j int) bool {
		if next[i].Admin != next[j].Admin {
			return next[i].Admin
		}
		return next[i].Name < next[j].Name
	})

	prev := ws.profiles
	ws.profiles = next
	if err := ws.saveUsersFileLocked(); err != nil {
		ws.profiles = prev
		return nil, err
	}
	if ws.libraries[targetID] == nil {
		ws.libraries[targetID] = &userLibrary{
			history:      []map[string]any{},
			favorites:    []map[string]any{},
			followStates: map[string]followStateRecord{},
			followChecks: map[string][2]int64{},
			settings: map[string]any{
				"source":              "hongguo",
				"playback":            map[string]any{},
				"downloadPreferences": map[string]any{},
				"recentSearches":      []any{},
			},
		}
		_ = ws.files.writeJSON(fmt.Sprintf("users/%s/settings.json", targetID), ws.libraries[targetID].settings)
		_ = ws.files.writeJSON(fmt.Sprintf("users/%s/history.json", targetID), []any{})
		_ = ws.files.writeJSON(fmt.Sprintf("users/%s/favorites.json", targetID), []any{})
		_ = ws.files.writeJSON(fmt.Sprintf("users/%s/followStates.json", targetID), map[string]any{})
	}
	return ws.stateSnapshotLocked(token), nil
}

func (ws *webStore) deleteProfile(token, targetID string) (map[string]any, error) {
	ws.mu.Lock()
	defer ws.mu.Unlock()
	sess, actor := ws.resolveSessionLocked(token)
	if !sess.unlocked || !actor.Admin {
		return nil, errors.New("仅管理员可删除用户")
	}
	if targetID == "default" {
		return nil, errors.New("不能删除管理员")
	}
	var next []userProfile
	for _, p := range ws.profiles {
		if p.ID != targetID {
			next = append(next, p)
		}
	}
	if ws.defaultID == targetID {
		ws.defaultID = "default"
	}
	ws.profiles = next
	if err := ws.saveUsersFileLocked(); err != nil {
		return nil, err
	}
	delete(ws.libraries, targetID)
	for _, bs := range ws.sessions {
		if bs.profileID == targetID {
			bs.profileID = "default"
			admin := ws.findProfileLocked("default")
			bs.unlocked = admin == nil || !admin.protected()
		}
	}
	ws.files.removeSubdir("users/" + targetID)
	return ws.stateSnapshotLocked(token), nil
}

func (ws *webStore) updateGlobalSettings(token string, patch map[string]any) (map[string]any, error) {
	ws.mu.Lock()
	defer ws.mu.Unlock()
	sess, actor := ws.resolveSessionLocked(token)
	if !sess.unlocked {
		return nil, errors.New("请先解锁当前用户")
	}

	if fl, hasForce := patch["forceLogin"]; hasForce {
		if !actor.Admin {
			return nil, errors.New("仅管理员可修改登录验证设置")
		}
		flBool, ok := fl.(bool)
		if !ok {
			return nil, errors.New("登录设置无效")
		}
		ws.forceLogin = &flBool
		if err := ws.saveUsersFileLocked(); err != nil {
			return nil, err
		}
	}

	next := map[string]any{}
	for k, v := range ws.settings {
		next[k] = v
	}

	if swPatch, ok := patch["sourceSwitches"].(map[string]any); ok {
		if !actor.Admin {
			return nil, errors.New("仅管理员可开启或关闭站源")
		}
		existing := map[string]any{}
		if prev, ok := next["sourceSwitches"].(map[string]any); ok {
			for k, v := range prev {
				existing[k] = v
			}
		}
		for src, val := range swPatch {
			enabled, ok := val.(bool)
			if !ok || !isAvailableSourceID(src) {
				return nil, errors.New("站源无效")
			}
			existing[src] = enabled
		}
		next["sourceSwitches"] = existing
	}

	if dm, ok := patch["displayMode"].(string); ok && validDisplayMode[dm] {
		next["displayMode"] = dm
	}
	if tm, ok := patch["themeMode"].(string); ok && validThemeMode[tm] {
		next["themeMode"] = tm
	}
	if tc, ok := patch["themeColor"].(string); ok && validThemeColors[tc] {
		next["themeColor"] = tc
	}
	if fsc, ok := patch["followSystemColor"].(bool); ok {
		next["followSystemColor"] = fsc
	}
	if iu, ok := patch["ignoredUpdate"].(string); ok {
		next["ignoredUpdate"] = iu
	}
	for _, flag := range backupBoolFlags {
		if v, ok := patch[flag].(bool); ok {
			next[flag] = v
		}
	}

	if err := ws.files.writeJSON("settings.json", next); err != nil {
		return nil, err
	}
	ws.settings = next
	return ws.stateSnapshotLocked(token), nil
}

func (ws *webStore) updateUserSettings(token string, patch map[string]any) (map[string]any, error) {
	ws.mu.Lock()
	defer ws.mu.Unlock()
	sess, prof := ws.resolveSessionLocked(token)
	if !sess.unlocked {
		return nil, errors.New("请先解锁当前用户")
	}
	lib := ws.libraries[prof.ID]
	next := map[string]any{}
	for k, v := range lib.settings {
		next[k] = v
	}
	if src, ok := patch["source"].(string); ok {
		if !ws.allowsSourceLocked(sess, prof, src) {
			return nil, errors.New("站源不可用")
		}
		next["source"] = src
	}
	if pb, ok := patch["playback"].(map[string]any); ok {
		merged := map[string]any{}
		if prev, ok := next["playback"].(map[string]any); ok {
			for k, v := range prev {
				merged[k] = v
			}
		}
		for k, v := range pb {
			merged[k] = v
		}
		next["playback"] = merged
	}
	if dp, ok := patch["downloadPreferences"].(map[string]any); ok {
		merged := map[string]any{}
		if prev, ok := next["downloadPreferences"].(map[string]any); ok {
			for k, v := range prev {
				merged[k] = v
			}
		}
		for k, v := range dp {
			merged[k] = v
		}
		next["downloadPreferences"] = merged
	}
	if q, ok := patch["rememberSearch"].(string); ok {
		trimmed := strings.TrimSpace(q)
		if trimmed != "" && utf8.RuneCountInString(trimmed) <= 80 {
			var list []any
			list = append(list, trimmed)
			if prev, ok := next["recentSearches"].([]any); ok {
				for _, item := range prev {
					s, ok := item.(string)
					if ok && !strings.EqualFold(strings.TrimSpace(s), trimmed) && len(list) < 20 {
						list = append(list, s)
					}
				}
			}
			next["recentSearches"] = list
		}
	}
	if clearSearch, ok := patch["clearRecentSearches"].(bool); ok && clearSearch {
		next["recentSearches"] = []any{}
	}

	if err := ws.files.writeJSON(fmt.Sprintf("users/%s/settings.json", prof.ID), next); err != nil {
		return nil, err
	}
	lib.settings = next
	return ws.stateSnapshotLocked(token), nil
}

func (ws *webStore) saveWatchProgress(token string, entry map[string]any) (map[string]any, error) {
	ws.mu.Lock()
	defer ws.mu.Unlock()
	sess, prof := ws.resolveSessionLocked(token)
	if !sess.unlocked {
		return nil, errors.New("请先解锁当前用户")
	}
	dramaMap, ok := entry["drama"].(map[string]any)
	if !ok {
		return nil, errors.New("剧集信息无效")
	}
	dramaID, _ := dramaMap["id"].(string)
	source, _ := dramaMap["source"].(string)
	if dramaID == "" || !ws.allowsSourceLocked(sess, prof, source) {
		return nil, errors.New("无权记录此剧集")
	}

	entry["updatedAt"] = time.Now().UTC().Format(time.RFC3339Nano)
	lib := ws.libraries[prof.ID]

	var next []map[string]any
	next = append(next, entry)
	for _, old := range lib.history {
		oldDrama, _ := old["drama"].(map[string]any)
		oldID, _ := oldDrama["id"].(string)
		if oldID != dramaID && len(next) < 300 {
			next = append(next, old)
		}
	}
	if err := ws.files.writeJSON(fmt.Sprintf("users/%s/history.json", prof.ID), next); err != nil {
		return nil, err
	}
	lib.history = next

	if st, followed := lib.followStates[dramaID]; followed {
		eps := intFromAny(dramaMap["episodes"])
		known := max(st.KnownEpisodes, eps)
		var readPtr *int
		if known > 0 {
			v := known
			readPtr = &v
		}
		st.KnownEpisodes = known
		st.ReadEpisodes = readPtr
		lib.followStates[dramaID] = st
		_ = ws.files.writeJSON(fmt.Sprintf("users/%s/followStates.json", prof.ID), lib.followStates)
	}
	return ws.stateSnapshotLocked(token), nil
}

func (ws *webStore) deleteHistoryItems(token string, ids []string) (map[string]any, error) {
	ws.mu.Lock()
	defer ws.mu.Unlock()
	sess, prof := ws.resolveSessionLocked(token)
	if !sess.unlocked {
		return nil, errors.New("请先解锁当前用户")
	}
	removeSet := map[string]bool{}
	for _, id := range ids {
		removeSet[id] = true
	}
	lib := ws.libraries[prof.ID]
	var next []map[string]any
	for _, row := range lib.history {
		dramaMap, _ := row["drama"].(map[string]any)
		id, _ := dramaMap["id"].(string)
		if !removeSet[id] {
			next = append(next, row)
		}
	}
	if err := ws.files.writeJSON(fmt.Sprintf("users/%s/history.json", prof.ID), next); err != nil {
		return nil, err
	}
	lib.history = next
	return ws.stateSnapshotLocked(token), nil
}

func intFromAny(v any) int {
	switch n := v.(type) {
	case int:
		return n
	case float64:
		return int(n)
	case json.Number:
		i, _ := n.Int64()
		return int(i)
	}
	return 0
}

func (ws *webStore) toggleFollow(token string, drama map[string]any, follow bool) (map[string]any, error) {
	ws.mu.Lock()
	defer ws.mu.Unlock()
	sess, prof := ws.resolveSessionLocked(token)
	if !sess.unlocked {
		return nil, errors.New("请先解锁当前用户")
	}
	id, _ := drama["id"].(string)
	src, _ := drama["source"].(string)
	if id == "" || !ws.allowsSourceLocked(sess, prof, src) {
		return nil, errors.New("剧集无效或站源不可用")
	}
	lib := ws.libraries[prof.ID]
	var nextFavs []map[string]any
	for _, item := range lib.favorites {
		itemID, _ := item["id"].(string)
		if itemID != id {
			nextFavs = append(nextFavs, item)
		}
	}
	if follow {
		if len(nextFavs) >= 20000 {
			return nil, errors.New("追剧数量已达上限")
		}
		nextFavs = append(nextFavs, drama)
		eps := max(0, intFromAny(drama["episodes"]))
		var readPtr *int
		if eps > 0 {
			v := eps
			readPtr = &v
		}
		lib.followStates[id] = followStateRecord{KnownEpisodes: eps, ReadEpisodes: readPtr}
	} else {
		delete(lib.followStates, id)
		delete(lib.followChecks, id)
	}

	if err := ws.files.writeJSON(fmt.Sprintf("users/%s/favorites.json", prof.ID), nextFavs); err != nil {
		return nil, err
	}
	_ = ws.files.writeJSON(fmt.Sprintf("users/%s/followStates.json", prof.ID), lib.followStates)
	lib.favorites = nextFavs
	return ws.stateSnapshotLocked(token), nil
}

func (ws *webStore) unfollowBatch(token string, ids []string) (map[string]any, error) {
	ws.mu.Lock()
	defer ws.mu.Unlock()
	sess, prof := ws.resolveSessionLocked(token)
	if !sess.unlocked {
		return nil, errors.New("请先解锁当前用户")
	}
	removeSet := map[string]bool{}
	for _, id := range ids {
		removeSet[id] = true
	}
	lib := ws.libraries[prof.ID]
	var nextFavs []map[string]any
	for _, item := range lib.favorites {
		itemID, _ := item["id"].(string)
		if !removeSet[itemID] {
			nextFavs = append(nextFavs, item)
		} else {
			delete(lib.followStates, itemID)
			delete(lib.followChecks, itemID)
		}
	}
	if err := ws.files.writeJSON(fmt.Sprintf("users/%s/favorites.json", prof.ID), nextFavs); err != nil {
		return nil, err
	}
	_ = ws.files.writeJSON(fmt.Sprintf("users/%s/followStates.json", prof.ID), lib.followStates)
	lib.favorites = nextFavs
	return ws.stateSnapshotLocked(token), nil
}

func (ws *webStore) markFollowRead(token, dramaID string, episodes int) {
	ws.mu.Lock()
	defer ws.mu.Unlock()
	sess, prof := ws.resolveSessionLocked(token)
	if !sess.unlocked {
		return
	}
	lib := ws.libraries[prof.ID]
	st, ok := lib.followStates[dramaID]
	if !ok {
		return
	}
	known := max(st.KnownEpisodes, episodes)
	var readPtr *int
	if known > 0 {
		v := known
		readPtr = &v
	}
	st.KnownEpisodes = known
	st.ReadEpisodes = readPtr
	lib.followStates[dramaID] = st
	_ = ws.files.writeJSON(fmt.Sprintf("users/%s/followStates.json", prof.ID), lib.followStates)
}

func (ws *webStore) observeDramaMetadata(drama map[string]any, markReadForToken string) {
	id, _ := drama["id"].(string)
	if id == "" {
		return
	}
	eps := intFromAny(drama["episodes"])
	ws.mu.Lock()
	defer ws.mu.Unlock()

	var readerProfile string
	if markReadForToken != "" {
		sess, prof := ws.resolveSessionLocked(markReadForToken)
		if sess.unlocked {
			readerProfile = prof.ID
		}
	}

	for profID, lib := range ws.libraries {
		favChanged := false
		for i, fav := range lib.favorites {
			if favID, _ := fav["id"].(string); favID == id {
				merged := mergeDramaMaps(fav, drama)
				lib.favorites[i] = merged
				favChanged = true
				break
			}
		}
		if favChanged {
			_ = ws.files.writeJSON(fmt.Sprintf("users/%s/favorites.json", profID), lib.favorites)
		}
		if st, ok := lib.followStates[id]; ok {
			nextKnown := max(st.KnownEpisodes, eps)
			readPtr := st.ReadEpisodes
			if profID == readerProfile && nextKnown > 0 {
				v := nextKnown
				readPtr = &v
			} else if readPtr == nil && nextKnown > 0 {
				v := nextKnown
				readPtr = &v
			}
			if nextKnown != st.KnownEpisodes || (readPtr != nil && (st.ReadEpisodes == nil || *readPtr != *st.ReadEpisodes)) {
				st.KnownEpisodes = nextKnown
				st.ReadEpisodes = readPtr
				lib.followStates[id] = st
				_ = ws.files.writeJSON(fmt.Sprintf("users/%s/followStates.json", profID), lib.followStates)
			}
		}
	}
}

func mergeDramaMaps(old, fresh map[string]any) map[string]any {
	out := map[string]any{}
	for k, v := range old {
		out[k] = v
	}
	for _, key := range []string{"source", "sourceId", "description", "cover", "heat", "views", "onlineDate", "releaseStatus"} {
		if s, ok := fresh[key].(string); ok && strings.TrimSpace(s) != "" && s != "unknown" {
			out[key] = s
		}
	}
	if title, ok := fresh["title"].(string); ok && strings.TrimSpace(title) != "" && title != "短剧" {
		out["title"] = title
	}
	if eps := intFromAny(fresh["episodes"]); eps > intFromAny(old["episodes"]) {
		out["episodes"] = eps
	}
	if cat, ok := fresh["category"].(string); ok && cat != "" {
		out["category"] = cat
	}
	if vip, exists := fresh["vip"]; exists && vip != nil {
		out["vip"] = vip
	}
	if tags, ok := fresh["tags"].([]any); ok && len(tags) > 0 {
		out["tags"] = tags
	}
	return out
}

func (ws *webStore) exportBackup(token string) ([]byte, error) {
	ws.mu.Lock()
	defer ws.mu.Unlock()
	sess, actor := ws.resolveSessionLocked(token)
	if !sess.unlocked || !actor.Admin {
		return nil, errors.New("仅管理员可导出备份")
	}

	libraries := map[string]any{}
	for _, prof := range ws.profiles {
		lib := ws.libraries[prof.ID]
		favIDs := map[string]bool{}
		for _, fav := range lib.favorites {
			if id, ok := fav["id"].(string); ok {
				favIDs[id] = true
			}
		}
		cleanStates := map[string]followStateRecord{}
		for id, st := range lib.followStates {
			if favIDs[id] {
				cleanStates[id] = st
			}
		}
		src, _ := lib.settings["source"].(string)
		pb, _ := lib.settings["playback"].(map[string]any)
		if pb == nil {
			pb = map[string]any{}
		}
		dp, _ := lib.settings["downloadPreferences"].(map[string]any)
		if dp == nil {
			dp = map[string]any{}
		}
		rs, _ := lib.settings["recentSearches"].([]any)
		if rs == nil {
			rs = []any{}
		}
		libraries[prof.ID] = map[string]any{
			"history":             lib.history,
			"favorites":           lib.favorites,
			"followStates":        cleanStates,
			"source":              src,
			"playback":            pb,
			"downloadPreferences": dp,
			"recentSearches":      rs,
		}
	}

	payload := map[string]any{
		"schema":                  1,
		"app":                     "zhenguojian",
		"profiles":                ws.profiles,
		"displayMode":             stringSetting(ws.settings, "displayMode", validDisplayMode, "auto"),
		"themeMode":               stringSetting(ws.settings, "themeMode", validThemeMode, "system"),
		"followSystemColor":       boolSetting(ws.settings, "followSystemColor", false),
		"themeColor":              stringSetting(ws.settings, "themeColor", validThemeColors, "orange"),
		"forceLogin":              ws.forceLoginEnabledLocked(),
		"checkUpdates":            boolSetting(ws.settings, "checkUpdates", true),
		"shareUsage":              boolSetting(ws.settings, "shareUsage", true),
		"exportAfterDownload":     boolSetting(ws.settings, "exportAfterDownload", false),
		"deleteAfterAutoExport":   boolSetting(ws.settings, "deleteAfterAutoExport", false),
		"deleteAfterManualExport": boolSetting(ws.settings, "deleteAfterManualExport", false),
		"televisionKeyboard":      boolSetting(ws.settings, "televisionKeyboard", false),
		"libraries":               libraries,
	}

	encoded, err := json.Marshal(payload)
	if err != nil {
		return nil, err
	}
	if len(encoded) > 8<<20 {
		return nil, errors.New("备份超过 8 MiB 保存上限，请先整理记录")
	}
	return encoded, nil
}

func (ws *webStore) importBackup(token string, content []byte) (map[string]any, error) {
	if len(content) > 8<<20 {
		return nil, errors.New("备份文件过大")
	}
	var data map[string]any
	if err := json.Unmarshal(content, &data); err != nil {
		return nil, errors.New("无法解析备份文件")
	}
	if intFromAny(data["schema"]) != 1 || data["app"] != "zhenguojian" {
		return nil, errors.New("不支持的备份格式")
	}
	profiles, err := parseAndValidateProfiles(data["profiles"])
	if err != nil {
		return nil, err
	}
	libsRaw, ok := data["libraries"].(map[string]any)
	if !ok {
		return nil, errors.New("备份缺少剧库数据")
	}

	ws.mu.Lock()
	defer ws.mu.Unlock()
	sess, actor := ws.resolveSessionLocked(token)
	if !sess.unlocked || !actor.Admin {
		return nil, errors.New("仅管理员可恢复备份")
	}

	nextSettings := map[string]any{}
	for k, v := range ws.settings {
		nextSettings[k] = v
	}
	nextSettings["displayMode"] = stringSetting(data, "displayMode", validDisplayMode, "auto")
	if tm, ok := data["themeMode"].(string); ok && validThemeMode[tm] {
		nextSettings["themeMode"] = tm
	}
	if fsc, ok := data["followSystemColor"].(bool); ok {
		nextSettings["followSystemColor"] = fsc
	}
	if tc, ok := data["themeColor"].(string); ok && validThemeColors[tc] {
		nextSettings["themeColor"] = tc
	}
	for _, flag := range backupBoolFlags {
		if b, ok := data[flag].(bool); ok {
			nextSettings[flag] = b
		}
	}

	usersMap := map[string]any{
		"profiles": profiles,
		"active":   "default",
	}
	if fl, ok := data["forceLogin"].(bool); ok {
		usersMap["forceLogin"] = fl
	}

	contents := map[string]any{
		"settings.json": nextSettings,
		"users.json":    usersMap,
	}

	for _, prof := range profiles {
		libEntry, ok := libsRaw[prof.ID].(map[string]any)
		if !ok {
			return nil, errors.New("备份用户记录不完整")
		}
		hist, okH := libEntry["history"].([]any)
		favs, okF := libEntry["favorites"].([]any)
		if !okH || !okF || len(hist) > 300 || len(favs) > 20000 {
			return nil, errors.New("备份记录数量或格式无效")
		}
		states, _ := libEntry["followStates"].(map[string]any)
		if states == nil {
			states = map[string]any{}
		}
		src, _ := libEntry["source"].(string)
		pb, _ := libEntry["playback"].(map[string]any)
		if pb == nil {
			pb = map[string]any{}
		}
		dp, _ := libEntry["downloadPreferences"].(map[string]any)
		if dp == nil {
			dp = map[string]any{}
		}
		rs, _ := libEntry["recentSearches"].([]any)
		if rs == nil {
			rs = []any{}
		}

		contents[fmt.Sprintf("users/%s/history.json", prof.ID)] = hist
		contents[fmt.Sprintf("users/%s/favorites.json", prof.ID)] = favs
		contents[fmt.Sprintf("users/%s/followStates.json", prof.ID)] = states
		contents[fmt.Sprintf("users/%s/followChecks.json", prof.ID)] = map[string]any{}
		contents[fmt.Sprintf("users/%s/settings.json", prof.ID)] = map[string]any{
			"source":              src,
			"playback":            pb,
			"downloadPreferences": dp,
			"recentSearches":      rs,
		}
	}

	if err := ws.files.replaceAll(contents); err != nil {
		return nil, errors.New("未能保存备份恢复结果，原有记录已保留")
	}
	if err := ws.initialize(); err != nil {
		return nil, err
	}
	ws.sessions = map[string]*browserSession{}
	return ws.stateSnapshotLocked(token), nil
}

func (ws *webStore) checkFollowUpdatesNow(ctx context.Context, token string, maxItems int) (int, error) {
	ws.mu.Lock()
	var targets []map[string]any
	if token != "" {
		sess, prof := ws.resolveSessionLocked(token)
		if !sess.unlocked {
			ws.mu.Unlock()
			return 0, errors.New("请先解锁当前用户")
		}
		lib := ws.libraries[prof.ID]
		for _, fav := range lib.favorites {
			src, _ := fav["source"].(string)
			status, _ := fav["releaseStatus"].(string)
			if status == "finished" || status == "completed" || !ws.allowsSourceLocked(sess, prof, src) {
				continue
			}
			targets = append(targets, fav)
		}
	} else {
		seen := map[string]bool{}
		for _, lib := range ws.libraries {
			for _, fav := range lib.favorites {
				id, _ := fav["id"].(string)
				src, _ := fav["source"].(string)
				status, _ := fav["releaseStatus"].(string)
				if id == "" || seen[id] || status == "finished" || status == "completed" || !isAvailableSourceID(src) || !ws.sourceEnabledLocked(src) {
					continue
				}
				seen[id] = true
				targets = append(targets, fav)
			}
		}
	}
	ws.mu.Unlock()

	if maxItems > 0 && len(targets) > maxItems {
		targets = targets[len(targets)-maxItems:]
	}

	updatedCount := 0
	for _, fav := range targets {
		if ctx.Err() != nil {
			break
		}
		reqBody, _ := json.Marshal(map[string]any{
			"action": "followCheck",
			"drama":  fav,
		})
		respRaw := core.NativeRequest(string(reqBody))
		var env struct {
			OK   bool `json:"ok"`
			Data struct {
				Drama map[string]any `json:"drama"`
			} `json:"data"`
		}
		if json.Unmarshal([]byte(respRaw), &env) == nil && env.OK && env.Data.Drama != nil {
			oldEps := intFromAny(fav["episodes"])
			newEps := intFromAny(env.Data.Drama["episodes"])
			if newEps > oldEps {
				updatedCount++
			}
			ws.observeDramaMetadata(env.Data.Drama, "")
		}
	}
	return updatedCount, nil
}

func (ws *webStore) runBackgroundFollowChecker(ctx context.Context) {
	ticker := time.NewTicker(30 * time.Minute)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			checkCtx, cancel := context.WithTimeout(ctx, 2*time.Minute)
			_, _ = ws.checkFollowUpdatesNow(checkCtx, "", 12)
			cancel()
		}
	}
}
