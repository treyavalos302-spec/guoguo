package app

import (
	"bytes"
	"context"
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"net/http"
	"net/url"
	"os"
	"strconv"
	"strings"
	"time"
)

const forwardWidgetSource = `WidgetMetadata = {
  id: "com.guoguo.juku",
  title: "果果剧库",
  version: "1.0.0",
  requiredVersion: "0.0.1",
  description: "通过私密 Forward API 浏览和播放果果剧库",
  author: "果果剧库",
  detailCacheDuration: 60,
  globalParams: [
    { name: "server", title: "服务器", type: "input", value: __JUKU_SERVER__ },
    { name: "token", title: "访问令牌", type: "input", value: __JUKU_TOKEN__ }
  ],
  modules: [{
    id: "loadList",
    title: "剧库",
    functionName: "loadList",
    cacheDuration: 60,
    requiresWebView: false,
    sectionMode: false,
    params: [
      { name: "page", title: "页码", type: "page" },
      { name: "count", title: "每页数量", type: "count", value: 24 }
    ]
  }],
  search: {
    title: "搜索",
    functionName: "search",
    params: [
      { name: "keyword", title: "关键词", type: "input" },
      { name: "page", title: "页码", type: "page" }
    ]
  }
};

const JUKU_DEFAULT_SERVER = __JUKU_SERVER__;
const JUKU_DEFAULT_TOKEN = __JUKU_TOKEN__;

function jukuSettings(params = {}) {
  const saved = Widget.storage.get("juku.forward.settings") || {};
  const server = String(params.server || saved.server || JUKU_DEFAULT_SERVER || "").replace(/\/+$/, "");
  const token = String(params.token || saved.token || JUKU_DEFAULT_TOKEN || "");
  if (!server || !token) throw new Error("请配置果果剧库服务器和访问令牌");
  Widget.storage.set("juku.forward.settings", { server, token });
  return { server, token };
}

async function jukuGet(endpoint, params, settings) {
  const current = settings || jukuSettings(params || {});
  const response = await Widget.http.get(current.server + endpoint, {
    headers: { Authorization: "Bearer " + current.token, Accept: "application/json" },
    params: params || {}
  });
  if (!response || !response.data) throw new Error("果果剧库返回空响应");
  return response.data;
}

function jukuItem(item) {
  return {
    id: String(item.id),
    type: "url",
    title: item.title || "短剧",
    link: "drama:" + String(item.id),
    posterPath: item.posterUrl || "",
    backdropPath: item.backdropUrl || item.posterUrl || "",
    description: item.description || "",
    releaseDate: item.onlineDate || "",
    rating: Number(item.rating || 0)
  };
}

async function loadList(params = {}) {
  try {
    const settings = jukuSettings(params);
    const body = await jukuGet("/v1/list", {
      page: Math.max(1, Number(params.page || 1)),
      pageSize: Math.max(1, Math.min(100, Number(params.count || 24)))
    }, settings);
    return (body.data || []).map(jukuItem);
  } catch (error) {
    console.error("[loadList] 失败:", error.message || error);
    throw error;
  }
}

async function search(params = {}) {
  try {
    const settings = jukuSettings(params);
    const body = await jukuGet("/search", {
      keyword: String(params.keyword || ""),
      page: Math.max(1, Number(params.page || 1)),
      pageSize: 24
    }, settings);
    return (body.data || []).map(jukuItem);
  } catch (error) {
    console.error("[search] 失败:", error.message || error);
    throw error;
  }
}

async function loadDetail(link) {
  try {
    const text = String(link || "");
    if (!text.startsWith("drama:")) return null;
    const id = text.slice(6);
    if (!id) return null;
    const settings = jukuSettings({});
    let page = 1;
    let detail = null;
    const episodes = [];
    while (page <= 20) {
      const body = await jukuGet("/detail", { id, page, pageSize: 100 }, settings);
      if (!detail) detail = body;
      episodes.push(...(body.episodes || []));
      if (!body.hasMore) break;
      page += 1;
    }
    if (!detail) return null;
    return {
      id: String(detail.id),
      type: "url",
      title: detail.title || "短剧",
      link: "drama:" + String(detail.id),
      posterPath: detail.posterUrl || "",
      backdropPath: detail.backdropUrl || detail.posterUrl || "",
      backdropPaths: detail.backdropUrl ? [detail.backdropUrl] : [],
      description: detail.description || "",
      releaseDate: detail.onlineDate || "",
      rating: Number(detail.rating || 0),
      episodeItems: episodes.map((episode) => ({
        id: String(episode.id),
        type: "url",
        title: episode.title || "分集",
        videoUrl: episode.videoUrl,
        playerType: "system"
      }))
    };
  } catch (error) {
    console.error("[loadDetail] 失败:", error.message || error);
    throw error;
  }
}
`

