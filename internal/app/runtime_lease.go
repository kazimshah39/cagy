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

	"github.com/kazimshah39/herdr-tandem/internal/developer"
	"github.com/kazimshah39/herdr-tandem/internal/herdr"
	"github.com/kazimshah39/herdr-tandem/internal/securestate"
	"github.com/kazimshah39/herdr-tandem/internal/supervisor"
)

const (
	runtimeRecordVersion = 4
	runtimeRecordsDir    = "runtimes"
	runtimeStateLockFile = "runtime-state.lock"
	maxRuntimeRecordSize = 32 << 10
)

type runtimeRecord struct {
	Version             int       `json:"version"`
	SupervisorKind      string    `json:"supervisor_kind"`
	DeveloperKind       string    `json:"developer_kind"`
	RuntimeID           string    `json:"runtime_id"`
	BuildRevision       string    `json:"build_revision,omitempty"`
	SidebarMode         string    `json:"sidebar_mode"`
	WorkspaceID         string    `json:"workspace_id"`
	SupervisorPaneID    string    `json:"supervisor_pane_id"`
	Developer           string    `json:"developer"`
	DeveloperPaneID     string    `json:"developer_pane_id,omitempty"`
	Project             string    `json:"project"`
	SupervisorModel     string    `json:"supervisor_model,omitempty"`
	DeveloperModel      string    `json:"developer_model,omitempty"`
	SupervisorAgentName string    `json:"supervisor_agent_name,omitempty"`
	CreatedAt           time.Time `json:"created_at"`
	UpdatedAt           time.Time `json:"updated_at"`
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
		return fmt.Errorf("acquire herdr-tandem runtime state: %w", err)
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
		return runtimeRecord{}, fmt.Errorf("decode herdr-tandem runtime record: %w", err)
	}
	var extra any
	if err := decoder.Decode(&extra); !errors.Is(err, io.EOF) {
		if err == nil {
			return runtimeRecord{}, errors.New("decode herdr-tandem runtime record: unexpected trailing JSON value")
		}
		return runtimeRecord{}, fmt.Errorf("decode herdr-tandem runtime record trailing data: %w", err)
	}
	if err := validateRuntimeRecord(record); err != nil {
		return runtimeRecord{}, fmt.Errorf("validate herdr-tandem runtime record: %w", err)
	}
	return record, nil
}
func validateRuntimeRecord(record runtimeRecord) error {
	if record.Version != runtimeRecordVersion {
		return fmt.Errorf("unsupported version %d", record.Version)
	}
	for name, value := range map[string]string{"supervisor kind": record.SupervisorKind, "developer kind": record.DeveloperKind, "runtime ID": record.RuntimeID, "workspace ID": record.WorkspaceID, "supervisor pane ID": record.SupervisorPaneID, "developer": record.Developer, "project": record.Project} {
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
	if record.SupervisorKind != supervisor.CodexID && record.SupervisorKind != supervisor.OpenCodeID && record.SupervisorKind != supervisor.AgyID {
		return fmt.Errorf("supervisor kind %q is unsupported", record.SupervisorKind)
	}
	if record.DeveloperKind != developer.AgyID {
		return fmt.Errorf("developer kind %q is unsupported", record.DeveloperKind)
	}
	if err := validateModelName(record.SupervisorModel); err != nil {
		return fmt.Errorf("supervisor model is invalid: %w", err)
	}
	if err := validateModelName(record.DeveloperModel); err != nil {
		return fmt.Errorf("developer model is invalid: %w", err)
	}
	if record.SupervisorModel != "" && record.SupervisorKind != supervisor.AgyID {
		return fmt.Errorf("supervisor model is only supported with %s supervisor", supervisor.AgyID)
	}
	if record.SupervisorAgentName != "" {
		expected := "herdr-tandem-" + record.RuntimeID
		if record.SupervisorAgentName != expected {
			return fmt.Errorf("supervisor agent name must be %q", expected)
		}
		if record.SupervisorKind != supervisor.AgyID {
			return fmt.Errorf("supervisor agent name is only supported with %s supervisor", supervisor.AgyID)
		}
	}
	if _, err := parseSidebarMode(record.SidebarMode); err != nil {
		return err
	}
	if filepath.Clean(record.Project) != record.Project || !filepath.IsAbs(record.Project) {
		return errors.New("project is invalid")
	}
	if record.CreatedAt.IsZero() || record.UpdatedAt.IsZero() || record.UpdatedAt.Before(record.CreatedAt) {
		return errors.New("timestamps are invalid")
	}
	return nil
}

func validateModelName(model string) error {
	if model == "" {
		return nil
	}
	if strings.HasPrefix(model, "-") {
		return errors.New("model cannot start with a dash")
	}
	if len(model) > 128 {
		return errors.New("model exceeds maximum length")
	}
	for _, r := range model {
		if r <= 32 || r == 127 || r == '\x00' {
			return errors.New("model contains invalid control characters or whitespace")
		}
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
		return fmt.Errorf("ensure herdr-tandem runtime directory: %w", err)
	}
	data, err := json.MarshalIndent(record, "", "  ")
	if err != nil {
		return fmt.Errorf("encode herdr-tandem runtime record: %w", err)
	}
	data = append(data, '\n')
	if len(data) > maxRuntimeRecordSize {
		return errors.New("herdr-tandem runtime record is too large")
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
func (m runtimeRecordManager) Prepare(ctx context.Context, record runtimeRecord) (runtimeRecord, error) {
	var prepared runtimeRecord
	err := m.withLock("runtime-prepare", func() error {
		if strings.TrimSpace(record.RuntimeID) == "" {
			if m.token == nil {
				return errors.New("runtime ID generator is unavailable")
			}
			id, err := m.token()
			if err != nil {
				return fmt.Errorf("create herdr-tandem runtime ID: %w", err)
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
				return fmt.Errorf("verify existing herdr-tandem runtime: %w", err)
			}
			if live.Live() {
				return errors.New("this Herdr pane already owns a herdr-tandem runtime; run herdr-tandem stop first")
			}
			if err := m.removeUnlocked(existing.RuntimeID); err != nil {
				return err
			}
		}
		if _, exists, err := m.loadByIDUnlocked(record.RuntimeID); err != nil {
			return err
		} else if exists {
			return errors.New("herdr-tandem runtime ID already exists; retry startup")
		}
		now := m.currentTime()
		record.Version = runtimeRecordVersion
		record.CreatedAt = now
		record.UpdatedAt = now
		record.Project = filepath.Clean(record.Project)
		if err := m.saveUnlocked(record); err != nil {
			return fmt.Errorf("save herdr-tandem runtime record: %w", err)
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
			return errors.New("herdr-tandem runtime record is missing")
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
func (m runtimeRecordManager) hasRuntimeRecordsUnlocked() (bool, error) {
	entries, err := os.ReadDir(m.recordsDir())
	if errors.Is(err, os.ErrNotExist) {
		return false, nil
	}
	if err != nil {
		return false, err
	}
	for _, entry := range entries {
		if !entry.IsDir() && strings.HasSuffix(entry.Name(), ".json") {
			return true, nil
		}
	}
	return false, nil
}

// RemoveAndClearViewIfLast serializes record removal with the final no-runtime
// check and the source-guarded Herdr view clear. This prevents a concurrent
// start from being left without Tandem's stable projection.
func (m runtimeRecordManager) RemoveAndClearViewIfLast(ctx context.Context, id string, clear func(context.Context) error) (bool, error) {
	clearAttempted := false
	err := m.withLock("runtime-remove-and-view-cleanup", func() error {
		if _, exists, err := m.loadByIDUnlocked(id); err != nil {
			return err
		} else if exists {
			if err := m.removeUnlocked(id); err != nil {
				return err
			}
		}
		remaining, err := m.hasRuntimeRecordsUnlocked()
		if err != nil || remaining || clear == nil {
			return err
		}
		clearAttempted = true
		return clear(ctx)
	})
	return clearAttempted, err
}

// Load supports diagnostics that inspect the current runtime scope.
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
				return errors.New("multiple herdr-tandem runtime records exist; select a supervisor-scoped runtime")
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
			if present && record.SupervisorKind == info.supervisorKind && record.DeveloperKind == info.developerKind && record.WorkspaceID == info.workspaceID && record.SupervisorPaneID == info.supervisor && record.Developer == info.developer && filepath.Clean(record.Project) == filepath.Clean(info.project) {
				if exists {
					return errors.New("multiple herdr-tandem runtime records match this supervisor")
				}
				found, exists = record, true
			}
		}
		return nil
	})
	return found, exists, err
}

func (m runtimeRecordManager) FindForSupervisorPane(workspaceID, supervisorPaneID, project string) (runtimeRecord, bool, error) {
	var found runtimeRecord
	var exists bool
	workspaceID = strings.TrimSpace(workspaceID)
	supervisorPaneID = strings.TrimSpace(supervisorPaneID)
	cleanProject := filepath.Clean(strings.TrimSpace(project))
	if workspaceID == "" || supervisorPaneID == "" || cleanProject == "" || cleanProject == "." {
		return runtimeRecord{}, false, errors.New("workspace ID, supervisor pane ID, and project are required")
	}
	err := m.withLock("runtime-find-pane", func() error {
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
			if present && record.WorkspaceID == workspaceID && record.SupervisorPaneID == supervisorPaneID && filepath.Clean(record.Project) == cleanProject {
				if exists {
					return errors.New("multiple herdr-tandem runtime records match this supervisor pane")
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
			return runtimeLiveness{}, errors.New("recorded herdr-tandem developer identity does not match Herdr")
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
			return runtimeLiveness{}, errors.New("recorded herdr-tandem developer pane does not match Herdr")
		}
		if pane.Tokens["herdr_tandem_owner"] != record.Developer || pane.Tokens["herdr_tandem_role"] != "developer" {
			return runtimeLiveness{}, errors.New("recorded herdr-tandem developer pane ownership is invalid")
		}
		if runtimeID := strings.TrimSpace(pane.Tokens["herdr_tandem_runtime_id"]); runtimeID != "" && runtimeID != record.RuntimeID {
			return runtimeLiveness{}, errors.New("recorded herdr-tandem runtime ID does not match Herdr")
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
	err := manager.withLock("runtime-cleanup", func() error {
		record, exists, err := manager.loadByIDUnlocked(id)
		if err != nil || !exists {
			return err
		}
		live, err := manager.livenessOf(ctx, record)
		if err != nil || live.Live() {
			return err
		}
		if err := manager.removeUnlocked(id); err != nil {
			return err
		}
		remaining, err := manager.hasRuntimeRecordsUnlocked()
		if err != nil || remaining || a.clearSidebarView == nil {
			return err
		}
		return a.clearSidebarView(ctx)
	})
	if err != nil {
		a.debugf("runtime cleanup id=%q ok=false error=%q", id, err)
	}
}
