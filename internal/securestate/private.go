package securestate

import (
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
)

// DefaultDir returns herdr-tandem's private state directory for the current user.
func DefaultDir() string {
	if stateHome := strings.TrimSpace(os.Getenv("XDG_STATE_HOME")); stateHome != "" && filepath.IsAbs(stateHome) {
		return filepath.Join(stateHome, "herdr-tandem")
	}
	configDir, err := os.UserConfigDir()
	if err == nil && strings.TrimSpace(configDir) != "" {
		return filepath.Join(configDir, "herdr-tandem", "state")
	}
	home, err := os.UserHomeDir()
	if err == nil && strings.TrimSpace(home) != "" {
		return filepath.Join(home, ".herdr-tandem", "state")
	}
	return ""
}

// InspectDir validates an existing private state directory.
func InspectDir(path string) (bool, error) {
	if strings.TrimSpace(path) == "" {
		return false, fmt.Errorf("herdr-tandem state directory is unavailable")
	}
	info, err := os.Lstat(path)
	if errors.Is(err, os.ErrNotExist) {
		return false, nil
	}
	if err != nil {
		return false, fmt.Errorf("inspect herdr-tandem state directory: %w", err)
	}
	if !info.IsDir() || info.Mode()&os.ModeSymlink != 0 {
		return true, fmt.Errorf("herdr-tandem state path is not a private directory")
	}
	if info.Mode().Perm() != 0o700 {
		return true, fmt.Errorf("herdr-tandem state directory permissions are %04o, want 0700", info.Mode().Perm())
	}
	return true, nil
}

// EnsureDir creates and validates a private state directory.
func EnsureDir(path string) error {
	if strings.TrimSpace(path) == "" {
		return fmt.Errorf("herdr-tandem state directory is unavailable")
	}
	if err := os.MkdirAll(path, 0o700); err != nil {
		return fmt.Errorf("create herdr-tandem state directory: %w", err)
	}
	info, err := os.Lstat(path)
	if err != nil {
		return fmt.Errorf("inspect herdr-tandem state directory: %w", err)
	}
	if !info.IsDir() || info.Mode()&os.ModeSymlink != 0 {
		return fmt.Errorf("herdr-tandem state path is not a private directory")
	}
	if info.Mode().Perm() != 0o700 {
		if err := os.Chmod(path, 0o700); err != nil {
			return fmt.Errorf("secure herdr-tandem state directory: %w", err)
		}
	}
	return nil
}

// ReadFile reads a bounded private regular file after validating its mode and identity.
func ReadFile(path string, maxBytes int64) ([]byte, bool, error) {
	if maxBytes <= 0 {
		return nil, false, fmt.Errorf("invalid private state size limit")
	}
	info, err := os.Lstat(path)
	if errors.Is(err, os.ErrNotExist) {
		return nil, false, nil
	}
	if err != nil {
		return nil, false, err
	}
	if !info.Mode().IsRegular() || info.Mode()&os.ModeSymlink != 0 {
		return nil, true, fmt.Errorf("private state is not a regular file")
	}
	if info.Mode().Perm() != 0o600 {
		return nil, true, fmt.Errorf("private state permissions are %04o, want 0600", info.Mode().Perm())
	}
	if info.Size() > maxBytes {
		return nil, true, fmt.Errorf("private state exceeds %d bytes", maxBytes)
	}
	// #nosec G304 -- callers pass fixed or digest-derived paths inside the private state directory.
	file, err := os.Open(path)
	if err != nil {
		return nil, true, err
	}
	defer file.Close()
	openedInfo, err := file.Stat()
	if err != nil {
		return nil, true, err
	}
	if !openedInfo.Mode().IsRegular() || !os.SameFile(info, openedInfo) {
		return nil, true, fmt.Errorf("private state changed while it was opened")
	}
	data, err := io.ReadAll(io.LimitReader(file, maxBytes+1))
	if err != nil {
		return nil, true, err
	}
	if int64(len(data)) > maxBytes {
		return nil, true, fmt.Errorf("private state exceeds %d bytes", maxBytes)
	}
	return data, true, nil
}

// WriteFile atomically writes one private state file and fsyncs the parent directory.
func WriteFile(stateDir, name string, data []byte) error {
	if name == "" || filepath.Base(name) != name {
		return fmt.Errorf("invalid private state filename")
	}
	if err := EnsureDir(stateDir); err != nil {
		return err
	}
	path := filepath.Join(stateDir, name)
	if info, err := os.Lstat(path); err == nil {
		if !info.Mode().IsRegular() || info.Mode()&os.ModeSymlink != 0 {
			return fmt.Errorf("private state target is not a regular file")
		}
	} else if !errors.Is(err, os.ErrNotExist) {
		return err
	}
	temporary, err := os.CreateTemp(stateDir, ".herdr-tandem-state-*.tmp")
	if err != nil {
		return err
	}
	temporaryPath := temporary.Name()
	cleanup := func() {
		_ = temporary.Close()
		_ = os.Remove(temporaryPath)
	}
	if err := temporary.Chmod(0o600); err != nil {
		cleanup()
		return err
	}
	if _, err := temporary.Write(data); err != nil {
		cleanup()
		return err
	}
	if err := temporary.Sync(); err != nil {
		cleanup()
		return err
	}
	if err := temporary.Close(); err != nil {
		_ = os.Remove(temporaryPath)
		return err
	}
	if err := os.Rename(temporaryPath, path); err != nil {
		_ = os.Remove(temporaryPath)
		return err
	}
	return SyncDir(stateDir)
}

// RemoveFile removes a fixed private-state filename and fsyncs the directory.
func RemoveFile(stateDir, name string) error {
	if name == "" || filepath.Base(name) != name {
		return fmt.Errorf("invalid private state filename")
	}
	path := filepath.Join(stateDir, name)
	if err := os.Remove(path); err != nil && !errors.Is(err, os.ErrNotExist) {
		return err
	}
	if _, err := os.Stat(stateDir); err == nil {
		return SyncDir(stateDir)
	}
	return nil
}

// SyncDir fsyncs a directory after a durable metadata change.
func SyncDir(path string) error {
	directory, err := os.Open(path)
	if err != nil {
		return fmt.Errorf("open herdr-tandem state directory for sync: %w", err)
	}
	defer directory.Close()
	if err := directory.Sync(); err != nil {
		return fmt.Errorf("sync herdr-tandem state directory: %w", err)
	}
	return nil
}

// HashedName creates a deterministic, filesystem-safe private-state filename.
func HashedName(prefix, identity, suffix string) string {
	sum := sha256.Sum256([]byte(identity))
	return fmt.Sprintf("%s-%s%s", prefix, hex.EncodeToString(sum[:8]), suffix)
}