type forwardListItem struct {
	ID          string `json:"id"`
	Title       string `json:"title"`
	Description string `json:"description,omitempty"`
	PosterURL   string `json:"posterUrl,omitempty"`
	BackdropURL string `json:"backdropUrl,omitempty"`
	OnlineDate  string `json:"onlineDate,omitempty"`
	Rating      string `json:"rating,omitempty"`
	Episodes    int    `json:"episodeCount,omitempty"`
}

type forwardEpisode struct {
	ID       string `json:"id"`
	Title    string `json:"title"`
	Number   int    `json:"number"`
	VideoURL string `json:"videoUrl"`
}

func (app *UIApp) forwardAccessToken() ([]byte, error) {
	app.forwardMu.Lock()
	defer app.forwardMu.Unlock()
	if len(app.forwardToken) > 0 {
		return append([]byte(nil), app.forwardToken...), nil
	}
	if configured, exists := os.LookupEnv("JUKU_FORWARD_TOKEN"); exists {
		if len(configured) < 16 || len(configured) > 1024 || strings.ContainsAny(configured, "\x00\r\n") {
			return nil, errors.New("JUKU_FORWARD_TOKEN 必须为 16–1024 个有效字符")
		}
		app.forwardToken = []byte(configured)
		return append([]byte(nil), app.forwardToken...), nil
	}
	value, err := readOrCreatePrivateValue(app.cfg.dataDirectory(), "forward-token", func() (string, error) {
		body := make([]byte, 32)
		if _, err := rand.Read(body); err != nil {
			return "", err
		}
		return hex.EncodeToString(body), nil
	}, func(value string) (string, error) {
		if len(value) != 64 {
			return "", errors.New("Forward 访问令牌文件无效")
		}
		if _, err := hex.DecodeString(value); err != nil {
			return "", errors.New("Forward 访问令牌文件无效")
		}
		return value, nil
	})
	if err != nil {
		return nil, err
	}
	app.forwardToken = []byte(value)
	return append([]byte(nil), app.forwardToken...), nil
}

func constantTimeTokenEqual(expected []byte, supplied string) bool {
	expectedHash := sha256.Sum256(expected)
	suppliedHash := sha256.Sum256([]byte(supplied))
	return hmac.Equal(expectedHash[:], suppliedHash[:])
}

func requestForwardToken(request *http.Request) string {
	authorization := request.Header.Get("Authorization")
	if len(authorization) >= 7 && strings.EqualFold(authorization[:7], "Bearer ") {
		return strings.TrimSpace(authorization[7:])
	}
	return request.URL.Query().Get("token")
}

func (app *UIApp) authorizeForward(writer http.ResponseWriter, request *http.Request) bool {
	token, err := app.forwardAccessToken()
	if err != nil {
		writeJSON(writer, http.StatusServiceUnavailable, map[string]string{"error": "Forward 服务尚未就绪"})
		return false
	}
	if !constantTimeTokenEqual(token, requestForwardToken(request)) {
		writer.Header().Set("WWW-Authenticate", `Bearer realm="ForwardWidget"`)
		writeJSON(writer, http.StatusUnauthorized, map[string]string{"error": "Forward 访问令牌无效"})
		return false
	}
	return true
}

func setForwardCORS(writer http.ResponseWriter) {
	writer.Header().Set("Cache-Control", "private, no-store")
	writer.Header().Set("Access-Control-Allow-Origin", "*")
	writer.Header().Set("Access-Control-Allow-Headers", "Authorization, Content-Type")
	writer.Header().Set("Access-Control-Allow-Methods", "GET, OPTIONS")
	writer.Header().Set("Access-Control-Max-Age", "86400")
	writer.Header().Set("X-Content-Type-Options", "nosniff")
}

