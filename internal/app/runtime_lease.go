package app

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/kazimshah39/cagy/internal/herdr"
	"github.com/kazimshah39/cagy/internal/securestate"
)

const (
	runtimeRecordVersion = 2
	runtimeRecordsDir    = "runtimes"
	legacyRuntimeFile    = "runtime.json"
	runtimeStateLockFile = "runtime-state.lock"
	maxRuntimeRecordSize = 32 << 10
)

type runtimeRecord struct {
	Version          int       `json:"version"`
	RuntimeID        string    `json:"runtime_id"`
	BuildRevision    string    `json:"build_revision,omitempty"`
	WorkspaceID      string    `json:"workspace_id"`
	SupervisorPaneID string    `json:"supervisor_pane_id"`
	Developer        string    `json:"developer"`
	DeveloperPaneID  string    `json:"developer_pane_id,omitempty"`
	Project          string    `json:"project"`
	CreatedAt        time.Time `json:"created_at"`
	UpdatedAt        time.Time `json:"updated_at"`
}
type runtimeLiveness struct {
	AgentLive bool
	PaneLive  bool
}

func (l runtimeLiveness) Live() bool { return l.AgentLive || l.PaneLive }

type runtimeRecordManager struct {
	stateDir   string
	now        func() time.Time
	token      func() (string, error)
	liveness   func(context.Context, runtimeRecord) (runtimeLiveness, error)
	diagnostic func(string, ...any)
}

