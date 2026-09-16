package app

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"html"
	"io"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"time"
)

var (
	huangguoAIBaseURL     = "https://huangguoai.com"
	huangguoAIBaseMirrors = []string{"https://huangguoai.com", "https://ttvoij.ediayikma.cc", "https://thu.ediayikma.cc", "https://pku.ediayikma.cc", "https://fdu.ediayikma.cc", "https://thu.agdkczeyx.cc"}
	huangguoVideoBaseURL  = "https://huangguo.video"
	huangguoAISlugs       = []string{"recommend", "newest", "ai-duanju", "ai-manju", "ai-huanlian", "ai-mogai", "ranks/hot"}
	huangguoAITypeNames   = map[string]string{"recommend": "精选推荐", "newest": "最近上新", "ai-duanju": "AI成人短剧", "ai-manju": "AI成人漫剧", "ai-huanlian": "AI换脸", "ai-mogai": "AI魔改", "ranks/hot": "排行榜"}
	reDetailHref          = regexp.MustCompile(`(?is)<a\b[^>]*href=["']([^"']*/detail/([^"'/?#]+)[^"']*)["'][^>]*>.*?</a>`)
	reDramaCardStart      = regexp.MustCompile(`(?is)<div\b[^>]*class=["'][^"']*\bhg-drama-card\b[^"']*["'][^>]*>`)
	reVideoCard           = regexp.MustCompile(`(?is)<article\b[^>]*class=["'][^"']*\bvideo-card\b[^"']*["'][^>]*>.*?</article>`)
	reAnyArticle          = regexp.MustCompile(`(?is)<article\b[^>]*>.*?</article>`)
	reHuangguoLink        = regexp.MustCompile(`(?is)<a\b[^>]*href=["']([^"']*/(?:series|video)/([^"'/?#]+)[^"']*)["'][^>]*>.*?</a>`)
	reAIEpisodeLink       = regexp.MustCompile(`(?is)<a\b[^>]*href=["']([^"']*/video/[^"']+)["'][^>]*>.*?</a>`)
	reTitleTag            = regexp.MustCompile(`(?is)<title[^>]*>(.*?)</title>`)
	reH1Tag               = regexp.MustCompile(`(?is)<h1[^>]*>(.*?)</h1>`)
	reDescMeta            = regexp.MustCompile(`(?is)<meta\b[^>]*(?:name|property)=["'](?:description|og:description)["'][^>]*content=["']([^"']+)["'][^>]*>`)
	reEpisodeNumber       = regexp.MustCompile(`(?i)(?:第\s*0*(\d+)\s*(?:集|话|期)|(?:更新至|共|全)\s*0*(\d+)\s*(?:集|话|期)|(?:episode|ep)\s*#?\s*0*(\d+))`)
	reDataHLS             = regexp.MustCompile(`(?is)data-hls=["']([^"']+)["']`)
	reDataPlaySrc         = regexp.MustCompile(`(?is)data-play-src=["']([^"']+)["']`)
	reMediaFieldValue     = regexp.MustCompile(`(?is)["']?(?:videoSrc|videoUrl|playUrl|src|url)["']?\s*[:=]\s*["']([^"']+\.(?:m3u8|mp4)(?:[^"']*)?)["']`)
	reViewsText           = regexp.MustCompile(`(?i)[0-9]+(?:\.[0-9]+)?\s*[w万]?\s*次播放`)
	reScoreText           = regexp.MustCompile(`[0-9]+(?:\.[0-9]+)?\s*分`)
)

type providerEpisode struct {
	Key, Title, URL string
	Index           int
	HLS             string
}

