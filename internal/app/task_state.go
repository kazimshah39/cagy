package app

import (
	"bytes"
	"context"
	"crypto/rand"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/kazimshah39/herdr-tandem/internal/herdr"
	"github.com/kazimshah39/herdr-tandem/internal/securestate"
	"github.com/kazimshah39/herdr-tandem/internal/transcript"
)

var errTaskAttention = errors.New("interrupted task needs attention")

const (
	taskJournalVersion  = 4
	maxTaskJournalBytes = 64 << 10
	maxTaskLockBytes    = 4 << 10
	staleLockGrace      = 30 * time.Second
	maxTaskLockLifetime = 24 * time.Hour
)

type taskPhase string

const (
	taskPhaseSubmitting taskPhase = "submitting"
	taskPhaseMonitoring taskPhase = "monitoring"
	taskPhaseBlocked    taskPhase = "blocked"
	taskPhaseCompleted  taskPhase = "completed_unacknowledged"
	taskPhaseUncertain  taskPhase = "uncertain"
)

type taskJournal struct {
	Version             int       `json:"version"`
	SupervisorKind      string    `json:"supervisor_kind"`
	DeveloperKind       string    `json:"developer_kind"`
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
	DeliveryReceipt     string    `json:"delivery_receipt,omitempty"`
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
	kind             taskInspectionKind
	response         string
	receipt          string
	message          string
	developerRunning bool
}

func defaultStateDir() string { return securestate.DefaultDir() }

func inspectPrivateStateDir(path string) (bool, error) { return securestate.InspectDir(path) }

func ensurePrivateStateDir(path string) error { return securestate.EnsureDir(path) }

func readPrivateStateFile(path string, maxBytes int64) ([]byte, bool, error) {
	return securestate.ReadFile(path, maxBytes)
}

func writePrivateStateFile(stateDir, name string, data []byte) error {
	return securestate.WriteFile(stateDir, name, data)
}

func stateFileName(prefix, developer, suffix string) string {
	return securestate.HashedName(prefix, developer, suffix)
}

func (a *App) taskJournalPath(developer string) string {
	return filepath.Join(a.stateDir, stateFileName("task", developer, ".json"))
}

func (a *App) loadTaskJournal(developer string) (taskJournal, bool, error) {
	a.debugf("task-journal load begin developer=%q", developer)
	exists, err := inspectPrivateStateDir(a.stateDir)
	if err != nil {
		a.debugf("task-journal load state-dir-error developer=%q error=%q", developer, err)
		return taskJournal{}, false, err
	}
	if !exists {
		a.debugf("task-journal load none developer=%q reason=%q", developer, "state-directory-missing")
		return taskJournal{}, false, nil
	}
	path := a.taskJournalPath(developer)
	data, fileExists, err := readPrivateStateFile(path, maxTaskJournalBytes)
	if err != nil {
		a.debugf("task-journal load read-error developer=%q exists=%t error=%q", developer, fileExists, err)
		return taskJournal{}, fileExists, fmt.Errorf("read interrupted-task state: %w", err)
	}
	if !fileExists {
		a.debugf("task-journal load none developer=%q reason=%q", developer, "journal-missing")
		return taskJournal{}, false, nil
	}
	decoder := json.NewDecoder(bytes.NewReader(data))
	decoder.DisallowUnknownFields()
	var record taskJournal
	if err := decoder.Decode(&record); err != nil {
		a.debugf("task-journal load corrupt developer=%q bytes=%d", developer, len(data))
		return taskJournal{}, true, fmt.Errorf("interrupted-task state is corrupt at %s", path)
	}
	var trailing any
	if err := decoder.Decode(&trailing); !errors.Is(err, io.EOF) {
		return taskJournal{}, true, fmt.Errorf("interrupted-task state has trailing data at %s", path)
	}
	a.debugf("task-journal load success developer=%q phase=%q task=%q has_session=%t receipt_present=%t", developer, record.Phase, debugHashPrefix(record.TaskHash), record.SessionID != "", record.DeliveryReceipt != "")
	return record, true, nil
}

