package app

import (
	"bytes"
	"context"
	"crypto/hmac"
	"encoding/hex"
	"fmt"
	"math"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"
)

const playbackNativeURLLifetime = 30 * time.Minute

func (app *UIApp) playbackNativeSignature(path, session string, run uint64, expires int64) string {
	value := fmt.Sprintf("playback-hls|%s|%s|%d|%d", path, session, run, expires)
	return hex.EncodeToString(app.browserViewers().signature(value))
}

func (app *UIApp) playbackNativeURL(path, session string, run uint64, expires int64) string {
	query := url.Values{
		"expires": {strconv.FormatInt(expires, 10)},
		"run":     {strconv.FormatUint(run, 10)},
		"session": {session},
	}
	query.Set("signature", app.playbackNativeSignature(path, session, run, expires))
	return path + "?" + query.Encode()
}

func (app *UIApp) validPlaybackNativeSignature(request *http.Request, session string, run uint64) bool {
	expires, err := strconv.ParseInt(request.URL.Query().Get("expires"), 10, 64)
	if err != nil || expires < time.Now().Unix() || expires > time.Now().Add(playbackNativeURLLifetime+time.Minute).Unix() {
		return false
	}
	provided, err := hex.DecodeString(request.URL.Query().Get("signature"))
	if err != nil {
		return false
	}
	expected, err := hex.DecodeString(app.playbackNativeSignature(request.URL.Path, session, run, expires))
	return err == nil && hmac.Equal(provided, expected)
}

func (app *UIApp) allowPlaybackNativeAsset(writer http.ResponseWriter, request *http.Request, session string, run uint64) bool {
	method := http.MethodGet
	if request.Method == http.MethodHead {
		method = http.MethodHead
	}
	signed := app.validPlaybackNativeSignature(request, session, run)
	if signed {
		origin := request.Header.Get("Origin")
		if site := request.Header.Get("Sec-Fetch-Site"); site != "" && site != "same-origin" && site != "none" && origin != "null" {
			writeJSON(writer, http.StatusForbidden, map[string]string{"error": "请从剧库页面发起播放"})
			return false
		}
		if origin != "" && origin != "null" {
			parsed, err := url.Parse(origin)
			if err != nil || parsed.User != nil || parsed.Scheme != "http" && parsed.Scheme != "https" || !originHostMatches(parsed, request.Host) {
				writeJSON(writer, http.StatusForbidden, map[string]string{"error": "不允许跨站播放请求"})
				return false
			}
		}
		writer.Header().Set("Cache-Control", "private, no-store, no-transform")
		writer.Header().Set("X-Content-Type-Options", "nosniff")
		writer.Header().Set("X-Accel-Buffering", "no")
		writer.Header().Set("Cross-Origin-Resource-Policy", "cross-origin")
		writer.Header().Add("Vary", "Origin")
		if request.Header.Get("Origin") == "null" {
			writer.Header().Set("Access-Control-Allow-Origin", "null")
		}
		if request.Method != method {
			writer.Header().Set("Allow", method)
			writeJSON(writer, http.StatusMethodNotAllowed, map[string]string{"error": "请求方法不支持"})
			return false
		}
		return true
	}
	if strings.HasPrefix(request.URL.Path, "/assets/") {
		writeJSON(writer, http.StatusForbidden, map[string]string{"error": "播放地址签名无效或已过期"})
		return false
	}
	return playbackRequestAllowed(writer, request, method)
}

func (app *UIApp) handlePlaybackNativeOpen(writer http.ResponseWriter, request *http.Request) {
	var input struct {
		Session string  `json:"session"`
		Episode int     `json:"episode"`
		Start   float64 `json:"start"`
		Quality int     `json:"quality"`
		Version uint64  `json:"version"`
	}
	if !readPlaybackRequest(writer, request, &input) {
		return
	}
	if input.Episode < 1 || math.IsNaN(input.Start) || math.IsInf(input.Start, 0) || input.Start < 0 || input.Start > 24*60*60 || input.Quality < 0 || input.Quality > 4320 {
		writeJSON(writer, http.StatusBadRequest, map[string]string{"error": "集数、播放位置或清晰度无效"})
		return
	}
	if request.Context().Err() != nil {
		return
	}
	run, status, err := app.beginPlayback(context.WithoutCancel(request.Context()), input.Session, input.Episode, input.Start, input.Quality, input.Version, true)
	if err != nil {
		writeJSON(writer, status, map[string]string{"error": err.Error()})
		return
	}
	var cache *playbackNative
	if run.cache != nil && run.cache.view().State != "failed" {
		cache = run.cache.native
	}
	prefetched := cache != nil
	if cache == nil {
		cache = newPlaybackNative(app, run.ctx, run.task, run.downloadID, input.Quality, input.Start, false)
	} else {
		cache.mu.Lock()
		cache.background = false
		cache.mu.Unlock()
	}
	stop := func() { run.stop(); cache.Close() }
	app.playbackMu.Lock()
	if app.playbacks[run.id] != run.session || run.session.run != run.run || run.ctx.Err() != nil {
		app.playbackMu.Unlock()
		stop()
		writeJSON(writer, http.StatusConflict, map[string]string{"error": "播放请求已更新"})
		return
	}
	run.session.native, run.session.cancel = cache, stop
	app.playbackMu.Unlock()
	cache.start()
	startup, cancel := context.WithTimeout(request.Context(), 90*time.Second)
	defer cancel()
	_, err = cache.segment(startup, int(input.Start/playbackNativeSegmentSeconds))
	if err != nil {
		stop()
		app.finishPlaybackRun(run, err, request.Context().Err() != nil)
		writeJSON(writer, http.StatusBadGateway, map[string]string{"error": app.redactError(err)})
		return
	}
	media, duration, source := cache.metadata()
	if duration <= 0 || duration > 24*60*60 || math.IsNaN(duration) || math.IsInf(duration, 0) || input.Start >= duration {
		stop()
		writeJSON(writer, http.StatusBadGateway, map[string]string{"error": "未取得有效播放时长或播放位置已超出本集"})
		return
	}
	app.playbackRunReady(run, duration)
	expires := time.Now().Add(playbackNativeURLLifetime).Unix()
	writeJSON(writer, http.StatusOK, map[string]any{
		"url": app.playbackNativeURL("/assets/playback/hls/index.m3u8", run.id, run.run, expires), "run": run.run, "duration": duration, "source": source,
		"quality": media.Quality, "qualities": playbackQualityOptions(media), "prefetched": prefetched,
	})
}

