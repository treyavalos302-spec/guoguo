package app

import (
	"net/http"
	"strings"
)

const (
	sourceHongguo       = "hongguo"
	sourceHuangdou      = "huangdou"
	sourceHuangguoAI    = "huangguoai"
	sourceHuangguoVideo = "huangguo-video"
	sourceCloudfront    = "cloudfront"
)

func canonicalProviderSource(source string) string {
	switch strings.ToLower(strings.TrimSpace(source)) {
	case sourceHongguo, "hongguoduanju.com", "www.hongguoduanju.com":
		return sourceHongguo
	case sourceHuangdou, "tideember.cc", "xqjurgek.top":
		return sourceHuangdou
	case "huangguo", sourceHuangguoAI, "huangguoai.com":
		return sourceHuangguoAI
	case sourceHuangguoVideo, "huangguo.video":
		return sourceHuangguoVideo
	case sourceCloudfront, "huangguo-legacy", "legacy":
		return sourceCloudfront
	default:
		return ""
	}
}

func supportedProviderSource(source string) bool {
	switch canonicalProviderSource(source) {
	case sourceHongguo, sourceHuangdou, sourceHuangguoAI, sourceHuangguoVideo, sourceCloudfront:
		return true
	default:
		return false
	}
}

func isHuangguoProviderSource(source string) bool {
	return supportedProviderSource(source)
}

func splitProviderDramaID(identifier string) (source, sourceID string, ok bool) {
	identifier = strings.TrimSpace(identifier)
	prefix, remainder, prefixed := strings.Cut(identifier, ":")
	if prefixed {
		source = canonicalProviderSource(prefix)
		sourceID = strings.TrimSpace(remainder)
		if source == "" || sourceID == "" || strings.ContainsAny(sourceID, "\x00\r\n") {
			return "", "", false
		}
	} else {
		source = sourceHongguo
		sourceID = identifier
	}
	if source == sourceHongguo {
		sourceID = strings.TrimSpace(strings.TrimPrefix(sourceID, "hg-series-v1:"))
		if !hongguoNumericID.MatchString(sourceID) {
			return "", "", false
		}
	} else if len(sourceID) > 512 {
		return "", "", false
	}
	return source, sourceID, true
}

func sourceFromDramaID(identifier string) string {
	source, _, ok := splitProviderDramaID(identifier)
	if !ok {
		return ""
	}
	return source
}

func normalizeProviderDrama(drama Drama) (Drama, bool) {
	source := canonicalProviderSource(drama.Source)
	parsedSource, parsedID, parsed := splitProviderDramaID(drama.ID)
	if source == "" && parsed {
		source = parsedSource
	}
	if source == "" {
		return Drama{}, false
	}
	sourceID := strings.TrimSpace(drama.SourceID)
	if sourceID == "" && parsed && parsedSource == source {
		sourceID = parsedID
	}
	if sourceID == "" && source != sourceHongguo && strings.TrimSpace(drama.ID) != "" && !strings.Contains(drama.ID, ":") {
		sourceID = strings.TrimSpace(drama.ID)
	}
	if sourceID == "" || strings.ContainsAny(sourceID, "\x00\r\n") || len(sourceID) > 512 {
		return Drama{}, false
	}
	if source == sourceHongguo && !hongguoNumericID.MatchString(sourceID) {
		return Drama{}, false
	}
	drama.Source = source
	drama.SourceID = sourceID
	drama.ID = providerDramaID(source, sourceID)
	return drama, true
}

func onlySupportedDramas(dramas []Drama) []Drama {
	filtered := make([]Drama, 0, len(dramas))
	seen := make(map[string]bool, len(dramas))
	for _, drama := range dramas {
		if normalized, valid := normalizeProviderDrama(drama); valid && !seen[normalized.ID] {
			seen[normalized.ID] = true
			filtered = append(filtered, normalized)
		}
	}
	return filtered
}

func isSupportedTask(task Task) bool {
	source, _, valid := splitProviderDramaID(task.DramaID)
	if !valid {
		return false
	}
	chapterSource := canonicalProviderSource(task.Chapter.Source)
	return chapterSource == "" || chapterSource == source
}

func readProviderIDsRequest(writer http.ResponseWriter, request *http.Request) ([]string, bool) {
	identifiers, valid := readIDsRequest(writer, request)
	if !valid {
		return nil, false
	}
	for index, identifier := range identifiers {
		source, sourceID, supported := splitProviderDramaID(identifier)
		if !supported {
			writeJSON(writer, http.StatusBadRequest, map[string]string{"error": "剧集 ID 或站源不受支持"})
			return nil, false
		}
		identifiers[index] = providerDramaID(source, sourceID)
	}
	return identifiers, true
}

// Compatibility wrappers keep existing call sites stable while the application is multi-source.
func normalizeHongguoDrama(drama Drama) (Drama, bool) { return normalizeProviderDrama(drama) }
func onlyHongguoDramas(dramas []Drama) []Drama        { return onlySupportedDramas(dramas) }
func isHongguoTask(task Task) bool                    { return isSupportedTask(task) }
func readHongguoIDsRequest(writer http.ResponseWriter, request *http.Request) ([]string, bool) {
	return readProviderIDsRequest(writer, request)
}