func (d *Downloader) fetchHuangguoAIDramas(ctx context.Context) ([]Drama, error) {
	type aiPage struct {
		url      string
		referer  string
		category string
		jsonAPI  bool
	}
	maxPages := d.cfg.MaxPagesPerSort
	if maxPages <= 0 {
		maxPages = 1
	}
	pages := []aiPage{{url: huangguoAIBaseURL + "/", referer: huangguoAIBaseURL + "/", category: "首页"}}
	for _, slug := range huangguoAISlugs {
		categoryURL := huangguoAIListURL(slug, 1)
		category := huangguoAITypeName(slug)
		pages = append(pages, aiPage{url: categoryURL, referer: huangguoAIBaseURL + "/", category: category})

		if !strings.HasPrefix(slug, "ai-") {
			continue
		}
		for page := 1; page <= maxPages; page++ {
			apiURL := fmt.Sprintf("%s/api/videos/category/%s?sort=hot&page=%d&size=24", strings.TrimRight(huangguoAIBaseURL, "/"), url.PathEscape(slug), page)
			pages = append(pages, aiPage{url: apiURL, referer: categoryURL, category: category, jsonAPI: true})
		}
	}
	type result struct {
		items []Drama
		err   error
	}
	ch := make(chan result, len(pages))
	semaphore := make(chan struct{}, 6)
	for _, page := range pages {
		page := page
		go func() {
			select {
			case semaphore <- struct{}{}:
				defer func() { <-semaphore }()
			case <-ctx.Done():
				ch <- result{err: ctx.Err()}
				return
			}
			pageCtx, cancel := context.WithTimeout(ctx, 45*time.Second)
			defer cancel()
			body, err := d.fetchProviderText(pageCtx, page.url, page.referer)
			if err != nil {
				ch <- result{err: fmt.Errorf("%s: %w", page.url, err)}
				return
			}
			if page.jsonAPI {
				ch <- result{items: parseHuangguoAIJSONCards([]byte(body), page.url, page.category)}
				return
			}
			ch <- result{items: parseHuangguoAIDramaCards(body, page.url, page.category)}
		}()
	}
	index := map[string]int{}
	var out []Drama
	var lastErr error
	for range pages {
		res := <-ch
		if res.err != nil {
			lastErr = res.err
			continue
		}
		reportLibraryProgress(ctx, sourceHuangguoAI, res.items, nil, false)
		for _, dr := range res.items {
			if dr.ID == "" {
				continue
			}
			if pos, ok := index[dr.ID]; ok {
				out[pos] = mergeDramaMetadata(out[pos], dr)
				continue
			}
			index[dr.ID] = len(out)
			out = append(out, dr)
		}
	}
	sort.SliceStable(out, func(i, j int) bool { return out[i].DisplayTitle() < out[j].DisplayTitle() })
	if len(out) == 0 && lastErr == nil {
		lastErr = fmt.Errorf("黄果页面未解析到视频，可能是站点结构改变或访问验证")
	}
	return out, lastErr
}

func huangguoAIListURL(slug string, page int) string {
	base := strings.TrimRight(huangguoAIBaseURL, "/")
	if page < 1 {
		page = 1
	}
	switch slug {
	case "recommend":
		return fmt.Sprintf("%s/recommend/%d/", base, page)
	case "newest":
		return fmt.Sprintf("%s/newest/%d/", base, page)
	case "ranks/hot":
		return base + "/ranks/hot/"
	default:
		return fmt.Sprintf("%s/%s/", base, strings.Trim(slug, "/"))
	}
}

func huangguoAITypeName(slug string) string {
	if name := huangguoAITypeNames[slug]; name != "" {
		return name
	}
	return slug
}

func (d *Downloader) fetchHuangguoVideoDramas(ctx context.Context) ([]Drama, error) {
	seen := map[string]bool{}
	var out []Drama
	var lastErr error
	add := func(items []Drama) {
		reportLibraryProgress(ctx, sourceHuangguoVideo, items, nil, false)
		for _, dr := range items {
			if dr.ID == "" || seen[dr.ID] {
				continue
			}
			seen[dr.ID] = true
			out = append(out, dr)
		}
	}
	pages := []string{strings.TrimRight(huangguoVideoBaseURL, "/") + "/videos"}
	for category := 1; category <= 4; category++ {
		pages = append(pages, fmt.Sprintf("%s/videos?category=%d", strings.TrimRight(huangguoVideoBaseURL, "/"), category))
	}
	for _, pageURL := range pages {
		body, err := d.fetchProviderText(ctx, pageURL, huangguoVideoBaseURL+"/")
		if err != nil {
			lastErr = err
			var backoff *requestBackoff
			if errors.As(err, &backoff) || ctx.Err() != nil {
				break
			}
			continue
		}
		add(parseHuangguoVideoCards(body, pageURL))
	}
	if len(out) == 0 && lastErr != nil {
		return nil, lastErr
	}
	sort.SliceStable(out, func(i, j int) bool { return out[i].DisplayTitle() < out[j].DisplayTitle() })
	return out, lastErr
}

func (d *Downloader) GetHuangguoChapters(ctx context.Context, source, sourceID string) (string, []Chapter, error) {
	switch canonicalProviderSource(source) {
	case sourceHuangguoAI:
		return d.fetchHuangguoAIChapters(ctx, sourceID)
	case sourceHuangguoVideo:
		return d.fetchHuangguoVideoChapters(ctx, sourceID)
	case sourceHuangdou:
		return d.fetchHuangdouChapters(ctx, sourceID)
	case sourceHongguo:
		return d.fetchHongguoChapters(ctx, sourceID)
	default:
		return "", nil, fmt.Errorf("unsupported provider source: %s", source)
	}
}

