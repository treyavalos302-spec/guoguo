package app

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestSyncEmbyDirectoryIsIdempotentAndPreservesUnrelatedFiles(t *testing.T) {
	root := t.TempDir()
	drama := Drama{ID: "hongguo:7000000000000000001", Title: "同步测试", Desc: "A < B"}
	chapters := []Chapter{{ID: drama.ID + ":1", Title: "第一集"}, {ID: drama.ID + ":2", Title: "第二集"}}
	key := []byte("0123456789abcdef0123456789abcdef")
	first, err := syncEmbyDirectory(root, drama, chapters, "https://juku.example", "/private-emby", key, "")
	if err != nil || first.Episodes != 2 || first.Written != 5 || first.Unchanged != 0 {
		t.Fatalf("initial sync failed: %#v %v", first, err)
	}
	unrelated := filepath.Join(first.Directory, "Season 01", "manual.txt")
	if err = os.WriteFile(unrelated, []byte("keep"), 0o644); err != nil {
		t.Fatal(err)
	}
	second, err := syncEmbyDirectory(root, drama, chapters, "https://juku.example", "/private-emby", key, "")
	if err != nil || second.Written != 0 || second.Unchanged != 5 {
		t.Fatalf("idempotent sync rewrote files: %#v %v", second, err)
	}
	if body, readErr := os.ReadFile(unrelated); readErr != nil || string(body) != "keep" {
		t.Fatal("sync removed or changed an unrelated file", readErr)
	}
	strm, err := os.ReadFile(filepath.Join(first.Directory, "Season 01", "S01E001.strm"))
	if err != nil || !strings.Contains(string(strm), "https://juku.example/private-emby/stream.m3u8?") || strings.Contains(string(strm), "/api/emby/") {
		t.Fatal("STRM did not use the private Emby path", err, string(strm))
	}
}

func TestSyncEmbyDirectoryRejectsSymlinks(t *testing.T) {
	root := t.TempDir()
	drama := Drama{ID: "hongguo:7000000000000000001", Title: "链接测试"}
	chapters := []Chapter{{ID: drama.ID + ":1", Title: "第一集"}}
	key := []byte("0123456789abcdef0123456789abcdef")
	result, err := syncEmbyDirectory(root, drama, chapters, "https://juku.example", "/private-emby", key, "")
	if err != nil {
		t.Fatal(err)
	}
	target := filepath.Join(t.TempDir(), "outside.nfo")
	if err = os.WriteFile(target, []byte("outside"), 0o644); err != nil {
		t.Fatal(err)
	}
	show := filepath.Join(result.Directory, "tvshow.nfo")
	if err = os.Remove(show); err != nil {
		t.Fatal(err)
	}
	if err = os.Symlink(target, show); err != nil {
		t.Skip("symlink creation unavailable:", err)
	}
	if _, err = syncEmbyDirectory(root, drama, chapters, "https://juku.example", "/private-emby", key, ""); err == nil {
		t.Fatal("sync followed a symlink target")
	}
	if body, readErr := os.ReadFile(target); readErr != nil || string(body) != "outside" {
		t.Fatal("symlink target was changed", readErr)
	}
}
