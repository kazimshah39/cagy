package app

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestResolveExecutableValidAndSymlinks(t *testing.T) {
	tempDir := t.TempDir()
	binPath := filepath.Join(tempDir, "fake-herdr-tandem")
	if err := os.WriteFile(binPath, []byte("#!/bin/sh\nexit 0\n"), 0755); err != nil {
		t.Fatal(err)
	}

	symlinkPath := filepath.Join(tempDir, "fake-herdr-tandem-link")
	if err := os.Symlink(binPath, symlinkPath); err != nil {
		t.Fatal(err)
	}

	realBin, err := filepath.EvalSymlinks(binPath)
	if err != nil {
		t.Fatal(err)
	}

	resolved, err := resolveExecutable(func() (string, error) {
		return symlinkPath, nil
	})
	if err != nil {
		t.Fatalf("unexpected error resolving executable: %v", err)
	}
	if resolved != realBin {
		t.Fatalf("expected resolved %q, got %q", realBin, resolved)
	}
}

func TestResolveExecutableRejectsInvalid(t *testing.T) {
	tempDir := t.TempDir()

	// 1. Non-executable file
	nonExec := filepath.Join(tempDir, "non-exec")
	if err := os.WriteFile(nonExec, []byte("data"), 0644); err != nil {
		t.Fatal(err)
	}
	if _, err := resolveExecutable(func() (string, error) { return nonExec, nil }); err == nil || !strings.Contains(err.Error(), "not executable") {
		t.Fatalf("expected not executable error, got: %v", err)
	}

	// 2. Directory
	dirPath := filepath.Join(tempDir, "a-dir")
	if err := os.Mkdir(dirPath, 0755); err != nil {
		t.Fatal(err)
	}
	if _, err := resolveExecutable(func() (string, error) { return dirPath, nil }); err == nil || !strings.Contains(err.Error(), "not a regular file") {
		t.Fatalf("expected not a regular file error, got: %v", err)
	}

	// 3. Missing file
	missingPath := filepath.Join(tempDir, "does-not-exist")
	if _, err := resolveExecutable(func() (string, error) { return missingPath, nil }); err == nil {
		t.Fatal("expected error for missing executable, got nil")
	}

	// 4. Empty path
	if _, err := resolveExecutable(func() (string, error) { return "", nil }); err == nil || !strings.Contains(err.Error(), "path is empty") {
		t.Fatalf("expected path is empty error, got: %v", err)
	}

	// 5. Resolver error
	testErr := errors.New("resolver failure")
	if _, err := resolveExecutable(func() (string, error) { return "", testErr }); !errors.Is(err, testErr) {
		t.Fatalf("expected resolver failure error, got: %v", err)
	}
}

func TestResolveExecutableHDTAliasSymlink(t *testing.T) {
	tempDir := t.TempDir()
	canonicalBin := filepath.Join(tempDir, "herdr-tandem")
	if err := os.WriteFile(canonicalBin, []byte("#!/bin/sh\nexit 0\n"), 0755); err != nil {
		t.Fatal(err)
	}

	aliasSymlink := filepath.Join(tempDir, "hdt")
	if err := os.Symlink("herdr-tandem", aliasSymlink); err != nil {
		t.Fatal(err)
	}

	canonicalTarget, err := filepath.EvalSymlinks(canonicalBin)
	if err != nil {
		t.Fatal(err)
	}

	resolved, err := resolveExecutable(func() (string, error) {
		return aliasSymlink, nil
	})
	if err != nil {
		t.Fatalf("unexpected error resolving hdt alias: %v", err)
	}
	if resolved != canonicalTarget {
		t.Fatalf("expected resolved path %q, got %q", canonicalTarget, resolved)
	}
}