func (d *Downloader) fetchHuangguoAIChapters(ctx context.Context, sourceID string) (string, []Chapter, error) {
	sourceID = strings.Trim(strings.TrimSpace(sourceID), "/")
	sourceID = strings.TrimPrefix(sourceID, "detail/")
	if sourceID == "" {
		return "", nil, fmt.Errorf("empty huangguoai sourceID")
	}
	detailURL := strings.TrimRight(huangguoAIBaseURL, "/") + "/detail/" + url.PathEscape(sourceID) + "/"
	body, err := d.fetchProviderText(ctx, detailURL, huangguoAIBaseURL+"/")
	if err != nil {
		return "", nil, err
	}
	title := firstNonEmpty(extractPageTitle(body), titleNearDetail(body, sourceID), "短剧")
	episodes := parseHuangguoAIEpisodes(body, detailURL, sourceID)
	if len(episodes) == 0 {
		if media := parseAIVideoURL(body, detailURL); media != "" {
			episodes = []providerEpisode{{Key: "1", Title: title, URL: detailURL, Index: 1, HLS: media}}
		}
	}
	if len(episodes) == 0 {
		return title, nil, nil
	}
	var chapters []Chapter
	for _, ep := range episodes {
		mediaURL := ep.HLS
		idx := ep.Index
		if idx <= 0 {
			idx = len(chapters) + 1
		}
		chapterTitle := strings.TrimSpace(ep.Title)
		if chapterTitle == "" {
			chapterTitle = fmt.Sprintf("第%d集", idx)
		}
		key := ep.Key
		if key == "" {
			key = strconv.Itoa(idx)
		}
		chapters = append(chapters, Chapter{ID: providerChapterID(sourceHuangguoAI, sourceID, key), Source: sourceHuangguoAI, Title: chapterTitle, VideoURL: mediaURL, PageURL: ep.URL, CurrentEpisode: rawEpisode(idx)})
	}
	sortProviderChapters(chapters)
	return title, uniqueChapters(chapters), nil
}

func (d *Downloader) fetchHuangguoVideoChapters(ctx context.Context, sourceID string) (string, []Chapter, error) {
	sourceID = strings.Trim(strings.TrimSpace(sourceID), "/")
	if sourceID == "" {
		return "", nil, fmt.Errorf("empty huangguo-video sourceID")
	}
	detailPath := sourceID
	if !strings.HasPrefix(detailPath, "series/") && !strings.HasPrefix(detailPath, "video/") {
		detailPath = "series/" + detailPath
	}
	detailURL := strings.TrimRight(huangguoVideoBaseURL, "/") + "/" + detailPath
	body, err := d.fetchProviderText(ctx, detailURL, huangguoVideoBaseURL+"/")
	if err != nil {
		return "", nil, err
	}
	title := firstNonEmpty(extractPageTitle(body), "短剧")
	var episodes []providerEpisode
	if strings.HasPrefix(detailPath, "video/") {
		if hls := parseDataHLS(body, detailURL); hls != "" {
			episodes = []providerEpisode{{Key: "1", Title: title, URL: detailURL, Index: 1, HLS: hls}}
		}
	} else {
		episodes = parseHuangguoVideoEpisodes(body, detailURL)
	}
	if len(episodes) == 0 {
		if hls := parseDataHLS(body, detailURL); hls != "" {
			episodes = []providerEpisode{{Key: "1", Title: title, URL: detailURL, Index: 1, HLS: hls}}
		}
	}
	var chapters []Chapter
	for _, ep := range episodes {
		hlsURL := ep.HLS
		idx := ep.Index
		if idx <= 0 {
			idx = len(chapters) + 1
		}
		chapterTitle := strings.TrimSpace(ep.Title)
		if chapterTitle == "" {
			chapterTitle = fmt.Sprintf("第%d集", idx)
		}
		key := ep.Key
		if key == "" {
			key = strconv.Itoa(idx)
		}
		chapters = append(chapters, Chapter{ID: providerChapterID(sourceHuangguoVideo, sourceID, key), Source: sourceHuangguoVideo, Title: chapterTitle, VideoURL: hlsURL, PageURL: ep.URL, CurrentEpisode: rawEpisode(idx)})
	}
	sortProviderChapters(chapters)
	return title, uniqueChapters(chapters), nil
}

func generatedHuangguoAIMirrors(seed string, n int) []string {
	if n <= 0 {
		return nil
	}
	out := make([]string, 0, n)
	seen := map[string]bool{}
	for i := 0; len(out) < n && i < n*4; i++ {
		sum := sha256.Sum256([]byte(fmt.Sprintf("%s:%d", seed, i)))
		hexText := fmt.Sprintf("%x", sum[:])
		sub := strings.TrimLeft(hexText[:6], "0123456789")
		if len(sub) < 4 {
			sub = hexText[6:12]
		}
		if len(sub) > 6 {
			sub = sub[:6]
		}
		if len(sub) < 3 || seen[sub] {
			continue
		}
		seen[sub] = true
		out = append(out, "https://"+sub+".ediayikma.cc")
	}
	return out
}

