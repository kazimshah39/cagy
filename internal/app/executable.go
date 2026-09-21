package app

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

// resolveExecutable resolves the path returned by resolver, evaluates symlinks,
// and ensures it is an absolute path to a regular executable file.
func resolveExecutable(resolver func() (string, error)) (string, error) {
	if resolver == nil {
		resolver = os.Executable
	}
	execPath, err := resolver()
	if err != nil {
		return "", fmt.Errorf("lookup executable path: %w", err)
	}
	if strings.TrimSpace(execPath) == "" {
		return "", fmt.Errorf("lookup executable path: path is empty")
	}
	resolved, err := filepath.EvalSymlinks(execPath)
	if err != nil {
		return "", fmt.Errorf("eval executable symlinks: %w", err)
	}
	absPath, err := filepath.Abs(resolved)
	if err != nil {
		return "", fmt.Errorf("resolve absolute executable path: %w", err)
	}
	info, err := os.Stat(absPath)
	if err != nil {
		return "", fmt.Errorf("stat executable: %w", err)
	}
	if !info.Mode().IsRegular() {
		return "", fmt.Errorf("executable is not a regular file: %s", absPath)
	}
	if info.Mode().Perm()&0111 == 0 {
		return "", fmt.Errorf("file is not executable: %s", absPath)
	}
	return absPath, nil
}
