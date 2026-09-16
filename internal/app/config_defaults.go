package app

import (
	"errors"
	"os"
)

const userAgent = "Mozilla/5.0 (iPhone; CPU iPhone OS 18_7 like Mac OS X) AppleWebKit/605.1.15 (KHTML, like Gecko) Version/26.6 Mobile/15E148 Safari/604.1"

type Config struct {
	adminUsername         string
	adminPassword         string
	adminUserExplicit     bool
	adminPasswordExplicit bool
	settingsLoaded        bool
	dataDir               string
	outputDirSetting      string
	OutputDir             string `json:"outputDir"`
	FFmpeg                string `json:"ffmpeg"`
	Concurrency           int    `json:"concurrency"`
	RequestConcurrency    int    `json:"requestConcurrency"`
	RequestIntervalMS     int    `json:"requestIntervalMs"`
	MaxPagesPerSort       int    `json:"maxPagesPerSort"`
	PageSize              int    `json:"pageSize"`
	Retries               int    `json:"retries"`
	SkipBytes             int64  `json:"skipBytes"`
	InsecureTLS           bool   `json:"insecureTLS"`
	ProxyURL              string `json:"proxyURL,omitempty"`
	HuangguoAIURL         string `json:"huangguoAIURL,omitempty"`
	HuangguoVideoURL      string `json:"huangguoVideoURL,omitempty"`
	HuangdouURL           string `json:"huangdouURL,omitempty"`
	HongguoURL            string `json:"hongguoURL,omitempty"`
}

func defaultConfig() Config {
	return Config{
		OutputDir: defaultOutputDir(), FFmpeg: "ffmpeg", Concurrency: 2,
		RequestConcurrency: 2, RequestIntervalMS: 500, MaxPagesPerSort: 20,
		PageSize: 50, Retries: 3, SkipBytes: 512 * 1024,
	}
}

func defaultOutputDir() string { return "短剧下载" }

func applyConfigEnvironment(config *Config) {
	for variable, target := range map[string]*string{
		"JUKU_HUANGGUO_AI_URL":    &config.HuangguoAIURL,
		"JUKU_HUANGGUO_VIDEO_URL": &config.HuangguoVideoURL,
		"JUKU_HUANGDOU_URL":       &config.HuangdouURL,
		"JUKU_HONGGUO_URL":        &config.HongguoURL,
		"JUKU_PROXY_URL":          &config.ProxyURL,
		"JUKU_OUTPUT_DIR":         &config.OutputDir,
		"JUKU_FFMPEG":             &config.FFmpeg,
	} {
		if value := os.Getenv(variable); value != "" {
			*target = value
		}
	}
}

func (config Config) validate() error {
	if _, err := configuredProxy(config.ProxyURL); err != nil {
		return err
	}
	for _, endpoint := range []string{config.HuangguoAIURL, config.HuangguoVideoURL, config.HuangdouURL, config.HongguoURL} {
		if endpoint != "" && !isProviderHTTPMediaURL(endpoint) {
			return errors.New("站点地址必须是有效的 HTTP/HTTPS URL")
		}
	}
	return nil
}