func (app *UIApp) handleForward(writer http.ResponseWriter, request *http.Request) {
	setForwardCORS(writer)
	if request.Method == http.MethodOptions {
		writer.WriteHeader(http.StatusNoContent)
		return
	}
	if request.Method != http.MethodGet && request.Method != http.MethodHead {
		writer.Header().Set("Allow", "GET, HEAD, OPTIONS")
		writeJSON(writer, http.StatusMethodNotAllowed, map[string]string{"error": "method not allowed"})
		return
	}
	paths, err := app.ensureExternalPaths()
	if err != nil || !strings.HasPrefix(request.URL.Path, paths.Forward+"/") {
		http.NotFound(writer, request)
		return
	}
	endpoint := strings.TrimPrefix(request.URL.Path, paths.Forward)
	switch endpoint {
	case "/module.js":
		app.handleForwardModule(writer, request)
	case "/v1/list":
		app.handleForwardList(writer, request, false)
	case "/search":
		app.handleForwardList(writer, request, true)
	case "/detail":
		app.handleForwardDetail(writer, request)
	case "/image":
		app.handleForwardImage(writer, request)
	case "/stream.m3u8":
		app.handleForwardStream(writer, request)
	case "/segment.ts":
		app.handleForwardSegment(writer, request)
	default:
		http.NotFound(writer, request)
	}
}

func (app *UIApp) handleForwardModule(writer http.ResponseWriter, request *http.Request) {
	if !app.authorizeForward(writer, request) {
		return
	}
	paths, _ := app.ensureExternalPaths()
	base, err := embyRequestBaseURL(request, "")
	if err != nil {
		writeJSON(writer, http.StatusBadRequest, map[string]string{"error": "无法确定公共服务地址"})
		return
	}
	token, _ := app.forwardAccessToken()
	serverJSON, _ := json.Marshal(base + paths.Forward)
	tokenJSON, _ := json.Marshal(string(token))
	source := strings.ReplaceAll(forwardWidgetSource, "__JUKU_SERVER__", string(serverJSON))
	source = strings.ReplaceAll(source, "__JUKU_TOKEN__", string(tokenJSON))
	writer.Header().Set("Content-Type", "application/javascript; charset=utf-8")
	writer.Header().Set("Cache-Control", "private, no-store")
	if request.Method == http.MethodGet {
		_, _ = writer.Write([]byte(source))
	}
}

func forwardPagination(query url.Values) (int, int, error) {
	page, err := strconv.Atoi(firstNonEmpty(query.Get("page"), "1"))
	if err != nil || page < 1 || page > 100000 {
		return 0, 0, errors.New("页码无效")
	}
	size, err := strconv.Atoi(firstNonEmpty(query.Get("pageSize"), "24"))
	if err != nil || size < 1 || size > 100 {
		return 0, 0, errors.New("每页数量范围为 1–100")
	}
	return page, size, nil
}

func paginateForwardDramas(items []Drama, page, size int) ([]Drama, bool) {
	start := (page - 1) * size
	if start >= len(items) {
		return []Drama{}, false
	}
	end := start + size
	if end > len(items) {
		end = len(items)
	}
	return items[start:end], end < len(items)
}

func (app *UIApp) handleForwardList(writer http.ResponseWriter, request *http.Request, searching bool) {
	if !app.authorizeForward(writer, request) {
		return
	}
	page, size, err := forwardPagination(request.URL.Query())
	if err != nil {
		writeJSON(writer, http.StatusBadRequest, map[string]string{"error": err.Error()})
		return
	}
	keyword := strings.ToLower(strings.TrimSpace(request.URL.Query().Get("keyword")))
	if searching && keyword == "" {
		writeJSON(writer, http.StatusOK, map[string]any{"data": []forwardListItem{}, "page": page, "pageSize": size, "hasMore": false})
		return
	}
	app.mu.Lock()
	dramas := append([]Drama(nil), app.dramas...)
	app.mu.Unlock()
	filtered := make([]Drama, 0, len(dramas))
	for _, drama := range dramas {
		if keyword != "" {
			haystack := strings.ToLower(strings.Join([]string{drama.DisplayTitle(), drama.Desc, drama.Intro, strings.Join(drama.Tags, " ")}, " "))
			if !strings.Contains(haystack, keyword) {
				continue
			}
		}
		filtered = append(filtered, drama)
	}
	selected, more := paginateForwardDramas(filtered, page, size)
	data := make([]forwardListItem, 0, len(selected))
	for _, drama := range selected {
		data = append(data, app.forwardDramaItem(request, drama))
	}
	writeJSON(writer, http.StatusOK, map[string]any{"data": data, "page": page, "pageSize": size, "total": len(filtered), "hasMore": more})
}