func (a *App) writeTaskJournal(record taskJournal) error {
	a.debugf("task-journal write begin developer=%q phase=%q task=%q has_session=%t receipt_present=%t compact_offset=%d full_offset=%d", record.Developer, record.Phase, debugHashPrefix(record.TaskHash), record.SessionID != "", record.DeliveryReceipt != "", record.CompactOffset, record.FullOffset)
	record.Version = taskJournalVersion
	record.UpdatedAt = a.now().UTC()
	data, err := json.MarshalIndent(record, "", "  ")
	if err != nil {
		a.debugf("task-journal write encode-error developer=%q phase=%q error=%q", record.Developer, record.Phase, err)
		return fmt.Errorf("encode interrupted-task state: %w", err)
	}
	data = append(data, '\n')
	if len(data) > maxTaskJournalBytes {
		a.debugf("task-journal write too-large developer=%q bytes=%d", record.Developer, len(data))
		return fmt.Errorf("interrupted-task state exceeds %d bytes", maxTaskJournalBytes)
	}
	name := stateFileName("task", record.Developer, ".json")
	if err := writePrivateStateFile(a.stateDir, name, data); err != nil {
		a.debugf("task-journal write error developer=%q phase=%q error=%q", record.Developer, record.Phase, err)
		return fmt.Errorf("write interrupted-task state: %w", err)
	}
	a.debugf("task-journal write success developer=%q phase=%q task=%q bytes=%d", record.Developer, record.Phase, debugHashPrefix(record.TaskHash), len(data))
	return nil
}

func syncDirectory(path string) error { return securestate.SyncDir(path) }

func (a *App) removeTaskJournal(developer string) error {
	a.debugf("task-journal remove begin developer=%q", developer)
	path := a.taskJournalPath(developer)
	if err := os.Remove(path); err != nil && !errors.Is(err, os.ErrNotExist) {
		a.debugf("task-journal remove error developer=%q error=%q", developer, err)
		return fmt.Errorf("remove interrupted-task state: %w", err)
	}
	if _, err := os.Stat(a.stateDir); err == nil {
		if err := syncDirectory(a.stateDir); err != nil {
			a.debugf("task-journal remove sync-error developer=%q error=%q", developer, err)
			return err
		}
	}
	a.debugf("task-journal remove success developer=%q", developer)
	return nil
}

