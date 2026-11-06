package core

import (
	"context"
	"errors"
	"net/http"
	"net/url"
	"sort"
	"strconv"
	"strings"
	"time"
	"unicode"
	"unicode/utf8"

	"golang.org/x/text/unicode/norm"
)

const (
	hongguoSearchTimeout         = 40 * time.Second
	hongguoSearchPageTimeout     = 12 * time.Second
	hongguoSearchPendingLimit    = 32
	hongguoSearchNameCount       = 50
	hongguoSearchPageSize        = 10
	hongguoSearchGeneralPageSize = 20
	hongguoSearchGeneralChunks   = 3
	hongguoSearchSeriesTab       = "29"
	hongguoSearchGeneralTab      = "11"
	hongguoSearchTabPath         = "/reading/bookapi/search/tab/v/"
	hongguoSearchFallbackTTL     = 30 * time.Minute
)

type hongguoSearchEntry struct {
	Dramas    []Drama
	Total     int
	HasMore   bool
	Warning   string
	ExpiresAt time.Time
}

type hongguoSearchCall struct {
	done     chan struct{}
	changed  chan struct{}
	entry    hongguoSearchEntry
	snapshot hongguoSearchEntry
	revision uint64
	err      error
}

type hongguoSearchProgressKey struct{}

func reportHongguoSearchProgress(ctx context.Context, entry hongguoSearchEntry) {
	if progress, ok := ctx.Value(hongguoSearchProgressKey{}).(func(hongguoSearchEntry)); ok && len(entry.Dramas) > 0 {
		entry.Total = max(entry.Total, len(entry.Dramas))
		progress(entry)
	}
}

func hongguoSearchKeyword(keyword string) (string, error) {
	keyword = norm.NFKC.String(strings.TrimSpace(keyword))
	if keyword == "" || !utf8.ValidString(keyword) || utf8.RuneCountInString(keyword) > 80 || strings.IndexFunc(keyword, unicode.IsControl) >= 0 {
		return "", errors.New("请输入 1 至 80 个字符的搜索词")
	}
	return keyword, nil
}

func (downloader *Downloader) searchHongguoDramas(ctx context.Context, keyword string) (hongguoSearchEntry, error) {
	return downloader.searchHongguoDramasProgress(ctx, keyword, nil)
}

func (downloader *Downloader) searchHongguoDramasProgress(ctx context.Context, keyword string, progress func(hongguoSearchEntry)) (hongguoSearchEntry, error) {
	keyword, err := hongguoSearchKeyword(keyword)
	if err != nil {
		return hongguoSearchEntry{}, err
	}
	ctx, cancel := context.WithTimeout(ctx, hongguoSearchTimeout)
	defer cancel()
	client := downloader.hongguoClient()
	for {
		if err := ctx.Err(); err != nil {
			return hongguoSearchEntry{}, err
		}
		client.mu.Lock()
		if cached, found := client.searches[keyword]; found && time.Now().Before(cached.ExpiresAt) {
			client.mu.Unlock()
			return cloneHongguoSearchEntry(cached), nil
		}
		if pending := client.searchPending[keyword]; pending != nil {
			client.mu.Unlock()
			entry, err := downloader.waitHongguoSearch(ctx, pending, progress)
			if (errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded)) && ctx.Err() == nil {
				continue
			}
			return entry, err
		}
		if len(client.searchPending) >= hongguoSearchPendingLimit {
			client.mu.Unlock()
			return hongguoSearchEntry{}, errors.New("红果搜索请求较多，请稍后重试")
		}
		pending := &hongguoSearchCall{done: make(chan struct{}), changed: make(chan struct{})}
		client.searchPending[keyword] = pending
		client.mu.Unlock()
		fetchCtx := context.WithValue(ctx, hongguoSearchProgressKey{}, func(entry hongguoSearchEntry) {
			client.mu.Lock()
			pending.snapshot = cloneHongguoSearchEntry(entry)
			pending.revision++
			close(pending.changed)
			pending.changed = make(chan struct{})
			client.mu.Unlock()
			if progress != nil {
				progress(cloneHongguoSearchEntry(entry))
			}
		})
		entry, err := downloader.fetchHongguoSearch(fetchCtx, keyword)
		client.mu.Lock()
		if err == nil && entry.Warning == "" {
			if len(client.searches) >= 64 {
				client.searches = map[string]hongguoSearchEntry{}
			}
			entry.ExpiresAt = time.Now().Add(5 * time.Minute)
			client.searches[keyword] = entry
		}
		pending.entry, pending.err = entry, err
		delete(client.searchPending, keyword)
		close(pending.done)
		client.mu.Unlock()
		return cloneHongguoSearchEntry(entry), err
	}
}