func (app *UIApp) forwardDramaItem(request *http.Request, drama Drama) forwardListItem {
	item := forwardListItem{ID: drama.ID, Title: drama.DisplayTitle(), Description: firstNonEmpty(drama.Desc, drama.Intro), OnlineDate: drama.OnlineDate, Rating: drama.Score}
	if count, err := strconv.Atoi(fmt.Sprint(drama.TotalEpisode)); err == nil && count > 0 {
		item.Episodes = count
	}
	if bestDramaCover(drama) != "" {
		if address, err := app.forwardSignedURL(request, "image", drama.ID, "", 24*time.Hour); err == nil {
			item.PosterURL, item.BackdropURL = address, address
		}
	}
	return item
}

func (app *UIApp) handleForwardDetail(writer http.ResponseWriter, request *http.Request) {
	if !app.authorizeForward(writer, request) {
		return
	}
	page, size, err := forwardPagination(request.URL.Query())
	if err != nil {
		writeJSON(writer, http.StatusBadRequest, map[string]string{"error": err.Error()})
		return
	}
	id := strings.TrimSpace(request.URL.Query().Get("id"))
	app.mu.Lock()
	var drama Drama
	for _, candidate := range app.dramas {
		if candidate.ID == id {
			drama = candidate
			break
		}
	}
	app.mu.Unlock()
	if drama.ID == "" || !validEmbyIdentity(drama.ID, "fixture") {
		writeJSON(writer, http.StatusNotFound, map[string]string{"error": "条目不存在"})
		return
	}
	ctx, cancel := context.WithTimeout(request.Context(), 90*time.Second)
	defer cancel()
	title, chapters, err := app.downloader.GetDramaChapters(ctx, drama.ID)
	if err != nil || len(chapters) == 0 || len(chapters) > 2000 {
		writeJSON(writer, http.StatusBadGateway, map[string]string{"error": "暂时无法读取分集"})
		return
	}
	drama.Title = firstNonEmpty(title, drama.DisplayTitle())
	start := (page - 1) * size
	if start > len(chapters) {
		start = len(chapters)
	}
	end := start + size
	if end > len(chapters) {
		end = len(chapters)
	}
	episodes := make([]forwardEpisode, 0, end-start)
	for index := start; index < end; index++ {
		chapter := chapters[index]
		if !validEmbyIdentity(drama.ID, chapter.ID) {
			writeJSON(writer, http.StatusBadGateway, map[string]string{"error": "分集标识无效"})
			return
		}
		videoURL, signErr := app.forwardSignedURL(request, "stream.m3u8", drama.ID, chapter.ID, 24*time.Hour)
		if signErr != nil {
			writeJSON(writer, http.StatusInternalServerError, map[string]string{"error": "无法签发播放链接"})
			return
		}
		episodes = append(episodes, forwardEpisode{ID: chapter.ID, Title: firstNonEmpty(chapter.Title, "第 "+chapter.EpisodeString(index+1)+" 集"), Number: index + 1, VideoURL: videoURL})
	}
	item := app.forwardDramaItem(request, drama)
	writeJSON(writer, http.StatusOK, map[string]any{"id": item.ID, "title": item.Title, "description": item.Description, "posterUrl": item.PosterURL, "backdropUrl": item.BackdropURL, "onlineDate": item.OnlineDate, "rating": item.Rating, "episodes": episodes, "page": page, "pageSize": size, "total": len(chapters), "hasMore": end < len(chapters)})
}

func (app *UIApp) forwardSignature(kind, id, chapter string, expires int64) ([]byte, error) {
	token, err := app.forwardAccessToken()
	if err != nil {
		return nil, err
	}
	mac := hmac.New(sha256.New, token)
	fmt.Fprintf(mac, "forward-v1\x00%s\x00%s\x00%s\x00%d", kind, id, chapter, expires)
	return mac.Sum(nil), nil
}