func parseHuangguoAIDramaCards(rawHTML, pageURL, category string) []Drama {
	positions := map[string]int{}
	var out []Drama
	add := func(dr Drama) {
		if dr.ID == "" || dr.SourceID == "" {
			return
		}
		if position, found := positions[dr.SourceID]; found {
			out[position] = mergeDramaMetadata(out[position], dr)
			return
		}
		positions[dr.SourceID] = len(out)
		out = append(out, dr)
	}
	markup := huangguoNonContent.ReplaceAllString(rawHTML, "")
	for _, block := range splitHuangguoAICardBlocks(markup) {
		sourceID := cleanID(firstNonEmpty(extractAttr(block, "data-track-id"), detailIDFromString(extractAttr(block, "href"))))
		if sourceID == "" {
			if m := reDetailHref.FindStringSubmatch(block); len(m) > 2 {
				sourceID = cleanID(m[2])
			}
		}
		if sourceID == "" {
			continue
		}
		title := huangguoCardTitle(block, sourceID)
		cover := resolveProviderURL(pageURL, firstNonEmpty(extractAttr(block, "data-src"), extractAttr(block, "data-original"), extractAttr(block, "src")))
		desc := firstNonEmpty(extractByClassText(block, "hg-drama-card__desc"), extractDescription(block))
		score := firstNonEmpty(extractByClassText(block, "hg-drama-card__score"), firstMatchText(reScoreText, block))
		episodeBlock := huangguoClassBlock(block, "hg-drama-card__episode")
		episode := firstNonEmpty(extractAttr(episodeBlock, "data-ep-base"), cleanText(episodeBlock))
		badge := extractByClassText(block, "hg-drama-card__badge")
		remark := strings.TrimSpace(strings.Join(nonEmptyStrings(badge, episode), " "))
		if remark == "" {
			remark = "在线观看"
		}
		views := normalizeViews(firstNonEmpty(firstMatchText(reViewsText, block), extractByClassText(block, "hg-drama-card__views"), extractByClassText(block, "hg-drama-card__play")))
		online := normalizeDate(firstMatchText(reDateText, block))
		tags := extractTags(block)
		typeName := firstNonEmpty(extractAttr(block, "data-track-type-name"), category)
		if title == "" {
			title = sourceID
		}
		dr := Drama{ID: providerDramaID(sourceHuangguoAI, sourceID), Source: sourceHuangguoAI, SourceID: sourceID, Title: title, Name: title, Desc: desc, Intro: desc, Cover: cover, CoverURL: cover, CategoryName: typeName, ChannelName: "huangguoai.com", Remark: remark, Score: score, Views: views, OnlineDate: online, Tags: tags}
		if n := episodeIndex(episode, 0); n > 0 {
			dr.TotalEpisode = n
			dr.EpisodeCount = n
		}
		add(dr)
	}
	for _, m := range reDetailHref.FindAllStringSubmatchIndex(markup, -1) {
		if len(m) < 6 || m[4] < 0 || m[5] < 0 {
			continue
		}
		sourceID := cleanID(markup[m[4]:m[5]])
		block := markup[m[0]:m[1]]
		title := firstHuangguoTitle(sourceID, huangguoCardTitle(block, sourceID), cleanText(block))
		if title == "" {
			continue
		}
		cover := resolveProviderURL(pageURL, extractAttr(block, "data-src", "data-original", "src"))
		add(Drama{ID: providerDramaID(sourceHuangguoAI, sourceID), Source: sourceHuangguoAI, SourceID: sourceID, Title: title, Name: title, Cover: cover, CoverURL: cover, CategoryName: category, ChannelName: "huangguoai.com"})
	}
	for _, dr := range parseHuangguoAIJSONLDDramas(rawHTML, category) {
		add(dr)
	}
	return out
}

func splitHuangguoAICardBlocks(raw string) []string {
	starts := reDramaCardStart.FindAllStringIndex(raw, -1)
	blocks := make([]string, 0, len(starts))
	for i, m := range starts {
		end := len(raw)
		if i+1 < len(starts) {
			end = starts[i+1][0]
		}
		blocks = append(blocks, huangguoElementBlock(raw[:end], m[0], m[1], "div"))
	}
	return blocks
}

func parseHuangguoAIJSONLDDramas(raw, category string) []Drama {
	var out []Drama
	seen := map[string]bool{}
	for _, block := range regexp.MustCompile(`(?is)<script[^>]*type=["']application/ld\+json["'][^>]*>(.*?)</script>`).FindAllStringSubmatch(raw, -1) {
		if len(block) < 2 {
			continue
		}
		var value any
		if json.Unmarshal([]byte(html.UnescapeString(block[1])), &value) != nil {
			continue
		}
		var walk func(any)
		walk = func(v any) {
			switch x := v.(type) {
			case []any:
				for _, item := range x {
					walk(item)
				}
			case map[string]any:
				name, _ := x["name"].(string)
				itemURL, _ := x["url"].(string)
				id := detailIDFromString(itemURL)
				if id != "" && !huangguoTitleNeedsRepair(name, id) && !seen[id] {
					seen[id] = true
					title := cleanText(name)
					out = append(out, Drama{ID: providerDramaID(sourceHuangguoAI, id), Source: sourceHuangguoAI, SourceID: id, Title: title, Name: title, CategoryName: category, ChannelName: "huangguoai.com"})
				}
				for _, child := range x {
					walk(child)
				}
			}
		}
		walk(value)
	}
	return out
}

