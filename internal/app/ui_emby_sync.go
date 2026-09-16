package app

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/xml"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"time"
)

type embySyncInput struct {
	DramaID string `json:"dramaId"`
	BaseURL string `json:"baseUrl"`
}

type embySyncResult struct {
	Directory string `json:"directory"`
	Episodes  int    `json:"episodes"`
	Written   int    `json:"written"`
	Unchanged int    `json:"unchanged"`
}

func (app *UIApp) handleEmbySync(writer http.ResponseWriter, request *http.Request) {
	var input embySyncInput
	if !readPlaybackRequest(writer, request, &input) {
		return
	}
	if !app.requireDramaSources(writer, request, []string{input.DramaID}) {
		return
	}
	exportDirectory := strings.TrimSpace(os.Getenv("JUKU_EMBY_EXPORT_DIR"))
	if exportDirectory == "" {
		writeJSON(writer, http.StatusBadRequest, map[string]string{"error": "请先设置 JUKU_EMBY_EXPORT_DIR"})
		return
	}
	base, err := embyRequestBaseURL(request, input.BaseURL)
	if err != nil {
		writeJSON(writer, http.StatusBadRequest, map[string]string{"error": err.Error()})
		return
	}
	paths, err := app.ensureExternalPaths()
	if err != nil {
		writeJSON(writer, http.StatusInternalServerError, map[string]string{"error": "私密入口尚未就绪"})
		return
	}
	app.mu.Lock()
	var drama Drama
	for _, item := range app.dramas {
		if item.ID == input.DramaID {
			drama = item
			break
		}
	}
	app.mu.Unlock()
	if drama.ID == "" {
		writeJSON(writer, http.StatusNotFound, map[string]string{"error": "请先在剧库中找到此剧"})
		return
	}
	ctx, cancel := context.WithTimeout(request.Context(), 90*time.Second)
	defer cancel()
	title, chapters, err := app.downloader.GetDramaChapters(ctx, drama.ID)
	if err != nil {
		writeJSON(writer, http.StatusBadGateway, map[string]string{"error": "读取分集失败：" + app.redactError(err)})
		return
	}
	if len(chapters) == 0 || len(chapters) > 2000 {
		writeJSON(writer, http.StatusBadGateway, map[string]string{"error": "可同步分集数无效"})
		return
	}
	for _, chapter := range chapters {
		if !validEmbyIdentity(drama.ID, chapter.ID) {
			writeJSON(writer, http.StatusBadGateway, map[string]string{"error": "此剧未提供稳定分集 ID，暂不能同步"})
			return
		}
	}
	key, err := app.embySigningKey(true)
	if err != nil {
		writeJSON(writer, http.StatusInternalServerError, map[string]string{"error": "无法保存 Emby 链接密钥"})
		return
	}
	drama.Title = firstNonEmpty(title, drama.DisplayTitle())
	owner := ""
	if scope := sourceScope(request.Context()); scope != nil {
		owner = scope.AccountID
	}
	result, err := syncEmbyDirectory(exportDirectory, drama, chapters, base, paths.Emby, key, owner)
	if err != nil {
		writeJSON(writer, http.StatusInternalServerError, map[string]string{"error": "同步 Emby 目录失败：" + app.redactError(err)})
		return
	}
	writeJSON(writer, http.StatusOK, result)
}

func syncEmbyDirectory(root string, drama Drama, chapters []Chapter, base, embyPath string, key []byte, owner string) (embySyncResult, error) {
	root, err := filepath.Abs(strings.TrimSpace(root))
	if err != nil || root == "" {
		return embySyncResult{}, errors.New("JUKU_EMBY_EXPORT_DIR 无效")
	}
	if err = ensurePlainDirectory(root); err != nil {
		return embySyncResult{}, err
	}
	identity := sha256.Sum256([]byte(drama.ID))
	folderName := safeFilename(drama.DisplayTitle()) + " [" + hex.EncodeToString(identity[:8]) + "]"
	showDirectory, err := secureChildDirectory(root, folderName)
	if err != nil {
		return embySyncResult{}, err
	}
	seasonDirectory, err := secureChildDirectory(showDirectory, "Season 01")
	if err != nil {
		return embySyncResult{}, err
	}
	showBody, err := embyShowNFO(drama)
	if err != nil {
		return embySyncResult{}, err
	}
	result := embySyncResult{Directory: showDirectory, Episodes: len(chapters)}
	changed, err := atomicSafeWrite(showDirectory, "tvshow.nfo", showBody)
	if err != nil {
		return embySyncResult{}, err
	}
	result.record(changed)
	for index, chapter := range chapters {
		query := url.Values{"id": {drama.ID}, "chapter": {chapter.ID}, "key": {embyToken(key, drama.ID, chapter.ID, owner)}}
		if owner != "" {
			query.Set("account", owner)
		}
		stem := fmt.Sprintf("S01E%03d", index+1)
		changed, err = atomicSafeWrite(seasonDirectory, stem+".strm", []byte(base+embyPath+"/stream.m3u8?"+query.Encode()+"\n"))
		if err != nil {
			return embySyncResult{}, err
		}
		result.record(changed)
		episodeBody, marshalErr := embyEpisodeNFO(drama.DisplayTitle(), chapter, index+1)
		if marshalErr != nil {
			return embySyncResult{}, marshalErr
		}
		changed, err = atomicSafeWrite(seasonDirectory, stem+".nfo", episodeBody)
		if err != nil {
			return embySyncResult{}, err
		}
		result.record(changed)
	}
	return result, nil
}