func (app *UIApp) forwardSignedURL(request *http.Request, kind, id, chapter string, lifetime time.Duration) (string, error) {
	paths, err := app.ensureExternalPaths()
	if err != nil {
		return "", err
	}
	base, err := embyRequestBaseURL(request, "")
	if err != nil {
		return "", err
	}
	expires := time.Now().Add(lifetime).Unix()
	signature, err := app.forwardSignature(kind, id, chapter, expires)
	if err != nil {
		return "", err
	}
	query := url.Values{"id": {id}, "expires": {strconv.FormatInt(expires, 10)}, "sig": {hex.EncodeToString(signature)}}
	if chapter != "" {
		query.Set("chapter", chapter)
	}
	return base + paths.Forward + "/" + kind + "?" + query.Encode(), nil
}

func (app *UIApp) authorizeForwardMedia(writer http.ResponseWriter, request *http.Request, kind string) (string, string, bool) {
	writer.Header().Set("Cache-Control", "private, no-store")
	query := request.URL.Query()
	id, chapter := query.Get("id"), query.Get("chapter")
	expires, err := strconv.ParseInt(query.Get("expires"), 10, 64)
	supplied, decodeErr := hex.DecodeString(query.Get("sig"))
	expected, signErr := app.forwardSignature(kind, id, chapter, expires)
	if err != nil || decodeErr != nil || signErr != nil || len(supplied) != sha256.Size || !validEmbyIdentity(id, firstNonEmpty(chapter, "image")) || expires < time.Now().Unix() || expires > time.Now().Add(48*time.Hour).Unix() || !hmac.Equal(expected, supplied) {
		writeJSON(writer, http.StatusForbidden, map[string]string{"error": "Forward 签名链接无效或已过期"})
		return "", "", false
	}
	return id, chapter, true
}

func (app *UIApp) handleForwardImage(writer http.ResponseWriter, request *http.Request) {
	id, _, ok := app.authorizeForwardMedia(writer, request, "image")
	if !ok {
		return
	}
	app.mu.Lock()
	var image string
	for _, drama := range app.dramas {
		if drama.ID == id {
			image = bestDramaCover(drama)
			break
		}
	}
	app.mu.Unlock()
	remote, valid := buildImageURL(image)
	if !valid {
		http.NotFound(writer, request)
		return
	}
	ctx, cancel := context.WithTimeout(request.Context(), 90*time.Second)
	defer cancel()
	body, err := app.loadCoverImage(ctx, remote, nil)
	if err != nil {
		writeJSON(writer, http.StatusBadGateway, map[string]string{"error": "封面暂时不可用"})
		return
	}
	writer.Header().Set("Content-Type", imageContentType(body))
	writer.Header().Set("Cache-Control", "public, max-age=3600")
	if request.Method == http.MethodGet {
		_, _ = writer.Write(body)
	}
}

