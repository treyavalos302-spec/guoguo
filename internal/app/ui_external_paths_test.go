package app

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestExternalPathsPersistPrivatelyAndDoNotOverlap(t *testing.T) {
	directory := t.TempDir()
	first, err := loadExternalPaths(directory)
	if err != nil {
		t.Fatal(err)
	}
	second, err := loadExternalPaths(directory)
	if err != nil {
		t.Fatal(err)
	}
	if first != second || first.Forward == first.Emby || externalPathsConflict(first.Forward, first.Emby) {
		t.Fatalf("private paths were not stable and distinct: %#v %#v", first, second)
	}
	for _, name := range []string{"forward-path", "emby-path"} {
		info, statErr := os.Stat(filepath.Join(directory, name))
		if statErr != nil || info.Mode().Perm() != 0o600 {
			t.Fatalf("%s was not stored with mode 0600: %v", name, statErr)
		}
	}
	if len(strings.TrimPrefix(first.Forward, "/forward-")) < 64 || len(strings.TrimPrefix(first.Emby, "/emby-")) < 64 {
		t.Fatal("generated private paths do not contain enough entropy")
	}
}

func TestNormalizeExternalPathStrictlyRejectsUnsafeValues(t *testing.T) {
	invalid := []string{"", "/", "relative", "//double", "/trailing/", "/a//b", "/a/../b", "/a/./b", "/api", "/api/ui", "/api/private", "/assets", "/assets/private", "/login", "/login/private", "/private?token=x", "/private#fragment", `/private\\child`, "/private%2Fchild"}
	for _, value := range invalid {
		if normalized, err := normalizeExternalPath(value); err == nil {
			t.Errorf("unsafe path %q accepted as %q", value, normalized)
		}
	}
	for _, value := range []string{"/forward-private", "/emby/private", "/apiary"} {
		if normalized, err := normalizeExternalPath(value); err != nil || normalized != value {
			t.Errorf("safe path %q rejected: %q %v", value, normalized, err)
		}
	}
}

func TestExternalPathEnvironmentValidationAndConflicts(t *testing.T) {
	t.Setenv("JUKU_FORWARD_PATH", "/same")
	t.Setenv("JUKU_EMBY_PATH", "/same/child")
	if _, err := loadExternalPaths(t.TempDir()); err == nil {
		t.Fatal("overlapping external paths were accepted")
	}
	t.Setenv("JUKU_FORWARD_PATH", "/api/forward")
	t.Setenv("JUKU_EMBY_PATH", "/private-emby")
	if _, err := loadExternalPaths(t.TempDir()); err == nil {
		t.Fatal("reserved external path was accepted")
	}
}
