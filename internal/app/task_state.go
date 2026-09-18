package app

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"syscall"
	"time"

	"github.com/kazimshah39/cagy/internal/herdr"
	"github.com/kazimshah39/cagy/internal/transcript"
)

var errTaskAttention = errors.New("interrupted task needs attention")

const (
	taskJournalVersion  = 1
	maxTaskJournalBytes = 64 << 10
	maxTaskLockBytes    = 4 << 10
	staleLockGrace      = 30 * time.Second
	maxTaskLockLifetime = 24 * time.Hour
)

type taskPhase string

const (
	taskPhaseSubmitting taskPhase = "submitting"
	taskPhaseMonitoring taskPhase = "monitoring"
	taskPhaseRecovering taskPhase = "recovering"
	taskPhaseBlocked    taskPhase = "blocked"
	taskPhaseCompleted  taskPhase = "completed_unacknowledged"
	taskPhaseUncertain  taskPhase = "uncertain"
)

type taskJournal struct {
	Version             int       `json:"version"`
	WorkspaceID         string    `json:"workspace_id"`
	SupervisorPaneID    string    `json:"supervisor_pane_id"`
	Developer           string    `json:"developer"`
	DeveloperPaneID     string    `json:"developer_pane_id"`
	Project             string    `json:"project"`
	SessionID           string    `json:"session_id,omitempty"`
	CheckpointSessionID string    `json:"checkpoint_session_id,omitempty"`
	CompactOffset       int64     `json:"compact_offset"`
	FullOffset          int64     `json:"full_offset"`
	TaskHash            string    `json:"task_sha256"`
	Phase               taskPhase `json:"phase"`
	StartedAt           time.Time `json:"started_at"`
	UpdatedAt           time.Time `json:"updated_at"`
}

type taskInspectionKind string

const (
	taskInspectionRunning   taskInspectionKind = "running"
	taskInspectionCompleted taskInspectionKind = "completed"
	taskInspectionBlocked   taskInspectionKind = "blocked"
	taskInspectionUncertain taskInspectionKind = "uncertain"
)

type taskInspection struct {
	kind     taskInspectionKind
	response string
	message  string
}

func defaultStateDir() string {
	if stateHome := strings.TrimSpace(os.Getenv("XDG_STATE_HOME")); stateHome != "" && filepath.IsAbs(stateHome) {
		return filepath.Join(stateHome, "cagy")
	}
	configDir, err := os.UserConfigDir()
	if err == nil && strings.TrimSpace(configDir) != "" {
		return filepath.Join(configDir, "cagy", "state")
	}
	home, err := os.UserHomeDir()
	if err == nil && strings.TrimSpace(home) != "" {
		return filepath.Join(home, ".cagy", "state")
	}
	return ""
}

func inspectPrivateStateDir(path string) (bool, error) {
	if strings.TrimSpace(path) == "" {
		return false, fmt.Errorf("cagy state directory is unavailable")
	}
	info, err := os.Lstat(path)
	if errors.Is(err, os.ErrNotExist) {
		return false, nil
	}
	if err != nil {
		return false, fmt.Errorf("inspect cagy state directory: %w", err)
	}
	if !info.IsDir() || info.Mode()&os.ModeSymlink != 0 {
		return true, fmt.Errorf("cagy state path is not a private directory")
	}
	if info.Mode().Perm() != 0o700 {
		return true, fmt.Errorf("cagy state directory permissions are %04o, want 0700", info.Mode().Perm())
	}
	return true, nil
}

func ensurePrivateStateDir(path string) error {
	if strings.TrimSpace(path) == "" {
		return fmt.Errorf("cagy state directory is unavailable")
	}
	if err := os.MkdirAll(path, 0o700); err != nil {
		return fmt.Errorf("create cagy state directory: %w", err)
	}
	info, err := os.Lstat(path)
	if err != nil {
		return fmt.Errorf("inspect cagy state directory: %w", err)
	}
	if !info.IsDir() || info.Mode()&os.ModeSymlink != 0 {
		return fmt.Errorf("cagy state path is not a private directory")
	}
	if info.Mode().Perm() != 0o700 {
		if err := os.Chmod(path, 0o700); err != nil {
			return fmt.Errorf("secure cagy state directory: %w", err)
		}
	}
	return nil
}