func (a *App) runtimeManager() runtimeRecordManager {
	return runtimeRecordManager{stateDir: a.stateDir, now: a.now, token: a.token, liveness: a.runtimeRecordLiveness, diagnostic: func(format string, args ...any) { a.debugf(format, args...) }}
}
func (m runtimeRecordManager) currentTime() time.Time {
	if m.now != nil {
		return m.now().UTC()
	}
	return time.Now().UTC()
}
func (m runtimeRecordManager) withLock(operation string, fn func() error) error {
	lock, err := securestate.Acquire(m.stateDir, securestate.LockOptions{Name: runtimeStateLockFile, Subject: "runtime-state", OperationID: operation, Now: m.currentTime()})
	if err != nil {
		return fmt.Errorf("acquire cagy runtime state: %w", err)
	}
	defer lock.Release()
	return fn()
}
func runtimeIDIsSafe(id string) bool {
	if id == "" || len(id) > 128 {
		return false
	}
	for _, r := range id {
		if !(r >= 'a' && r <= 'z' || r >= 'A' && r <= 'Z' || r >= '0' && r <= '9' || r == '-' || r == '_') {
			return false
		}
	}
	return true
}
func (m runtimeRecordManager) recordsDir() string {
	return filepath.Join(m.stateDir, runtimeRecordsDir)
}
func (m runtimeRecordManager) recordPath(id string) (string, error) {
	if !runtimeIDIsSafe(id) {
		return "", errors.New("runtime ID is invalid")
	}
	return filepath.Join(m.recordsDir(), id+".json"), nil
}
func decodeRuntimeRecord(data []byte) (runtimeRecord, error) {
	decoder := json.NewDecoder(bytes.NewReader(data))
	decoder.DisallowUnknownFields()
	var record runtimeRecord
	if err := decoder.Decode(&record); err != nil {
		return runtimeRecord{}, fmt.Errorf("decode cagy runtime record: %w", err)
	}
	var extra any
	if err := decoder.Decode(&extra); !errors.Is(err, io.EOF) {
		if err == nil {
			return runtimeRecord{}, errors.New("decode cagy runtime record: unexpected trailing JSON value")
		}
		return runtimeRecord{}, fmt.Errorf("decode cagy runtime record trailing data: %w", err)
	}
	if err := validateRuntimeRecord(record); err != nil {
		return runtimeRecord{}, fmt.Errorf("validate cagy runtime record: %w", err)
	}
	return record, nil
}
func validateRuntimeRecord(record runtimeRecord) error {
	if record.Version != runtimeRecordVersion {
		return fmt.Errorf("unsupported version %d", record.Version)
	}
	for name, value := range map[string]string{"runtime ID": record.RuntimeID, "workspace ID": record.WorkspaceID, "supervisor pane ID": record.SupervisorPaneID, "developer": record.Developer, "project": record.Project} {
		if strings.TrimSpace(value) == "" {
			return fmt.Errorf("%s is required", name)
		}
		if strings.ContainsAny(value, "\r\n\x00") {
			return fmt.Errorf("%s is invalid", name)
		}
	}
	if !runtimeIDIsSafe(record.RuntimeID) {
		return errors.New("runtime ID is invalid")
	}
	if filepath.Clean(record.Project) != record.Project || !filepath.IsAbs(record.Project) {
		return errors.New("project is invalid")
	}
	if record.CreatedAt.IsZero() || record.UpdatedAt.IsZero() || record.UpdatedAt.Before(record.CreatedAt) {
		return errors.New("timestamps are invalid")
	}
	return nil
}
func (m runtimeRecordManager) loadByIDUnlocked(id string) (runtimeRecord, bool, error) {
	path, err := m.recordPath(id)
	if err != nil {
		return runtimeRecord{}, false, err
	}
	data, exists, err := securestate.ReadFile(path, maxRuntimeRecordSize)
	if err != nil || !exists {
		return runtimeRecord{}, exists, err
	}
	record, err := decodeRuntimeRecord(data)
	return record, true, err
}
func (m runtimeRecordManager) saveUnlocked(record runtimeRecord) error {
	if err := validateRuntimeRecord(record); err != nil {
		return err
	}
	if err := securestate.EnsureDir(m.recordsDir()); err != nil {
		return fmt.Errorf("ensure cagy runtime directory: %w", err)
	}
	data, err := json.MarshalIndent(record, "", "  ")
	if err != nil {
		return fmt.Errorf("encode cagy runtime record: %w", err)
	}
	data = append(data, '\n')
	if len(data) > maxRuntimeRecordSize {
		return errors.New("cagy runtime record is too large")
	}
	return securestate.WriteFile(m.recordsDir(), record.RuntimeID+".json", data)
}
func (m runtimeRecordManager) removeUnlocked(id string) error {
	path, err := m.recordPath(id)
	if err != nil {
		return err
	}
	if err := os.Remove(path); err != nil && !errors.Is(err, os.ErrNotExist) {
		return err
	}
	return nil
}
func (m runtimeRecordManager) livenessOf(ctx context.Context, record runtimeRecord) (runtimeLiveness, error) {
	if m.liveness == nil {
		return runtimeLiveness{}, errors.New("runtime liveness check is unavailable")
	}
	return m.liveness(ctx, record)
}
func (m runtimeRecordManager) legacyRecordUnlocked() (runtimeRecord, bool, error) {
	data, exists, err := securestate.ReadFile(filepath.Join(m.stateDir, legacyRuntimeFile), maxRuntimeRecordSize)
	if err != nil || !exists {
		return runtimeRecord{}, exists, err
	}
	var legacy struct {
		Version          int       `json:"version"`
		RuntimeID        string    `json:"runtime_id"`
		BuildRevision    string    `json:"build_revision,omitempty"`
		WorkspaceID      string    `json:"workspace_id"`
		SupervisorPaneID string    `json:"supervisor_pane_id"`
		Developer        string    `json:"developer"`
		DeveloperPaneID  string    `json:"developer_pane_id,omitempty"`
		Project          string    `json:"project"`
		CreatedAt        time.Time `json:"created_at"`
		UpdatedAt        time.Time `json:"updated_at"`
	}
	if err := json.Unmarshal(data, &legacy); err != nil {
		return runtimeRecord{}, true, fmt.Errorf("decode legacy cagy runtime record: %w", err)
	}
	record := runtimeRecord{Version: runtimeRecordVersion, RuntimeID: legacy.RuntimeID, BuildRevision: legacy.BuildRevision, WorkspaceID: legacy.WorkspaceID, SupervisorPaneID: legacy.SupervisorPaneID, Developer: legacy.Developer, DeveloperPaneID: legacy.DeveloperPaneID, Project: legacy.Project, CreatedAt: legacy.CreatedAt, UpdatedAt: legacy.UpdatedAt}
	if !runtimeIDIsSafe(record.RuntimeID) {
		record.RuntimeID = "legacy-runtime"
	}
	if err := validateRuntimeRecord(record); err != nil {
		return runtimeRecord{}, true, fmt.Errorf("validate legacy cagy runtime record: %w", err)
	}
	return record, true, nil
}
func (m runtimeRecordManager) Prepare(ctx context.Context, record runtimeRecord) (runtimeRecord, error) {
	var prepared runtimeRecord
	err := m.withLock("runtime-prepare", func() error {
		legacy, exists, err := m.legacyRecordUnlocked()
		if err != nil {
			return err
		}
		if exists {
			live, err := m.livenessOf(ctx, legacy)
			if err != nil {
				return fmt.Errorf("verify legacy cagy runtime: %w", err)
			}
			if live.Live() {
				return errors.New("a legacy cagy session is still active; run cagy stop there before starting router-mode sessions")
			}
			if err := securestate.RemoveFile(m.stateDir, legacyRuntimeFile); err != nil {
				return fmt.Errorf("remove stale legacy cagy runtime record: %w", err)
			}
		}
		if strings.TrimSpace(record.RuntimeID) == "" {
			if m.token == nil {
				return errors.New("runtime ID generator is unavailable")
			}
			id, err := m.token()
			if err != nil {
				return fmt.Errorf("create cagy runtime ID: %w", err)
			}
			record.RuntimeID = id
		}
		entries, err := os.ReadDir(m.recordsDir())
		if err != nil && !errors.Is(err, os.ErrNotExist) {
			return err
		}
		for _, entry := range entries {
			if entry.IsDir() || !strings.HasSuffix(entry.Name(), ".json") {
				continue
			}
			id := strings.TrimSuffix(entry.Name(), ".json")
			existing, exists, err := m.loadByIDUnlocked(id)
			if err != nil {
				return err
			}
			if !exists || existing.WorkspaceID != record.WorkspaceID || existing.SupervisorPaneID != record.SupervisorPaneID {
				continue
			}
			live, err := m.livenessOf(ctx, existing)
			if err != nil {
				return fmt.Errorf("verify existing cagy runtime: %w", err)
			}
			if live.Live() {
				return errors.New("this Herdr pane already owns a cagy runtime; run cagy stop first")
			}
			if err := m.removeUnlocked(existing.RuntimeID); err != nil {
				return err
			}
		}
		if _, exists, err := m.loadByIDUnlocked(record.RuntimeID); err != nil {
			return err
		} else if exists {
			return errors.New("cagy runtime ID already exists; retry startup")
		}
		now := m.currentTime()
		record.Version = runtimeRecordVersion
		record.CreatedAt = now
		record.UpdatedAt = now
		record.Project = filepath.Clean(record.Project)
		if err := m.saveUnlocked(record); err != nil {
			return fmt.Errorf("save cagy runtime record: %w", err)
		}
		prepared = record
		return nil
	})
	return prepared, err
}
func (m runtimeRecordManager) Update(id string, update func(*runtimeRecord) error) (runtimeRecord, error) {
	var updated runtimeRecord
	err := m.withLock("runtime-update", func() error {
		record, exists, err := m.loadByIDUnlocked(id)
		if err != nil {
			return err
		}
		if !exists {
			return errors.New("cagy runtime record is missing")
		}
		if update != nil {
			if err := update(&record); err != nil {
				return err
			}
		}
		record.UpdatedAt = m.currentTime()
		if err := m.saveUnlocked(record); err != nil {
			return err
		}
		updated = record
		return nil
	})
	return updated, err
}
func (m runtimeRecordManager) Remove(id string) error {
	return m.withLock("runtime-remove", func() error {
		_, exists, err := m.loadByIDUnlocked(id)
		if err != nil {
			return err
		}
		if !exists {
			return nil
		}
		return m.removeUnlocked(id)
	})
}

