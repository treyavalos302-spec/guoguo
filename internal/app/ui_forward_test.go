package app

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func forwardRequest(t *testing.T, handler http.Handler, method, address string, token []byte) *httptest.ResponseRecorder {
	t.Helper()
	request := httptest.NewRequest(method, address, nil)
	if len(token) > 0 {
		request.Header.Set("Authorization", "Bearer "+string(token))
	}
	writer := httptest.NewRecorder()
	handler.ServeHTTP(writer, request)
	return writer
}

func TestForwardPrivateRoutesTokenPaginationAndLegacyPaths(t *testing.T) {
	app := viewerTestApp(t)
	app.dramas = append(app.dramas,
		Drama{ID: "hongguo:7000000000000000002", Title: "第二部", Source: sourceHongguo},
		Drama{ID: "hongguo:7000000000000000003", Title: "第三部", Source: sourceHongguo},
	)
	paths, err := app.ensureExternalPaths()
	if err != nil {
		t.Fatal(err)
	}
	token, err := app.forwardAccessToken()
	if err != nil {
		t.Fatal(err)
	}
	router := app.routes()
	for _, legacy := range []string{"/api/forward/module.js", "/api/forward/v1/list", "/api/emby/stream.m3u8"} {
		if result := forwardRequest(t, router, http.MethodGet, "http://juku.test"+legacy, token); result.Code != http.StatusNotFound {
			t.Fatalf("legacy fixed path %s is still registered: %d", legacy, result.Code)
		}
	}
	unauthorized := forwardRequest(t, router, http.MethodGet, "http://juku.test"+paths.Forward+"/v1/list", nil)
	if unauthorized.Code != http.StatusUnauthorized || unauthorized.Header().Get("Access-Control-Allow-Origin") != "*" {
		t.Fatal("Forward API did not enforce token with CORS", unauthorized.Code, unauthorized.Body.String())
	}
	wrong := forwardRequest(t, router, http.MethodGet, "http://juku.test"+paths.Forward+"/v1/list", []byte("wrong-token"))
	if wrong.Code != http.StatusUnauthorized {
		t.Fatal("wrong Forward token was accepted")
	}
	listed := forwardRequest(t, router, http.MethodGet, "http://juku.test"+paths.Forward+"/v1/list?page=1&pageSize=2", token)
	if listed.Code != http.StatusOK {
		t.Fatal(listed.Code, listed.Body.String())
	}
	var response struct {
		Data    []forwardListItem `json:"data"`
		Total   int               `json:"total"`
		HasMore bool              `json:"hasMore"`
	}
	if err = json.Unmarshal(listed.Body.Bytes(), &response); err != nil || len(response.Data) != 2 || response.Total != 3 || !response.HasMore {
		t.Fatal("Forward list pagination failed", err, listed.Body.String())
	}
	info, err := os.Stat(filepath.Join(app.cfg.dataDirectory(), "forward-token"))
	if err != nil || info.Mode().Perm() != 0o600 {
		t.Fatal("Forward token was not persisted privately", err)
	}
	restarted := &UIApp{cfg: app.cfg}
	restartedToken, err := restarted.forwardAccessToken()
	if err != nil || string(restartedToken) != string(token) {
		t.Fatal("Forward token did not survive restart", err)
	}
}

func TestForwardModuleDetailAndOpaqueSignedMedia(t *testing.T) {
	app := viewerTestApp(t)
	paths, err := app.ensureExternalPaths()
	if err != nil {
		t.Fatal(err)
	}
	token, err := app.forwardAccessToken()
	if err != nil {
		t.Fatal(err)
	}
	router := app.routes()
	moduleURL := "http://juku.test" + paths.Forward + "/module.js?token=" + url.QueryEscape(string(token))
	module := forwardRequest(t, router, http.MethodGet, moduleURL, nil)
	if module.Code != http.StatusOK || !strings.HasPrefix(module.Body.String(), "WidgetMetadata =") || !strings.Contains(module.Body.String(), paths.Forward) {
		t.Fatal("private module was not rendered correctly", module.Code, module.Body.String())
	}
	detailURL := "http://juku.test" + paths.Forward + "/detail?id=" + url.QueryEscape(historyFixtureDramaID) + "&page=1&pageSize=2"
	detail := forwardRequest(t, router, http.MethodGet, detailURL, token)
	if detail.Code != http.StatusOK {
		t.Fatal(detail.Code, detail.Body.String())
	}
	if strings.Contains(detail.Body.String(), "hongguo-cenc://") || strings.Contains(detail.Body.String(), "videoUrl\":\"http") && strings.Contains(detail.Body.String(), "tideember") {
		t.Fatal("Forward detail leaked an upstream media URL", detail.Body.String())
	}
	var response struct {
		Episodes []forwardEpisode `json:"episodes"`
		HasMore  bool             `json:"hasMore"`
		Total    int              `json:"total"`
	}
	if err = json.Unmarshal(detail.Body.Bytes(), &response); err != nil || len(response.Episodes) != 2 || !response.HasMore || response.Total != 3 {
		t.Fatal("Forward detail pagination failed", err, detail.Body.String())
	}
	media, err := url.Parse(response.Episodes[0].VideoURL)
	if err != nil || !media.IsAbs() || media.Path != paths.Forward+"/stream.m3u8" || media.Query().Get("sig") == "" || media.Query().Get("token") != "" {
		t.Fatal("Forward episode did not use an opaque absolute signed URL", response.Episodes[0].VideoURL)
	}
	valid := forwardRequest(t, router, http.MethodHead, response.Episodes[0].VideoURL, nil)
	if valid.Code != http.StatusOK {
		t.Fatal("signed Forward media URL was rejected", valid.Code, valid.Body.String())
	}
	tampered := *media
	query := tampered.Query()
	query.Set("chapter", "hongguo:7000000000000000001:999")
	tampered.RawQuery = query.Encode()
	invalid := forwardRequest(t, router, http.MethodHead, tampered.String(), nil)
	if invalid.Code != http.StatusForbidden {
		t.Fatal("tampered Forward media URL was accepted", invalid.Code)
	}
}