func readPrivateStateFile(path string, maxBytes int64) ([]byte, bool, error) {
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
	// #nosec G304 -- callers pass fixed or SHA-256-derived names below the private cagy state directory.
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

func writePrivateStateFile(stateDir, name string, data []byte) error {
	if name == "" || filepath.Base(name) != name {
		return fmt.Errorf("invalid private state filename")
	}
	if err := ensurePrivateStateDir(stateDir); err != nil {
		return err
	}
	path := filepath.Join(stateDir, name)
	temporary, err := os.CreateTemp(stateDir, ".cagy-state-*.tmp")
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
	return syncDirectory(stateDir)
}

func stateFileName(prefix, developer, suffix string) string {
	sum := sha256.Sum256([]byte(developer))
	return fmt.Sprintf("%s-%s%s", prefix, hex.EncodeToString(sum[:8]), suffix)
}

func (a *App) taskJournalPath(developer string) string {
	return filepath.Join(a.stateDir, stateFileName("task", developer, ".json"))
}

func (a *App) loadTaskJournal(developer string) (taskJournal, bool, error) {
	exists, err := inspectPrivateStateDir(a.stateDir)
	if err != nil {
		return taskJournal{}, false, err
	}
	if !exists {
		return taskJournal{}, false, nil
	}
	path := a.taskJournalPath(developer)
	data, fileExists, err := readPrivateStateFile(path, maxTaskJournalBytes)
	if err != nil {
		return taskJournal{}, fileExists, fmt.Errorf("read interrupted-task state: %w", err)
	}
	if !fileExists {
		return taskJournal{}, false, nil
	}
	var record taskJournal
	if err := json.Unmarshal(data, &record); err != nil {
		return taskJournal{}, true, fmt.Errorf("interrupted-task state is corrupt at %s", path)
	}
	return record, true, nil
}

func (a *App) writeTaskJournal(record taskJournal) error {
	record.Version = taskJournalVersion
	record.UpdatedAt = a.now().UTC()
	data, err := json.MarshalIndent(record, "", "  ")
	if err != nil {
		return fmt.Errorf("encode interrupted-task state: %w", err)
	}
	data = append(data, '\n')
	if len(data) > maxTaskJournalBytes {
		return fmt.Errorf("interrupted-task state exceeds %d bytes", maxTaskJournalBytes)
	}
	name := stateFileName("task", record.Developer, ".json")
	if err := writePrivateStateFile(a.stateDir, name, data); err != nil {
		return fmt.Errorf("write interrupted-task state: %w", err)
	}
	return nil
}

func syncDirectory(path string) error {
	directory, err := os.Open(path)
	if err != nil {
		return fmt.Errorf("open cagy state directory for sync: %w", err)
	}
	defer directory.Close()
	if err := directory.Sync(); err != nil {
		return fmt.Errorf("sync cagy state directory: %w", err)
	}
	return nil
}

func (a *App) removeTaskJournal(developer string) error {
	path := a.taskJournalPath(developer)
	if err := os.Remove(path); err != nil && !errors.Is(err, os.ErrNotExist) {
		return fmt.Errorf("remove interrupted-task state: %w", err)
	}
	if _, err := os.Stat(a.stateDir); err == nil {
		return syncDirectory(a.stateDir)
	}
	return nil
}

func (a *App) beginTaskTracking(info runtimeContext, developer herdr.AgentInfo, task string, checkpoint transcript.Checkpoint, phase taskPhase) error {
	now := a.now().UTC()
	record := taskJournal{
		Version:             taskJournalVersion,
		WorkspaceID:         info.workspaceID,
		SupervisorPaneID:    info.supervisor,
		Developer:           info.developer,
		DeveloperPaneID:     developer.PaneID,
		Project:             filepath.Clean(info.project),
		CheckpointSessionID: checkpoint.SessionID,
		CompactOffset:       checkpoint.Offset,
		FullOffset:          checkpoint.FullOffset,
		TaskHash:            transcript.TaskHash(task),
		Phase:               phase,
		StartedAt:           now,
		UpdatedAt:           now,
	}
	if sessionID, err := exactAgySessionID(developer); err == nil {
		record.SessionID = sessionID
	}
	if err := a.writeTaskJournal(record); err != nil {
		return err
	}
	a.activeTask = &record
	return nil
}

func (a *App) replaceTrackedPrompt(developer herdr.AgentInfo, task string, checkpoint transcript.Checkpoint, phase taskPhase) error {
	if a.activeTask == nil {
		return nil
	}
	a.activeTask.DeveloperPaneID = developer.PaneID
	a.activeTask.TaskHash = transcript.TaskHash(task)
	a.activeTask.CheckpointSessionID = checkpoint.SessionID
	a.activeTask.CompactOffset = checkpoint.Offset
	a.activeTask.FullOffset = checkpoint.FullOffset
	a.activeTask.Phase = phase
	if sessionID, err := exactAgySessionID(developer); err == nil {
		a.activeTask.SessionID = sessionID
	}
	return a.writeTaskJournal(*a.activeTask)
}

func (a *App) setTrackedPhase(phase taskPhase, developer herdr.AgentInfo) error {
	if a.activeTask == nil {
		return nil
	}
	a.activeTask.Phase = phase
	if developer.PaneID != "" {
		a.activeTask.DeveloperPaneID = developer.PaneID
	}
	if sessionID, err := exactAgySessionID(developer); err == nil {
		a.activeTask.SessionID = sessionID
	}
	return a.writeTaskJournal(*a.activeTask)
}

func (a *App) warnTrackedPhase(phase taskPhase, developer herdr.AgentInfo) {
	if err := a.setTrackedPhase(phase, developer); err != nil {
		fmt.Fprintf(a.stderr, "cagy warning: could not update interrupted-task state: %v\n", err)
	}
}

func validateTaskJournal(record taskJournal, info runtimeContext) error {
	if record.Version != taskJournalVersion {
		return fmt.Errorf("unsupported interrupted-task state version %d", record.Version)
	}
	if record.WorkspaceID != info.workspaceID || record.SupervisorPaneID != info.supervisor || record.Developer != info.developer {
		return fmt.Errorf("interrupted-task state belongs to another cagy session")
	}
	if filepath.Clean(record.Project) != filepath.Clean(info.project) {
		return fmt.Errorf("interrupted-task state belongs to another project")
	}
	if record.DeveloperPaneID == "" {
		return fmt.Errorf("interrupted-task state has no developer pane")
	}
	if record.SessionID != "" && !agyConversationIDPattern.MatchString(record.SessionID) {
		return fmt.Errorf("interrupted-task state has an invalid conversation ID")
	}
	if record.CheckpointSessionID != "" && !agyConversationIDPattern.MatchString(record.CheckpointSessionID) {
		return fmt.Errorf("interrupted-task state has an invalid checkpoint conversation ID")
	}
	if record.CompactOffset < 0 || record.FullOffset < 0 {
		return fmt.Errorf("interrupted-task state has invalid transcript offsets")
	}
	if record.CheckpointSessionID == "" && (record.CompactOffset != 0 || record.FullOffset != 0) {
		return fmt.Errorf("interrupted-task state has offsets without a checkpoint conversation")
	}
	if len(record.TaskHash) != sha256.Size*2 {
		return fmt.Errorf("interrupted-task state has an invalid task hash")
	}
	if _, err := hex.DecodeString(record.TaskHash); err != nil {
		return fmt.Errorf("interrupted-task state has an invalid task hash")
	}
	switch record.Phase {
	case taskPhaseSubmitting, taskPhaseMonitoring, taskPhaseRecovering, taskPhaseBlocked, taskPhaseCompleted, taskPhaseUncertain:
	default:
		return fmt.Errorf("interrupted-task state has an invalid phase %q", record.Phase)
	}
	if record.StartedAt.IsZero() || record.UpdatedAt.IsZero() || record.UpdatedAt.Before(record.StartedAt) {
		return fmt.Errorf("interrupted-task state has invalid timestamps")
	}
	return nil
}

func (a *App) journalCheckpoint(record taskJournal, sessionID string) (transcript.Checkpoint, error) {
	if record.CheckpointSessionID == "" {
		return transcript.Checkpoint{}, nil
	}
	if record.CheckpointSessionID != sessionID {
		return transcript.Checkpoint{}, fmt.Errorf("interrupted-task transcript checkpoint belongs to another conversation")
	}
	paths, err := transcript.PathsFor(a.agyBrainRoot, transcript.Ref{
		Source: "herdr:antigravity_cli",
		Agent:  "agy",
		Kind:   "id",
		Value:  sessionID,
	})
	if err != nil {
		return transcript.Checkpoint{}, err
	}
	return transcript.Checkpoint{
		SessionID:  sessionID,
		Path:       paths.Compact,
		Offset:     record.CompactOffset,
		FullPath:   paths.Full,
		FullOffset: record.FullOffset,
	}, nil
}

func (a *App) inspectTaskJournal(ctx context.Context, info runtimeContext, record taskJournal) (taskInspection, error) {
	if err := validateTaskJournal(record, info); err != nil {
		return taskInspection{}, err
	}

	developer, developerErr := a.herdr.GetAgent(ctx, info.developer)
	if developerErr != nil && !herdr.IsCode(developerErr, "agent_not_found") {
		return taskInspection{}, fmt.Errorf("find interrupted-task developer: %w", developerErr)
	}
	if developerErr == nil {
		supervisor, err := a.supervisorPane(ctx, info)
		if err != nil {
			return taskInspection{}, err
		}
		if err := a.validateExistingDeveloper(ctx, developer, info, supervisor); err != nil {
			return taskInspection{}, err
		}
		if developer.PaneID != record.DeveloperPaneID {
			return taskInspection{}, fmt.Errorf("interrupted-task developer moved to another pane")
		}
	}

	sessionID := record.SessionID
	if developerErr == nil {
		if liveSessionID, sessionErr := exactAgySessionID(developer); sessionErr == nil {
			if sessionID != "" && sessionID != liveSessionID {
				return taskInspection{}, fmt.Errorf("interrupted-task conversation no longer matches the live developer")
			}
			sessionID = liveSessionID
		}
	}

	responseState := transcript.ResponseState{}
	if sessionID != "" {
		checkpoint, err := a.journalCheckpoint(record, sessionID)
		if err != nil {
			return taskInspection{}, fmt.Errorf("restore interrupted-task transcript checkpoint: %w", err)
		}
		responseState, err = transcript.FinalResponseStateForHash(a.agyBrainRoot, transcript.Ref{
			Source: "herdr:antigravity_cli",
			Agent:  "agy",
			Kind:   "id",
			Value:  sessionID,
		}, checkpoint, record.TaskHash)
		if err != nil {
			return taskInspection{}, fmt.Errorf("read interrupted-task response: %w", err)
		}
	}

	if developerErr != nil {
		if responseState.Found && record.Phase == taskPhaseCompleted {
			return taskInspection{kind: taskInspectionCompleted, response: responseState.Response, message: "task completed, but the previous caller may not have received the final answer"}, nil
		}
		return taskInspection{kind: taskInspectionUncertain, message: "developer is no longer running; inspect the saved task state before forgetting it"}, nil
	}
	if responseState.Found && record.Phase == taskPhaseCompleted {
		return taskInspection{kind: taskInspectionCompleted, response: responseState.Response, message: "task completed, but the previous caller may not have received the final answer"}, nil
	}
	if developer.AgentStatus == "blocked" {
		return taskInspection{kind: taskInspectionBlocked, message: "developer is blocked; check the right pane"}, nil
	}

	visible, visibleErr := a.herdr.ReadAgentVisible(ctx, info.developer, 80)
	if visibleErr != nil {
		return taskInspection{}, fmt.Errorf("read interrupted-task developer state: %w", visibleErr)
	}
	visibleState := agyVisibleState(visible)
	if visibleState == "working" || responseState.BackgroundPending || responseState.AwaitingResponse {
		return taskInspection{kind: taskInspectionRunning, message: "task is still running in the visible developer pane"}, nil
	}
	if responseState.Found && visibleState == "idle" {
		return taskInspection{kind: taskInspectionCompleted, response: responseState.Response, message: "task completed, but the previous caller may not have received the final answer"}, nil
	}
	if developer.AgentStatus == "working" {
		return taskInspection{kind: taskInspectionRunning, message: "task is still running in the visible developer pane"}, nil
	}
	if record.Phase == taskPhaseBlocked {
		return taskInspection{kind: taskInspectionBlocked, message: "task was blocked; check the right pane"}, nil
	}
	if record.Phase == taskPhaseRecovering {
		return taskInspection{kind: taskInspectionUncertain, message: "the previous caller stopped during quota recovery; inspect the recovery and developer panes"}, nil
	}
	if sessionID == "" {
		return taskInspection{kind: taskInspectionUncertain, message: "agy has not reported a conversation ID, so the result cannot be matched safely"}, nil
	}
	return taskInspection{kind: taskInspectionUncertain, message: "developer is no longer visibly working, but no complete matching final response is available"}, nil
}

func (a *App) interruptedTaskStillRunning(ctx context.Context, info runtimeContext) (bool, error) {
	developer, err := a.herdr.GetAgent(ctx, info.developer)
	if herdr.IsCode(err, "agent_not_found") {
		return false, nil
	}
	if err != nil {
		return false, fmt.Errorf("find interrupted-task developer: %w", err)
	}
	supervisor, err := a.supervisorPane(ctx, info)
	if err != nil {
		return false, err
	}
	if err := a.validateExistingDeveloper(ctx, developer, info, supervisor); err != nil {
		return false, err
	}
	visible, err := a.herdr.ReadAgentVisible(ctx, info.developer, 80)
	if err != nil {
		return false, fmt.Errorf("read interrupted-task developer state: %w", err)
	}
	return agyVisibleState(visible) == "working" || developer.AgentStatus == "working", nil
}

func (a *App) ensureNoInterruptedTask(ctx context.Context, info runtimeContext) error {
	record, exists, err := a.loadTaskJournal(info.developer)
	if err != nil {
		return err
	}
	if !exists {
		return nil
	}
	inspection, err := a.inspectTaskJournal(ctx, info, record)
	if err != nil {
		return fmt.Errorf("interrupted-task state is invalid: %w; run cagy doctor", err)
	}
	switch inspection.kind {
	case taskInspectionCompleted:
		return fmt.Errorf("a previous task completed but its answer was not acknowledged; run: cagy ask --recover")
	case taskInspectionRunning:
		return fmt.Errorf("a previous task is still running; check the right pane or run cagy doctor")
	case taskInspectionBlocked:
		return fmt.Errorf("a previous task is blocked; check the right pane")
	default:
		return fmt.Errorf("a previous task has uncertain state: %s; run cagy doctor", inspection.message)
	}
}

func (a *App) recoverInterruptedTask(ctx context.Context) error {
	info, err := a.context()
	if err != nil {
		return err
	}
	lock, err := acquireLock(a.stateDir, info.developer, a.now())
	if err != nil {
		return err
	}
	defer lock.release()
	record, exists, err := a.loadTaskJournal(info.developer)
	if err != nil {
		return err
	}
	if !exists {
		return fmt.Errorf("there is no interrupted task to recover")
	}
	inspection, err := a.inspectTaskJournal(ctx, info, record)
	if err != nil {
		return err
	}
	if inspection.kind != taskInspectionCompleted {
		return fmt.Errorf("interrupted task is not safely recoverable yet: %s", inspection.message)
	}
	if _, err := fmt.Fprintln(a.stdout, inspection.response); err != nil {
		return fmt.Errorf("write recovered agy response; retry cagy ask --recover: %w", err)
	}
	if err := a.removeTaskJournal(info.developer); err != nil {
		return fmt.Errorf("agy answer was recovered, but durable task state could not be cleared; run cagy doctor: %w", err)
	}
	return nil
}

func (a *App) forgetInterruptedTask(ctx context.Context) error {
	info, err := a.context()
	if err != nil {
		return err
	}
	lock, err := acquireLock(a.stateDir, info.developer, a.now())
	if err != nil {
		return err
	}
	defer lock.release()
	record, exists, loadErr := a.loadTaskJournal(info.developer)
	if loadErr != nil {
		if !exists {
			return loadErr
		}
		running, checkErr := a.interruptedTaskStillRunning(ctx, info)
		if checkErr != nil {
			return fmt.Errorf("cannot safely forget unreadable interrupted-task state: %w", checkErr)
		}
		if running {
			return fmt.Errorf("refusing to forget unreadable task state while the developer is still running")
		}
		if removeErr := a.removeTaskJournal(info.developer); removeErr != nil {
			return fmt.Errorf("discard unreadable interrupted-task state: %w", removeErr)
		}
		fmt.Fprintln(a.stdout, "forgot unreadable interrupted-task state; no task was submitted")
		return nil
	}
	if !exists {
		return fmt.Errorf("there is no interrupted task state to forget")
	}
	inspection, inspectErr := a.inspectTaskJournal(ctx, info, record)
	if inspectErr != nil {
		running, checkErr := a.interruptedTaskStillRunning(ctx, info)
		if checkErr != nil {
			return fmt.Errorf("cannot safely forget invalid interrupted-task state: %w", checkErr)
		}
		if running {
			return fmt.Errorf("refusing to forget invalid task state while the developer is still running")
		}
	} else if inspection.kind == taskInspectionRunning {
		return fmt.Errorf("refusing to forget a task that is still running")
	}
	if err := a.removeTaskJournal(info.developer); err != nil {
		return err
	}
	fmt.Fprintln(a.stdout, "forgot interrupted-task state; no task was submitted")
	return nil
}

func (a *App) reportTaskJournal(ctx context.Context) error {
	info, err := a.context()
	if err != nil {
		return err
	}
	record, exists, err := a.loadTaskJournal(info.developer)
	if err != nil {
		return err
	}
	if !exists {
		fmt.Fprintln(a.stdout, "✓ interrupted task state: none")
		return nil
	}
	inspection, err := a.inspectTaskJournal(ctx, info, record)
	if err != nil {
		return err
	}
	age := a.now().Sub(record.StartedAt).Round(time.Second)
	if age < 0 {
		age = 0
	}
	switch inspection.kind {
	case taskInspectionRunning:
		fmt.Fprintf(a.stdout, "! interrupted task state: still running (%s elapsed); check the right pane\n", age)
	case taskInspectionCompleted:
		fmt.Fprintln(a.stdout, "! interrupted task state: completed but answer was not acknowledged; run: cagy ask --recover")
	case taskInspectionBlocked:
		fmt.Fprintln(a.stdout, "! interrupted task state: blocked; check the right pane")
	default:
		fmt.Fprintf(a.stdout, "! interrupted task state: uncertain: %s\n", inspection.message)
	}
	return errTaskAttention
}

type taskLockMetadata struct {
	PID       int       `json:"pid"`
	Developer string    `json:"developer"`
	StartedAt time.Time `json:"started_at"`
}

type taskLock struct {
	path string
	file *os.File
	info os.FileInfo
}

func acquireLock(stateDir, developer string, now time.Time) (*taskLock, error) {
	if err := ensurePrivateStateDir(stateDir); err != nil {
		return nil, err
	}
	path := filepath.Join(stateDir, stateFileName("lock", developer, ".json"))
	for attempt := 0; attempt < 2; attempt++ {
		// #nosec G304 -- path is below cagy's private state directory and uses a SHA-256 filename.
		file, err := os.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600)
		if err == nil {
			metadata := taskLockMetadata{PID: os.Getpid(), Developer: developer, StartedAt: now.UTC()}
			if encodeErr := json.NewEncoder(file).Encode(metadata); encodeErr != nil {
				_ = file.Close()
				_ = os.Remove(path)
				return nil, fmt.Errorf("write task lock: %w", encodeErr)
			}
			if syncErr := file.Sync(); syncErr != nil {
				_ = file.Close()
				_ = os.Remove(path)
				return nil, fmt.Errorf("sync task lock: %w", syncErr)
			}
			info, statErr := file.Stat()
			if statErr != nil {
				_ = file.Close()
				_ = os.Remove(path)
				return nil, fmt.Errorf("inspect task lock: %w", statErr)
			}
			if syncErr := syncDirectory(stateDir); syncErr != nil {
				_ = file.Close()
				_ = os.Remove(path)
				return nil, syncErr
			}
			return &taskLock{path: path, file: file, info: info}, nil
		}
		if !errors.Is(err, os.ErrExist) {
			return nil, fmt.Errorf("create task lock: %w", err)
		}
		stale, staleErr := staleTaskLock(path, developer, now)
		if staleErr != nil {
			return nil, staleErr
		}
		if !stale {
			return nil, fmt.Errorf("developer is busy; another cagy ask process owns %s", path)
		}
		if removeErr := os.Remove(path); removeErr != nil && !errors.Is(removeErr, os.ErrNotExist) {
			return nil, fmt.Errorf("remove stale task lock: %w", removeErr)
		}
		if syncErr := syncDirectory(stateDir); syncErr != nil {
			return nil, syncErr
		}
	}
	return nil, fmt.Errorf("could not acquire task lock after stale-lock cleanup")
}