func parseHuangguoAIJSONCards(raw []byte, pageURL, category string) []Drama {
	var decoded any
	if err := json.Unmarshal(raw, &decoded); err != nil {
		return nil
	}
	seen := map[string]bool{}
	var out []Drama
	var walk func(any)
	walk = func(v any) {
		switch x := v.(type) {
		case []any:
			for _, item := range x {
				walk(item)
			}
		case map[string]any:
			if dr, ok := dramaFromAIMap(x, pageURL, category); ok {
				if !seen[dr.SourceID] {
					seen[dr.SourceID] = true
					out = append(out, dr)
				}
				return
			}
			for _, child := range x {
				walk(child)
			}
		}
	}
	walk(decoded)
	return out
}

func dramaFromAIMap(m map[string]any, pageURL, category string) (Drama, bool) {
	sourceID := firstNonEmpty(mapString(m, "id"), mapString(m, "videoId"), mapString(m, "video_id"), mapString(m, "vid"), mapString(m, "slug"))
	if sourceID == "" {
		for _, v := range m {
			if s, ok := v.(string); ok {
				if id := detailIDFromString(s); id != "" {
					sourceID = id
					break
				}
			}
		}
	}
	if detailID := detailIDFromString(sourceID); detailID != "" {
		sourceID = detailID
	}
	sourceID = cleanID(sourceID)
	if sourceID == "" {
		return Drama{}, false
	}
	title := firstNonEmpty(mapString(m, "title"), mapString(m, "name"), mapString(m, "videoTitle"), mapString(m, "video_title"), mapString(m, "videoName"), mapString(m, "video_name"))
	if huangguoTitleNeedsRepair(title, sourceID) {
		return Drama{}, false
	}
	cover := resolveProviderURL(pageURL, firstNonEmpty(mapString(m, "cover"), mapString(m, "coverUrl"), mapString(m, "cover_url"), mapString(m, "coverImage"), mapString(m, "cover_image"), mapString(m, "image"), mapString(m, "imageUrl"), mapString(m, "thumbnail"), mapString(m, "poster"), mapString(m, "posterUrl")))
	desc := firstNonEmpty(mapString(m, "desc"), mapString(m, "description"), mapString(m, "intro"), mapString(m, "summary"))
	eps := huangguoMapEpisodes(m)
	remark := firstNonEmpty(mapString(m, "vod_remarks"), mapString(m, "remark"), mapString(m, "remarks"))
	if remark == "" && eps != "" {
		if strings.EqualFold(mapString(m, "is_finished"), "true") || mapString(m, "is_finished") == "1" {
			remark = "全" + eps + "集"
		} else {
			remark = "更新至 " + eps + " 集"
		}
	}
	score := firstNonEmpty(mapString(m, "score"), mapString(m, "rating"))
	if score != "" && !strings.Contains(score, "分") {
		score += "分"
	}
	views := normalizeViews(firstNonEmpty(mapString(m, "views"), mapString(m, "view_count"), mapString(m, "viewCount"), mapString(m, "view_num"), mapString(m, "viewNum"), mapString(m, "views_text"), mapString(m, "view_count_text"), mapString(m, "play_count"), mapString(m, "playCount"), mapString(m, "play_num"), mapString(m, "playNum"), mapString(m, "play_count_text"), mapString(m, "hits"), mapString(m, "hit_count"), mapString(m, "plays"), mapString(m, "play")))
	online := providerReleaseDate(firstNonEmpty(mapString(m, "onlineDate"), mapString(m, "online_date"), mapString(m, "online_time"), mapString(m, "onlineTime"), mapString(m, "publish_date"), mapString(m, "publishDate"), mapString(m, "publish_time"), mapString(m, "publishTime"), mapString(m, "release_date"), mapString(m, "releaseDate")))
	tags := mapStringSlice(m, "tags", "tag", "categories", "category")
	return Drama{ID: providerDramaID(sourceHuangguoAI, sourceID), Source: sourceHuangguoAI, SourceID: sourceID, Title: title, Name: title, Desc: desc, Intro: desc, Cover: cover, CoverURL: cover, TotalEpisode: eps, EpisodeCount: eps, CategoryName: category, ChannelName: "huangguoai.com", Remark: remark, Score: score, Views: views, OnlineDate: online, Tags: tags}, true
}