func (app *UIApp) handleForwardStream(writer http.ResponseWriter, request *http.Request) {
	id, chapter, ok := app.authorizeForwardMedia(writer, request, "stream.m3u8")
	if !ok {
		return
	}
	if request.Method == http.MethodHead {
		writer.Header().Set("Content-Type", "application/vnd.apple.mpegurl")
		return
	}
	sessionID := randomHex(24)
	if sessionID == "" {
		writeJSON(writer, http.StatusInternalServerError, map[string]string{"error": "无法创建播放会话"})
		return
	}
	ctx, cancel := context.WithTimeout(request.Context(), 90*time.Second)
	defer cancel()
	app.playbackMu.Lock()
	if len(app.playbacks) >= playbackSessionLimit {
		app.playbackMu.Unlock()
		writeJSON(writer, http.StatusTooManyRequests, map[string]string{"error": "同时播放数量已达上限"})
		return
	}
	if app.playbacks == nil {
		app.playbacks = make(map[string]*playbackSession)
	}
	session := &playbackSession{dramaID: id, id: sessionID, run: 1, state: "opening", expires: time.Now().Add(playbackIdleTimeout), cancel: cancel}
	session.timer = time.AfterFunc(playbackIdleTimeout, func() { app.expirePlayback(sessionID) })
	app.playbacks[sessionID] = session
	app.playbackMu.Unlock()
	ready := false
	defer func() {
		if !ready {
			app.closePlayback(sessionID)
		}
	}()
	task, downloadID, err := app.embyTask(ctx, id, chapter)
	if err != nil {
		writeJSON(writer, http.StatusBadGateway, map[string]string{"error": "分集暂时不可播放"})
		return
	}
	cache := newPlaybackNative(app, context.Background(), task, downloadID, 0, 0, false)
	app.playbackMu.Lock()
	if app.playbacks[sessionID] != session || ctx.Err() != nil {
		app.playbackMu.Unlock()
		cache.Close()
		return
	}
	session.tasks, session.native, session.cancel = []Task{task}, cache, cache.Close
	app.playbackMu.Unlock()
	cache.start()
	if _, err = cache.segment(ctx, 0); err != nil {
		writeJSON(writer, http.StatusBadGateway, map[string]string{"error": "播放流暂时不可用"})
		return
	}
	_, duration, _ := cache.metadata()
	if duration <= 0 || duration > 24*60*60 || math.IsNaN(duration) || math.IsInf(duration, 0) {
		writeJSON(writer, http.StatusBadGateway, map[string]string{"error": "播放时长无效"})
		return
	}
	var playlist strings.Builder
	fmt.Fprintf(&playlist, "#EXTM3U\n#EXT-X-VERSION:3\n#EXT-X-TARGETDURATION:%d\n#EXT-X-MEDIA-SEQUENCE:0\n#EXT-X-PLAYLIST-TYPE:VOD\n#EXT-X-INDEPENDENT-SEGMENTS\n", playbackNativeSegmentSeconds)
	for part, count := 0, int(math.Ceil(duration/playbackNativeSegmentSeconds)); part < count; part++ {
		query := request.URL.Query()
		query.Set("session", sessionID)
		query.Set("segment", strconv.Itoa(part))
		fmt.Fprintf(&playlist, "#EXTINF:%.6f,\nsegment.ts?%s\n", math.Min(playbackNativeSegmentSeconds, duration-float64(part*playbackNativeSegmentSeconds)), query.Encode())
	}
	playlist.WriteString("#EXT-X-ENDLIST\n")
	app.playbackMu.Lock()
	if app.playbacks[sessionID] == session {
		session.state, session.duration = "streaming", duration
		app.touchPlaybackLocked(session)
		ready = true
	}
	app.playbackMu.Unlock()
	if !ready {
		return
	}
	writer.Header().Set("Content-Type", "application/vnd.apple.mpegurl")
	http.ServeContent(writer, request, "index.m3u8", time.Time{}, strings.NewReader(playlist.String()))
}

func (app *UIApp) handleForwardSegment(writer http.ResponseWriter, request *http.Request) {
	id, chapter, ok := app.authorizeForwardMedia(writer, request, "stream.m3u8")
	if !ok {
		return
	}
	part, err := strconv.Atoi(request.URL.Query().Get("segment"))
	if err != nil || part < 0 {
		writeJSON(writer, http.StatusBadRequest, map[string]string{"error": "分片编号无效"})
		return
	}
	app.playbackMu.Lock()
	session := app.playbacks[request.URL.Query().Get("session")]
	if session == nil || session.native == nil || len(session.tasks) != 1 || session.tasks[0].DramaID != id || session.tasks[0].Chapter.ID != chapter {
		app.playbackMu.Unlock()
		writeJSON(writer, http.StatusGone, map[string]string{"error": "播放已过期，请重新播放"})
		return
	}
	cache := session.native
	app.touchPlaybackLocked(session)
	app.playbackMu.Unlock()
	ctx, cancel := context.WithTimeout(request.Context(), 90*time.Second)
	defer cancel()
	body, err := cache.segment(ctx, part)
	if err != nil {
		writeJSON(writer, http.StatusBadGateway, map[string]string{"error": "播放分片暂时不可用"})
		return
	}
	writer.Header().Set("Content-Type", "video/mp2t")
	http.ServeContent(writer, request, "segment.ts", time.Time{}, bytes.NewReader(body))
}
