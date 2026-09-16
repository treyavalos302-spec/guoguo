package app

import (
	"encoding/json"
	"errors"
	"regexp"
	"strconv"
	"strings"
)

var rankingScriptTags = regexp.MustCompile(`(?is)<script\b[^>]*>`)

type hongguoRankingContent struct {
	Success bool `json:"isSuccess"`
	Rows    []struct {
		ID          string   `json:"id"`
		SeriesID    string   `json:"seriesId"`
		Rank        int      `json:"rank"`
		Title       string   `json:"title"`
		Heat        string   `json:"heatText"`
		Score       string   `json:"scoreText"`
		Tags        []string `json:"tags"`
		Description string   `json:"description"`
		EpisodeIDs  []string `json:"episodeVids"`
		Cover       any      `json:"cover"`
	} `json:"rankList"`
	Pagination struct {
		Page       int `json:"pageNum"`
		TotalPages int `json:"totalPages"`
	} `json:"pagination"`
}

func parseHongguoRanking(body string, board rankingBoard, page int) (rankingPage, error) {
	failure := errors.New("红果榜单格式或分页已变化，请稍后重试")
	loaderKey := "rank_" + board.path + "/page"
	loader := nestedMap(parseRouterData(body), "loaderData", loaderKey)
	if mapString(loader, "rankKey") != board.upstreamKey || mapString(loader, "pageNum") != strconv.Itoa(page) {
		return rankingPage{}, failure
	}
	var content hongguoRankingContent
	if inline := nestedMap(loader, "content"); len(inline) > 0 {
		raw, err := json.Marshal(inline)
		if err != nil || json.Unmarshal(raw, &content) != nil {
			return rankingPage{}, failure
		}
	} else {

		for _, tag := range rankingScriptTags.FindAllString(body, -1) {
			if extractAttr(tag, "data-fn-name") != "r" || extractAttr(tag, "data-script-src") != "modern-run-router-data-fn" {
				continue
			}
			var args []json.RawMessage
			if json.Unmarshal([]byte(extractAttr(tag, "data-fn-args")), &args) != nil || len(args) != 3 {
				continue
			}
			var route, field string
			if json.Unmarshal(args[0], &route) != nil || json.Unmarshal(args[1], &field) != nil || route != loaderKey || field != "content" {
				continue
			}
			if json.Unmarshal(args[2], &content) != nil {
				return rankingPage{}, failure
			}
			break
		}
	}
	if !content.Success || content.Rows == nil || content.Pagination.Page != page || content.Pagination.TotalPages < page || content.Pagination.TotalPages > 500 {
		return rankingPage{}, failure
	}
	result := rankingPage{Items: make([]rankingItem, 0, len(content.Rows)), TotalPages: content.Pagination.TotalPages, HasMore: page < content.Pagination.TotalPages, UpdatedText: mapString(loader, "updatedText")}
	seen := map[string]bool{}
	previous := (page - 1) * 20
	for _, row := range content.Rows {
		id := firstNonEmpty(row.SeriesID, row.ID)
		if !hongguoNumericID.MatchString(id) || row.ID != "" && row.SeriesID != "" && row.ID != row.SeriesID || strings.TrimSpace(row.Title) == "" || row.Rank <= previous || row.Rank > page*20 || seen[id] {
			return rankingPage{}, failure
		}
		seen[id], previous = true, row.Rank
		drama := Drama{ID: providerDramaID(sourceHongguo, id), Source: sourceHongguo, SourceID: id, Title: row.Title, Name: row.Title, Desc: row.Description, Intro: row.Description, Tags: row.Tags, Heat: row.Heat, Score: strings.TrimPrefix(row.Score, "评分"), ChannelName: "红果"}
		if cover := hongguoCoverAddress(coverPathFromAny(row.Cover)); cover != "" {
			drama.Cover, drama.CoverURL = cover, cover
		}
		if len(row.Tags) > 0 {
			drama.CategoryName = row.Tags[0]
		}
		if len(row.EpisodeIDs) > 0 {
			drama.TotalEpisode = len(row.EpisodeIDs)
		}
		result.Items = append(result.Items, rankingItem{Rank: row.Rank, Drama: drama, Metric: row.Heat})
	}
	if len(result.Items) == 0 && result.HasMore {
		return rankingPage{}, failure
	}
	return result, nil
}
