package securestate

import (
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"time"
)

const maxLockBytes = 4 << 10

// LockMetadata is intentionally non-secret and suitable for doctor diagnostics.
type LockMetadata struct {
	Version      int       `json:"version,omitempty"`
	PID          int       `json:"pid"`
	ProcessStart string    `json:"process_start,omitempty"`
	Subject      string    `json:"subject,omitempty"`
	Developer    string    `json:"developer,omitempty"` // reads legacy task locks
	OperationID  string    `json:"operation_id,omitempty"`
	OwnerToken   string    `json:"owner_token,omitempty"`
	StartedAt    time.Time `json:"started_at"`
}

func (m LockMetadata) owner() string {
	if m.Subject != "" {
		return m.Subject
	}
	return m.Developer
}

// LockOptions controls one private cross-process lock.
type LockOptions struct {
	Name        string
	Subject     string
	OperationID string
	Now         time.Time
	StaleGrace  time.Duration
	MaxLifetime time.Duration
}

// Lock owns one lock file until Release.
type Lock struct {
	path       string
	file       *os.File
	info       os.FileInfo
	ownerToken string
}

// Acquire creates a private lock or safely reclaims a provably stale one.
func Acquire(stateDir string, options LockOptions) (*Lock, error) {
	if options.Name == "" || filepath.Base(options.Name) != options.Name {
		return nil, fmt.Errorf("invalid lock filename")
	}
	if options.Subject == "" {
		return nil, fmt.Errorf("lock subject is required")
	}
	if options.Now.IsZero() {
		options.Now = time.Now()
	}
	if options.StaleGrace <= 0 {
		options.StaleGrace = 30 * time.Second
	}
	if options.MaxLifetime <= 0 {
		options.MaxLifetime = 24 * time.Hour
	}
	if err := EnsureDir(stateDir); err != nil {
		return nil, err
	}
	path := filepath.Join(stateDir, options.Name)
	for attempt := 0; attempt < 2; attempt++ {
		token, err := randomToken()
		if err != nil {
			return nil, fmt.Errorf("create lock owner token: %w", err)
		}
		// #nosec G304 -- the validated basename is joined below herdr-tandem's private state directory.
		file, err := os.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600)
		if err == nil {
			start, _ := processStartIdentity(os.Getpid())
			metadata := LockMetadata{
				Version:      1,
				PID:          os.Getpid(),
				ProcessStart: start,
				Subject:      options.Subject,
				OperationID:  options.OperationID,
				OwnerToken:   token,
				StartedAt:    options.Now.UTC(),
			}
			if encodeErr := json.NewEncoder(file).Encode(metadata); encodeErr != nil {
				_ = file.Close()
				_ = os.Remove(path)
				return nil, fmt.Errorf("write lock: %w", encodeErr)
			}
			if syncErr := file.Sync(); syncErr != nil {
				_ = file.Close()
				_ = os.Remove(path)
				return nil, fmt.Errorf("sync lock: %w", syncErr)
			}
			info, statErr := file.Stat()
			if statErr != nil {
				_ = file.Close()
				_ = os.Remove(path)
				return nil, fmt.Errorf("inspect lock: %w", statErr)
			}
			if syncErr := SyncDir(stateDir); syncErr != nil {
				_ = file.Close()
				_ = os.Remove(path)
				return nil, syncErr
			}
			return &Lock{path: path, file: file, info: info, ownerToken: token}, nil
		}
		if !errors.Is(err, os.ErrExist) {
			return nil, fmt.Errorf("create lock: %w", err)
		}
		stale, staleErr := IsStale(path, options.Subject, options.Now, options.StaleGrace, options.MaxLifetime)
		if staleErr != nil {
			return nil, staleErr
		}
		if !stale {
			return nil, fmt.Errorf("operation is busy; another herdr-tandem process owns %s", path)
		}
		if removeErr := os.Remove(path); removeErr != nil && !errors.Is(removeErr, os.ErrNotExist) {
			return nil, fmt.Errorf("remove stale lock: %w", removeErr)
		}
		if syncErr := SyncDir(stateDir); syncErr != nil {
			return nil, syncErr
		}
	}
	return nil, fmt.Errorf("could not acquire lock after stale-lock cleanup")
}

// IsStale reports true only when the current owner is provably dead, replaced, or implausible.
func IsStale(path, subject string, now time.Time, staleGrace, maxLifetime time.Duration) (bool, error) {
	data, exists, err := ReadFile(path, maxLockBytes)
	if err != nil {
		return false, fmt.Errorf("read existing lock: %w", err)
	}
	if !exists {
		return true, nil
	}
	var metadata LockMetadata
	decodeErr := json.Unmarshal(data, &metadata)
	if decodeErr == nil && metadata.owner() == subject && metadata.PID > 0 {
		age := now.Sub(metadata.StartedAt)
		if metadata.StartedAt.IsZero() || age < -staleGrace || age > maxLifetime {
			return true, nil
		}
		if !processAlive(metadata.PID) {
			return true, nil
		}
		if metadata.ProcessStart != "" {
			current, identityErr := processStartIdentity(metadata.PID)
			if identityErr == nil && current != metadata.ProcessStart {
				return true, nil
			}
		}
		return false, nil
	}
	info, statErr := os.Lstat(path)
	if statErr != nil {
		if errors.Is(statErr, os.ErrNotExist) {
			return true, nil
		}
		return false, fmt.Errorf("inspect existing lock: %w", statErr)
	}
	if now.Sub(info.ModTime()) >= staleGrace {
		return true, nil
	}
	return false, fmt.Errorf("lock is incomplete and too new to remove safely")
}

// Release removes the lock only when the path still identifies the file this process created.
func (l *Lock) Release() {
	if l == nil {
		return
	}
	_ = l.file.Close()
	current, err := os.Lstat(l.path)
	if err != nil || !os.SameFile(l.info, current) {
		return
	}
	data, exists, readErr := ReadFile(l.path, maxLockBytes)
	if readErr == nil && exists {
		var metadata LockMetadata
		if json.Unmarshal(data, &metadata) == nil && metadata.OwnerToken != "" && metadata.OwnerToken != l.ownerToken {
			return
		}
	}
	_ = os.Remove(l.path)
	_ = SyncDir(filepath.Dir(l.path))
}

func randomToken() (string, error) {
	var raw [16]byte
	if _, err := rand.Read(raw[:]); err != nil {
		return "", err
	}
	return hex.EncodeToString(raw[:]), nil
}