func parseHuangguoVideoCards(rawHTML, pageURL string) []Drama {
	blocks := reVideoCard.FindAllString(rawHTML, -1)
	if len(blocks) == 0 {
		for _, block := range reAnyArticle.FindAllString(rawHTML, -1) {
			if strings.Contains(strings.ToLower(block), "video-card") {
				blocks = append(blocks, block)
			}
		}
	}
	seen := map[string]bool{}
	var out []Drama
	for _, block := range blocks {
		link := reHuangguoLink.FindStringSubmatch(block)
		if len(link) < 3 {
			continue
		}
		path, sourceID := huangguoVideoSourceID(link[1])
		if sourceID == "" || seen[sourceID] {
			continue
		}
		seen[sourceID] = true
		title := firstNonEmpty(extractClosestAttr(block, 0, len(block), "title", "alt", "aria-label"), cleanText(block))
		cover := resolveProviderURL(pageURL, extractClosestAttr(block, 0, len(block), "data-src", "data-original", "src"))
		category := "video"
		if strings.HasPrefix(path, "series/") {
			category = "series"
		}
		desc := firstNonEmpty(extractClosestAttr(block, 0, len(block), "data-description", "description"), extractDescription(block))
		out = append(out, Drama{ID: providerDramaID(sourceHuangguoVideo, sourceID), Source: sourceHuangguoVideo, SourceID: sourceID, Title: title, Name: title, Desc: desc, Intro: desc, Cover: cover, CoverURL: cover, CategoryName: category, ChannelName: "huangguo.video", Remark: firstNonEmpty(extractByClassText(block, "bg-black/55"), extractByClassText(block, "text-gold-dim"))})
	}
	return out
}

func parseHuangguoAIEpisodes(rawHTML, pageURL, sourceID string) []providerEpisode {
	matches := reAIEpisodeLink.FindAllStringSubmatchIndex(rawHTML, -1)
	seen := map[string]bool{}
	var episodes []providerEpisode
	usedIndex := map[int]bool{}
	for _, m := range matches {
		if len(m) < 4 || m[2] < 0 || m[3] < 0 {
			continue
		}
		href := rawHTML[m[2]:m[3]]
		fullURL := resolveProviderURL(pageURL, href)
		linkHTML := rawHTML[m[0]:m[1]]
		title := firstNonEmpty(extractAttr(linkHTML, "title", "aria-label"), cleanText(linkHTML))
		key := aiEpisodeKey(href, sourceID)
		if key == "" {
			key = fmt.Sprintf("auto-%d", len(episodes)+1)
		}
		if seen[key] || seen[fullURL] {
			continue
		}
		seen[key] = true
		seen[fullURL] = true
		idx := episodeIndex(title, 0)
		if idx <= 0 {
			idx = len(episodes) + 1
		}
		idx = nextUnusedEpisodeIndex(idx, usedIndex)
		usedIndex[idx] = true
		episodes = append(episodes, providerEpisode{Key: key, Title: title, URL: fullURL, Index: idx})
	}
	sortProviderEpisodes(episodes)
	return episodes
}

func parseHuangguoVideoEpisodes(rawHTML, pageURL string) []providerEpisode {
	blocks := reVideoCard.FindAllString(rawHTML, -1)
	if len(blocks) == 0 {
		blocks = reHuangguoLink.FindAllString(rawHTML, -1)
	}
	seen := map[string]bool{}
	var episodes []providerEpisode
	for _, block := range blocks {
		link := reHuangguoLink.FindStringSubmatch(block)
		if len(link) < 2 {
			continue
		}
		path, key := huangguoVideoSourceID(link[1])
		if key == "" || !strings.HasPrefix(path, "video/") {
			continue
		}
		fullURL := resolveProviderURL(pageURL, link[1])
		if seen[key] || seen[fullURL] {
			continue
		}
		seen[key] = true
		seen[fullURL] = true
		title := firstNonEmpty(extractAttr(block, "title"), extractAttr(block, "alt"), cleanText(block))
		episodes = append(episodes, providerEpisode{Key: key, Title: title, URL: fullURL, Index: episodeIndex(title, len(episodes)+1), HLS: parseDataHLS(block, pageURL)})
	}
	sortProviderEpisodes(episodes)
	return episodes
}

func parseAIVideoURL(rawHTML, pageURL string) string {
	if media := parseAIVideoInitialData(rawHTML, pageURL); media != "" {
		return media
	}
	if m := reDataPlaySrc.FindStringSubmatch(rawHTML); len(m) > 1 {
		if media := normalizeProviderMediaURL(pageURL, m[1]); media != "" {
			return media
		}
	}
	for _, m := range reMediaFieldValue.FindAllStringSubmatch(rawHTML, -1) {
		if len(m) > 1 {
			if media := normalizeProviderMediaURL(pageURL, m[1]); media != "" {
				return media
			}
		}
	}
	return ""
}

