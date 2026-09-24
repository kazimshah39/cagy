package app

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/kazimshah39/herdr-tandem/internal/herdr"
	"github.com/kazimshah39/herdr-tandem/internal/transcript"
)

const taskTestConversationID = "9feb8bfd-bcb6-4ff3-a06b-61164f0c2a35"

func TestTaskJournalPersistsOnlyHashAndOffsets(t *testing.T) {
	stateDir := filepath.Join(t.TempDir(), "state")
	app := New(fakeRunner{}, nil, nil)
	app.stateDir = stateDir
	app.now = func() time.Time { return time.Date(2026, 9, 20, 12, 0, 0, 0, time.UTC) }
	info := runtimeContext{supervisorKind: "codex", developerKind: "agy", workspaceID: "w1", supervisor: "w1:p1", developer: "developer", project: filepath.Clean(t.TempDir())}
	developer := herdr.AgentInfo{
		Agent: "agy", PaneID: "w1:p2",
		AgentSession: &herdr.AgentSessionInfo{Source: "herdr:antigravity_cli", Agent: "agy", Kind: "id", Value: taskTestConversationID},
	}
	task := "sensitive task text must never be stored"
	checkpoint := transcript.Checkpoint{SessionID: taskTestConversationID, Offset: 123, FullOffset: 456}
	if err := app.beginTaskTracking(info, developer, task, checkpoint, taskPhaseSubmitting); err != nil {
		t.Fatal(err)
	}
	path := app.taskJournalPath(info.developer)
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(data), task) {
		t.Fatal("task plaintext was written to the journal")
	}
	if !strings.Contains(string(data), transcript.TaskHash(task)) {
		t.Fatal("task hash was not written to the journal")
	}
	if info, err := os.Stat(path); err != nil || info.Mode().Perm() != 0o600 {
		t.Fatalf("journal mode=%v err=%v", info.Mode().Perm(), err)
	}
	record, exists, err := app.loadTaskJournal(info.developer)
	if err != nil || !exists {
		t.Fatalf("exists=%t err=%v", exists, err)
	}
	if record.CompactOffset != 123 || record.FullOffset != 456 || record.SessionID != taskTestConversationID {
		t.Fatalf("record=%+v", record)
	}
}

func TestTaskJournalRejectsUnknownMetadata(t *testing.T) {
	stateDir := t.TempDir()
	app := New(fakeRunner{}, nil, nil)
	app.stateDir = stateDir
	developer := "developer"
	journal := `{"version":4,"supervisor_kind":"codex","developer_kind":"agy","workspace_id":"w1","supervisor_pane_id":"w1:p1","developer":"developer","developer_pane_id":"w1:p2","project":"/tmp/project","task_sha256":"abc","phase":"uncertain","legacy_field":true,"started_at":"2026-09-20T11:00:00Z","updated_at":"2026-09-20T12:00:00Z"}`
	if err := writePrivateStateFile(stateDir, stateFileName("task", developer, ".json"), []byte(journal)); err != nil {
		t.Fatal(err)
	}
	if _, _, err := app.loadTaskJournal(developer); err == nil {
		t.Fatal("unknown task metadata was accepted")
	}
}

func TestTaskLockRejectsConcurrentOwnerAndReleases(t *testing.T) {
	stateDir := t.TempDir()
	now := time.Date(2026, 9, 20, 12, 0, 0, 0, time.UTC)
	first, err := acquireLock(stateDir, "developer", now)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := acquireLock(stateDir, "developer", now); err == nil || !strings.Contains(err.Error(), "developer is busy") {
		first.release()
		t.Fatalf("second lock error=%v", err)
	}
	first.release()
	second, err := acquireLock(stateDir, "developer", now.Add(time.Second))
	if err != nil {
		t.Fatalf("released lock was not reusable: %v", err)
	}
	second.release()
}

func TestReadTaskInputPreservesLiteralShellText(t *testing.T) {
	input := "  run `echo no` && $(touch /tmp/never) with 'quotes' and 🚀\n"
	got, err := readTaskInput(strings.NewReader(input))
	if err != nil {
		t.Fatal(err)
	}
	want := strings.TrimSpace(input)
	if got != want {
		t.Fatalf("task=%q want=%q", got, want)
	}
	if err := validateTaskString(got); err != nil {
		t.Fatal(err)
	}
}

func TestTrackedCompletionPreservesAlreadyPersistedRecoveryReceipt(t *testing.T) {
	app := New(fakeRunner{}, nil, nil)
	app.stateDir = t.TempDir()
	info := runtimeContext{supervisorKind: "codex", developerKind: "agy", workspaceID: "w1", supervisor: "w1:p1", developer: "developer", project: filepath.Clean(t.TempDir())}
	developer := herdr.AgentInfo{
		Agent: "agy", PaneID: "w1:p2",
		AgentSession: &herdr.AgentSessionInfo{Source: "herdr:antigravity_cli", Agent: "agy", Kind: "id", Value: taskTestConversationID},
	}
	if err := app.beginTaskTracking(info, developer, "receipt race task", transcript.Checkpoint{SessionID: taskTestConversationID}, taskPhaseMonitoring); err != nil {
		t.Fatal(err)
	}
	record, exists, err := app.loadTaskJournal(info.developer)
	if err != nil || !exists {
		t.Fatalf("load record exists=%t err=%v", exists, err)
	}
	receipt, err := app.ensureCompletedReceipt(&record)
	if err != nil {
		t.Fatal(err)
	}
	if err := app.setTrackedPhase(taskPhaseCompleted, developer); err != nil {
		t.Fatal(err)
	}
	updated, exists, err := app.loadTaskJournal(info.developer)
	if err != nil || !exists {
		t.Fatalf("reload record exists=%t err=%v", exists, err)
	}
	if updated.DeliveryReceipt != receipt {
		t.Fatalf("receipt changed during finalization: got %q want %q", updated.DeliveryReceipt, receipt)
	}
}

func TestValidateTaskString(t *testing.T) {
	tests := []struct {
		name    string
		task    string
		wantErr string
	}{
		{
			name:    "empty string rejected",
			task:    "",
			wantErr: "task cannot be empty",
		},
		{
			name:    "whitespace-only string rejected",
			task:    "   \t\r\n   ",
			wantErr: "task cannot be empty",
		},
		{
			name:    "valid task accepted",
			task:    "run unit tests",
			wantErr: "",
		},
		{
			name:    "exactly 1 MiB nonblank task accepted",
			task:    strings.Repeat("a", maxTaskInputBytes),
			wantErr: "",
		},
		{
			name:    "1 MiB plus one byte rejected",
			task:    strings.Repeat("a", maxTaskInputBytes+1),
			wantErr: "task exceeds 1 MiB",
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			err := validateTaskString(tc.task)
			if tc.wantErr != "" {
				if err == nil {
					t.Fatalf("%s: expected error %q, got nil", tc.name, tc.wantErr)
				}
				if err.Error() != tc.wantErr {
					t.Fatalf("%s: got error %q, want %q", tc.name, err.Error(), tc.wantErr)
				}
				return
			}
			if err != nil {
				t.Fatalf("%s: unexpected error: %v", tc.name, err)
			}
		})
	}
}
