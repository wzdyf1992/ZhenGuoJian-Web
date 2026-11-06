package core

import (
	"context"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"time"
	"unicode"
	"unicode/utf8"

	"golang.org/x/text/unicode/norm"
)

const (
	hongguoSearchSeasonLimit   = 200
	hongguoSearchSeasonQueries = 48
	hongguoSearchSeasonTimeout = 25 * time.Second
)

var (
	hongguoSearchExplicitSeasonSuffix = regexp.MustCompile(`第\s*([0-9零〇一二两兩三四五六七八九十百]+)\s*([季部期])[\p{P}\s]*$`)
	hongguoSearchBareSeasonSuffix     = regexp.MustCompile(`([0-9零〇一二两兩三四五六七八九十百]+)\s*([季部期])[\p{P}\s]*$`)
	hongguoQueryTrailingNumberSuffix  = regexp.MustCompile(`([1-9][0-9]{0,2})[\p{P}\s]*$`)
)

type hongguoSearchSeries struct {
	title   string
	sep     string
	key     string
	unit    string
	maximum int
	known   map[int]bool
	labels  map[int]string
}

func parseHongguoSeasonNumber(label string) int {
	if label == "" {
		return 0
	}
	if number, err := strconv.Atoi(label); err == nil {
		if number >= 1 && number <= hongguoSearchSeasonLimit {
			return number
		}
		return 0
	}
	digits := map[rune]int{'零': 0, '〇': 0, '一': 1, '二': 2, '两': 2, '兩': 2, '三': 3, '四': 4, '五': 5, '六': 6, '七': 7, '八': 8, '九': 9}
	number := 0
	if !strings.ContainsAny(label, "十百") {
		for _, r := range label {
			value, ok := digits[r]
			if !ok {
				return 0
			}
			number = number*10 + value
			if number > hongguoSearchSeasonLimit {
				return 0
			}
		}
	} else {
		digit, previous := 0, 1000
		for _, r := range label {
			if value, ok := digits[r]; ok {
				digit = digit*10 + value
				continue
			}
			value := 10
			if r == '百' {
				value = 100
			}
			if value >= previous || digit > 9 {
				return 0
			}
			number += max(digit, 1) * value
			digit, previous = 0, value
		}
		number += digit
	}
	if number < 1 || number > hongguoSearchSeasonLimit {
		return 0
	}
	return number
}

func hongguoSearchSeasonWithSep(title string) (string, int, string, string, string) {
	title = norm.NFKC.String(strings.TrimSpace(title))
	if match := hongguoSearchExplicitSeasonSuffix.FindStringSubmatchIndex(title); match != nil {
		rawPrefix := title[:match[0]]
		base := strings.TrimRightFunc(rawPrefix, func(r rune) bool { return unicode.IsSpace(r) || unicode.IsPunct(r) })
		label, unit := title[match[2]:match[3]], title[match[4]:match[5]]
		number := parseHongguoSeasonNumber(label)
		if base != "" && number > 0 {
			sep := ""
			if strings.ContainsAny(rawPrefix[len(base):], ":：") {
				sep = "："
			}
			return base, number, unit, label, sep
		}
	}
	if match := hongguoSearchBareSeasonSuffix.FindStringSubmatchIndex(title); match != nil {
		rawPrefix := title[:match[0]]
		label, unit := title[match[2]:match[3]], title[match[4]:match[5]]
		number := parseHongguoSeasonNumber(label)
		if number > 0 && rawPrefix != "" {
			allDigits := true
			for _, r := range label {
				if r < '0' || r > '9' {
					allDigits = false
					break
				}
			}
			lastRune, _ := utf8.DecodeLastRuneInString(rawPrefix)
			hasSep := unicode.IsSpace(lastRune) || strings.ContainsRune(":：-—·", lastRune)
			base := strings.TrimRightFunc(rawPrefix, func(r rune) bool { return unicode.IsSpace(r) || unicode.IsPunct(r) })
			if base != "" && (allDigits || number >= 10 || hasSep) {
				sep := ""
				if strings.ContainsAny(rawPrefix[len(base):], ":：") {
					sep = "："
				}
				return base, number, unit, label, sep
			}
		}
	}
	return "", 0, "", "", ""
}

func hongguoSearchSeason(title string) (string, int, string, string) {
	base, number, unit, label, _ := hongguoSearchSeasonWithSep(title)
	return base, number, unit, label
}

func hongguoQuerySeason(query string) (string, int, string) {
	if base, number, unit, _ := hongguoSearchSeason(query); number > 0 {
		return base, number, unit
	}
	query = norm.NFKC.String(strings.TrimSpace(query))
	if match := hongguoQueryTrailingNumberSuffix.FindStringSubmatchIndex(query); match != nil {
		rawPrefix := query[:match[0]]
		if rawPrefix != "" {
			lastRune, _ := utf8.DecodeLastRuneInString(rawPrefix)
			if lastRune < '0' || lastRune > '9' {
				base := strings.TrimRightFunc(rawPrefix, func(r rune) bool { return unicode.IsSpace(r) || unicode.IsPunct(r) })
				number, err := strconv.Atoi(query[match[2]:match[3]])
				if err == nil && base != "" && number >= 1 && number <= hongguoSearchSeasonLimit {
					return base, number, ""
				}
			}
		}
	}
	return "", 0, ""
}