func parseAIVideoInitialData(rawHTML, pageURL string) string {
	re := regexp.MustCompile(`(?is)<script\b[^>]*id=["']videoInitialData["'][^>]*>(.*?)</script>`)
	m := re.FindStringSubmatch(rawHTML)
	if len(m) < 2 {
		return ""
	}
	var data map[string]any
	if err := json.Unmarshal([]byte(html.UnescapeString(m[1])), &data); err != nil {
		return ""
	}
	if media := normalizeProviderMediaURL(pageURL, mapString(data, "videoSrc", "videoUrl", "playUrl")); media != "" {
		return media
	}
	eps, _ := data["epPlaySrcs"].(map[string]any)
	if len(eps) == 0 {
		return ""
	}
	preferred := firstNonEmpty(mapString(data, "ep", "episode"))
	if m := regexp.MustCompile(`/ep-(\d+)(?:/|$)`).FindStringSubmatch(pageURL); len(m) > 1 {
		preferred = m[1]
	}
	if preferred != "" {
		if media := normalizeProviderMediaURL(pageURL, fmt.Sprint(eps[preferred])); media != "" {
			return media
		}
	}
	keys := make([]string, 0, len(eps))
	for key := range eps {
		keys = append(keys, key)
	}
	sort.SliceStable(keys, func(i, j int) bool { return episodeIndex(keys[i], i+1) < episodeIndex(keys[j], j+1) })
	for _, key := range keys {
		if media := normalizeProviderMediaURL(pageURL, fmt.Sprint(eps[key])); media != "" {
			return media
		}
	}
	return ""
}

func parseDataHLS(rawHTML, pageURL string) string {
	if m := reDataHLS.FindStringSubmatch(rawHTML); len(m) > 1 {
		return normalizeProviderMediaURL(pageURL, m[1])
	}
	return ""
}

func (d *Downloader) resolveHuangguoVideoHLS(ctx context.Context, hlsURL, referer string) string {
	if strings.HasSuffix(strings.ToLower(strings.SplitN(hlsURL, "?", 2)[0]), ".mp4") {
		return hlsURL
	}
	body, err := d.fetchProviderText(ctx, hlsURL, referer)
	if err != nil {
		return hlsURL
	}
	if best := selectBestM3U8Variant(body, hlsURL); best != "" {
		return best
	}
	return hlsURL
}

func sortProviderEpisodes(episodes []providerEpisode) {
	sort.SliceStable(episodes, func(i, j int) bool {
		if episodes[i].Index != episodes[j].Index {
			return episodes[i].Index < episodes[j].Index
		}
		return episodes[i].Key < episodes[j].Key
	})
}

func chapterEpisodeNumber(ch Chapter, fallback int) int {
	if len(ch.CurrentEpisode) > 0 && string(ch.CurrentEpisode) != "null" {
		var n int
		if err := json.Unmarshal(ch.CurrentEpisode, &n); err == nil && n > 0 {
			return n
		}
		var s string
		if err := json.Unmarshal(ch.CurrentEpisode, &s); err == nil {
			return episodeIndex(s, fallback)
		}
	}
	return episodeIndex(ch.Title, fallback)
}

func episodeIndex(s string, fallback int) int {
	if m := reEpisodeNumber.FindStringSubmatch(s); len(m) > 1 {
		for _, group := range m[1:] {
			if group == "" {
				continue
			}
			if n, err := strconv.Atoi(group); err == nil && n > 0 {
				return n
			}
		}
	}
	return fallback
}

func nextUnusedEpisodeIndex(preferred int, used map[int]bool) int {
	if preferred <= 0 {
		preferred = 1
	}
	if !used[preferred] {
		return preferred
	}
	for i := 1; ; i++ {
		if !used[i] {
			return i
		}
	}
}

func aiEpisodeKey(href, sourceID string) string {
	u, err := url.Parse(html.UnescapeString(href))
	path := href
	if err == nil {
		path = u.Path
	}
	path = strings.Trim(strings.TrimPrefix(path, "/video/"), "/")
	parts := strings.Split(path, "/")
	if len(parts) > 1 && parts[0] == sourceID {
		return cleanID(parts[len(parts)-1])
	}
	if len(parts) == 1 && parts[0] == sourceID {
		return ""
	}
	return cleanID(strings.Join(parts, "-"))
}

func huangguoVideoSourceID(href string) (path, sourceID string) {
	u, err := url.Parse(html.UnescapeString(href))
	if err == nil {
		path = strings.Trim(u.Path, "/")
	} else {
		path = strings.Trim(href, "/")
	}
	parts := strings.Split(path, "/")
	for i := 0; i+1 < len(parts); i++ {
		if parts[i] == "series" || parts[i] == "video" {
			code := cleanID(parts[i+1])
			if code == "" {
				return "", ""
			}
			return parts[i] + "/" + code, parts[i] + "/" + code
		}
	}
	return "", ""
}

func detailIDFromString(s string) string {
	u, err := url.Parse(html.UnescapeString(s))
	path := s
	if err == nil {
		path = u.Path
	}
	parts := strings.Split(strings.Trim(path, "/"), "/")
	for i := 0; i+1 < len(parts); i++ {
		if parts[i] == "detail" {
			return cleanID(parts[i+1])
		}
	}
	return ""
}

