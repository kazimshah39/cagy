package app

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/kazimshah39/cagy/internal/herdr"
	"github.com/kazimshah39/cagy/internal/transcript"
)

const routerTestConversationID = "9feb8bfd-bcb6-4ff3-a06b-61164f0c2a35"

func TestTaskJournalPersistsOnlyHashAndOffsets(t *testing.T) {
	stateDir := filepath.Join(t.TempDir(), "state")
	app := New(fakeRunner{}, nil, nil)
	app.stateDir = stateDir
	app.now = func() time.Time { return time.Date(2026, 9, 20, 12, 0, 0, 0, time.UTC) }
	info := runtimeContext{workspaceID: "w1", supervisor: "w1:p1", developer: "developer", project: filepath.Clean(t.TempDir())}
	developer := herdr.AgentInfo{
		Agent: "agy", PaneID: "w1:p2",
		AgentSession: &herdr.AgentSessionInfo{Source: "herdr:antigravity_cli", Agent: "agy", Kind: "id", Value: routerTestConversationID},
	}
	task := "sensitive task text must never be stored"
	checkpoint := transcript.Checkpoint{SessionID: routerTestConversationID, Offset: 123, FullOffset: 456}
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
	if record.CompactOffset != 123 || record.FullOffset != 456 || record.SessionID != routerTestConversationID {
		t.Fatalf("record=%+v", record)
	}
}

func TestLegacyQuotaJournalFieldsRemainReadableDuringMigration(t *testing.T) {
	stateDir := t.TempDir()
	app := New(fakeRunner{}, nil, nil)
	app.stateDir = stateDir
	developer := "legacy-developer"
	journal := `{
  "version": 3,
  "workspace_id": "w1",
  "supervisor_pane_id": "w1:p1",
  "developer": "legacy-developer",
  "developer_pane_id": "w1:p2",
  "project": "/tmp/project",
  "session_id": "9feb8bfd-bcb6-4ff3-a06b-61164f0c2a35",
  "checkpoint_session_id": "9feb8bfd-bcb6-4ff3-a06b-61164f0c2a35",
  "compact_offset": 10,
  "full_offset": 20,
  "task_sha256": "6e96cb42ca077227d1e29e4f65f91b1cb20e6522d314761781d7c1da22c1fc41",
  "phase": "uncertain",
  "failure_code": "quota_accounts_unavailable",
  "failure_at": "2026-09-20T12:00:00Z",
  "started_at": "2026-09-20T11:00:00Z",
  "updated_at": "2026-09-20T12:00:00Z"
}`
	if err := writePrivateStateFile(stateDir, stateFileName("task", developer, ".json"), []byte(journal)); err != nil {
		t.Fatal(err)
	}
	record, exists, err := app.loadTaskJournal(developer)
	if err != nil || !exists {
		t.Fatalf("exists=%t err=%v", exists, err)
	}
	info := runtimeContext{workspaceID: "w1", supervisor: "w1:p1", developer: developer, project: "/tmp/project"}
	if err := validateTaskJournal(record, info); err != nil {
		t.Fatalf("legacy journal rejected: %v", err)
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
