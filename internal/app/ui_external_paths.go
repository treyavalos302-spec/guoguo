package app

import (
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"net/url"
	"os"
	"path"
	"path/filepath"
	"strings"
)

type externalPaths struct {
	Forward string `json:"forwardPath"`
	Emby    string `json:"embyPath"`
}

func normalizeExternalPath(raw string) (string, error) {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return "", errors.New("私密路径不能为空")
	}
	if len(raw) > 512 || strings.ContainsAny(raw, "\x00\r\n\\?#") || !strings.HasPrefix(raw, "/") || strings.HasPrefix(raw, "//") {
		return "", errors.New("私密路径必须是绝对 URL 路径，且不能包含查询、片段或反斜杠")
	}
	parsed, err := url.ParseRequestURI(raw)
	if err != nil || parsed.RawQuery != "" || parsed.Fragment != "" || parsed.Path != raw || parsed.RawPath != "" {
		return "", errors.New("私密路径格式无效")
	}
	for _, segment := range strings.Split(raw, "/") {
		if segment == ".." || segment == "." {
			return "", errors.New("私密路径不能包含 . 或 ..")
		}
	}
	cleaned := path.Clean(raw)
	if cleaned != raw || cleaned == "/" {
		return "", errors.New("私密路径必须规范化，且不能是根路径")
	}
	for _, reserved := range []string{"/api", "/assets", "/login"} {
		if cleaned == reserved || strings.HasPrefix(cleaned, reserved+"/") {
			return "", fmt.Errorf("私密路径不能使用保留前缀 %s", reserved)
		}
	}
	return cleaned, nil
}

func externalPathsConflict(first, second string) bool {
	return first == second || strings.HasPrefix(first, second) || strings.HasPrefix(second, first)
}

func randomExternalPath(label string) (string, error) {
	body := make([]byte, 32)
	if _, err := rand.Read(body); err != nil {
		return "", err
	}
	return "/" + label + "-" + hex.EncodeToString(body), nil
}

func readOrCreatePrivateValue(directory, name string, create func() (string, error), validate func(string) (string, error)) (string, error) {
	if err := os.MkdirAll(directory, 0o700); err != nil {
		return "", err
	}
	filePath := filepath.Join(directory, name)
	read := func() (string, error) {
		info, err := os.Lstat(filePath)
		if err != nil {
			return "", err
		}
		if !info.Mode().IsRegular() || info.Mode()&os.ModeSymlink != 0 {
			return "", fmt.Errorf("%s 必须是普通文件", filePath)
		}
		if err := os.Chmod(filePath, 0o600); err != nil {
			return "", err
		}
		body, err := os.ReadFile(filePath)
		if err != nil {
			return "", err
		}
		return validate(strings.TrimSpace(string(body)))
	}
	value, err := read()
	if err == nil {
		return value, nil
	}
	if !errors.Is(err, os.ErrNotExist) {
		return "", err
	}
	value, err = create()
	if err != nil {
		return "", err
	}
	value, err = validate(value)
	if err != nil {
		return "", err
	}
	file, err := os.OpenFile(filePath, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600)
	if errors.Is(err, os.ErrExist) {
		return read()
	}
	if err != nil {
		return "", err
	}
	complete := false
	defer func() {
		_ = file.Close()
		if !complete {
			_ = os.Remove(filePath)
		}
	}()
	if _, err = file.WriteString(value + "\n"); err == nil {
		err = file.Sync()
	}
	if closeErr := file.Close(); err == nil {
		err = closeErr
	}
	if err != nil {
		return "", err
	}
	complete = true
	return value, nil
}

func loadExternalPaths(dataDirectory string) (externalPaths, error) {
	load := func(variable, file, label string) (string, error) {
		if configured, exists := os.LookupEnv(variable); exists {
			value, err := normalizeExternalPath(configured)
			if err != nil {
				return "", fmt.Errorf("%s: %w", variable, err)
			}
			return value, nil
		}
		return readOrCreatePrivateValue(dataDirectory, file, func() (string, error) {
			return randomExternalPath(label)
		}, normalizeExternalPath)
	}
	forward, err := load("JUKU_FORWARD_PATH", "forward-path", "forward")
	if err != nil {
		return externalPaths{}, err
	}
	emby, err := load("JUKU_EMBY_PATH", "emby-path", "emby")
	if err != nil {
		return externalPaths{}, err
	}
	if externalPathsConflict(forward, emby) {
		return externalPaths{}, errors.New("JUKU_FORWARD_PATH 与 JUKU_EMBY_PATH 不能相同或互为前缀")
	}
	return externalPaths{Forward: forward, Emby: emby}, nil
}

func (app *UIApp) ensureExternalPaths() (externalPaths, error) {
	app.externalMu.Lock()
	defer app.externalMu.Unlock()
	if app.externalReady || app.externalErr != nil {
		return app.externalPaths, app.externalErr
	}
	app.externalPaths, app.externalErr = loadExternalPaths(app.cfg.dataDirectory())
	app.externalReady = app.externalErr == nil
	return app.externalPaths, app.externalErr
}

func (app *UIApp) externalPublicPath(requestPath string) bool {
	paths, err := app.ensureExternalPaths()
	if err != nil {
		return false
	}
	return strings.HasPrefix(requestPath, paths.Emby+"/") || strings.HasPrefix(requestPath, paths.Forward+"/")
}