func staleTaskLock(path, developer string, now time.Time) (bool, error) {
	data, exists, err := readPrivateStateFile(path, maxTaskLockBytes)
	if err != nil {
		return false, fmt.Errorf("read existing task lock: %w", err)
	}
	if !exists {
		return true, nil
	}
	var metadata taskLockMetadata
	decodeErr := json.Unmarshal(data, &metadata)
	if decodeErr == nil && metadata.Developer == developer && metadata.PID > 0 {
		age := now.Sub(metadata.StartedAt)
		if metadata.StartedAt.IsZero() || age < -staleLockGrace || age > maxTaskLockLifetime {
			return true, nil
		}
		return !processAlive(metadata.PID), nil
	}
	info, statErr := os.Lstat(path)
	if statErr != nil {
		if errors.Is(statErr, os.ErrNotExist) {
			return true, nil
		}
		return false, fmt.Errorf("inspect existing task lock: %w", statErr)
	}
	if now.Sub(info.ModTime()) >= staleLockGrace {
		return true, nil
	}
	return false, fmt.Errorf("developer task lock is incomplete and too new to remove safely")
}

func processAlive(pid int) bool {
	process, err := os.FindProcess(pid)
	if err != nil {
		return false
	}
	err = process.Signal(syscall.Signal(0))
	return err == nil || errors.Is(err, syscall.EPERM)
}

func (l *taskLock) release() {
	if l == nil {
		return
	}
	_ = l.file.Close()
	current, err := os.Lstat(l.path)
	if err != nil || !os.SameFile(l.info, current) {
		return
	}
	_ = os.Remove(l.path)
	_ = syncDirectory(filepath.Dir(l.path))
}
