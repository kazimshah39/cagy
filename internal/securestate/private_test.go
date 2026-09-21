package securestate

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestPrivateFilesRejectUnsafePathsAndEnforceModes(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "state")
	if err := EnsureDir(dir); err != nil {
		t.Fatal(err)
	}
	info, err := os.Stat(dir)
	if err != nil || info.Mode().Perm() != 0o700 {
		t.Fatalf("state dir mode=%v err=%v", info.Mode().Perm(), err)
	}
	if err := WriteFile(dir, "record.json", []byte("safe")); err != nil {
		t.Fatal(err)
	}
	data, exists, err := ReadFile(filepath.Join(dir, "record.json"), 16)
	if err != nil || !exists || string(data) != "safe" {
		t.Fatalf("read=%q exists=%v err=%v", data, exists, err)
	}
	fileInfo, err := os.Stat(filepath.Join(dir, "record.json"))
	if err != nil || fileInfo.Mode().Perm() != 0o600 {
		t.Fatalf("file mode=%v err=%v", fileInfo.Mode().Perm(), err)
	}
	if err := WriteFile(dir, "../escape", []byte("bad")); err == nil {
		t.Fatal("expected invalid filename rejection")
	}
	if _, _, err := ReadFile(filepath.Join(dir, "record.json"), 2); err == nil || !strings.Contains(err.Error(), "exceeds") {
		t.Fatalf("oversized read error=%v", err)
	}

	target := filepath.Join(dir, "target")
	if err := os.WriteFile(target, []byte("target"), 0o600); err != nil {
		t.Fatal(err)
	}
	link := filepath.Join(dir, "link")
	if err := os.Symlink(target, link); err != nil {
		t.Fatal(err)
	}
	if _, _, err := ReadFile(link, 16); err == nil {
		t.Fatal("expected symlink read rejection")
	}
	if err := WriteFile(dir, "link", []byte("replacement")); err == nil {
		t.Fatal("expected symlink target rejection")
	}
}

func TestEnsureDirRejectsSymlinkAndRepairsMode(t *testing.T) {
	root := t.TempDir()
	dir := filepath.Join(root, "state")
	if err := os.Mkdir(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := EnsureDir(dir); err != nil {
		t.Fatal(err)
	}
	info, _ := os.Stat(dir)
	if info.Mode().Perm() != 0o700 {
		t.Fatalf("mode=%o", info.Mode().Perm())
	}
	link := filepath.Join(root, "state-link")
	if err := os.Symlink(dir, link); err != nil {
		t.Fatal(err)
	}
	if err := EnsureDir(link); err == nil {
		t.Fatal("expected symlink directory rejection")
	}
}

func TestRemoveFileIsIdempotent(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "state")
	if err := WriteFile(dir, "record", []byte("x")); err != nil {
		t.Fatal(err)
	}
	if err := RemoveFile(dir, "record"); err != nil {
		t.Fatal(err)
	}
	if err := RemoveFile(dir, "record"); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(dir, "record")); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("file still exists: %v", err)
	}
}

func TestDefaultDirUsesOnlyHerdrTandemName(t *testing.T) {
	t.Setenv("XDG_STATE_HOME", t.TempDir())
	path := DefaultDir()
	if filepath.Base(path) != "herdr-tandem" {
		t.Fatalf("path=%q", path)
	}
	if strings.Contains(strings.ToLower(path), "ca"+"gy") {
		t.Fatalf("old state name leaked into %q", path)
	}
}