func hongguoSearchChineseSeason(number int) string {
	digits := []rune("零一二三四五六七八九")
	if number < 10 {
		return string(digits[number])
	}
	if number < 100 {
		prefix := ""
		if number >= 20 {
			prefix = string(digits[number/10])
		}
		prefix += "十"
		if number%10 != 0 {
			prefix += string(digits[number%10])
		}
		return prefix
	}
	prefix, rest := string(digits[number/100])+"百", number%100
	if rest == 0 {
		return prefix
	}
	if rest < 10 {
		prefix += "零"
	} else if rest < 20 {
		prefix += "一"
	}
	return prefix + hongguoSearchChineseSeason(rest)
}

func (series *hongguoSearchSeries) add(drama Drama) bool {
	base, season, unit, label, sep := hongguoSearchSeasonWithSep(drama.DisplayTitle())
	if season == 0 && series.sep == "" && hongguoSearchText(drama.DisplayTitle()) == series.key {
		season = 1
	} else if season == 0 || unit != series.unit || sep != series.sep || hongguoSearchText(base) != series.key {
		return false
	}
	series.known[season] = true
	series.maximum = max(series.maximum, season)
	if label != "" {
		series.labels[season] = label
	}
	return true
}

func hongguoSearchSeriesGroups(keyword string, dramas []Drama) []*hongguoSearchSeries {
	if _, season, _ := hongguoQuerySeason(keyword); season > 0 {
		return nil
	}
	query := hongguoSearchText(keyword)
	var groups []*hongguoSearchSeries
	byKey := map[string]*hongguoSearchSeries{}
	for _, drama := range dramas {
		base, season, unit, _, sep := hongguoSearchSeasonWithSep(drama.DisplayTitle())
		key := hongguoSearchText(base)
		if season == 0 || !strings.Contains(key, query) {
			continue
		}
		groupKey := key + ":" + sep + ":" + unit
		if byKey[groupKey] == nil {
			series := &hongguoSearchSeries{title: base, sep: sep, key: key, unit: unit, known: map[int]bool{}, labels: map[int]string{}}
			byKey[groupKey] = series
			groups = append(groups, series)
		}
	}
	var incomplete []*hongguoSearchSeries
	for _, group := range groups {
		for _, drama := range dramas {
			group.add(drama)
		}
		if len(group.known) < group.maximum && (len(group.known) >= 2 || (len(group.known) >= 1 && group.maximum >= 2 && group.key == query)) {
			incomplete = append(incomplete, group)
		}
	}
	sort.SliceStable(incomplete, func(left, right int) bool {
		l, r := incomplete[left], incomplete[right]
		lr, rr := hongguoTitleSearchRank(l.title, query), hongguoTitleSearchRank(r.title, query)
		if lr != rr {
			return lr < rr
		}
		return len(l.known) > len(r.known)
	})
	return incomplete
}

func (series *hongguoSearchSeries) hasArabicLabel() bool {
	for _, label := range series.labels {
		if _, err := strconv.Atoi(label); err == nil {
			return true
		}
	}
	return false
}

func (series *hongguoSearchSeries) nextQuery(attempted map[string]bool) string {
	useBothStyles := len(series.labels) < 2 || series.hasArabicLabel()
	prefix := series.title + series.sep
	for season := 1; season <= series.maximum; season++ {
		if series.known[season] {
			continue
		}
		nearest, distance := 0, hongguoSearchSeasonLimit+1
		for known := 1; known <= series.maximum; known++ {
			delta := max(known-season, season-known)
			if series.labels[known] != "" && delta < distance {
				nearest, distance = known, delta
			}
		}
		labels := []string{hongguoSearchChineseSeason(season), strconv.Itoa(season)}
		if _, err := strconv.Atoi(series.labels[nearest]); err == nil {
			labels[0], labels[1] = labels[1], labels[0]
		}
		if !useBothStyles {
			labels = labels[:1]
		}
		var queries []string
		if season == 1 && series.sep == "" {
			queries = append(queries, series.title)
		}
		for _, label := range labels {
			queries = append(queries, prefix+"第"+label+series.unit)
		}
		for _, query := range queries {
			if _, err := hongguoSearchKeyword(query); err == nil && !attempted[query] {
				return query
			}
		}
	}
	return ""
}

func (downloader *Downloader) completeHongguoSearchSeasons(ctx context.Context, keyword string, entry *hongguoSearchEntry) bool {
	groups := hongguoSearchSeriesGroups(keyword, entry.Dramas)
	if len(groups) == 0 {
		return false
	}
	budget := hongguoSearchSeasonTimeout
	if deadline, ok := ctx.Deadline(); ok {
		budget = min(budget, time.Until(deadline)-100*time.Millisecond)
	}
	if budget <= 0 {
		return true
	}
	ctx, cancel := context.WithTimeout(ctx, budget)
	defer cancel()
	attempted := map[string]bool{keyword: true}
	requests := 0
	for requests < hongguoSearchSeasonQueries {
		progressed := false
		for _, group := range groups {
			if requests >= hongguoSearchSeasonQueries {
				break
			}
			query := group.nextQuery(attempted)
			if query == "" {
				continue
			}
			progressed = true
			attempted[query] = true
			requests++
			names, err := downloader.fetchHongguoSearchNamesSingle(ctx, query)
			if err != nil {
				return true
			}
			var matches []Drama
			for _, drama := range names {
				matched := false
				for _, candidate := range groups {
					if candidate.add(drama) {
						matched = true
					}
				}
				if matched {
					matches = append(matches, drama)
				}
			}
			if len(matches) > 0 {
				entry.Dramas = mergeHongguoSearchDramas(entry.Dramas, matches)
				reportHongguoSearchProgress(ctx, *entry)
			}
		}
		if !progressed {
			break
		}
	}
	for _, group := range groups {
		if group.nextQuery(attempted) != "" {
			return true
		}
	}
	return false
}