func cleanID(s string) string {
	s = strings.TrimSpace(html.UnescapeString(s))
	s = strings.Trim(s, " /\t\r\n\"'")
	if q := strings.IndexAny(s, "?#"); q >= 0 {
		s = s[:q]
	}
	return s
}

func extractPageTitle(rawHTML string) string {
	if m := reH1Tag.FindStringSubmatch(rawHTML); len(m) > 1 {
		if t := cleanText(m[1]); t != "" {
			return t
		}
	}
	if m := regexp.MustCompile(`(?is)<meta\b[^>]*property=["']og:title["'][^>]*content=["']([^"']+)["'][^>]*>`).FindStringSubmatch(rawHTML); len(m) > 1 {
		if t := strings.TrimSpace(html.UnescapeString(m[1])); t != "" {
			return t
		}
	}
	if m := reTitleTag.FindStringSubmatch(rawHTML); len(m) > 1 {
		return cleanText(m[1])
	}
	return ""
}

func extractDescription(rawHTML string) string {
	if m := reDescMeta.FindStringSubmatch(rawHTML); len(m) > 1 {
		return strings.TrimSpace(html.UnescapeString(m[1]))
	}
	return ""
}

func extractByClassText(raw, className string) string {
	if className == "" {
		return ""
	}
	re := regexp.MustCompile(`(?is)<[^>]+class=["'][^"']*` + regexp.QuoteMeta(className) + `[^"']*["'][^>]*>(.*?)</[^>]+>`)
	if m := re.FindStringSubmatch(raw); len(m) > 1 {
		return cleanText(m[1])
	}
	return ""
}

func firstMatchText(re *regexp.Regexp, raw string) string {
	if m := re.FindString(raw); m != "" {
		return strings.TrimSpace(m)
	}
	return ""
}

func extractTags(raw string) []string {
	re := regexp.MustCompile(`(?is)<[^>]+class=["'][^"']*hg-tag[^"']*["'][^>]*>(.*?)</[^>]+>`)
	seen := map[string]bool{}
	var out []string
	for _, m := range re.FindAllStringSubmatch(raw, -1) {
		if len(m) < 2 {
			continue
		}
		v := cleanText(m[1])
		if v != "" && !seen[v] {
			seen[v] = true
			out = append(out, v)
		}
	}
	return out
}

func cleanText(s string) string {
	s = regexp.MustCompile(`(?is)<script\b.*?</script>`).ReplaceAllString(s, " ")
	s = regexp.MustCompile(`(?is)<style\b.*?</style>`).ReplaceAllString(s, " ")
	s = regexp.MustCompile(`(?is)<[^>]+>`).ReplaceAllString(s, " ")
	s = html.UnescapeString(s)
	s = spaceChars.ReplaceAllString(s, " ")
	return strings.TrimSpace(s)
}

func contextBlock(s string, start, end, padding int) string {
	if start < 0 {
		start = 0
	}
	if end < start {
		end = start
	}
	lo := start - padding
	if lo < 0 {
		lo = 0
	}
	hi := end + padding
	if hi > len(s) {
		hi = len(s)
	}
	return s[lo:hi]
}

func nonEmptyStrings(values ...string) []string {
	var out []string
	for _, v := range values {
		if strings.TrimSpace(v) != "" {
			out = append(out, strings.TrimSpace(v))
		}
	}
	return out
}

func extractClosestAttr(raw string, start, end int, names ...string) string {
	if start < 0 {
		start = 0
	}
	if end < start {
		end = start
	}
	if end > len(raw) {
		end = len(raw)
	}
	for _, value := range []string{raw[start:end], contextBlock(raw, start, end, 600)} {
		if value == "" {
			continue
		}
		if attr := extractAttr(value, names...); attr != "" {
			return attr
		}
	}
	return ""
}

func titleNearDetail(raw, sourceID string) string {
	needle := "/detail/" + sourceID
	idx := strings.Index(raw, needle)
	if idx < 0 {
		return ""
	}
	return extractByClassText(contextBlock(raw, idx, idx+len(needle), 1800), "hg-drama-card__title")
}

func normalizeProviderMediaURL(pageURL, raw string) string {
	raw = strings.TrimSpace(html.UnescapeString(raw))
	raw = strings.Trim(raw, " \t\r\n\"'")
	if raw == "" {
		return ""
	}
	if unquoted, err := strconv.Unquote("\"" + strings.ReplaceAll(raw, "\"", "\\\"") + "\""); err == nil {
		raw = unquoted
	}
	raw = strings.ReplaceAll(raw, `\/`, `/`)
	resolved := resolveProviderURL(pageURL, raw)
	if !strings.Contains(strings.ToLower(resolved), ".m3u8") && !strings.Contains(strings.ToLower(resolved), ".mp4") {
		return ""
	}
	return resolved
}

func isHuangguoProviderChapter(ch Chapter) bool {
	return isHuangguoProviderSource(ch.Source) && isProviderHTTPMediaURL(ch.VideoURL)
}