func (a *App) beginTaskTracking(info runtimeContext, developer herdr.AgentInfo, task string, checkpoint transcript.Checkpoint, phase taskPhase) error {
	a.debugf("task-tracking begin task=%q developer=%q pane=%q phase=%q", debugTaskFingerprint(task), info.developer, developer.PaneID, phase)
	now := a.now().UTC()
	record := taskJournal{
		Version:             taskJournalVersion,
		SupervisorKind:      info.supervisorKind,
		DeveloperKind:       info.developerKind,
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
	if phase == taskPhaseCompleted && record.DeliveryReceipt == "" {
		receipt, err := randomDeliveryReceipt()
		if err != nil {
			return err
		}
		record.DeliveryReceipt = receipt
	}
	if sessionID, err := a.exactDeveloperSessionID(developer); err == nil {
		record.SessionID = sessionID
	}
	if err := a.writeTaskJournal(record); err != nil {
		return err
	}
	a.taskStateMu.Lock()
	a.activeTask = &record
	a.taskStateMu.Unlock()
	a.debugf("task-tracking active task=%q developer=%q phase=%q", debugHashPrefix(record.TaskHash), record.Developer, record.Phase)
	return nil
}

func (a *App) replaceTrackedPrompt(developer herdr.AgentInfo, task string, checkpoint transcript.Checkpoint, phase taskPhase) error {
	a.taskStateMu.Lock()
	defer a.taskStateMu.Unlock()
	if a.activeTask == nil {
		a.debugf("task-tracking replace skipped task=%q reason=%q", debugTaskFingerprint(task), "no-active-task")
		return nil
	}
	record, exists, err := a.loadTaskJournal(a.activeTask.Developer)
	if err != nil {
		return err
	}
	if !exists {
		return fmt.Errorf("interrupted-task state disappeared while the task was running")
	}
	a.debugf("task-tracking replace task=%q developer=%q pane=%q from_phase=%q to_phase=%q", debugTaskFingerprint(task), record.Developer, developer.PaneID, record.Phase, phase)
	record.DeveloperPaneID = developer.PaneID
	record.TaskHash = transcript.TaskHash(task)
	record.CheckpointSessionID = checkpoint.SessionID
	record.CompactOffset = checkpoint.Offset
	record.FullOffset = checkpoint.FullOffset
	record.Phase = phase
	if sessionID, sessionErr := a.exactDeveloperSessionID(developer); sessionErr == nil {
		record.SessionID = sessionID
	}
	record.UpdatedAt = a.now().UTC()
	if err := a.writeTaskJournal(record); err != nil {
		return err
	}
	a.activeTask = &record
	return nil
}

func randomDeliveryReceipt() (string, error) {
	bytes := make([]byte, 16)
	if _, err := rand.Read(bytes); err != nil {
		return "", fmt.Errorf("create delivery receipt: %w", err)
	}
	return hex.EncodeToString(bytes), nil
}

func isValidDeliveryReceipt(receipt string) bool {
	if len(receipt) != 32 {
		return false
	}
	for i := 0; i < len(receipt); i++ {
		c := receipt[i]
		if !((c >= '0' && c <= '9') || (c >= 'a' && c <= 'f')) {
			return false
		}
	}
	return true
}

func (a *App) setTrackedPhase(phase taskPhase, developer herdr.AgentInfo) error {
	a.taskStateMu.Lock()
	defer a.taskStateMu.Unlock()
	if a.activeTask == nil {
		a.debugf("task-tracking phase skipped to=%q reason=%q", phase, "no-active-task")
		return nil
	}
	record, exists, err := a.loadTaskJournal(a.activeTask.Developer)
	if err != nil {
		return err
	}
	if !exists {
		return fmt.Errorf("interrupted-task state disappeared while the task was running")
	}
	if record.TaskHash != a.activeTask.TaskHash {
		return fmt.Errorf("interrupted-task state changed while the task was running")
	}
	previous := record.Phase
	record.Phase = phase
	if phase == taskPhaseCompleted && record.DeliveryReceipt == "" {
		receipt, receiptErr := randomDeliveryReceipt()
		if receiptErr != nil {
			return receiptErr
		}
		record.DeliveryReceipt = receipt
	}
	if developer.PaneID != "" {
		record.DeveloperPaneID = developer.PaneID
	}
	if sessionID, sessionErr := a.exactDeveloperSessionID(developer); sessionErr == nil {
		record.SessionID = sessionID
	}
	record.UpdatedAt = a.now().UTC()
	err = a.writeTaskJournal(record)
	if err == nil {
		a.activeTask = &record
	}
	a.debugf("task-tracking phase task=%q developer=%q from=%q to=%q ok=%t error=%q", debugHashPrefix(record.TaskHash), record.Developer, previous, phase, err == nil, err)
	return err
}

func (a *App) trackedTaskReceipt() string {
	a.taskStateMu.Lock()
	defer a.taskStateMu.Unlock()
	if a.activeTask == nil {
		return ""
	}
	return a.activeTask.DeliveryReceipt
}

func (a *App) trackedTaskPhase() taskPhase {
	a.taskStateMu.Lock()
	defer a.taskStateMu.Unlock()
	if a.activeTask == nil {
		return ""
	}
	return a.activeTask.Phase
}

func (a *App) trackedTaskDeveloper() string {
	a.taskStateMu.Lock()
	defer a.taskStateMu.Unlock()
	if a.activeTask == nil {
		return ""
	}
	return a.activeTask.Developer
}

func (a *App) clearTrackedTask(developer string) {
	a.taskStateMu.Lock()
	defer a.taskStateMu.Unlock()
	if a.activeTask != nil && a.activeTask.Developer == developer {
		a.activeTask = nil
	}
}

func (a *App) warnTrackedPhase(phase taskPhase, developer herdr.AgentInfo) {
	if err := a.setTrackedPhase(phase, developer); err != nil {
		fmt.Fprintf(a.stderr, "herdr-tandem warning: could not update interrupted-task state: %v\n", err)
	}
}

func validateTaskJournal(record taskJournal, info runtimeContext) error {
	if record.Phase == "recovering" {
		record.Phase = taskPhaseUncertain
	}
	if record.Version != taskJournalVersion {
		return fmt.Errorf("unsupported interrupted-task state version %d", record.Version)
	}
	if record.SupervisorKind != info.supervisorKind || record.DeveloperKind != info.developerKind {
		return fmt.Errorf("interrupted-task state uses another workflow profile")
	}
	if record.WorkspaceID != info.workspaceID || record.SupervisorPaneID != info.supervisor || record.Developer != info.developer {
		return fmt.Errorf("interrupted-task state belongs to another herdr-tandem session")
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
	if record.DeliveryReceipt != "" && !isValidDeliveryReceipt(record.DeliveryReceipt) {
		return fmt.Errorf("interrupted-task state has an invalid delivery receipt")
	}
	switch record.Phase {
	case taskPhaseSubmitting, taskPhaseMonitoring, taskPhaseBlocked, taskPhaseCompleted, taskPhaseUncertain:
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
	paths, err := transcript.PathsFor(a.transcriptRoot, transcript.Ref{
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

func (a *App) ensureCompletedReceipt(record *taskJournal) (string, error) {
	a.taskStateMu.Lock()
	defer a.taskStateMu.Unlock()
	a.debugf("task-receipt ensure begin developer=%q task=%q phase=%q receipt_present=%t", record.Developer, debugHashPrefix(record.TaskHash), record.Phase, record.DeliveryReceipt != "")
	receipt := record.DeliveryReceipt
	needsWrite := false

	if receipt == "" || !isValidDeliveryReceipt(receipt) {
		newReceipt, err := randomDeliveryReceipt()
		if err != nil {
			return "", err
		}
		receipt = newReceipt
		record.DeliveryReceipt = receipt
		needsWrite = true
	}

	if record.Version < taskJournalVersion {
		record.Version = taskJournalVersion
		needsWrite = true
	}
	if record.Phase != taskPhaseCompleted {
		record.Phase = taskPhaseCompleted
		needsWrite = true
	}

	if needsWrite {
		if err := a.writeTaskJournal(*record); err != nil {
			a.debugf("task-receipt ensure error developer=%q task=%q error=%q", record.Developer, debugHashPrefix(record.TaskHash), err)
			return "", fmt.Errorf("save delivery receipt for completed task: %w", err)
		}
	}
	if a.activeTask != nil && a.activeTask.Developer == record.Developer && a.activeTask.TaskHash == record.TaskHash {
		copy := *record
		a.activeTask = &copy
	}
	a.debugf("task-receipt ensure success developer=%q task=%q created=%t", record.Developer, debugHashPrefix(record.TaskHash), needsWrite)
	return receipt, nil
}

// inspectTaskJournal is side-effect-free: it reads state and returns an
// inspection result without writing to disk. If the record already has a
// persisted delivery receipt it is included unchanged. If the record is in
// taskPhaseCompleted but has no receipt, the receipt field of the returned
// taskInspection is empty. Callers in the locked delivery paths must call
// ensureCompletedReceipt to generate and persist a receipt before delivering.
// Legacy journal receipt migration also happens only in that persisting path.
func (a *App) inspectTaskJournal(ctx context.Context, info runtimeContext, record taskJournal) (inspection taskInspection, inspectErr error) {
	a.debugf("task-inspect begin developer=%q task=%q phase=%q pane=%q has_session=%t", record.Developer, debugHashPrefix(record.TaskHash), record.Phase, record.DeveloperPaneID, record.SessionID != "")
	defer func() {
		a.debugf("task-inspect end developer=%q task=%q kind=%q response_bytes=%d developer_running=%t ok=%t error=%q", record.Developer, debugHashPrefix(record.TaskHash), inspection.kind, len(inspection.response), inspection.developerRunning, inspectErr == nil, inspectErr)
	}()
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
		if liveSessionID, sessionErr := a.exactDeveloperSessionID(developer); sessionErr == nil {
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
		responseState, err = transcript.FinalResponseStateForHash(a.transcriptRoot, transcript.Ref{
			Source: "herdr:antigravity_cli",
			Agent:  "agy",
			Kind:   "id",
			Value:  sessionID,
		}, checkpoint, record.TaskHash)
		if err != nil {
			return taskInspection{}, fmt.Errorf("read interrupted-task response: %w", err)
		}
	}

	// Return the already-persisted delivery receipt without modification.
	// The locked delivery paths (recoverInterruptedTask, recoverTask) call
	// ensureCompletedReceipt to generate and persist a receipt before
	// delivering. Legacy journal migration also happens there.
	existingReceipt := record.DeliveryReceipt

	if developerErr != nil {
		if responseState.Found && (record.Phase == taskPhaseCompleted || record.Phase == taskPhaseUncertain) && !responseState.BackgroundPending && !responseState.AwaitingResponse {
			return taskInspection{kind: taskInspectionCompleted, response: responseState.Response, receipt: existingReceipt, message: "task completed, but the previous caller may not have received the final answer"}, nil
		}
		return taskInspection{kind: taskInspectionUncertain, message: "developer is no longer running; inspect the saved task state before forgetting it"}, nil
	}
	if responseState.Found && record.Phase == taskPhaseCompleted {
		return taskInspection{kind: taskInspectionCompleted, response: responseState.Response, receipt: existingReceipt, message: "task completed, but the previous caller may not have received the final answer"}, nil
	}
	if developer.AgentStatus == "blocked" {
		return taskInspection{kind: taskInspectionBlocked, message: "developer is blocked; check the right pane"}, nil
	}

	visible, visibleErr := a.herdr.ReadAgentVisible(ctx, info.developer, 80)
	if visibleErr != nil {
		return taskInspection{}, fmt.Errorf("read interrupted-task developer state: %w", visibleErr)
	}
	visibleState := agyVisibleState(visible)
	devRunning := visibleState == "working" || developer.AgentStatus == "working"
	if devRunning {
		return taskInspection{kind: taskInspectionRunning, developerRunning: true, message: "task is still running in the visible developer pane"}, nil
	}
	if responseState.BackgroundPending || responseState.AwaitingResponse {
		return taskInspection{kind: taskInspectionRunning, developerRunning: false, message: "task is still running in the visible developer pane"}, nil
	}
	if responseState.Found && visibleState == "idle" {
		return taskInspection{kind: taskInspectionCompleted, response: responseState.Response, receipt: existingReceipt, message: "task completed, but the previous caller may not have received the final answer"}, nil
	}
	if record.Phase == taskPhaseUncertain {
		return taskInspection{kind: taskInspectionUncertain, message: "the previous task ended without a complete matching response"}, nil
	}
	if record.Phase == taskPhaseBlocked {
		return taskInspection{kind: taskInspectionBlocked, message: "task was blocked; check the right pane"}, nil
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
	a.debugf("task-guard begin developer=%q", info.developer)
	record, exists, err := a.loadTaskJournal(info.developer)
	if err != nil {
		return err
	}
	if !exists {
		a.debugf("task-guard clear developer=%q", info.developer)
		return nil
	}
	inspection, err := a.inspectTaskJournal(ctx, info, record)
	if err != nil {
		a.debugf("task-guard invalid developer=%q task=%q error=%q", info.developer, debugHashPrefix(record.TaskHash), err)
		return fmt.Errorf("interrupted-task state is invalid: %w; run herdr-tandem doctor", err)
	}
	a.debugf("task-guard blocked developer=%q task=%q kind=%q", info.developer, debugHashPrefix(record.TaskHash), inspection.kind)
	switch inspection.kind {
	case taskInspectionCompleted:
		return fmt.Errorf("a previous task completed but its answer was not acknowledged; run: herdr-tandem ask --recover")
	case taskInspectionRunning:
		return fmt.Errorf("a previous task is still running; check the right pane or run herdr-tandem doctor")
	case taskInspectionBlocked:
		return fmt.Errorf("a previous task is blocked; check the right pane")
	default:
		return fmt.Errorf("a previous task has uncertain state: %s; run herdr-tandem doctor", inspection.message)
	}
}

func (a *App) recoverInterruptedTask(ctx context.Context) error {
	a.debugf("task-cli-recover begin")
	info, err := a.context()
	if err != nil {
		return err
	}
	runtimeRecord, mode, err := a.runtimeSidebarRecord(info)
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
		a.debugf("task-cli-recover none developer=%q", info.developer)
		return fmt.Errorf("there is no interrupted task to recover")
	}
	inspection, err := a.inspectTaskJournal(ctx, info, record)
	if err != nil {
		return err
	}
	if inspection.kind != taskInspectionCompleted {
		a.debugf("task-cli-recover unavailable developer=%q task=%q kind=%q", info.developer, debugHashPrefix(record.TaskHash), inspection.kind)
		return fmt.Errorf("interrupted task is not safely recoverable yet: %s", inspection.message)
	}
	// Persist the delivery receipt (and v1→v2 migration) before attempting
	// stdout write. If write fails, the caller can retry --recover with the
	// already-persisted receipt intact.
	receipt, err := a.ensureCompletedReceipt(&record)
	if err != nil {
		return fmt.Errorf("save delivery receipt before output: %w", err)
	}
	a.warnSidebarTransition(ctx, mode, info, runtimeRecord.DeveloperPaneID, sidebarRepresentativeSupervisor)
	if _, err := fmt.Fprintln(a.stdout, inspection.response); err != nil {
		return fmt.Errorf("write recovered agy response; retry herdr-tandem ask --recover: %w", err)
	}
	// Reload so acknowledgeLockedTask sees the persisted receipt on disk.
	updatedRecord, exists, loadErr := a.loadTaskJournal(info.developer)
	if loadErr != nil {
		return fmt.Errorf("agy answer was recovered, but durable task state could not be reloaded: %w; run herdr-tandem doctor", loadErr)
	}
	if !exists {
		return fmt.Errorf("agy answer was recovered, but durable task state disappeared before acknowledgment; run herdr-tandem doctor")
	}
	if err := a.acknowledgeLockedTask(updatedRecord, receipt, info); err != nil {
		return fmt.Errorf("agy answer was recovered, but durable task state could not be cleared; run herdr-tandem doctor: %w", err)
	}
	a.debugf("task-cli-recover success developer=%q task=%q response_bytes=%d", info.developer, debugHashPrefix(record.TaskHash), len(inspection.response))
	return nil
}

func (a *App) acknowledgeLockedTask(record taskJournal, receipt string, info runtimeContext) error {
	a.debugf("task-ack locked begin developer=%q task=%q phase=%q receipt_shape_valid=%t", info.developer, debugHashPrefix(record.TaskHash), record.Phase, isValidDeliveryReceipt(receipt))
	if err := validateTaskJournal(record, info); err != nil {
		return err
	}
	if record.Phase != taskPhaseCompleted {
		return fmt.Errorf("task is not completed (current phase: %s)", record.Phase)
	}
	if subtle.ConstantTimeCompare([]byte(record.DeliveryReceipt), []byte(receipt)) != 1 {
		a.debugf("task-ack locked mismatch developer=%q task=%q", info.developer, debugHashPrefix(record.TaskHash))
		return fmt.Errorf("invalid delivery receipt: receipt does not match the active completed task")
	}
	if err := a.removeTaskJournal(info.developer); err != nil {
		return fmt.Errorf("clear completed task state: %w", err)
	}
	a.clearTrackedTask(info.developer)
	a.debugf("task-ack locked success developer=%q task=%q", info.developer, debugHashPrefix(record.TaskHash))
	return nil
}

func (a *App) acknowledgeTask(ctx context.Context, receipt string) error {
	a.debugf("task-ack begin receipt_shape_valid=%t", isValidDeliveryReceipt(strings.TrimSpace(receipt)))
	receipt = strings.TrimSpace(receipt)
	if !isValidDeliveryReceipt(receipt) {
		return fmt.Errorf("invalid delivery receipt: must be 32 lowercase hexadecimal characters")
	}
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
		a.debugf("task-ack none developer=%q", info.developer)
		return fmt.Errorf("no unacknowledged task exists")
	}
	return a.acknowledgeLockedTask(record, receipt, info)
}

func (a *App) getTaskStatus(ctx context.Context) (*TaskStatusOutput, error) {
	a.debugf("task-status begin")
	info, err := a.context()
	if err != nil {
		return nil, err
	}
	record, exists, loadErr := a.loadTaskJournal(info.developer)
	if loadErr != nil {
		a.debugf("task-status journal-error developer=%q error=%q", info.developer, loadErr)
		fmt.Fprintf(a.stderr, "herdr-tandem warning: task journal is unreadable: %v\n", loadErr)
		return &TaskStatusOutput{
			Status:  "uncertain",
			Message: "task journal is unreadable; run herdr-tandem doctor",
		}, nil
	}
	if !exists {
		developer, err := a.herdr.GetAgent(ctx, info.developer)
		if err != nil {
			if herdr.IsCode(err, "agent_not_found") {
				a.debugf("task-status none developer=%q developer_running=false", info.developer)
				return &TaskStatusOutput{
					Status:  "none",
					Message: "no active task; developer is not running",
				}, nil
			}
			return nil, fmt.Errorf("check developer status: %w", err)
		}
		a.debugf("task-status none developer=%q agent_status=%q", info.developer, developer.AgentStatus)
		return &TaskStatusOutput{
			Status:  "none",
			Message: fmt.Sprintf("no active task; developer is %s", developer.AgentStatus),
		}, nil
	}

	inspection, err := a.inspectTaskJournal(ctx, info, record)
	if err != nil {
		a.debugf("task-status inspect-error developer=%q task=%q error=%q", info.developer, debugHashPrefix(record.TaskHash), err)
		fmt.Fprintf(a.stderr, "herdr-tandem warning: task state is invalid: %v\n", err)
		return &TaskStatusOutput{
			Status:  "uncertain",
			Message: "task state is invalid; run herdr-tandem doctor",
		}, nil
	}

	elapsed := ""
	age := time.Duration(0)
	if !record.StartedAt.IsZero() {
		age = a.now().Sub(record.StartedAt)
		if age < 0 {
			age = 0
		}
		elapsed = age.Round(time.Second).String()
	}
	deadline, _ := a.developerTaskTiming()
	if record.Phase == taskPhaseUncertain && inspection.kind != taskInspectionCompleted {
		a.debugf("task-status result developer=%q task=%q status=%q elapsed=%q journal_phase=%q", info.developer, debugHashPrefix(record.TaskHash), "uncertain", elapsed, record.Phase)
		return &TaskStatusOutput{
			Status:  "uncertain",
			Message: "background monitoring ended without a confirmed final response; check the right pane before forgetting the task",
			Elapsed: elapsed,
		}, nil
	}
	if inspection.kind == taskInspectionUncertain && (record.Phase == taskPhaseSubmitting || record.Phase == taskPhaseMonitoring) && age < deadline {
		status := "running"
		if record.Phase == taskPhaseSubmitting {
			status = "submitting"
		}
		a.debugf("task-status result developer=%q task=%q status=%q elapsed=%q transient=true", info.developer, debugHashPrefix(record.TaskHash), status, elapsed)
		return &TaskStatusOutput{
			Status:  status,
			Message: "task was submitted and background monitoring is still active",
			Elapsed: elapsed,
		}, nil
	}

	switch inspection.kind {
	case taskInspectionCompleted:
		a.debugf("task-status result developer=%q task=%q status=%q elapsed=%q", info.developer, debugHashPrefix(record.TaskHash), "completed_unacknowledged", elapsed)
		return &TaskStatusOutput{
			Status:                  "completed_unacknowledged",
			Message:                 inspection.message,
			Elapsed:                 elapsed,
			Recoverable:             true,
			AcknowledgementRequired: true,
		}, nil
	case taskInspectionRunning:
		status := "running"
		if record.Phase == taskPhaseSubmitting {
			status = "submitting"
		}
		a.debugf("task-status result developer=%q task=%q status=%q elapsed=%q", info.developer, debugHashPrefix(record.TaskHash), status, elapsed)
		return &TaskStatusOutput{
			Status:  status,
			Message: inspection.message,
			Elapsed: elapsed,
		}, nil
	case taskInspectionBlocked:
		a.debugf("task-status result developer=%q task=%q status=%q elapsed=%q", info.developer, debugHashPrefix(record.TaskHash), "blocked", elapsed)
		return &TaskStatusOutput{
			Status:  "blocked",
			Message: inspection.message,
			Elapsed: elapsed,
		}, nil
	default:
		a.debugf("task-status result developer=%q task=%q status=%q elapsed=%q", info.developer, debugHashPrefix(record.TaskHash), "uncertain", elapsed)
		return &TaskStatusOutput{
			Status:  "uncertain",
			Message: inspection.message,
			Elapsed: elapsed,
		}, nil
	}
}

func (a *App) recoverTask(ctx context.Context) (*RecoverTaskOutput, error) {
	a.debugf("task-recover begin")
	info, err := a.context()
	if err != nil {
		return nil, err
	}
	runtimeRecord, mode, err := a.runtimeSidebarRecord(info)
	if err != nil {
		return nil, err
	}
	lock, err := acquireLock(a.stateDir, info.developer, a.now())
	if err != nil {
		return nil, err
	}
	defer lock.release()

	record, exists, err := a.loadTaskJournal(info.developer)
	if err != nil {
		return nil, err
	}
	if !exists {
		a.debugf("task-recover none developer=%q", info.developer)
		return nil, fmt.Errorf("there is no interrupted task to recover")
	}
	inspection, err := a.inspectTaskJournal(ctx, info, record)
	if err != nil {
		return nil, err
	}
	if inspection.kind != taskInspectionCompleted {
		a.debugf("task-recover unavailable developer=%q task=%q kind=%q", info.developer, debugHashPrefix(record.TaskHash), inspection.kind)
		return nil, fmt.Errorf("interrupted task is not safely recoverable yet: %s", inspection.message)
	}
	// Persist receipt (and v1→v2 migration) before returning it to the caller.
	receipt, err := a.ensureCompletedReceipt(&record)
	if err != nil {
		return nil, fmt.Errorf("save delivery receipt before recovery: %w", err)
	}
	a.warnSidebarTransition(ctx, mode, info, runtimeRecord.DeveloperPaneID, sidebarRepresentativeSupervisor)
	a.debugf("task-recover success developer=%q task=%q response_bytes=%d", info.developer, debugHashPrefix(record.TaskHash), len(inspection.response))
	return &RecoverTaskOutput{
		Status:                  "completed_unacknowledged",
		Answer:                  inspection.response,
		Receipt:                 receipt,
		AcknowledgementRequired: true,
	}, nil
}

func (a *App) forgetTask(ctx context.Context, confirm bool) (*ForgetTaskOutput, error) {
	a.debugf("task-forget begin confirm=%t", confirm)
	if !confirm {
		return nil, fmt.Errorf("forgetting task state requires explicit confirm=true")
	}
	info, err := a.context()
	if err != nil {
		return nil, err
	}
	runtimeRecord, mode, err := a.runtimeSidebarRecord(info)
	if err != nil {
		return nil, err
	}
	lock, err := acquireLock(a.stateDir, info.developer, a.now())
	if err != nil {
		return nil, err
	}
	defer lock.release()

	record, exists, loadErr := a.loadTaskJournal(info.developer)
	if loadErr != nil {
		if !exists {
			return nil, loadErr
		}
		running, checkErr := a.interruptedTaskStillRunning(ctx, info)
		if checkErr != nil {
			return nil, fmt.Errorf("cannot safely forget unreadable interrupted-task state: %w", checkErr)
		}
		if running {
			return nil, fmt.Errorf("refusing to forget unreadable task state while the developer is still running")
		}
		if removeErr := a.removeTaskJournal(info.developer); removeErr != nil {
			return nil, fmt.Errorf("discard unreadable interrupted-task state: %w", removeErr)
		}
		a.clearTrackedTask(info.developer)
		a.warnSidebarTransition(ctx, mode, info, runtimeRecord.DeveloperPaneID, sidebarRepresentativeSupervisor)
		a.debugf("task-forget success developer=%q kind=%q", info.developer, "unreadable")
		return &ForgetTaskOutput{Status: "forgotten", Message: "forgot unreadable interrupted-task state"}, nil
	}
	if !exists {
		a.debugf("task-forget none developer=%q", info.developer)
		return nil, fmt.Errorf("there is no interrupted task state to forget")
	}
	inspection, inspectErr := a.inspectTaskJournal(ctx, info, record)
	if inspectErr != nil {
		running, checkErr := a.interruptedTaskStillRunning(ctx, info)
		if checkErr != nil {
			return nil, fmt.Errorf("cannot safely forget invalid interrupted-task state: %w", checkErr)
		}
		if running {
			return nil, fmt.Errorf("refusing to forget invalid task state while the developer is still running")
		}
	} else if inspection.kind == taskInspectionRunning {
		return nil, fmt.Errorf("refusing to forget a task that is still running")
	}
	if err := a.removeTaskJournal(info.developer); err != nil {
		return nil, err
	}
	a.clearTrackedTask(info.developer)
	a.warnSidebarTransition(ctx, mode, info, runtimeRecord.DeveloperPaneID, sidebarRepresentativeSupervisor)
	a.debugf("task-forget success developer=%q task=%q kind=%q", info.developer, debugHashPrefix(record.TaskHash), "normal")
	return &ForgetTaskOutput{Status: "forgotten", Message: "forgot interrupted-task state"}, nil
}

func (a *App) forgetInterruptedTask(ctx context.Context) error {
	out, err := a.forgetTask(ctx, true)
	if err != nil {
		return err
	}
	fmt.Fprintln(a.stdout, out.Message+"; no task was submitted")
	return nil
}

func (a *App) reportTaskJournal(ctx context.Context) error {
	a.debugf("task-report begin")
	info, err := a.context()
	if err != nil {
		return err
	}
	record, exists, err := a.loadTaskJournal(info.developer)
	if err != nil {
		return err
	}
	if !exists {
		a.debugf("task-report none developer=%q", info.developer)
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
		fmt.Fprintln(a.stdout, "! interrupted task state: completed but answer was not acknowledged; run: herdr-tandem ask --recover")
	case taskInspectionBlocked:
		fmt.Fprintln(a.stdout, "! interrupted task state: blocked; check the right pane")
	default:
		fmt.Fprintf(a.stdout, "! interrupted task state: uncertain: %s\n", inspection.message)
	}
	a.debugf("task-report attention developer=%q task=%q kind=%q age=%s", info.developer, debugHashPrefix(record.TaskHash), inspection.kind, age)
	return errTaskAttention
}

type taskLockMetadata = securestate.LockMetadata

type taskLock struct {
	lock *securestate.Lock
}

func acquireLock(stateDir, developer string, now time.Time) (*taskLock, error) {
	lock, err := securestate.Acquire(stateDir, securestate.LockOptions{
		Name:        stateFileName("lock", developer, ".json"),
		Subject:     developer,
		Now:         now,
		StaleGrace:  staleLockGrace,
		MaxLifetime: maxTaskLockLifetime,
	})
	if err != nil {
		if strings.Contains(err.Error(), "operation is busy") {
			return nil, fmt.Errorf("developer is busy; another herdr-tandem ask process owns %s", filepath.Join(stateDir, stateFileName("lock", developer, ".json")))
		}
		return nil, err
	}
	return &taskLock{lock: lock}, nil
}

func staleTaskLock(path, developer string, now time.Time) (bool, error) {
	return securestate.IsStale(path, developer, now, staleLockGrace, maxTaskLockLifetime)
}

func (l *taskLock) release() {
	if l != nil {
		l.lock.Release()
	}
}