func (downloader *Downloader) waitHongguoSearch(ctx context.Context, pending *hongguoSearchCall, progress func(hongguoSearchEntry)) (hongguoSearchEntry, error) {
	client := downloader.hongguoClient()
	var revision uint64
	for {
		client.mu.Lock()
		changed := pending.changed
		var update *hongguoSearchEntry
		if progress != nil && pending.revision > revision {
			entry := cloneHongguoSearchEntry(pending.snapshot)
			update = &entry
			revision = pending.revision
		}
		client.mu.Unlock()
		if update != nil {
			progress(*update)
		}
		select {
		case <-ctx.Done():
			return hongguoSearchEntry{}, ctx.Err()
		case <-pending.done:
			return cloneHongguoSearchEntry(pending.entry), pending.err
		case <-changed:
		}
	}
}

func (downloader *Downloader) fetchHongguoSearch(ctx context.Context, keyword string) (hongguoSearchEntry, error) {
	names, namesErr := downloader.fetchHongguoSearchNames(ctx, keyword)
	if err := ctx.Err(); err != nil {
		if errors.Is(err, context.DeadlineExceeded) && len(names) > 0 {
			return hongguoSearchEntry{Dramas: names, Total: len(names), Warning: "搜索已到时限，已保留名称匹配结果，可重试"}, nil
		}
		return hongguoSearchEntry{}, err
	}
	reportHongguoSearchProgress(ctx, hongguoSearchEntry{Dramas: mergeHongguoSearchDramas([]Drama{}, names)})
	pageCtx, cancel := context.WithTimeout(ctx, hongguoSearchPageTimeout)
	defer cancel()
	page, pageErr := downloader.fetchHongguoSearchPage(pageCtx, keyword, 1)
	if errors.Is(ctx.Err(), context.Canceled) {
		return hongguoSearchEntry{}, ctx.Err()
	}
	if pageErr != nil && namesErr != nil || len(page.Dramas)+len(names) == 0 && (pageErr != nil || namesErr != nil) {
		return hongguoSearchEntry{}, errors.Join(pageErr, namesErr)
	}
	entry := hongguoSearchEntry{Dramas: make([]Drama, 0, len(page.Dramas)+len(names)), Total: page.Total, HasMore: page.HasMore}
	for _, batch := range [][]Drama{page.Dramas, names} {
		entry.Dramas = mergeHongguoSearchDramas(entry.Dramas, batch)
	}
	reportHongguoSearchProgress(ctx, entry)
	seasonsLimited := downloader.completeHongguoSearchSeasons(ctx, keyword, &entry)
	if errors.Is(ctx.Err(), context.Canceled) {
		return hongguoSearchEntry{}, ctx.Err()
	}
	seasonsLimited = seasonsLimited || errors.Is(ctx.Err(), context.DeadlineExceeded)
	query := keyword
	sort.SliceStable(entry.Dramas, func(left, right int) bool {
		leftTitle, rightTitle := entry.Dramas[left].DisplayTitle(), entry.Dramas[right].DisplayTitle()
		leftRank, rightRank := hongguoTitleSearchRank(leftTitle, query), hongguoTitleSearchRank(rightTitle, query)
		if leftRank != rightRank {
			return leftRank < rightRank
		}
		leftBase, leftSeason, leftUnit, _ := hongguoSearchSeason(leftTitle)
		rightBase, rightSeason, rightUnit, _ := hongguoSearchSeason(rightTitle)
		if leftSeason > 0 && rightSeason > 0 && leftUnit == rightUnit && hongguoSearchText(leftBase) == hongguoSearchText(rightBase) {
			return leftSeason < rightSeason
		}
		return false
	})
	if pageErr != nil {
		entry.Warning = "红果综合搜索暂不可用，已保留名称匹配结果，可重试"
	} else if namesErr != nil {
		entry.Warning = "红果名称检索暂不可用，结果可能缺少部分剧集，可重试"
	}
	if seasonsLimited {
		if entry.Warning != "" {
			entry.Warning += "；"
		}
		entry.Warning += "部分季数暂未补齐，已保留当前结果，可重试"
	}
	if entry.Total < len(entry.Dramas) {
		entry.Total = len(entry.Dramas)
	}
	return entry, nil
}