func (app *UIApp) handlePlaybackNativeAsset(writer http.ResponseWriter, request *http.Request) {
	query := request.URL.Query()
	run, runErr := strconv.ParseUint(query.Get("run"), 10, 64)
	if runErr != nil || run == 0 {
		writeJSON(writer, http.StatusBadRequest, map[string]string{"error": "播放请求编号无效"})
		return
	}
	sessionID := query.Get("session")
	signed := app.validPlaybackNativeSignature(request, sessionID, run)
	if !app.allowPlaybackNativeAsset(writer, request, sessionID, run) {
		return
	}
	app.playbackMu.Lock()
	session := app.playbacks[sessionID]
	if session == nil || session.run != run || session.native == nil || !signed && !viewerOwnsPlayback(request.Context(), session) {
		app.playbackMu.Unlock()
		writeJSON(writer, http.StatusGone, map[string]string{"error": "此播放流已结束，请重新选择分集"})
		return
	}
	cache := session.native
	app.touchPlaybackLocked(session)
	app.playbackMu.Unlock()
	if strings.HasSuffix(request.URL.Path, "/index.m3u8") {
		_, duration, _ := cache.metadata()
		count := playbackNativeSegmentCount(duration)
		if count == 0 {
			writeJSON(writer, http.StatusServiceUnavailable, map[string]string{"error": "播放列表尚未就绪"})
			return
		}
		expires, _ := strconv.ParseInt(query.Get("expires"), 10, 64)
		if expires < time.Now().Unix() {
			expires = time.Now().Add(playbackNativeURLLifetime).Unix()
		}
		var playlist strings.Builder
		fmt.Fprintf(&playlist, "#EXTM3U\n#EXT-X-VERSION:3\n#EXT-X-TARGETDURATION:%d\n#EXT-X-MEDIA-SEQUENCE:0\n#EXT-X-PLAYLIST-TYPE:VOD\n#EXT-X-INDEPENDENT-SEGMENTS\n", playbackNativeSegmentSeconds)
		if cache.offset > 0 {
			fmt.Fprintf(&playlist, "#EXT-X-START:TIME-OFFSET=%.3f,PRECISE=YES\n", cache.offset)
		}
		for index := 0; index < count; index++ {
			length := playbackNativeSegmentDuration(duration, index)
			asset := app.playbackNativeURL("/assets/playback/hls/segment.ts", session.id, run, expires)
			separator := "?"
			if strings.Contains(asset, "?") {
				separator = "&"
			}
			fmt.Fprintf(&playlist, "#EXTINF:%.6f,\n%s%ssegment=%d\n", length, asset, separator, index)
		}
		playlist.WriteString("#EXT-X-ENDLIST\n")
		writer.Header().Set("Cache-Control", "private, max-age=15, no-transform")
		writer.Header().Set("Content-Type", "application/vnd.apple.mpegurl")
		http.ServeContent(writer, request, "index.m3u8", time.Time{}, strings.NewReader(playlist.String()))
		return
	}
	index, err := strconv.Atoi(query.Get("segment"))
	_, duration, _ := cache.metadata()
	if err != nil || index < 0 || index >= playbackNativeSegmentCount(duration) {
		writeJSON(writer, http.StatusBadRequest, map[string]string{"error": "播放分片编号无效"})
		return
	}
	writer.Header().Set("Accept-Ranges", "bytes")
	writer.Header().Set("Cache-Control", "private, max-age=300, immutable, no-transform")
	writer.Header().Set("Content-Type", "video/mp2t")
	if body := cache.cachedSegment(index); len(body) > 0 {
		http.ServeContent(writer, request, "segment.ts", time.Time{}, bytes.NewReader(body))
		return
	}
	if request.Method == http.MethodHead {
		writer.WriteHeader(http.StatusOK)
		return
	}
	ctx, cancel := context.WithTimeout(request.Context(), 90*time.Second)
	defer cancel()
	body, err := cache.segment(ctx, index)
	if err != nil {
		writeJSON(writer, http.StatusBadGateway, map[string]string{"error": app.redactError(err)})
		return
	}
	http.ServeContent(writer, request, "segment.ts", time.Time{}, bytes.NewReader(body))
}
