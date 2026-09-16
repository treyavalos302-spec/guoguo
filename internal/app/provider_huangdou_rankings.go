package app

import (
	"errors"
	"strings"
)

func parseHuangdouRanking(decoded any, page int) (rankingPage, error) {
	data := huangdouDataMap(decoded)
	rows, valid := data["list"].([]any)
	if !valid {
		return rankingPage{}, errors.New("黄豆未返回有效榜单")
	}
	if len(rows) > 20 {
		return rankingPage{}, errors.New("黄豆榜单分页格式已变化")
	}
	result := rankingPage{Items: make([]rankingItem, 0, len(rows)), HasMore: len(rows) == 20}
	seen := map[string]bool{}
	for index, value := range rows {
		row, valid := value.(map[string]any)
		id := strings.TrimPrefix(firstNonEmpty(mapString(row, "id"), mapString(row, "drama_id")), "rp_")
		title := mapString(row, "name", "title", "t")
		if !valid || !rankingSourceID.MatchString(id) || strings.TrimSpace(title) == "" || seen[id] {
			return rankingPage{}, errors.New("黄豆榜单包含无效或重复条目")
		}
		seen[id] = true
		remark := firstNonEmpty(mapString(row, "update_label"), mapString(row, "corner"))
		drama := Drama{VIP: huangdouVIPFlag(row), ID: providerDramaID(sourceHuangdou, id), Source: sourceHuangdou, SourceID: id, Title: title, Name: title, ChannelName: "黄豆", CategoryName: mapString(row, "category"), TotalEpisode: mapString(row, "episode_count"), Remark: remark, ReleaseStatus: releaseStatusFromRemark(remark), Heat: mapString(row, "hot_rate"), Views: normalizeViews(mapString(row, "click")), OnlineDate: providerReleaseDate(mapString(row, "issue_date"))}
		metric := ""
		if drama.Heat != "" {
			metric = drama.Heat + "热度"
		}
		result.Items = append(result.Items, rankingItem{Rank: (page-1)*20 + index + 1, Drama: drama, Metric: metric})
	}
	return result, nil
}