func mergeHongguoSearchDramas(dramas, batch []Drama) []Drama {
	positions := make(map[string]int, len(dramas)+len(batch))
	for index, drama := range dramas {
		positions[drama.ID] = index
	}
	for _, drama := range batch {
		if drama.ID == "" {
			continue
		}
		if index, found := positions[drama.ID]; found {
			dramas[index] = mergeDramaMetadata(dramas[index], drama)
		} else {
			positions[drama.ID] = len(dramas)
			dramas = append(dramas, drama)
		}
	}
	return dramas
}

func hongguoSearchQueryVariants(keyword string) []string {
	var raw []string
	raw = append(raw, keyword)
	compact := strings.Map(func(r rune) rune {
		if unicode.IsSpace(r) {
			return -1
		}
		return r
	}, keyword)
	raw = append(raw, compact)
	plain := strings.NewReplacer(":", "", "：", "", "·", "", "-", "").Replace(compact)
	raw = append(raw, plain)
	if base, season, unit := hongguoQuerySeason(keyword); season > 0 {
		u := unit
		if u == "" {
			u = "季"
		}
		cn := hongguoSearchChineseSeason(season)
		arab := strconv.Itoa(season)
		raw = append(raw, base+"第"+cn+u, base+"：第"+cn+u, base+"第"+arab+u)
		if unit == "" {
			raw = append(raw, base+"第"+cn+"部")
		}
	}
	seen := make(map[string]bool, len(raw))
	variants := make([]string, 0, len(raw))
	for _, candidate := range raw {
		normalized, err := hongguoSearchKeyword(candidate)
		if err != nil || seen[normalized] {
			continue
		}
		seen[normalized] = true
		variants = append(variants, normalized)
		if len(variants) >= 6 {
			break
		}
	}
	return variants
}

func (downloader *Downloader) fetchHongguoSearchNamesSingle(ctx context.Context, keyword string) ([]Drama, error) {
	records, err := downloader.fetchHongguoSuggestionRecords(ctx, keyword, hongguoSearchNameCount)
	if err != nil {
		return nil, err
	}
	dramas := make([]Drama, 0, len(records))
	for _, record := range records {
		if record.WordType != "short_play_name" || !hongguoNumericID.MatchString(mapString(record.VideoData, "series_id_str", "series_id")) {
			continue
		}
		drama := hongguoDramaFromAny(map[string]any{"video_data": record.VideoData, "name": record.Name, "keyword": record.Keyword}, "短剧")
		if drama.ID != "" && drama.DisplayTitle() != drama.SourceID {
			dramas = append(dramas, drama)
		}
	}
	return dramas, nil
}

func (downloader *Downloader) fetchHongguoSearchNames(ctx context.Context, keyword string) ([]Drama, error) {
	variants := hongguoSearchQueryVariants(keyword)
	var dramas []Drama
	var firstErr error
	succeeded := 0
	for _, variant := range variants {
		if ctx.Err() != nil {
			break
		}
		batch, err := downloader.fetchHongguoSearchNamesSingle(ctx, variant)
		if err != nil {
			if firstErr == nil {
				firstErr = err
			}
			continue
		}
		succeeded++
		if len(batch) > 0 {
			dramas = mergeHongguoSearchDramas(dramas, batch)
			if len(variants) > 1 {
				reportHongguoSearchProgress(ctx, hongguoSearchEntry{Dramas: dramas})
			}
		}
	}
	if succeeded == 0 && firstErr != nil {
		return nil, firstErr
	}
	return dramas, nil
}

func cloneHongguoSearchEntry(entry hongguoSearchEntry) hongguoSearchEntry {
	entry.Dramas = append([]Drama{}, entry.Dramas...)
	for index := range entry.Dramas {
		entry.Dramas[index].Tags = append([]string(nil), entry.Dramas[index].Tags...)
	}
	return entry
}

func hongguoSearchText(text string) string {
	normalized := strings.Map(func(r rune) rune {
		if unicode.IsSpace(r) || unicode.IsPunct(r) {
			return -1
		}
		return unicode.ToLower(r)
	}, norm.NFKC.String(text))
	if normalized == "" {
		return strings.ToLower(strings.TrimSpace(text))
	}
	return normalized
}