func (result *embySyncResult) record(changed bool) {
	if changed {
		result.Written++
	} else {
		result.Unchanged++
	}
}

func embyShowNFO(drama Drama) ([]byte, error) {
	show := struct {
		XMLName xml.Name `xml:"tvshow"`
		Title   string   `xml:"title"`
		Plot    string   `xml:"plot,omitempty"`
	}{Title: drama.DisplayTitle(), Plot: firstNonEmpty(drama.Desc, drama.Intro)}
	body, err := xml.MarshalIndent(show, "", "  ")
	return append([]byte(xml.Header), body...), err
}

func embyEpisodeNFO(show string, chapter Chapter, episodeNumber int) ([]byte, error) {
	episode := struct {
		XMLName xml.Name `xml:"episodedetails"`
		Title   string   `xml:"title"`
		Show    string   `xml:"showtitle"`
		Season  int      `xml:"season"`
		Episode int      `xml:"episode"`
	}{Title: firstNonEmpty(chapter.Title, "第 "+chapter.EpisodeString(episodeNumber)+" 集"), Show: show, Season: 1, Episode: episodeNumber}
	body, err := xml.MarshalIndent(episode, "", "  ")
	return append([]byte(xml.Header), body...), err
}

func ensurePlainDirectory(directory string) error {
	if err := os.MkdirAll(directory, 0o755); err != nil {
		return err
	}
	info, err := os.Lstat(directory)
	if err != nil {
		return err
	}
	if info.Mode()&os.ModeSymlink != 0 || !info.IsDir() {
		return errors.New("Emby 同步目录不能是符号链接，且必须是文件夹")
	}
	return nil
}

func secureChildDirectory(parent, name string) (string, error) {
	if filepath.Base(name) != name || name == "." || name == ".." {
		return "", errors.New("Emby 目录名无效")
	}
	if err := ensurePlainDirectory(parent); err != nil {
		return "", err
	}
	child := filepath.Join(parent, name)
	if err := os.Mkdir(child, 0o755); err != nil && !errors.Is(err, os.ErrExist) {
		return "", err
	}
	info, err := os.Lstat(child)
	if err != nil {
		return "", err
	}
	if info.Mode()&os.ModeSymlink != 0 || !info.IsDir() {
		return "", fmt.Errorf("拒绝写入非普通目录：%s", child)
	}
	return child, nil
}

func atomicSafeWrite(directory, name string, body []byte) (bool, error) {
	if filepath.Base(name) != name || name == "." || name == ".." {
		return false, errors.New("Emby 文件名无效")
	}
	if err := ensurePlainDirectory(directory); err != nil {
		return false, err
	}
	target := filepath.Join(directory, name)
	if info, err := os.Lstat(target); err == nil {
		if info.Mode()&os.ModeSymlink != 0 || !info.Mode().IsRegular() {
			return false, fmt.Errorf("拒绝覆盖非普通文件：%s", target)
		}
		current, readErr := os.ReadFile(target)
		if readErr != nil {
			return false, readErr
		}
		if bytes.Equal(current, body) {
			return false, nil
		}
	} else if !errors.Is(err, os.ErrNotExist) {
		return false, err
	}
	temporary, err := os.CreateTemp(directory, ".juku-emby-*.tmp")
	if err != nil {
		return false, err
	}
	temporaryName := temporary.Name()
	defer os.Remove(temporaryName)
	if err = temporary.Chmod(0o644); err == nil {
		_, err = temporary.Write(body)
	}
	if err == nil {
		err = temporary.Sync()
	}
	if closeErr := temporary.Close(); err == nil {
		err = closeErr
	}
	if err != nil {
		return false, err
	}
	if err = ensurePlainDirectory(directory); err != nil {
		return false, err
	}
	if info, statErr := os.Lstat(target); statErr == nil && (info.Mode()&os.ModeSymlink != 0 || !info.Mode().IsRegular()) {
		return false, fmt.Errorf("拒绝覆盖非普通文件：%s", target)
	} else if statErr != nil && !errors.Is(statErr, os.ErrNotExist) {
		return false, statErr
	}
	if err = os.Rename(temporaryName, target); err != nil {
		return false, err
	}
	return true, nil
}
