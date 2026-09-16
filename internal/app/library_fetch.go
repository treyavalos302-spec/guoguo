package app

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"
)

func (d *Downloader) fetchAllDramas(ctx context.Context, sourceFilter string) ([]Drama, error) {
	if sourceFilter != "" && !matchesSourceFilter(sourceHongguo, sourceFilter) && !matchesSourceFilter(sourceHuangdou, sourceFilter) {
		return nil, errors.New("站源尚未接入或筛选无效")
	}
	if more, _ := ctx.Value(libraryMoreKey{}).(bool); more {
		return d.fetchMoreLibrary(ctx, sourceFilter)
	}
	fmt.Println("正在获取剧库列表...")
	type result struct {
		name  string
		items []Drama
		err   error
	}
	jobs := []struct {
		name string
		fn   func(context.Context) ([]Drama, error)
	}{
		{name: sourceHongguo, fn: d.fetchHongguoDramas},
		{name: sourceHuangdou, fn: d.fetchHuangdouDramas},
	}
	selected := jobs[:0]
	for _, job := range jobs {
		if matchesSourceFilter(job.name, sourceFilter) && sourceAllowed(ctx, job.name) {
			selected = append(selected, job)
		}
	}
	jobs = selected
	results := make(chan result, len(jobs))
	for _, job := range jobs {
		job := job
		go func() {
			jobCtx, cancel := context.WithTimeout(ctx, 10*time.Minute)
			defer cancel()
			items, err := job.fn(jobCtx)
			results <- result{name: job.name, items: onlySupportedDramas(items), err: err}
		}()
	}
	seen := map[string]bool{}
	var dramas []Drama
	failures := map[string]error{}
	for range jobs {
		result := <-results
		if len(result.items) == 0 && result.err == nil && !(result.name == sourceHongguo && hongguoCatalogInitialized(d.hongguoCatalogSnapshot())) {
			result.err = errors.New("未返回可识别的视频数据")
		}
		if result.err != nil {
			failures[result.name] = result.err
			fmt.Printf(" %s 获取失败: %v\n", result.name, publicError(result.err))
		}
		reportLibraryProgress(ctx, result.name, result.items, result.err, true)
		for _, drama := range result.items {
			if !seen[drama.ID] {
				seen[drama.ID] = true
				dramas = append(dramas, drama)
			}
		}
	}
	sort.SliceStable(dramas, func(left, right int) bool { return dramas[left].DisplayTitle() < dramas[right].DisplayTitle() })
	fmt.Printf(" 剧库获取完成：%d 部\n", len(dramas))
	if len(failures) > 0 {
		return dramas, &libraryLoadError{failures: failures}
	}
	return dramas, nil
}

func (d *Downloader) GetDramaChapters(ctx context.Context, seriesID string) (string, []Chapter, error) {
	source, sourceID, valid := splitProviderDramaID(seriesID)
	if !valid {
		return "", nil, errors.New("剧集 ID 无效或站源不受支持")
	}
	var title string
	var chapters []Chapter
	var err error
	switch source {
	case sourceHongguo:
		title, chapters, err = d.fetchHongguoChapters(ctx, sourceID)
	case sourceHuangdou:
		title, chapters, err = d.fetchHuangdouChapters(ctx, sourceID)
	default:
		return "", nil, errors.New("此站源的分集解析尚未接入")
	}
	return title, uniqueChapters(chapters), err
}

func uniqueChapters(chapters []Chapter) []Chapter {
	seen := map[string]bool{}
	var out []Chapter
	for i, chapter := range chapters {
		key := chapter.ID
		if key == "" {
			key = chapter.VideoURL
		}
		if key == "" {
			key = fmt.Sprintf("idx_%d", i)
		}
		if seen[key] {
			continue
		}
		seen[key] = true
		out = append(out, chapter)
	}
	return out
}

func (d *Downloader) BuildDramaTasks(ctx context.Context, drama Drama) ([]Task, error) {
	return d.buildDramaTasksInDirectory(ctx, drama, "")
}

func (d *Downloader) buildDramaTasksInDirectory(ctx context.Context, drama Drama, existingDirectory string) ([]Task, error) {
	var valid bool
	drama, valid = normalizeProviderDrama(drama)
	if !valid {
		return nil, errors.New("剧集 ID 或站源不受支持")
	}
	title, chapters, err := d.GetDramaChapters(ctx, drama.ID)
	if err != nil {
		return nil, err
	}
	if len(chapters) == 0 {
		return nil, nil
	}
	if drama.Source == sourceHongguo {
		drama = d.hongguoCachedDrama(drama)
	}
	safeDrama := safeFilename(title)
	dramaDir, err := safeJoin(d.cfg.OutputDir, safeDrama)
	if err != nil {
		return nil, err
	}
	if existingDirectory != "" {
		dramaDir = existingDirectory
	}
	markerPath := filepath.Join(dramaDir, ".drama-id")
	if body, readErr := os.ReadFile(markerPath); readErr == nil && strings.TrimSpace(string(body)) != "" && strings.TrimSpace(string(body)) != drama.ID {
		dramaDir, err = safeJoin(d.cfg.OutputDir, safeFilename(fmt.Sprintf("%s_%s", title, hashShort(drama.ID))))
		if err != nil {
			return nil, err
		}
		markerPath = filepath.Join(dramaDir, ".drama-id")
	}
	if err := os.MkdirAll(dramaDir, 0o755); err != nil {
		return nil, err
	}
	if drama.ID != "" {
		_ = os.WriteFile(markerPath, []byte(drama.ID), 0o644)
	}
	var tasks []Task
	for index, chapter := range chapters {
		episode := chapter.EpisodeString(index + 1)
		outputPath, err := safeJoin(dramaDir, safeFilename(padEpisode(episode)+".mp4"))
		if err != nil {
			return nil, err
		}
		tasks = append(tasks, Task{DramaID: drama.ID, DramaTitle: title, Chapter: chapter, Index: index + 1, Total: len(chapters), OutPath: outputPath, ReleaseStatus: dramaReleaseStatus(drama)})
	}
	return tasks, nil
}