func hongguoTitleSearchRank(title, query string) int {
	qBase, qSeason, qUnit := hongguoQuerySeason(query)
	tBase, tSeason, tUnit, _ := hongguoSearchSeason(title)
	if qSeason > 0 && tSeason == qSeason && (qUnit == "" || qUnit == tUnit) {
		qb, tb := hongguoSearchText(qBase), hongguoSearchText(tBase)
		switch {
		case tb == qb:
			return 0
		case strings.HasPrefix(tb, qb):
			return 1
		case strings.Contains(tb, qb):
			return 2
		}
	}
	words := strings.FieldsFunc(norm.NFKC.String(query), func(r rune) bool { return unicode.IsSpace(r) || unicode.IsPunct(r) })
	queryNorm := hongguoSearchText(query)
	titleNorm := hongguoSearchText(title)
	if titleNorm == queryNorm {
		return 0
	}
	if qSeason > 0 && tSeason > 0 && qSeason != tSeason {
		if hongguoSearchText(tBase) == hongguoSearchText(qBase) {
			return 3
		}
		return 4
	}
	switch {
	case strings.HasPrefix(titleNorm, queryNorm):
		return 1
	case strings.Contains(titleNorm, queryNorm):
		return 2
	default:
		matches := 0
		for _, word := range words {
			if w := hongguoSearchText(word); w != "" && strings.Contains(titleNorm, w) {
				matches++
			}
		}
		if len(words) > 0 && matches == len(words) {
			return 3
		}
		return 4
	}
}

func hongguoVideoCandidates(value any) []map[string]any {
	switch typed := value.(type) {
	case []any:
		out := make([]map[string]any, 0, len(typed))
		for _, item := range typed {
			if m, ok := item.(map[string]any); ok && len(m) > 0 {
				out = append(out, m)
			}
		}
		return out
	case map[string]any:
		if list := anyList(typed); len(list) > 0 {
			return hongguoVideoCandidates(list)
		}
		if len(typed) > 0 {
			return []map[string]any{typed}
		}
	}
	return nil
}

func hongguoSearchCellDramas(cell any) []Drama {
	row, ok := cell.(map[string]any)
	if !ok || len(row) == 0 {
		return nil
	}
	candidates := hongguoVideoCandidates(row["video_data"])
	if len(candidates) == 0 {
		for _, sub := range anyList(row["cell_data"]) {
			if subMap, ok := sub.(map[string]any); ok {
				candidates = append(candidates, hongguoVideoCandidates(subMap["video_data"])...)
			}
		}
	}
	if len(candidates) == 0 {
		return nil
	}
	dramas := make([]Drama, 0, len(candidates))
	for _, video := range candidates {
		if mapString(video, "series_title", "series_name") == "" && mapString(video, "episode_cnt") == "0" {
			continue
		}
		drama := hongguoDramaFromAny(map[string]any{"video_data": video}, "短剧")
		if drama.ID != "" && drama.DisplayTitle() != drama.SourceID {
			dramas = append(dramas, drama)
		}
	}
	return dramas
}

func (downloader *Downloader) requestHongguoSearchTabAt(ctx context.Context, keyword, tabType string, rawOffset, pageSize int) (map[string]any, map[string]any, error) {
	client := downloader.hongguoClient()
	offset := strconv.Itoa(rawOffset)
	query := url.Values{"query": {keyword}, "offset": {offset}, "count": {strconv.Itoa(pageSize)}, "tab_type": {tabType}}
	if rawOffset > 0 {
		query.Set("passback", offset)
		client.mu.Lock()
		searchID := client.searchIDs[tabType+":"+keyword]
		if searchID == "" {
			searchID = client.searchIDs[keyword]
		}
		client.mu.Unlock()
		if searchID != "" {
			query.Set("search_id", searchID)
		}
	}
	result, err := downloader.hongguoAppRequest(ctx, http.MethodGet, hongguoSearchTabPath, query, nil)
	if err != nil {
		return nil, nil, err
	}
	var tab map[string]any
	for _, item := range anyList(result["search_tabs"]) {
		if candidate, _ := item.(map[string]any); mapString(candidate, "tab_type") == tabType {
			tab = candidate
			break
		}
	}
	return result, tab, nil
}

