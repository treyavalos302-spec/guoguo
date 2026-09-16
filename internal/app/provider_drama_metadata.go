package app

import (
	"context"
	"errors"
	"net/url"
	"strings"
)

func (d *Downloader) fetchDramaMetadata(ctx context.Context, drama Drama) (Drama, error) {
	source, id, valid := splitProviderDramaID(drama.ID)
	if !valid {
		return Drama{}, errors.New("无效的剧集 ID")
	}
	switch source {
	case sourceHongguo:
		return d.fetchHongguoDramaMetadata(ctx, drama, id)
	case sourceHuangdou:
		row, err := d.huangdouDetail(ctx, id)
		if err != nil {
			return Drama{}, err
		}
		patch := Drama{ID: drama.ID, Source: source, SourceID: id, Title: mapString(row, "name", "title"), Name: mapString(row, "name", "title"), Desc: mapString(row, "description", "summary"), Intro: mapString(row, "description", "summary"), VIP: huangdouVIPFlag(row), OnlineDate: providerReleaseDate(mapString(row, "issue_date")), Views: normalizeViews(mapString(row, "click")), Heat: mapString(row, "hot_rate"), TotalEpisode: mapString(row, "episode_count"), EpisodeCount: mapString(row, "episode_count"), CategoryName: mapString(row, "category")}
		return mergeDramaMetadata(patch, drama), nil
	default:
		return Drama{}, errors.New("该站源的资料补齐接口尚未接入")
	}
}

func (d *Downloader) fetchHongguoDramaMetadata(ctx context.Context, drama Drama, id string) (Drama, error) {
	if !hongguoNumericID.MatchString(id) {
		return Drama{}, errors.New("无效的红果剧集 ID")
	}
	entry, appErr := d.hongguoAppDetail(ctx, id)
	patch := entry.Drama
	if patch.ID == drama.ID {
		appErr = nil
		if patch.Title == id {
			patch.Title, patch.Name = "", ""
		}
		if patch.CategoryName == "短剧" {
			patch.CategoryName = ""
		}
		if patch.OnlineDate != "" || drama.OnlineDate != "" {
			return patch, nil
		}
	}
	body, webErr := d.fetchProviderText(ctx, hongguoBaseURL+"/detail?series_id="+url.QueryEscape(id), hongguoBaseURL+"/")
	if webErr == nil {
		var web Drama
		web, webErr = parseHongguoSortDetail(body, id)
		patch = mergeDramaMetadata(patch, web)
	}
	if patch.ID == drama.ID && strings.TrimSpace(patch.DisplayTitle()) != "" && webErr == nil {
		return patch, nil
	}
	return patch, errors.Join(appErr, webErr)
}