// Load is retained for internal compatibility with single-runtime diagnostics.
// It returns an error when more than one runtime exists; production paths use
// FindForScope and are therefore safe for concurrent projects.
func (m runtimeRecordManager) Load() (runtimeRecord, bool, error) {
	var record runtimeRecord
	var found bool
	err := m.withLock("runtime-load", func() error {
		entries, err := os.ReadDir(m.recordsDir())
		if errors.Is(err, os.ErrNotExist) {
			return nil
		}
		if err != nil {
			return err
		}
		for _, entry := range entries {
			if entry.IsDir() || !strings.HasSuffix(entry.Name(), ".json") {
				continue
			}
			id := strings.TrimSuffix(entry.Name(), ".json")
			candidate, exists, err := m.loadByIDUnlocked(id)
			if err != nil {
				return err
			}
			if !exists {
				continue
			}
			if found {
				return errors.New("multiple cagy runtime records exist; select a supervisor-scoped runtime")
			}
			record, found = candidate, true
		}
		return nil
	})
	return record, found, err
}

func (m runtimeRecordManager) FindForScope(info runtimeContext) (runtimeRecord, bool, error) {
	var found runtimeRecord
	var exists bool
	err := m.withLock("runtime-find", func() error {
		entries, err := os.ReadDir(m.recordsDir())
		if errors.Is(err, os.ErrNotExist) {
			return nil
		}
		if err != nil {
			return err
		}
		for _, entry := range entries {
			if entry.IsDir() || !strings.HasSuffix(entry.Name(), ".json") {
				continue
			}
			id := strings.TrimSuffix(entry.Name(), ".json")
			record, present, err := m.loadByIDUnlocked(id)
			if err != nil {
				return err
			}
			if present && record.WorkspaceID == info.workspaceID && record.SupervisorPaneID == info.supervisor && record.Developer == info.developer && filepath.Clean(record.Project) == filepath.Clean(info.project) {
				if exists {
					return errors.New("multiple cagy runtime records match this supervisor")
				}
				found, exists = record, true
			}
		}
		return nil
	})
	return found, exists, err
}
func (a *App) runtimeRecordLiveness(ctx context.Context, record runtimeRecord) (runtimeLiveness, error) {
	var result runtimeLiveness
	agent, err := a.herdr.GetAgent(ctx, record.Developer)
	switch {
	case err == nil:
		if agent.WorkspaceID != record.WorkspaceID || (record.DeveloperPaneID != "" && agent.PaneID != record.DeveloperPaneID) {
			return runtimeLiveness{}, errors.New("recorded cagy developer identity does not match Herdr")
		}
		result.AgentLive = true
	case herdr.IsCode(err, "agent_not_found"):
	default:
		return runtimeLiveness{}, err
	}
	if record.DeveloperPaneID == "" {
		return result, nil
	}
	pane, err := a.herdr.GetPane(ctx, record.DeveloperPaneID)
	switch {
	case err == nil:
		project, projectErr := paneCWD(pane)
		if projectErr != nil {
			return runtimeLiveness{}, projectErr
		}
		if pane.WorkspaceID != record.WorkspaceID || project != filepath.Clean(record.Project) {
			return runtimeLiveness{}, errors.New("recorded cagy developer pane does not match Herdr")
		}
		if pane.Tokens["cagy_owner"] != record.Developer || pane.Tokens["cagy_role"] != "developer" {
			return runtimeLiveness{}, errors.New("recorded cagy developer pane ownership is invalid")
		}
		if runtimeID := strings.TrimSpace(pane.Tokens["cagy_runtime_id"]); runtimeID != "" && runtimeID != record.RuntimeID {
			return runtimeLiveness{}, errors.New("recorded cagy runtime ID does not match Herdr")
		}
		result.PaneLive = true
	case herdr.IsCode(err, "pane_not_found"):
	default:
		return runtimeLiveness{}, err
	}
	return result, nil
}
func (a *App) cleanupPreparedRuntime(ctx context.Context, id string) {
	manager := a.runtimeManager()
	_ = manager.withLock("runtime-cleanup", func() error {
		record, exists, err := manager.loadByIDUnlocked(id)
		if err != nil || !exists {
			return err
		}
		live, err := manager.livenessOf(ctx, record)
		if err != nil || live.Live() {
			return err
		}
		return manager.removeUnlocked(id)
	})
}