func (downloader *Downloader) fetchHongguoSearchPage(ctx context.Context, keyword string, pageNum int) (hongguoSearchEntry, error) {
	client := downloader.hongguoClient()
	client.mu.Lock()
	useGeneral := time.Now().Before(client.generalSearchUntil)
	client.mu.Unlock()

	tabType := hongguoSearchSeriesTab
	pageSize := hongguoSearchPageSize
	if useGeneral {
		tabType = hongguoSearchGeneralTab
		pageSize = hongguoSearchGeneralPageSize
	}

	rawOffset := (pageNum - 1) * pageSize
	if pageNum > 1 {
		client.mu.Lock()
		if saved := client.searchOffsets[tabType+":"+keyword]; saved > 0 {
			rawOffset = saved
		}
		client.mu.Unlock()
	}

	result, tab, err := downloader.requestHongguoSearchTabAt(ctx, keyword, tabType, rawOffset, pageSize)
	if err != nil {
		return hongguoSearchEntry{}, err
	}
	if tab == nil && tabType == hongguoSearchSeriesTab {
		var dramaTab map[string]any
		var generalTab map[string]any
		hasGeneralTab := false
		for _, item := range anyList(result["search_tabs"]) {
			candidate, _ := item.(map[string]any)
			if candidate == nil {
				continue
			}
			candType := mapString(candidate, "tab_type")
			candName := mapString(candidate, "tab_name", "title", "name")
			if candName == "短剧" || candName == "漫剧" {
				dramaTab = candidate
				break
			}
			if candType == hongguoSearchGeneralTab {
				hasGeneralTab = true
				generalTab = candidate
			}
		}
		if dramaTab != nil && mapString(dramaTab, "tab_type") != "" {
			tabType = mapString(dramaTab, "tab_type")
			if len(anyList(dramaTab["data"])) > 0 {
				tab = dramaTab
			} else {
				_, tab, err = downloader.requestHongguoSearchTabAt(ctx, keyword, tabType, rawOffset, pageSize)
				if err != nil {
					return hongguoSearchEntry{}, err
				}
			}
		} else if hasGeneralTab {
			client.mu.Lock()
			client.generalSearchUntil = time.Now().Add(hongguoSearchFallbackTTL)
			if pageNum > 1 {
				if saved := client.searchOffsets[hongguoSearchGeneralTab+":"+keyword]; saved > 0 {
					rawOffset = saved
				} else {
					rawOffset = (pageNum - 1) * hongguoSearchGeneralPageSize
				}
			} else {
				rawOffset = 0
			}
			client.mu.Unlock()
			tabType = hongguoSearchGeneralTab
			pageSize = hongguoSearchGeneralPageSize
			if pageNum == 1 && generalTab != nil && len(anyList(generalTab["data"])) > 0 {
				tab = generalTab
			} else {
				_, tab, err = downloader.requestHongguoSearchTabAt(ctx, keyword, tabType, rawOffset, pageSize)
				if err != nil {
					return hongguoSearchEntry{}, err
				}
			}
		}
	}
	if tab == nil {
		return hongguoSearchEntry{}, errors.New("红果搜索未返回有效结果")
	}

	saveSearchID := func(t map[string]any, firstChunk bool) {
		searchID := mapString(t, "search_id")
		if searchID == "" {
			return
		}
		client.mu.Lock()
		if len(client.searchIDs) >= 64 {
			client.searchIDs = map[string]string{}
		}
		if firstChunk || client.searchIDs[tabType+":"+keyword] == "" {
			client.searchIDs[keyword] = searchID
			client.searchIDs[tabType+":"+keyword] = searchID
		}
		client.mu.Unlock()
	}

	saveSearchID(tab, pageNum == 1)

	entry := hongguoSearchEntry{Dramas: make([]Drama, 0, hongguoSearchGeneralPageSize)}
	seen := make(map[string]bool, hongguoSearchGeneralPageSize)
	appendTabCells := func(t map[string]any) int {
		cells := anyList(t["data"])
		for _, cell := range cells {
			for _, drama := range hongguoSearchCellDramas(cell) {
				if !seen[drama.ID] {
					seen[drama.ID] = true
					entry.Dramas = append(entry.Dramas, drama)
				}
			}
		}
		entry.HasMore = t["has_more"] == true
		return len(cells)
	}

	firstCells := appendTabCells(tab)
	nextOffset := rawOffset + pageSize

	if tabType == hongguoSearchGeneralTab {
		for chunk := 1; chunk < hongguoSearchGeneralChunks && len(entry.Dramas) < hongguoSearchPageSize && entry.HasMore && ctx.Err() == nil; chunk++ {
			_, moreTab, moreErr := downloader.requestHongguoSearchTabAt(ctx, keyword, tabType, nextOffset, pageSize)
			if moreErr != nil || moreTab == nil {
				break
			}
			saveSearchID(moreTab, false)
			if appendTabCells(moreTab) == 0 {
				entry.HasMore = false
				break
			}
			nextOffset += pageSize
		}
	}

	client.mu.Lock()
	if len(client.searchOffsets) >= 64 {
		client.searchOffsets = map[string]int{}
	}
	client.searchOffsets[tabType+":"+keyword] = nextOffset
	client.mu.Unlock()

	if tabType == hongguoSearchSeriesTab && firstCells > 0 && len(entry.Dramas) == 0 {
		return hongguoSearchEntry{}, errors.New("红果搜索结果中没有可识别的剧集")
	}
	if entry.Total < len(entry.Dramas) {
		entry.Total = len(entry.Dramas)
	}
	return entry, nil
}
