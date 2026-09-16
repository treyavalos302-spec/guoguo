package app

import (
	"context"
	"testing"
)

func TestMultiSourceCacheKeepsSupportedSourcesAndMergesByFullID(t *testing.T) {
	cache := libraryCache{Dramas: []Drama{
		{ID: "700001", Title: "红果旧名称"},
		{ID: "huangdou:700001", Source: sourceHuangdou, Title: "黄豆同号"},
		{ID: "unknown:700001", Source: "unknown", Title: "应拒绝"},
	}, Sources: map[string]librarySourceState{sourceHongguo: {Status: "ready"}, sourceHuangdou: {Status: "ready"}, "unknown": {Status: "failed"}}}
	directory := t.TempDir()
	if err := writeLibraryCache(directory, cache); err != nil {
		t.Fatal(err)
	}
	restored, err := readLibraryCache(directory)
	if err != nil {
		t.Fatal(err)
	}
	if len(restored.Dramas) != 2 || len(restored.Sources) != 2 {
		t.Fatalf("supported cache entries were not preserved: %+v", restored)
	}
	merged := mergeSourceDramas(restored.Dramas, []Drama{{ID: "hongguo:700001", Source: sourceHongguo, Title: "红果新名称"}, {ID: "huangdou:700002", Source: sourceHuangdou, Title: "黄豆新增"}}, nil, "")
	if len(merged) != 3 {
		t.Fatalf("full source IDs did not merge independently: %d", len(merged))
	}
	if _, ok := normalizeProviderDrama(Drama{ID: "unknown:1", Source: "unknown"}); ok {
		t.Fatal("unknown source accepted")
	}
}

func TestMultiSourceRequestIDsAndSourceGroups(t *testing.T) {
	for _, test := range []struct {
		id, source string
		ok         bool
	}{
		{"700001", sourceHongguo, true},
		{"hongguo:700001", sourceHongguo, true},
		{"huangdou:rp_fixture", sourceHuangdou, true},
		{"huangguoai:fixture", sourceHuangguoAI, true},
		{"unknown:fixture", "", false},
	} {
		source, _, ok := splitProviderDramaID(test.id)
		if ok != test.ok || ok && source != test.source {
			t.Fatalf("split %q => %q,%v, want %q,%v", test.id, source, ok, test.source, test.ok)
		}
	}
	ctx := withSourceScope(context.Background(), accountRecord{Sources: []string{"huangguo"}})
	for _, source := range []string{sourceCloudfront, sourceHuangguoAI, sourceHuangguoVideo} {
		if !sourceAllowed(ctx, source) {
			t.Fatal("huangguo group excludes provider", source)
		}
	}
	if sourceAllowed(ctx, sourceHuangdou) {
		t.Fatal("huangguo group includes huangdou")
	}
}
