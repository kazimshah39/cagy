package app

import (
	"context"
	"crypto/subtle"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/kazimshah39/cagy/internal/herdr"
	"github.com/kazimshah39/cagy/internal/transcript"
)

func TestTaskJournalIsPrivateAtomicAndDoesNotPersistTaskText(t *testing.T) {
	stateDir := filepath.Join(t.TempDir(), "private-state")
	now := time.Date(2026, time.September, 18, 12, 0, 0, 0, time.UTC)
	app := New(&scriptedRunner{t: t}, &strings.Builder{}, &strings.Builder{})
	app.stateDir = stateDir
	app.now = func() time.Time { return now }
	info := runtimeContext{workspaceID: "w1", supervisor: "w1:p1", developer: "developer", project: "/tmp/project"}
	developer := herdr.AgentInfo{
		Agent:        "agy",
		PaneID:       "w1:p2",
		WorkspaceID:  "w1",
		TabID:        "w1:t1",
		AgentSession: &herdr.AgentSessionInfo{Source: "herdr:antigravity_cli", Agent: "agy", Kind: "id", Value: testConversationID},
	}
	task := "Use `safe shell text` and never expose secret-looking prompt content."
	checkpoint := transcript.Checkpoint{SessionID: testConversationID, Offset: 12, FullOffset: 34}

	if err := app.beginTaskTracking(info, developer, task, checkpoint, taskPhaseSubmitting); err != nil {
		t.Fatal(err)
	}
	path := app.taskJournalPath(info.developer)
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(data), task) || strings.Contains(string(data), "safe shell text") {
		t.Fatalf("journal persisted task plaintext: %s", data)
	}
	if !strings.Contains(string(data), transcript.TaskHash(task)) {
		t.Fatalf("journal does not contain expected task hash: %s", data)
	}
	fileInfo, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	if got := fileInfo.Mode().Perm(); got != 0o600 {
		t.Fatalf("journal mode=%o want=600", got)
	}
	dirInfo, err := os.Stat(stateDir)
	if err != nil {
		t.Fatal(err)
	}
	if got := dirInfo.Mode().Perm(); got != 0o700 {
		t.Fatalf("state dir mode=%o want=700", got)
	}
	record, exists, err := app.loadTaskJournal(info.developer)
	if err != nil || !exists {
		t.Fatalf("load record exists=%v err=%v", exists, err)
	}
	if record.TaskHash != transcript.TaskHash(task) || record.CompactOffset != 12 || record.FullOffset != 34 {
		t.Fatalf("record=%+v", record)
	}
}

func TestInterruptedTaskCanBeRecoveredFromExactTranscriptHash(t *testing.T) {
	project, _ := filepath.EvalSymlinks(t.TempDir())
	stateDir := t.TempDir()
	brainRoot := t.TempDir()
	developerName := developerName("w1", "w1:p1")
	task := "Finish the exact interrupted task"
	writeAgyTranscript(t, brainRoot, testConversationID, task, "Recovered final answer.")

	runner := &scriptedRunner{t: t, steps: []runStep{
		{want: []string{"herdr", "agent", "get", developerName}, result: agentJSONWithSession("w1:p2", "w1", project, "idle", testConversationID)},
		{want: []string{"herdr", "pane", "get", "w1:p1"}, result: supervisorPaneJSON(project)},
		{want: []string{"herdr", "pane", "get", "w1:p2"}, result: paneJSON("w1:p2", "w1:t1", project, developerPaneLabel, map[string]string{"cagy_owner": developerName, "cagy_role": "developer"})},
		{want: []string{"herdr", "agent", "read", developerName, "--source", "visible", "--lines", "80"}, result: textResult(">\n────────────────────\n? for shortcuts\n")},
	}}
	var stdout strings.Builder
	app := New(runner, &stdout, &strings.Builder{})
	app.stateDir = stateDir
	app.agyBrainRoot = brainRoot
	app.getenv = cagyEnv(project, developerName)
	info, err := app.context()
	if err != nil {
		t.Fatal(err)
	}
	developer := herdr.AgentInfo{Agent: "agy", PaneID: "w1:p2", WorkspaceID: "w1", TabID: "w1:t1", ForegroundCWD: project, AgentSession: &herdr.AgentSessionInfo{Source: "herdr:antigravity_cli", Agent: "agy", Kind: "id", Value: testConversationID}}
	if err := app.beginTaskTracking(info, developer, task, transcript.Checkpoint{SessionID: testConversationID}, taskPhaseMonitoring); err != nil {
		t.Fatal(err)
	}
	app.activeTask = nil // Simulate a new process after the original caller vanished.

	if err := app.recoverInterruptedTask(context.Background()); err != nil {
		t.Fatal(err)
	}
	runner.assertDone()
	if stdout.String() != "Recovered final answer.\n" {
		t.Fatalf("stdout=%q", stdout.String())
	}
	if _, err := os.Stat(app.taskJournalPath(developerName)); !os.IsNotExist(err) {
		t.Fatalf("journal still exists after recovery: %v", err)
	}
}

func TestInterruptedRunningTaskBlocksDuplicateSubmission(t *testing.T) {
	project, _ := filepath.EvalSymlinks(t.TempDir())
	developerName := developerName("w1", "w1:p1")
	stateDir := t.TempDir()
	runner := &scriptedRunner{t: t, steps: []runStep{
		{want: []string{"herdr", "agent", "get", developerName}, result: agentJSONWithSession("w1:p2", "w1", project, "working", testConversationID)},
		{want: []string{"herdr", "pane", "get", "w1:p1"}, result: supervisorPaneJSON(project)},
		{want: []string{"herdr", "pane", "get", "w1:p2"}, result: paneJSON("w1:p2", "w1:t1", project, developerPaneLabel, map[string]string{"cagy_owner": developerName, "cagy_role": "developer"})},
		{want: []string{"herdr", "agent", "read", developerName, "--source", "visible", "--lines", "80"}, result: textResult("work in progress\n────────────────────\nesc to cancel\n")},
	}}
	app := New(runner, &strings.Builder{}, &strings.Builder{})
	app.stateDir = stateDir
	app.getenv = cagyEnv(project, developerName)
	info, err := app.context()
	if err != nil {
		t.Fatal(err)
	}
	developer := herdr.AgentInfo{Agent: "agy", PaneID: "w1:p2", WorkspaceID: "w1", TabID: "w1:t1", ForegroundCWD: project, AgentSession: &herdr.AgentSessionInfo{Source: "herdr:antigravity_cli", Agent: "agy", Kind: "id", Value: testConversationID}}
	if err := app.beginTaskTracking(info, developer, "old task", transcript.Checkpoint{SessionID: testConversationID}, taskPhaseMonitoring); err != nil {
		t.Fatal(err)
	}
	app.activeTask = nil

	err = app.ask(context.Background(), "new task must not be sent")
	if err == nil || !strings.Contains(err.Error(), "previous task is still running") {
		t.Fatalf("error=%v", err)
	}
	runner.assertDone()
	for _, call := range runner.calls {
		if hasPrefix(call, "herdr", "agent", "prompt") {
			t.Fatalf("duplicate task was submitted: %v", call)
		}
	}
}

func TestTaskLockReclaimsDeadOwnerAndRejectsLiveOwner(t *testing.T) {
	stateDir := t.TempDir()
	now := time.Date(2026, time.September, 18, 12, 0, 0, 0, time.UTC)
	developer := "developer"
	path := filepath.Join(stateDir, stateFileName("lock", developer, ".json"))
	dead := taskLockMetadata{PID: 99999999, Developer: developer, StartedAt: now.Add(-time.Minute)}
	data, _ := json.Marshal(dead)
	if err := os.WriteFile(path, data, 0o600); err != nil {
		t.Fatal(err)
	}
	lock, err := acquireLock(stateDir, developer, now)
	if err != nil {
		t.Fatalf("reclaim dead lock: %v", err)
	}
	lock.release()

	live := taskLockMetadata{PID: os.Getpid(), Developer: developer, StartedAt: now}
	data, _ = json.Marshal(live)
	if err := os.WriteFile(path, data, 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := acquireLock(stateDir, developer, now); err == nil || !strings.Contains(err.Error(), "developer is busy") {
		t.Fatalf("live lock error=%v", err)
	}
}

func TestTaskLockReclaimsImplausiblyOldLivePID(t *testing.T) {
	stateDir := t.TempDir()
	if err := ensurePrivateStateDir(stateDir); err != nil {
		t.Fatal(err)
	}
	now := time.Now().UTC()
	developer := "developer"
	path := filepath.Join(stateDir, stateFileName("lock", developer, ".json"))
	metadata := taskLockMetadata{PID: os.Getpid(), Developer: developer, StartedAt: now.Add(-maxTaskLockLifetime - time.Second)}
	data, err := json.Marshal(metadata)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, data, 0o600); err != nil {
		t.Fatal(err)
	}
	lock, err := acquireLock(stateDir, developer, now)
	if err != nil {
		t.Fatalf("old PID-reuse lock was not reclaimed: %v", err)
	}
	lock.release()
}

func TestForgetRemovesCorruptJournalButNotRunningTask(t *testing.T) {
	project, _ := filepath.EvalSymlinks(t.TempDir())
	developerName := developerName("w1", "w1:p1")
	stateDir := t.TempDir()
	var stdout strings.Builder
	runner := &scriptedRunner{t: t, steps: []runStep{
		{want: []string{"herdr", "agent", "get", developerName}, result: jsonError("agent_not_found", "missing")},
	}}
	app := New(runner, &stdout, &strings.Builder{})
	app.stateDir = stateDir
	app.getenv = cagyEnv(project, developerName)
	if err := ensurePrivateStateDir(stateDir); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(app.taskJournalPath(developerName), []byte("not-json"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := app.forgetInterruptedTask(context.Background()); err != nil {
		t.Fatal(err)
	}
	runner.assertDone()
	if !strings.Contains(stdout.String(), "forgot unreadable") {
		t.Fatalf("stdout=%q", stdout.String())
	}
	if _, err := os.Stat(app.taskJournalPath(developerName)); !os.IsNotExist(err) {
		t.Fatalf("corrupt journal still exists: %v", err)
	}
}

func TestReadTaskInputPreservesShellMetacharactersWithoutExecution(t *testing.T) {
	input := "Use `doctor` and $HOME literally; keep \"quotes\" and 'apostrophes'.\n"
	got, err := readTaskInput(strings.NewReader(input))
	if err != nil {
		t.Fatal(err)
	}
	if got != strings.TrimSpace(input) {
		t.Fatalf("task=%q", got)
	}
	if _, err := readTaskInput(strings.NewReader(" \n\t")); err == nil {
		t.Fatal("expected empty stdin to fail")
	}
	tooLarge := strings.NewReader(strings.Repeat("x", maxTaskInputBytes+1))
	if _, err := readTaskInput(tooLarge); err == nil || !strings.Contains(err.Error(), "exceeds 1 MiB") {
		t.Fatalf("large input error=%v", err)
	}
}

func TestPromptTransportFailureKeepsDurableUncertainState(t *testing.T) {
	project, _ := filepath.EvalSymlinks(t.TempDir())
	developerName := developerName("w1", "w1:p1")
	task := "task whose caller disappears"
	runner := &scriptedRunner{t: t, steps: []runStep{
		{want: []string{"herdr", "pane", "get", "w1:p1"}, result: supervisorPaneJSON(project)},
		{want: []string{"herdr", "agent", "get", developerName}, result: agentJSONWithSession("w1:p2", "w1", project, "idle", testConversationID)},
		{want: []string{"herdr", "pane", "get", "w1:p2"}, result: paneJSON("w1:p2", "w1:t1", project, developerPaneLabel, map[string]string{"cagy_owner": developerName, "cagy_role": "developer"})},
		{want: agyProbeArgs("/model"), result: agyModelResult("gemini-3.8-flash-high", "Gemini 3.8 Flash (High)")},
		{want: agyProbeArgs("/quota"), result: agyQuotaResult("Gemini Models", 0.8, 0.9)},
		{want: []string{"herdr", "agent", "read", developerName, "--source", "recent-unwrapped", "--lines", "400"}, result: textResult("old\n")},
		{want: []string{"herdr", "agent", "prompt", developerName, task, "--wait", "--timeout", "30000"}, result: jsonError("transport_closed", "caller disappeared")},
		{want: []string{"herdr", "agent", "read", developerName, "--source", "recent-unwrapped", "--lines", "400"}, result: textResult("old\n")},
	}}
	var stderr strings.Builder
	app := New(runner, &strings.Builder{}, &stderr)
	app.stateDir = t.TempDir()
	app.agyBrainRoot = t.TempDir()
	app.getenv = cagyEnv(project, developerName)

	err := app.ask(context.Background(), task)
	if err == nil || !strings.Contains(err.Error(), "run cagy doctor") {
		t.Fatalf("error=%v", err)
	}
	runner.assertDone()
	if !strings.Contains(stderr.String(), "submitting one task") {
		t.Fatalf("stderr=%q", stderr.String())
	}
	record, exists, loadErr := app.loadTaskJournal(developerName)
	if loadErr != nil || !exists {
		t.Fatalf("journal exists=%v err=%v", exists, loadErr)
	}
	if record.Phase != taskPhaseUncertain || record.TaskHash != transcript.TaskHash(task) {
		t.Fatalf("record=%+v", record)
	}
	if strings.Contains(string(mustReadFile(t, app.taskJournalPath(developerName))), task) {
		t.Fatal("task plaintext leaked into journal")
	}
}

func mustReadFile(t *testing.T, path string) []byte {
	t.Helper()
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	return data
}

type alwaysFailWriter struct{}

func (alwaysFailWriter) Write([]byte) (int, error) {
	return 0, errors.New("output closed")
}

func validTaskJournal(info runtimeContext) taskJournal {
	started := time.Date(2026, time.September, 18, 12, 0, 0, 0, time.UTC)
	return taskJournal{
		Version:             taskJournalVersion,
		WorkspaceID:         info.workspaceID,
		SupervisorPaneID:    info.supervisor,
		Developer:           info.developer,
		DeveloperPaneID:     "w1:p2",
		Project:             info.project,
		SessionID:           testConversationID,
		CheckpointSessionID: testConversationID,
		CompactOffset:       0,
		FullOffset:          0,
		TaskHash:            transcript.TaskHash("task"),
		Phase:               taskPhaseMonitoring,
		StartedAt:           started,
		UpdatedAt:           started,
	}
}

func TestLoadTaskJournalRejectsUnsafeOrOversizedFiles(t *testing.T) {
	developer := "developer"
	tests := []struct {
		name  string
		setup func(t *testing.T, path string)
	}{
		{
			name: "symlink",
			setup: func(t *testing.T, path string) {
				target := filepath.Join(t.TempDir(), "target")
				if err := os.WriteFile(target, []byte(`{"version":1}`), 0o600); err != nil {
					t.Fatal(err)
				}
				if err := os.Symlink(target, path); err != nil {
					t.Skipf("symlinks unavailable: %v", err)
				}
			},
		},
		{
			name: "directory",
			setup: func(t *testing.T, path string) {
				if err := os.Mkdir(path, 0o700); err != nil {
					t.Fatal(err)
				}
			},
		},
		{
			name: "world readable",
			setup: func(t *testing.T, path string) {
				if err := os.WriteFile(path, []byte(`{"version":1}`), 0o600); err != nil {
					t.Fatal(err)
				}
				if err := os.Chmod(path, 0o644); err != nil {
					t.Fatal(err)
				}
			},
		},
		{
			name: "oversized",
			setup: func(t *testing.T, path string) {
				if err := os.WriteFile(path, []byte(strings.Repeat("x", maxTaskJournalBytes+1)), 0o600); err != nil {
					t.Fatal(err)
				}
			},
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			app := New(&scriptedRunner{t: t}, &strings.Builder{}, &strings.Builder{})
			app.stateDir = t.TempDir()
			if err := ensurePrivateStateDir(app.stateDir); err != nil {
				t.Fatal(err)
			}
			test.setup(t, app.taskJournalPath(developer))
			_, exists, err := app.loadTaskJournal(developer)
			if err == nil || !exists {
				t.Fatalf("exists=%v err=%v", exists, err)
			}
		})
	}
}

func TestValidateTaskJournalRejectsInvalidIdentityAndMetadata(t *testing.T) {
	info := runtimeContext{workspaceID: "w1", supervisor: "w1:p1", developer: "developer", project: "/tmp/project"}
	tests := []struct {
		name   string
		mutate func(*taskJournal)
	}{
		{name: "version", mutate: func(r *taskJournal) { r.Version++ }},
		{name: "workspace", mutate: func(r *taskJournal) { r.WorkspaceID = "w2" }},
		{name: "project", mutate: func(r *taskJournal) { r.Project = "/tmp/other" }},
		{name: "pane", mutate: func(r *taskJournal) { r.DeveloperPaneID = "" }},
		{name: "session", mutate: func(r *taskJournal) { r.SessionID = "bad" }},
		{name: "checkpoint session", mutate: func(r *taskJournal) { r.CheckpointSessionID = "bad" }},
		{name: "negative offset", mutate: func(r *taskJournal) { r.CompactOffset = -1 }},
		{name: "offset without session", mutate: func(r *taskJournal) { r.CheckpointSessionID = ""; r.CompactOffset = 1 }},
		{name: "hash", mutate: func(r *taskJournal) { r.TaskHash = "bad" }},
		{name: "phase", mutate: func(r *taskJournal) { r.Phase = "mystery" }},
		{name: "timestamps", mutate: func(r *taskJournal) { r.UpdatedAt = r.StartedAt.Add(-time.Second) }},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			record := validTaskJournal(info)
			test.mutate(&record)
			if err := validateTaskJournal(record, info); err == nil {
				t.Fatalf("record unexpectedly valid: %+v", record)
			}
		})
	}
	if err := validateTaskJournal(validTaskJournal(info), info); err != nil {
		t.Fatalf("valid record rejected: %v", err)
	}
}

func TestJournalCheckpointRejectsAnotherConversation(t *testing.T) {
	app := New(&scriptedRunner{t: t}, &strings.Builder{}, &strings.Builder{})
	record := taskJournal{CheckpointSessionID: testConversationID}
	if _, err := app.journalCheckpoint(record, "aaaaaaaa-aaaa-4aaa-8aaa-aaaaaaaaaaaa"); err == nil {
		t.Fatal("expected mismatched checkpoint session to fail")
	}
}

func TestRecoverInterruptedTaskWithoutState(t *testing.T) {
	project, _ := filepath.EvalSymlinks(t.TempDir())
	developer := developerName("w1", "w1:p1")
	app := New(&scriptedRunner{t: t}, &strings.Builder{}, &strings.Builder{})
	app.stateDir = t.TempDir()
	app.getenv = cagyEnv(project, developer)
	if err := app.recoverInterruptedTask(context.Background()); err == nil || !strings.Contains(err.Error(), "no interrupted task") {
		t.Fatalf("error=%v", err)
	}
}

func TestRecoverCompletedTaskWhenDeveloperIsGone(t *testing.T) {
	project, _ := filepath.EvalSymlinks(t.TempDir())
	developerName := developerName("w1", "w1:p1")
	stateDir := t.TempDir()
	brainRoot := t.TempDir()
	task := "completed before developer stopped"
	writeAgyTranscript(t, brainRoot, testConversationID, task, "Durable answer.")
	runner := &scriptedRunner{t: t, steps: []runStep{
		{want: []string{"herdr", "agent", "get", developerName}, result: jsonError("agent_not_found", "missing")},
	}}
	var stdout strings.Builder
	app := New(runner, &stdout, &strings.Builder{})
	app.stateDir = stateDir
	app.agyBrainRoot = brainRoot
	app.getenv = cagyEnv(project, developerName)
	info, err := app.context()
	if err != nil {
		t.Fatal(err)
	}
	developer := herdr.AgentInfo{Agent: "agy", PaneID: "w1:p2", WorkspaceID: "w1", TabID: "w1:t1", ForegroundCWD: project, AgentSession: &herdr.AgentSessionInfo{Source: "herdr:antigravity_cli", Agent: "agy", Kind: "id", Value: testConversationID}}
	if err := app.beginTaskTracking(info, developer, task, transcript.Checkpoint{SessionID: testConversationID}, taskPhaseCompleted); err != nil {
		t.Fatal(err)
	}
	app.activeTask = nil
	if err := app.recoverInterruptedTask(context.Background()); err != nil {
		t.Fatal(err)
	}
	runner.assertDone()
	if stdout.String() != "Durable answer.\n" {
		t.Fatalf("stdout=%q", stdout.String())
	}
}

func TestRecoverRejectsMismatchedLiveConversation(t *testing.T) {
	project, _ := filepath.EvalSymlinks(t.TempDir())
	developerName := developerName("w1", "w1:p1")
	otherSession := "aaaaaaaa-aaaa-4aaa-8aaa-aaaaaaaaaaaa"
	runner := &scriptedRunner{t: t, steps: []runStep{
		{want: []string{"herdr", "agent", "get", developerName}, result: agentJSONWithSession("w1:p2", "w1", project, "idle", otherSession)},
		{want: []string{"herdr", "pane", "get", "w1:p1"}, result: supervisorPaneJSON(project)},
		{want: []string{"herdr", "pane", "get", "w1:p2"}, result: paneJSON("w1:p2", "w1:t1", project, developerPaneLabel, map[string]string{"cagy_owner": developerName, "cagy_role": "developer"})},
	}}
	app := New(runner, &strings.Builder{}, &strings.Builder{})
	app.stateDir = t.TempDir()
	app.getenv = cagyEnv(project, developerName)
	info, err := app.context()
	if err != nil {
		t.Fatal(err)
	}
	developer := herdr.AgentInfo{Agent: "agy", PaneID: "w1:p2", WorkspaceID: "w1", TabID: "w1:t1", ForegroundCWD: project, AgentSession: &herdr.AgentSessionInfo{Source: "herdr:antigravity_cli", Agent: "agy", Kind: "id", Value: testConversationID}}
	if err := app.beginTaskTracking(info, developer, "task", transcript.Checkpoint{SessionID: testConversationID}, taskPhaseMonitoring); err != nil {
		t.Fatal(err)
	}
	app.activeTask = nil
	if err := app.recoverInterruptedTask(context.Background()); err == nil || !strings.Contains(err.Error(), "conversation no longer matches") {
		t.Fatalf("error=%v", err)
	}
	runner.assertDone()
}

func TestStaleBlockedJournalRecoversCompletedResponse(t *testing.T) {
	project, _ := filepath.EvalSymlinks(t.TempDir())
	developerName := developerName("w1", "w1:p1")
	brainRoot := t.TempDir()
	task := "formerly blocked task"
	writeAgyTranscript(t, brainRoot, testConversationID, task, "Finished later.")
	runner := &scriptedRunner{t: t, steps: []runStep{
		{want: []string{"herdr", "agent", "get", developerName}, result: agentJSONWithSession("w1:p2", "w1", project, "idle", testConversationID)},
		{want: []string{"herdr", "pane", "get", "w1:p1"}, result: supervisorPaneJSON(project)},
		{want: []string{"herdr", "pane", "get", "w1:p2"}, result: paneJSON("w1:p2", "w1:t1", project, developerPaneLabel, map[string]string{"cagy_owner": developerName, "cagy_role": "developer"})},
		{want: []string{"herdr", "agent", "read", developerName, "--source", "visible", "--lines", "80"}, result: textResult(">\n────────────────────\n? for shortcuts\n")},
	}}
	app := New(runner, &strings.Builder{}, &strings.Builder{})
	app.stateDir = t.TempDir()
	app.agyBrainRoot = brainRoot
	app.getenv = cagyEnv(project, developerName)
	info, _ := app.context()
	developer := herdr.AgentInfo{Agent: "agy", PaneID: "w1:p2", WorkspaceID: "w1", TabID: "w1:t1", ForegroundCWD: project, AgentSession: &herdr.AgentSessionInfo{Source: "herdr:antigravity_cli", Agent: "agy", Kind: "id", Value: testConversationID}}
	if err := app.beginTaskTracking(info, developer, task, transcript.Checkpoint{SessionID: testConversationID}, taskPhaseBlocked); err != nil {
		t.Fatal(err)
	}
	record := *app.activeTask
	inspection, err := app.inspectTaskJournal(context.Background(), info, record)
	if err != nil {
		t.Fatal(err)
	}
	if inspection.kind != taskInspectionCompleted || inspection.response != "Finished later." {
		t.Fatalf("inspection=%+v", inspection)
	}
	runner.assertDone()
}

func TestForgetRefusesUnreadableStateWhileDeveloperRuns(t *testing.T) {
	project, _ := filepath.EvalSymlinks(t.TempDir())
	developerName := developerName("w1", "w1:p1")
	runner := &scriptedRunner{t: t, steps: []runStep{
		{want: []string{"herdr", "agent", "get", developerName}, result: agentJSONWithSession("w1:p2", "w1", project, "working", testConversationID)},
		{want: []string{"herdr", "pane", "get", "w1:p1"}, result: supervisorPaneJSON(project)},
		{want: []string{"herdr", "pane", "get", "w1:p2"}, result: paneJSON("w1:p2", "w1:t1", project, developerPaneLabel, map[string]string{"cagy_owner": developerName, "cagy_role": "developer"})},
		{want: []string{"herdr", "agent", "read", developerName, "--source", "visible", "--lines", "80"}, result: textResult("work\n────────────────────\nesc to cancel\n")},
	}}
	app := New(runner, &strings.Builder{}, &strings.Builder{})
	app.stateDir = t.TempDir()
	app.getenv = cagyEnv(project, developerName)
	if err := ensurePrivateStateDir(app.stateDir); err != nil {
		t.Fatal(err)
	}
	path := app.taskJournalPath(developerName)
	if err := os.WriteFile(path, []byte("not-json"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := app.forgetInterruptedTask(context.Background()); err == nil || !strings.Contains(err.Error(), "still running") {
		t.Fatalf("error=%v", err)
	}
	runner.assertDone()
	if _, err := os.Stat(path); err != nil {
		t.Fatalf("journal was removed: %v", err)
	}
}

func TestTaskLockIncompleteGraceAndReleaseOwnership(t *testing.T) {
	stateDir := t.TempDir()
	if err := ensurePrivateStateDir(stateDir); err != nil {
		t.Fatal(err)
	}
	now := time.Now().UTC()
	developer := "developer"
	path := filepath.Join(stateDir, stateFileName("lock", developer, ".json"))
	if err := os.WriteFile(path, nil, 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := acquireLock(stateDir, developer, now); err == nil || !strings.Contains(err.Error(), "too new") {
		t.Fatalf("new incomplete lock error=%v", err)
	}
	old := now.Add(-staleLockGrace - time.Second)
	if err := os.Chtimes(path, old, old); err != nil {
		t.Fatal(err)
	}
	lock, err := acquireLock(stateDir, developer, now)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.Remove(path); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte("replacement"), 0o600); err != nil {
		t.Fatal(err)
	}
	lock.release()
	if got, err := os.ReadFile(path); err != nil || string(got) != "replacement" {
		t.Fatalf("replacement lock was removed: data=%q err=%v", got, err)
	}
}

func TestAtomicTaskStateLeavesNoTemporaryFiles(t *testing.T) {
	app := New(&scriptedRunner{t: t}, &strings.Builder{}, &strings.Builder{})
	app.stateDir = t.TempDir()
	app.now = func() time.Time { return time.Date(2026, time.September, 18, 12, 0, 0, 0, time.UTC) }
	info := runtimeContext{workspaceID: "w1", supervisor: "w1:p1", developer: "developer", project: "/tmp/project"}
	developer := herdr.AgentInfo{PaneID: "w1:p2"}
	if err := app.beginTaskTracking(info, developer, "task", transcript.Checkpoint{}, taskPhaseSubmitting); err != nil {
		t.Fatal(err)
	}
	matches, err := filepath.Glob(filepath.Join(app.stateDir, ".cagy-state-*.tmp"))
	if err != nil {
		t.Fatal(err)
	}
	if len(matches) != 0 {
		t.Fatalf("temporary files remain: %v", matches)
	}
}

func TestOutputFailureRetainsCompletedJournalForRecovery(t *testing.T) {
	stateDir := t.TempDir()
	app := New(&scriptedRunner{t: t}, alwaysFailWriter{}, &strings.Builder{})
	app.stateDir = stateDir
	app.now = func() time.Time { return time.Date(2026, time.September, 18, 12, 0, 0, 0, time.UTC) }
	info := runtimeContext{workspaceID: "w1", supervisor: "w1:p1", developer: "developer", project: "/tmp/project"}
	developer := herdr.AgentInfo{PaneID: "w1:p2"}
	if err := app.beginTaskTracking(info, developer, "task", transcript.Checkpoint{}, taskPhaseMonitoring); err != nil {
		t.Fatal(err)
	}
	if err := app.deliverTrackedOutput("answer", developer); err == nil || !strings.Contains(err.Error(), "recover it") {
		t.Fatalf("error=%v", err)
	}
	record, exists, err := app.loadTaskJournal(info.developer)
	if err != nil || !exists {
		t.Fatalf("exists=%v err=%v", exists, err)
	}
	if record.Phase != taskPhaseCompleted {
		t.Fatalf("phase=%q", record.Phase)
	}
}

func TestJournalWriteFailurePreventsTaskSubmission(t *testing.T) {
	project, _ := filepath.EvalSymlinks(t.TempDir())
	developerName := developerName("w1", "w1:p1")
	task := "must not be submitted without durable state"
	stateDir := t.TempDir()
	runner := &scriptedRunner{t: t, steps: []runStep{
		{want: []string{"herdr", "pane", "get", "w1:p1"}, result: supervisorPaneJSON(project)},
		{want: []string{"herdr", "agent", "get", developerName}, result: agentJSONWithSession("w1:p2", "w1", project, "idle", testConversationID)},
		{want: []string{"herdr", "pane", "get", "w1:p2"}, result: paneJSON("w1:p2", "w1:t1", project, developerPaneLabel, map[string]string{"cagy_owner": developerName, "cagy_role": "developer"})},
		{want: agyProbeArgs("/model"), result: agyModelResult("gemini-3.8-flash-high", "Gemini 3.8 Flash (High)")},
		{want: agyProbeArgs("/quota"), before: func() {
			if err := os.Mkdir(filepath.Join(stateDir, stateFileName("task", developerName, ".json")), 0o700); err != nil {
				t.Fatal(err)
			}
		}, result: agyQuotaResult("Gemini Models", 0.8, 0.9)},
	}}
	app := New(runner, &strings.Builder{}, &strings.Builder{})
	app.stateDir = stateDir
	app.agyBrainRoot = t.TempDir()
	app.getenv = cagyEnv(project, developerName)
	if err := app.ask(context.Background(), task); err == nil || !strings.Contains(err.Error(), "prepare durable task state") {
		t.Fatalf("error=%v", err)
	}
	runner.assertDone()
	for _, call := range runner.calls {
		if hasPrefix(call, "herdr", "agent", "prompt") {
			t.Fatalf("task was submitted without a journal: %v", call)
		}
	}
}

func TestContextCancellationKeepsRecoverableTaskState(t *testing.T) {
	project, _ := filepath.EvalSymlinks(t.TempDir())
	developerName := developerName("w1", "w1:p1")
	task := "task interrupted by caller cancellation"
	runner := &scriptedRunner{t: t, steps: []runStep{
		{want: []string{"herdr", "pane", "get", "w1:p1"}, result: supervisorPaneJSON(project)},
		{want: []string{"herdr", "agent", "get", developerName}, result: agentJSONWithSession("w1:p2", "w1", project, "idle", testConversationID)},
		{want: []string{"herdr", "pane", "get", "w1:p2"}, result: paneJSON("w1:p2", "w1:t1", project, developerPaneLabel, map[string]string{"cagy_owner": developerName, "cagy_role": "developer"})},
		{want: agyProbeArgs("/model"), result: agyModelResult("gemini-3.8-flash-high", "Gemini 3.8 Flash (High)")},
		{want: agyProbeArgs("/quota"), result: agyQuotaResult("Gemini Models", 0.8, 0.9)},
		{want: []string{"herdr", "agent", "read", developerName, "--source", "recent-unwrapped", "--lines", "400"}, result: textResult("old\n")},
		{want: []string{"herdr", "agent", "prompt", developerName, task, "--wait", "--timeout", "30000"}, err: context.Canceled},
		{want: []string{"herdr", "agent", "read", developerName, "--source", "recent-unwrapped", "--lines", "400"}, result: textResult("old\n")},
	}}
	app := New(runner, &strings.Builder{}, &strings.Builder{})
	app.stateDir = t.TempDir()
	app.agyBrainRoot = t.TempDir()
	app.getenv = cagyEnv(project, developerName)
	if err := app.ask(context.Background(), task); err == nil || !strings.Contains(err.Error(), "monitoring stopped") {
		t.Fatalf("error=%v", err)
	}
	runner.assertDone()
	record, exists, err := app.loadTaskJournal(developerName)
	if err != nil || !exists {
		t.Fatalf("exists=%v err=%v", exists, err)
	}
	if record.Phase != taskPhaseUncertain || record.TaskHash != transcript.TaskHash(task) {
		t.Fatalf("record=%+v", record)
	}
}

func TestReplacingTrackedPromptUpdatesRecoveryHashAndCheckpoint(t *testing.T) {
	app := New(&scriptedRunner{t: t}, &strings.Builder{}, &strings.Builder{})
	app.stateDir = t.TempDir()
	app.now = func() time.Time { return time.Date(2026, time.September, 18, 12, 0, 0, 0, time.UTC) }
	info := runtimeContext{workspaceID: "w1", supervisor: "w1:p1", developer: "developer", project: "/tmp/project"}
	developer := herdr.AgentInfo{PaneID: "w1:p2", AgentSession: &herdr.AgentSessionInfo{Source: "herdr:antigravity_cli", Agent: "agy", Kind: "id", Value: testConversationID}}
	if err := app.beginTaskTracking(info, developer, "original", transcript.Checkpoint{SessionID: testConversationID, Offset: 1, FullOffset: 2}, taskPhaseRecovering); err != nil {
		t.Fatal(err)
	}
	continuation := continuationPrompt("original")
	checkpoint := transcript.Checkpoint{SessionID: testConversationID, Offset: 30, FullOffset: 40}
	if err := app.replaceTrackedPrompt(developer, continuation, checkpoint, taskPhaseSubmitting); err != nil {
		t.Fatal(err)
	}
	record, exists, err := app.loadTaskJournal(info.developer)
	if err != nil || !exists {
		t.Fatalf("exists=%v err=%v", exists, err)
	}
	if record.TaskHash != transcript.TaskHash(continuation) || record.CompactOffset != 30 || record.FullOffset != 40 || record.Phase != taskPhaseSubmitting {
		t.Fatalf("record=%+v", record)
	}
}

func TestInterruptedBackgroundTaskIsRunningEvenWithIdleFooter(t *testing.T) {
	project, _ := filepath.EvalSymlinks(t.TempDir())
	developerName := developerName("w1", "w1:p1")
	brainRoot := t.TempDir()
	task := "background task"
	taskID := testConversationID + "/task-77"
	writeAgyTaskOnly(t, brainRoot, testConversationID, task)
	appendAgyEvent(t, brainRoot, testConversationID, "MODEL", "GENERIC", "RUNNING", "Tool is running as a background task with task id: "+taskID)
	appendAgyAnswer(t, brainRoot, testConversationID, "Waiting for background work.")
	runner := &scriptedRunner{t: t, steps: []runStep{
		{want: []string{"herdr", "agent", "get", developerName}, result: agentJSONWithSession("w1:p2", "w1", project, "idle", testConversationID)},
		{want: []string{"herdr", "pane", "get", "w1:p1"}, result: supervisorPaneJSON(project)},
		{want: []string{"herdr", "pane", "get", "w1:p2"}, result: paneJSON("w1:p2", "w1:t1", project, developerPaneLabel, map[string]string{"cagy_owner": developerName, "cagy_role": "developer"})},
		{want: []string{"herdr", "agent", "read", developerName, "--source", "visible", "--lines", "80"}, result: textResult(">\n────────────────────\n? for shortcuts\n")},
	}}
	app := New(runner, &strings.Builder{}, &strings.Builder{})
	app.stateDir = t.TempDir()
	app.agyBrainRoot = brainRoot
	app.getenv = cagyEnv(project, developerName)
	info, _ := app.context()
	developer := herdr.AgentInfo{Agent: "agy", PaneID: "w1:p2", WorkspaceID: "w1", TabID: "w1:t1", ForegroundCWD: project, AgentSession: &herdr.AgentSessionInfo{Source: "herdr:antigravity_cli", Agent: "agy", Kind: "id", Value: testConversationID}}
	if err := app.beginTaskTracking(info, developer, task, transcript.Checkpoint{SessionID: testConversationID}, taskPhaseMonitoring); err != nil {
		t.Fatal(err)
	}
	inspection, err := app.inspectTaskJournal(context.Background(), info, *app.activeTask)
	if err != nil {
		t.Fatal(err)
	}
	if inspection.kind != taskInspectionRunning {
		t.Fatalf("inspection=%+v", inspection)
	}
	runner.assertDone()
}

func TestTaskJournalVersion2AndReceiptGeneration(t *testing.T) {
	stateDir := t.TempDir()
	now := time.Date(2026, time.September, 18, 12, 0, 0, 0, time.UTC)
	app := New(&scriptedRunner{t: t}, &strings.Builder{}, &strings.Builder{})
	app.stateDir = stateDir
	app.now = func() time.Time { return now }
	info := runtimeContext{workspaceID: "w1", supervisor: "w1:p1", developer: "developer", project: "/tmp/project"}
	developer := herdr.AgentInfo{
		Agent:       "agy",
		PaneID:      "w1:p2",
		WorkspaceID: "w1",
		TabID:       "w1:t1",
	}
	task := "Run task and verify receipt"
	checkpoint := transcript.Checkpoint{SessionID: testConversationID, Offset: 0, FullOffset: 0}

	if err := app.beginTaskTracking(info, developer, task, checkpoint, taskPhaseSubmitting); err != nil {
		t.Fatal(err)
	}
	record, exists, err := app.loadTaskJournal(info.developer)
	if err != nil || !exists {
		t.Fatalf("load failed exists=%v err=%v", exists, err)
	}
	if record.Version != 2 {
		t.Fatalf("expected version 2, got %d", record.Version)
	}
	if record.DeliveryReceipt != "" {
		t.Fatalf("expected empty receipt while submitting, got %q", record.DeliveryReceipt)
	}

	if err := app.setTrackedPhase(taskPhaseCompleted, developer); err != nil {
		t.Fatal(err)
	}
	record, exists, err = app.loadTaskJournal(info.developer)
	if err != nil || !exists {
		t.Fatalf("load failed exists=%v err=%v", exists, err)
	}
	if record.Phase != taskPhaseCompleted {
		t.Fatalf("expected phase completed, got %q", record.Phase)
	}
	if !isValidDeliveryReceipt(record.DeliveryReceipt) {
		t.Fatalf("invalid delivery receipt: %q", record.DeliveryReceipt)
	}
}

func TestLegacyVersion1JournalMigrationAndReceipt(t *testing.T) {
	project, _ := filepath.EvalSymlinks(t.TempDir())
	stateDir := t.TempDir()
	brainRoot := t.TempDir()
	developerName := developerName("w1", "w1:p1")
	task := "Legacy v1 task"
	writeAgyTranscript(t, brainRoot, testConversationID, task, "Legacy answer.")

	now := time.Date(2026, time.September, 18, 12, 0, 0, 0, time.UTC)
	rawV1 := taskJournal{
		Version:             1,
		WorkspaceID:         "w1",
		SupervisorPaneID:    "w1:p1",
		Developer:           developerName,
		DeveloperPaneID:     "w1:p2",
		Project:             project,
		SessionID:           testConversationID,
		CheckpointSessionID: testConversationID,
		TaskHash:            transcript.TaskHash(task),
		Phase:               taskPhaseCompleted,
		StartedAt:           now,
		UpdatedAt:           now,
	}
	data, _ := json.Marshal(rawV1)
	name := stateFileName("task", developerName, ".json")
	if err := writePrivateStateFile(stateDir, name, data); err != nil {
		t.Fatal(err)
	}

	runner := &scriptedRunner{t: t, steps: []runStep{
		{want: []string{"herdr", "agent", "get", developerName}, result: agentJSONWithSession("w1:p2", "w1", project, "idle", testConversationID)},
		{want: []string{"herdr", "pane", "get", "w1:p1"}, result: supervisorPaneJSON(project)},
		{want: []string{"herdr", "pane", "get", "w1:p2"}, result: paneJSON("w1:p2", "w1:t1", project, developerPaneLabel, map[string]string{"cagy_owner": developerName, "cagy_role": "developer"})},
		{want: []string{"herdr", "agent", "get", developerName}, result: agentJSONWithSession("w1:p2", "w1", project, "idle", testConversationID)},
		{want: []string{"herdr", "pane", "get", "w1:p1"}, result: supervisorPaneJSON(project)},
		{want: []string{"herdr", "pane", "get", "w1:p2"}, result: paneJSON("w1:p2", "w1:t1", project, developerPaneLabel, map[string]string{"cagy_owner": developerName, "cagy_role": "developer"})},
	}}
	app := New(runner, &strings.Builder{}, &strings.Builder{})
	app.stateDir = stateDir
	app.agyBrainRoot = brainRoot
	app.getenv = cagyEnv(project, developerName)
	info, _ := app.context()

	loaded, exists, err := app.loadTaskJournal(developerName)
	if err != nil || !exists {
		t.Fatalf("load failed exists=%v err=%v", exists, err)
	}
	if loaded.Version != 1 {
		t.Fatalf("expected loaded version 1, got %d", loaded.Version)
	}
	if loaded.DeliveryReceipt != "" {
		t.Fatalf("expected empty receipt on v1, got %q", loaded.DeliveryReceipt)
	}

	inspection, err := app.inspectTaskJournal(context.Background(), info, loaded)
	if err != nil {
		t.Fatal(err)
	}
	if inspection.kind != taskInspectionCompleted {
		t.Fatalf("expected completed inspection, got %q", inspection.kind)
	}
	// inspectTaskJournal is side-effect-free: it must not generate a receipt or mutate v1 journal
	if inspection.receipt != "" {
		t.Fatalf("expected empty receipt from side-effect-free inspection, got %q", inspection.receipt)
	}
	stillV1, exists, err := app.loadTaskJournal(developerName)
	if err != nil || !exists {
		t.Fatalf("load stillV1 failed: %v", err)
	}
	if stillV1.Version != 1 || stillV1.DeliveryReceipt != "" {
		t.Fatalf("inspection mutated journal before locked recovery: version=%d receipt=%q", stillV1.Version, stillV1.DeliveryReceipt)
	}

	// Locked recovery path performs the legacy v1 migration and receipt persistence
	recovered, err := app.recoverTask(context.Background())
	if err != nil {
		t.Fatalf("recoverTask failed: %v", err)
	}
	if !isValidDeliveryReceipt(recovered.Receipt) {
		t.Fatalf("expected valid 32-hex receipt from recovery, got %q", recovered.Receipt)
	}
	if recovered.Answer != "Legacy answer." {
		t.Fatalf("recovered answer=%q, want %q", recovered.Answer, "Legacy answer.")
	}

	migrated, exists, err := app.loadTaskJournal(developerName)
	if err != nil || !exists {
		t.Fatalf("load migrated failed: %v", err)
	}
	if migrated.Version != 2 {
		t.Fatalf("expected migrated version 2, got %d", migrated.Version)
	}
	if migrated.DeliveryReceipt != recovered.Receipt {
		t.Fatalf("migrated receipt %q != recovered receipt %q", migrated.DeliveryReceipt, recovered.Receipt)
	}
}

func TestLegacyVersion1JournalWithExistingReceiptMigration(t *testing.T) {
	project, _ := filepath.EvalSymlinks(t.TempDir())
	stateDir := t.TempDir()
	brainRoot := t.TempDir()
	developerName := developerName("w1", "w1:p1")
	task := "Legacy v1 task with receipt"
	writeAgyTranscript(t, brainRoot, testConversationID, task, "Legacy answer with existing receipt.")

	existingReceipt := "fedcba9876543210fedcba9876543210"
	now := time.Date(2026, time.September, 18, 12, 0, 0, 0, time.UTC)
	rawV1 := taskJournal{
		Version:             1,
		WorkspaceID:         "w1",
		SupervisorPaneID:    "w1:p1",
		Developer:           developerName,
		DeveloperPaneID:     "w1:p2",
		Project:             project,
		SessionID:           testConversationID,
		CheckpointSessionID: testConversationID,
		TaskHash:            transcript.TaskHash(task),
		Phase:               taskPhaseCompleted,
		DeliveryReceipt:     existingReceipt,
		StartedAt:           now,
		UpdatedAt:           now,
	}
	data, _ := json.Marshal(rawV1)
	name := stateFileName("task", developerName, ".json")
	if err := writePrivateStateFile(stateDir, name, data); err != nil {
		t.Fatal(err)
	}

	runner := &scriptedRunner{t: t, steps: []runStep{
		{want: []string{"herdr", "agent", "get", developerName}, result: agentJSONWithSession("w1:p2", "w1", project, "idle", testConversationID)},
		{want: []string{"herdr", "pane", "get", "w1:p1"}, result: supervisorPaneJSON(project)},
		{want: []string{"herdr", "pane", "get", "w1:p2"}, result: paneJSON("w1:p2", "w1:t1", project, developerPaneLabel, map[string]string{"cagy_owner": developerName, "cagy_role": "developer"})},
		{want: []string{"herdr", "agent", "get", developerName}, result: agentJSONWithSession("w1:p2", "w1", project, "idle", testConversationID)},
		{want: []string{"herdr", "pane", "get", "w1:p1"}, result: supervisorPaneJSON(project)},
		{want: []string{"herdr", "pane", "get", "w1:p2"}, result: paneJSON("w1:p2", "w1:t1", project, developerPaneLabel, map[string]string{"cagy_owner": developerName, "cagy_role": "developer"})},
	}}
	app := New(runner, &strings.Builder{}, &strings.Builder{})
	app.stateDir = stateDir
	app.agyBrainRoot = brainRoot
	app.getenv = cagyEnv(project, developerName)
	info, _ := app.context()

	loaded, exists, err := app.loadTaskJournal(developerName)
	if err != nil || !exists {
		t.Fatalf("load failed exists=%v err=%v", exists, err)
	}
	if loaded.Version != 1 {
		t.Fatalf("expected loaded version 1, got %d", loaded.Version)
	}
	if loaded.DeliveryReceipt != existingReceipt {
		t.Fatalf("expected receipt %q, got %q", existingReceipt, loaded.DeliveryReceipt)
	}

	inspection, err := app.inspectTaskJournal(context.Background(), info, loaded)
	if err != nil {
		t.Fatal(err)
	}
	if inspection.kind != taskInspectionCompleted {
		t.Fatalf("expected completed inspection, got %q", inspection.kind)
	}
	if inspection.receipt != existingReceipt {
		t.Fatalf("expected existing receipt preserved in inspection, got %q", inspection.receipt)
	}

	// Still v1 on disk: inspectTaskJournal did not mutate the file
	stillV1, exists, err := app.loadTaskJournal(developerName)
	if err != nil || !exists {
		t.Fatalf("load stillV1 failed: %v", err)
	}
	if stillV1.Version != 1 || stillV1.DeliveryReceipt != existingReceipt {
		t.Fatalf("inspection mutated journal on disk: version=%d receipt=%q", stillV1.Version, stillV1.DeliveryReceipt)
	}

	// Locked recovery migrates v1 to v2 while preserving the existing receipt
	recovered, err := app.recoverTask(context.Background())
	if err != nil {
		t.Fatalf("recoverTask failed: %v", err)
	}
	if recovered.Receipt != existingReceipt {
		t.Fatalf("expected exact existing receipt %q, got %q", existingReceipt, recovered.Receipt)
	}
	if recovered.Answer != "Legacy answer with existing receipt." {
		t.Fatalf("recovered answer=%q, want %q", recovered.Answer, "Legacy answer with existing receipt.")
	}

	migrated, exists, err := app.loadTaskJournal(developerName)
	if err != nil || !exists {
		t.Fatalf("load migrated failed: %v", err)
	}
	if migrated.Version != 2 {
		t.Fatalf("expected migrated version 2, got %d", migrated.Version)
	}
	if migrated.DeliveryReceipt != existingReceipt {
		t.Fatalf("migrated receipt %q != existing receipt %q", migrated.DeliveryReceipt, existingReceipt)
	}
}

type mutatingWriter struct {
	onWrite func() error
	buf     strings.Builder
}

func (m *mutatingWriter) Write(p []byte) (n int, err error) {
	if m.onWrite != nil {
		if err := m.onWrite(); err != nil {
			return 0, err
		}
	}
	return m.buf.Write(p)
}

func TestRecoverInterruptedTaskDurableStateReloadFailure(t *testing.T) {
	project, _ := filepath.EvalSymlinks(t.TempDir())
	stateDir := t.TempDir()
	brainRoot := t.TempDir()
	developerName := developerName("w1", "w1:p1")
	task := "Interrupted task to recover"
	writeAgyTranscript(t, brainRoot, testConversationID, task, "Recovered answer.")

	now := time.Date(2026, time.September, 18, 12, 0, 0, 0, time.UTC)
	record := taskJournal{
		Version:             2,
		WorkspaceID:         "w1",
		SupervisorPaneID:    "w1:p1",
		Developer:           developerName,
		DeveloperPaneID:     "w1:p2",
		Project:             project,
		SessionID:           testConversationID,
		CheckpointSessionID: testConversationID,
		TaskHash:            transcript.TaskHash(task),
		Phase:               taskPhaseCompleted,
		DeliveryReceipt:     "1234567890abcdef1234567890abcdef",
		StartedAt:           now,
		UpdatedAt:           now,
	}
	data, _ := json.Marshal(record)
	name := stateFileName("task", developerName, ".json")
	filePath := filepath.Join(stateDir, name)

	t.Run("journal file removed during stdout write", func(t *testing.T) {
		if err := writePrivateStateFile(stateDir, name, data); err != nil {
			t.Fatal(err)
		}
		runner := &scriptedRunner{t: t, steps: []runStep{
			{want: []string{"herdr", "agent", "get", developerName}, result: agentJSONWithSession("w1:p2", "w1", project, "idle", testConversationID)},
			{want: []string{"herdr", "pane", "get", "w1:p1"}, result: supervisorPaneJSON(project)},
			{want: []string{"herdr", "pane", "get", "w1:p2"}, result: paneJSON("w1:p2", "w1:t1", project, developerPaneLabel, map[string]string{"cagy_owner": developerName, "cagy_role": "developer"})},
		}}
		writer := &mutatingWriter{
			onWrite: func() error {
				return os.Remove(filePath)
			},
		}
		app := New(runner, writer, &strings.Builder{})
		app.stateDir = stateDir
		app.agyBrainRoot = brainRoot
		app.getenv = cagyEnv(project, developerName)

		err := app.recoverInterruptedTask(context.Background())
		if err == nil || !strings.Contains(err.Error(), "durable task state disappeared before acknowledgment") {
			t.Fatalf("expected disappearing file error, got: %v", err)
		}
		if !strings.Contains(writer.buf.String(), "Recovered answer.") {
			t.Fatalf("stdout did not receive recovered answer: %q", writer.buf.String())
		}
	})

	t.Run("journal file corrupted during stdout write", func(t *testing.T) {
		if err := writePrivateStateFile(stateDir, name, data); err != nil {
			t.Fatal(err)
		}
		runner := &scriptedRunner{t: t, steps: []runStep{
			{want: []string{"herdr", "agent", "get", developerName}, result: agentJSONWithSession("w1:p2", "w1", project, "idle", testConversationID)},
			{want: []string{"herdr", "pane", "get", "w1:p1"}, result: supervisorPaneJSON(project)},
			{want: []string{"herdr", "pane", "get", "w1:p2"}, result: paneJSON("w1:p2", "w1:t1", project, developerPaneLabel, map[string]string{"cagy_owner": developerName, "cagy_role": "developer"})},
		}}
		writer := &mutatingWriter{
			onWrite: func() error {
				return os.WriteFile(filePath, []byte("invalid-json{"), 0o600)
			},
		}
		app := New(runner, writer, &strings.Builder{})
		app.stateDir = stateDir
		app.agyBrainRoot = brainRoot
		app.getenv = cagyEnv(project, developerName)

		err := app.recoverInterruptedTask(context.Background())
		if err == nil || !strings.Contains(err.Error(), "durable task state could not be reloaded") {
			t.Fatalf("expected reload failure error, got: %v", err)
		}
		if !strings.Contains(writer.buf.String(), "Recovered answer.") {
			t.Fatalf("stdout did not receive recovered answer: %q", writer.buf.String())
		}
	})
}

func TestAcknowledgeTaskValidationAndRemoval(t *testing.T) {
	project, _ := filepath.EvalSymlinks(t.TempDir())
	stateDir := t.TempDir()
	developerName := developerName("w1", "w1:p1")
	now := time.Date(2026, time.September, 18, 12, 0, 0, 0, time.UTC)
	validReceipt := "0123456789abcdef0123456789abcdef"

	setupJournal := func(phase taskPhase, receipt string, proj string) *App {
		record := taskJournal{
			Version:             2,
			WorkspaceID:         "w1",
			SupervisorPaneID:    "w1:p1",
			Developer:           developerName,
			DeveloperPaneID:     "w1:p2",
			Project:             proj,
			SessionID:           testConversationID,
			CheckpointSessionID: testConversationID,
			TaskHash:            transcript.TaskHash("task"),
			Phase:               phase,
			DeliveryReceipt:     receipt,
			StartedAt:           now,
			UpdatedAt:           now,
		}
		data, _ := json.Marshal(record)
		_ = writePrivateStateFile(stateDir, stateFileName("task", developerName, ".json"), data)
		app := New(&scriptedRunner{t: t}, &strings.Builder{}, &strings.Builder{})
		app.stateDir = stateDir
		app.getenv = cagyEnv(project, developerName)
		return app
	}

	// 1. Malformed receipt rejected without touching journal
	app := setupJournal(taskPhaseCompleted, validReceipt, project)
	err := app.acknowledgeTask(context.Background(), "short")
	if err == nil || !strings.Contains(err.Error(), "must be 32 lowercase hexadecimal") {
		t.Fatalf("expected malformed receipt error, got: %v", err)
	}
	if _, exists, _ := app.loadTaskJournal(developerName); !exists {
		t.Fatal("journal unexpectedly deleted on malformed receipt")
	}

	// 2. Non-matching receipt rejected without touching journal
	wrongReceipt := "ffffffffffffffffffffffffffffffff"
	err = app.acknowledgeTask(context.Background(), wrongReceipt)
	if err == nil || !strings.Contains(err.Error(), "receipt does not match") {
		t.Fatalf("expected receipt mismatch error, got: %v", err)
	}
	if _, exists, _ := app.loadTaskJournal(developerName); !exists {
		t.Fatal("journal unexpectedly deleted on wrong receipt")
	}

	// 3. Reject unfinished phase (e.g. monitoring) without touching journal
	app = setupJournal(taskPhaseMonitoring, validReceipt, project)
	err = app.acknowledgeTask(context.Background(), validReceipt)
	if err == nil || !strings.Contains(err.Error(), "task is not completed") {
		t.Fatalf("expected not completed error, got: %v", err)
	}
	if _, exists, _ := app.loadTaskJournal(developerName); !exists {
		t.Fatal("journal unexpectedly deleted on monitoring task")
	}

	// 4. Reject cross-project acknowledgment without touching journal
	app = setupJournal(taskPhaseCompleted, validReceipt, "/other/project")
	err = app.acknowledgeTask(context.Background(), validReceipt)
	if err == nil || !strings.Contains(err.Error(), "belongs to another project") {
		t.Fatalf("expected cross-project error, got: %v", err)
	}
	if _, exists, _ := app.loadTaskJournal(developerName); !exists {
		t.Fatal("journal unexpectedly deleted on cross-project task")
	}

	// 5. Correct receipt removes matching completed state
	app = setupJournal(taskPhaseCompleted, validReceipt, project)
	err = app.acknowledgeTask(context.Background(), validReceipt)
	if err != nil {
		t.Fatalf("expected successful acknowledgment, got: %v", err)
	}
	if _, exists, _ := app.loadTaskJournal(developerName); exists {
		t.Fatal("journal still exists after valid acknowledgment")
	}
}

func TestAcknowledgeTaskConcurrentRaces(t *testing.T) {
	project, _ := filepath.EvalSymlinks(t.TempDir())
	stateDir := t.TempDir()
	developerName := developerName("w1", "w1:p1")
	now := time.Date(2026, time.September, 18, 12, 0, 0, 0, time.UTC)
	validReceipt := "abcdef0123456789abcdef0123456789"

	record := taskJournal{
		Version:             2,
		WorkspaceID:         "w1",
		SupervisorPaneID:    "w1:p1",
		Developer:           developerName,
		DeveloperPaneID:     "w1:p2",
		Project:             project,
		SessionID:           testConversationID,
		CheckpointSessionID: testConversationID,
		TaskHash:            transcript.TaskHash("task"),
		Phase:               taskPhaseCompleted,
		DeliveryReceipt:     validReceipt,
		StartedAt:           now,
		UpdatedAt:           now,
	}
	data, _ := json.Marshal(record)
	_ = writePrivateStateFile(stateDir, stateFileName("task", developerName, ".json"), data)

	const concurrency = 10
	errs := make(chan error, concurrency)
	for i := 0; i < concurrency; i++ {
		go func() {
			app := New(&scriptedRunner{t: t}, &strings.Builder{}, &strings.Builder{})
			app.stateDir = stateDir
			app.getenv = cagyEnv(project, developerName)
			errs <- app.acknowledgeTask(context.Background(), validReceipt)
		}()
	}

	successCount := 0
	for i := 0; i < concurrency; i++ {
		err := <-errs
		if err == nil {
			successCount++
		}
	}
	if successCount != 1 {
		t.Fatalf("expected exactly 1 concurrent acknowledgment to succeed, got %d", successCount)
	}
}

func TestRecoverInterruptedTaskOutputFailureRetainsJournal(t *testing.T) {
	project, _ := filepath.EvalSymlinks(t.TempDir())
	stateDir := t.TempDir()
	brainRoot := t.TempDir()
	developerName := developerName("w1", "w1:p1")
	task := "Interrupted task"
	writeAgyTranscript(t, brainRoot, testConversationID, task, "Recoverable answer.")

	runner := &scriptedRunner{t: t, steps: []runStep{
		{want: []string{"herdr", "agent", "get", developerName}, result: agentJSONWithSession("w1:p2", "w1", project, "idle", testConversationID)},
		{want: []string{"herdr", "pane", "get", "w1:p1"}, result: supervisorPaneJSON(project)},
		{want: []string{"herdr", "pane", "get", "w1:p2"}, result: paneJSON("w1:p2", "w1:t1", project, developerPaneLabel, map[string]string{"cagy_owner": developerName, "cagy_role": "developer"})},
		{want: []string{"herdr", "agent", "read", developerName, "--source", "visible", "--lines", "80"}, result: textResult(">\n────────────────────\n? for shortcuts\n")},
	}}
	app := New(runner, alwaysFailWriter{}, &strings.Builder{})
	app.stateDir = stateDir
	app.agyBrainRoot = brainRoot
	app.getenv = cagyEnv(project, developerName)
	info, _ := app.context()
	developer := herdr.AgentInfo{Agent: "agy", PaneID: "w1:p2", WorkspaceID: "w1", TabID: "w1:t1", ForegroundCWD: project, AgentSession: &herdr.AgentSessionInfo{Source: "herdr:antigravity_cli", Agent: "agy", Kind: "id", Value: testConversationID}}
	if err := app.beginTaskTracking(info, developer, task, transcript.Checkpoint{SessionID: testConversationID}, taskPhaseMonitoring); err != nil {
		t.Fatal(err)
	}
	app.activeTask = nil

	err := app.recoverInterruptedTask(context.Background())
	if err == nil || !strings.Contains(err.Error(), "retry cagy ask --recover") {
		t.Fatalf("expected write failure error, got: %v", err)
	}
	record, exists, err := app.loadTaskJournal(developerName)
	if err != nil || !exists {
		t.Fatalf("journal was deleted on stdout write failure: exists=%v err=%v", exists, err)
	}
	if record.Phase != taskPhaseCompleted {
		t.Fatalf("expected completed phase, got %q", record.Phase)
	}
	if !isValidDeliveryReceipt(record.DeliveryReceipt) {
		t.Fatalf("expected valid delivery receipt generated before write attempt: %q", record.DeliveryReceipt)
	}
}

func TestGetTaskStatusStates(t *testing.T) {
	project, _ := filepath.EvalSymlinks(t.TempDir())
	stateDir := filepath.Join(t.TempDir(), "private-state")
	_ = ensurePrivateStateDir(stateDir)
	developerName := developerName("w1", "w1:p1")
	now := time.Date(2026, time.September, 18, 12, 0, 0, 0, time.UTC)

	// 1. None: no task journal, developer idle
	runner := &scriptedRunner{t: t, steps: []runStep{
		{want: []string{"herdr", "agent", "get", developerName}, result: agentJSON("w1:p2", "w1", project, "idle")},
	}}
	app := New(runner, &strings.Builder{}, &strings.Builder{})
	app.stateDir = stateDir
	app.getenv = cagyEnv(project, developerName)
	app.now = func() time.Time { return now }

	status, err := app.getTaskStatus(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if status.Status != "none" {
		t.Fatalf("expected status none, got: %s", status.Status)
	}
	if !strings.Contains(status.Message, "no active task") {
		t.Fatalf("expected message mentioning no active task, got: %s", status.Message)
	}

	// 2. Running task
	taskSecret := "SUPER_SECRET_TASK_CONTENT"
	checkpoint := transcript.Checkpoint{SessionID: testConversationID}
	developer := herdr.AgentInfo{Agent: "agy", PaneID: "w1:p2", WorkspaceID: "w1", TabID: "w1:t1", ForegroundCWD: project, AgentSession: &herdr.AgentSessionInfo{Source: "herdr:antigravity_cli", Agent: "agy", Kind: "id", Value: testConversationID}}
	info, _ := app.context()
	if err := app.beginTaskTracking(info, developer, taskSecret, checkpoint, taskPhaseMonitoring); err != nil {
		t.Fatal(err)
	}
	app.activeTask = nil

	runner.steps = []runStep{
		{want: []string{"herdr", "agent", "get", developerName}, result: agentJSONWithSession("w1:p2", "w1", project, "working", testConversationID)},
		{want: []string{"herdr", "pane", "get", "w1:p1"}, result: supervisorPaneJSON(project)},
		{want: []string{"herdr", "pane", "get", "w1:p2"}, result: paneJSON("w1:p2", "w1:t1", project, developerPaneLabel, map[string]string{"cagy_owner": developerName, "cagy_role": "developer"})},
		{want: []string{"herdr", "agent", "read", developerName, "--source", "visible", "--lines", "80"}, result: textResult("working...\n")},
	}

	status, err = app.getTaskStatus(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if status.Status != "running" {
		t.Fatalf("expected status running, got: %s", status.Status)
	}
	// Verify secret task text is NOT exposed in status
	if strings.Contains(status.Message, taskSecret) || strings.Contains(status.Status, taskSecret) {
		t.Fatalf("task status leaked secret task text: %+v", status)
	}
}

func TestRecoverTaskStructuredOutput(t *testing.T) {
	project, _ := filepath.EvalSymlinks(t.TempDir())
	stateDir := filepath.Join(t.TempDir(), "private-state")
	_ = ensurePrivateStateDir(stateDir)
	brainRoot := t.TempDir()
	developerName := developerName("w1", "w1:p1")
	task := "Interrupted completed task"
	secretAnswer := "SECRET_MODEL_ANSWER_CONTENT"
	writeAgyTranscript(t, brainRoot, testConversationID, task, secretAnswer)

	runner := &scriptedRunner{t: t, steps: []runStep{
		{want: []string{"herdr", "agent", "get", developerName}, result: agentJSONWithSession("w1:p2", "w1", project, "idle", testConversationID)},
		{want: []string{"herdr", "pane", "get", "w1:p1"}, result: supervisorPaneJSON(project)},
		{want: []string{"herdr", "pane", "get", "w1:p2"}, result: paneJSON("w1:p2", "w1:t1", project, developerPaneLabel, map[string]string{"cagy_owner": developerName, "cagy_role": "developer"})},
		{want: []string{"herdr", "agent", "read", developerName, "--source", "visible", "--lines", "80"}, result: textResult(">\n────────────────────\n? for shortcuts\n")},
	}}
	app := New(runner, &strings.Builder{}, &strings.Builder{})
	app.stateDir = stateDir
	app.agyBrainRoot = brainRoot
	app.getenv = cagyEnv(project, developerName)
	info, _ := app.context()
	developer := herdr.AgentInfo{Agent: "agy", PaneID: "w1:p2", WorkspaceID: "w1", TabID: "w1:t1", ForegroundCWD: project, AgentSession: &herdr.AgentSessionInfo{Source: "herdr:antigravity_cli", Agent: "agy", Kind: "id", Value: testConversationID}}
	if err := app.beginTaskTracking(info, developer, task, transcript.Checkpoint{SessionID: testConversationID}, taskPhaseCompleted); err != nil {
		t.Fatal(err)
	}
	app.activeTask = nil

	out, err := app.recoverTask(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if out.Status != "completed_unacknowledged" {
		t.Fatalf("expected completed_unacknowledged, got: %s", out.Status)
	}
	if out.Answer != secretAnswer {
		t.Fatalf("expected answer %q, got %q", secretAnswer, out.Answer)
	}
	if !isValidDeliveryReceipt(out.Receipt) {
		t.Fatalf("invalid receipt: %q", out.Receipt)
	}
	if !out.AcknowledgementRequired {
		t.Fatal("expected acknowledgement_required=true")
	}

	// Crucial check: recoverTask MUST NOT delete state!
	if _, exists, _ := app.loadTaskJournal(developerName); !exists {
		t.Fatal("recoverTask unexpectedly deleted journal state before acknowledge")
	}
}

func TestForgetTaskRequiresConfirmationAndRejectsRunning(t *testing.T) {
	project, _ := filepath.EvalSymlinks(t.TempDir())
	stateDir := filepath.Join(t.TempDir(), "private-state")
	_ = ensurePrivateStateDir(stateDir)
	developerName := developerName("w1", "w1:p1")

	app := New(&scriptedRunner{t: t}, &strings.Builder{}, &strings.Builder{})
	app.stateDir = stateDir
	app.getenv = cagyEnv(project, developerName)
	info, _ := app.context()

	// 1. Refuse without confirm
	_, err := app.forgetTask(context.Background(), false)
	if err == nil || !strings.Contains(err.Error(), "requires explicit confirm=true") {
		t.Fatalf("expected confirm required error, got: %v", err)
	}

	// 2. Refuse running task
	developer := herdr.AgentInfo{Agent: "agy", PaneID: "w1:p2", WorkspaceID: "w1", TabID: "w1:t1", ForegroundCWD: project, AgentSession: &herdr.AgentSessionInfo{Source: "herdr:antigravity_cli", Agent: "agy", Kind: "id", Value: testConversationID}}
	if err := app.beginTaskTracking(info, developer, "running task", transcript.Checkpoint{SessionID: testConversationID}, taskPhaseMonitoring); err != nil {
		t.Fatal(err)
	}
	app.activeTask = nil

	runner := &scriptedRunner{t: t, steps: []runStep{
		{want: []string{"herdr", "agent", "get", developerName}, result: agentJSONWithSession("w1:p2", "w1", project, "working", testConversationID)},
		{want: []string{"herdr", "pane", "get", "w1:p1"}, result: supervisorPaneJSON(project)},
		{want: []string{"herdr", "pane", "get", "w1:p2"}, result: paneJSON("w1:p2", "w1:t1", project, developerPaneLabel, map[string]string{"cagy_owner": developerName, "cagy_role": "developer"})},
		{want: []string{"herdr", "agent", "read", developerName, "--source", "visible", "--lines", "80"}, result: textResult("working...\n")},
	}}
	app.runner = runner
	app.herdr = herdr.New(runner)

	_, err = app.forgetTask(context.Background(), true)
	if err == nil || !strings.Contains(err.Error(), "refusing to forget a task that is still running") {
		t.Fatalf("expected refusal for running task, got: %v", err)
	}
	if _, exists, _ := app.loadTaskJournal(developerName); !exists {
		t.Fatal("journal was deleted despite running task")
	}

	// 3. Completed task can be forgotten with confirm=true
	_ = app.setTrackedPhase(taskPhaseCompleted, developer)
	app.activeTask = nil
	runner.steps = []runStep{
		{want: []string{"herdr", "agent", "get", developerName}, result: agentJSONWithSession("w1:p2", "w1", project, "idle", testConversationID)},
		{want: []string{"herdr", "pane", "get", "w1:p1"}, result: supervisorPaneJSON(project)},
		{want: []string{"herdr", "pane", "get", "w1:p2"}, result: paneJSON("w1:p2", "w1:t1", project, developerPaneLabel, map[string]string{"cagy_owner": developerName, "cagy_role": "developer"})},
		{want: []string{"herdr", "agent", "read", developerName, "--source", "visible", "--lines", "80"}, result: textResult(">\n────────────────────\n? for shortcuts\n")},
	}

	forgetOut, err := app.forgetTask(context.Background(), true)
	if err != nil {
		t.Fatalf("unexpected error forgetting completed task: %v", err)
	}
	if forgetOut.Status != "forgotten" {
		t.Fatalf("expected status forgotten, got: %s", forgetOut.Status)
	}
	if _, exists, _ := app.loadTaskJournal(developerName); exists {
		t.Fatal("journal was not deleted after confirmed forget")
	}
}

func TestGetDeveloperStatusNonSecret(t *testing.T) {
	project, _ := filepath.EvalSymlinks(t.TempDir())
	developerName := developerName("w1", "w1:p1")
	runner := &scriptedRunner{t: t, steps: []runStep{
		{want: []string{"herdr", "pane", "get", "w1:p1"}, result: supervisorPaneJSON(project)},
		{want: []string{"herdr", "agent", "get", developerName}, result: agentJSONWithSession("w1:p2", "w1", project, "idle", testConversationID)},
		{want: []string{"herdr", "pane", "get", "w1:p2"}, result: paneJSON("w1:p2", "w1:t1", project, developerPaneLabel, map[string]string{
			"cagy_owner":         developerName,
			"cagy_role":          "developer",
			"cagy_session":       testConversationID,
			agySessionStateToken: agySessionStateReady,
			"cagy_secret_token":  "MUST_NEVER_BE_EXPOSED",
		})},
	}}
	app := New(runner, &strings.Builder{}, &strings.Builder{})
	app.stateDir = filepath.Join(t.TempDir(), "private-state")
	_ = ensurePrivateStateDir(app.stateDir)
	app.getenv = cagyEnv(project, developerName)

	devStatus, err := app.getDeveloperStatus(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if devStatus.Developer != developerName {
		t.Fatalf("expected developer %s, got: %s", developerName, devStatus.Developer)
	}
	if devStatus.PaneID != "w1:p2" {
		t.Fatalf("expected pane w1:p2, got: %s", devStatus.PaneID)
	}
	if devStatus.Status != "idle" {
		t.Fatalf("expected status idle, got: %s", devStatus.Status)
	}
	if !devStatus.SessionReady {
		t.Fatal("expected session_ready=true")
	}
	// Verify no secret tokens leaked
	jsonBytes, _ := json.Marshal(devStatus)
	if strings.Contains(string(jsonBytes), "MUST_NEVER_BE_EXPOSED") {
		t.Fatalf("developer status leaked tokens: %s", string(jsonBytes))
	}
}

func TestGetTaskStatusNonAgentNotFoundHerdrError(t *testing.T) {
	project, _ := filepath.EvalSymlinks(t.TempDir())
	stateDir := filepath.Join(t.TempDir(), "private-state")
	if err := ensurePrivateStateDir(stateDir); err != nil {
		t.Fatal(err)
	}
	developerName := developerName("w1", "w1:p1")
	runner := &scriptedRunner{t: t, steps: []runStep{
		{want: []string{"herdr", "agent", "get", developerName}, result: jsonError("server_error", "herdr daemon communication failure")},
	}}
	app := New(runner, &strings.Builder{}, &strings.Builder{})
	app.stateDir = stateDir
	app.getenv = cagyEnv(project, developerName)

	status, err := app.getTaskStatus(context.Background())
	if err == nil {
		t.Fatalf("expected error on non-agent-not-found Herdr error, got status: %+v", status)
	}
	if !strings.Contains(err.Error(), "check developer status") {
		t.Fatalf("unexpected error message: %v", err)
	}
	runner.assertDone()
}

func TestTaskStatusAndDeveloperStatusNoPathLeak(t *testing.T) {
	project, _ := filepath.EvalSymlinks(t.TempDir())
	stateDir := filepath.Join(t.TempDir(), "super-secret-private-dir-xyz")
	if err := ensurePrivateStateDir(stateDir); err != nil {
		t.Fatal(err)
	}
	developerName := developerName("w1", "w1:p1")
	// Write corrupt/unreadable journal
	corruptPath := filepath.Join(stateDir, stateFileName("task", developerName, ".json"))
	if err := os.WriteFile(corruptPath, []byte("NOT_VALID_JSON{"), 0o600); err != nil {
		t.Fatal(err)
	}

	runner := &scriptedRunner{t: t, steps: []runStep{
		{want: []string{"herdr", "pane", "get", "w1:p1"}, result: supervisorPaneJSON(project)},
		{want: []string{"herdr", "agent", "get", developerName}, result: agentJSONWithSession("w1:p2", "w1", project, "idle", testConversationID)},
		{want: []string{"herdr", "pane", "get", "w1:p2"}, result: paneJSON("w1:p2", "w1:t1", project, developerPaneLabel, map[string]string{
			"cagy_owner":         developerName,
			"cagy_role":          "developer",
			"cagy_session":       testConversationID,
			agySessionStateToken: agySessionStateReady,
		})},
	}}
	var stderr strings.Builder
	app := New(runner, &strings.Builder{}, &stderr)
	app.stateDir = stateDir
	app.getenv = cagyEnv(project, developerName)

	status, err := app.getTaskStatus(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if status.Status != "uncertain" {
		t.Fatalf("expected uncertain status, got: %s", status.Status)
	}
	if strings.Contains(status.Message, stateDir) || strings.Contains(status.Message, "super-secret") {
		t.Fatalf("task_status leaked private path in message: %s", status.Message)
	}
	if status.Message != "task journal is unreadable; run cagy doctor" {
		t.Fatalf("unexpected task status message: %s", status.Message)
	}

	devStatus, err := app.getDeveloperStatus(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(devStatus.TaskSummary, stateDir) || strings.Contains(devStatus.TaskSummary, "super-secret") {
		t.Fatalf("developer_status leaked private path in TaskSummary: %s", devStatus.TaskSummary)
	}
	if devStatus.TaskSummary != "interrupted task state is unreadable; run cagy doctor" {
		t.Fatalf("unexpected devStatus task summary: %s", devStatus.TaskSummary)
	}
}

func TestInspectionSideEffectFreeNoMutation(t *testing.T) {
	project, _ := filepath.EvalSymlinks(t.TempDir())
	stateDir := filepath.Join(t.TempDir(), "private-state")
	if err := ensurePrivateStateDir(stateDir); err != nil {
		t.Fatal(err)
	}
	brainRoot := t.TempDir()
	developerName := developerName("w1", "w1:p1")
	now := time.Date(2026, time.September, 18, 12, 0, 0, 0, time.UTC)
	writeAgyTranscript(t, brainRoot, testConversationID, "read-only task", "Completed answer.")
	record := taskJournal{
		Version:             1, // Legacy v1 journal
		WorkspaceID:         "w1",
		SupervisorPaneID:    "w1:p1",
		Developer:           developerName,
		DeveloperPaneID:     "w1:p2",
		Project:             project,
		SessionID:           testConversationID,
		CheckpointSessionID: testConversationID,
		TaskHash:            transcript.TaskHash("read-only task"),
		Phase:               taskPhaseCompleted,
		DeliveryReceipt:     "",
		StartedAt:           now,
		UpdatedAt:           now,
	}
	data, _ := json.MarshalIndent(record, "", "  ")
	data = append(data, '\n')
	filePath := filepath.Join(stateDir, stateFileName("task", developerName, ".json"))
	if err := os.WriteFile(filePath, data, 0o600); err != nil {
		t.Fatal(err)
	}
	initialStat, err := os.Stat(filePath)
	if err != nil {
		t.Fatal(err)
	}

	steps := []runStep{
		// Steps for getTaskStatus: inspectTaskJournal (3 calls when completed response is in transcript)
		{want: []string{"herdr", "agent", "get", developerName}, result: agentJSONWithSession("w1:p2", "w1", project, "idle", testConversationID)},
		{want: []string{"herdr", "pane", "get", "w1:p1"}, result: supervisorPaneJSON(project)},
		{want: []string{"herdr", "pane", "get", "w1:p2"}, result: paneJSON("w1:p2", "w1:t1", project, developerPaneLabel, map[string]string{"cagy_owner": developerName, "cagy_role": "developer"})},
		// Steps for getDeveloperStatus: supervisorPane, GetAgent, validatedDeveloperPane, then inspectTaskJournal
		{want: []string{"herdr", "pane", "get", "w1:p1"}, result: supervisorPaneJSON(project)},
		{want: []string{"herdr", "agent", "get", developerName}, result: agentJSONWithSession("w1:p2", "w1", project, "idle", testConversationID)},
		{want: []string{"herdr", "pane", "get", "w1:p2"}, result: paneJSON("w1:p2", "w1:t1", project, developerPaneLabel, map[string]string{"cagy_owner": developerName, "cagy_role": "developer", "cagy_session": testConversationID, agySessionStateToken: agySessionStateReady})},
		{want: []string{"herdr", "agent", "get", developerName}, result: agentJSONWithSession("w1:p2", "w1", project, "idle", testConversationID)},
		{want: []string{"herdr", "pane", "get", "w1:p1"}, result: supervisorPaneJSON(project)},
		{want: []string{"herdr", "pane", "get", "w1:p2"}, result: paneJSON("w1:p2", "w1:t1", project, developerPaneLabel, map[string]string{"cagy_owner": developerName, "cagy_role": "developer"})},
		// Steps for ensureNoInterruptedTask: inspectTaskJournal
		{want: []string{"herdr", "agent", "get", developerName}, result: agentJSONWithSession("w1:p2", "w1", project, "idle", testConversationID)},
		{want: []string{"herdr", "pane", "get", "w1:p1"}, result: supervisorPaneJSON(project)},
		{want: []string{"herdr", "pane", "get", "w1:p2"}, result: paneJSON("w1:p2", "w1:t1", project, developerPaneLabel, map[string]string{"cagy_owner": developerName, "cagy_role": "developer"})},
		// Steps for reportTaskJournal: inspectTaskJournal
		{want: []string{"herdr", "agent", "get", developerName}, result: agentJSONWithSession("w1:p2", "w1", project, "idle", testConversationID)},
		{want: []string{"herdr", "pane", "get", "w1:p1"}, result: supervisorPaneJSON(project)},
		{want: []string{"herdr", "pane", "get", "w1:p2"}, result: paneJSON("w1:p2", "w1:t1", project, developerPaneLabel, map[string]string{"cagy_owner": developerName, "cagy_role": "developer"})},
	}
	runner := &scriptedRunner{t: t, steps: steps}
	app := New(runner, &strings.Builder{}, &strings.Builder{})
	app.stateDir = stateDir
	app.agyBrainRoot = brainRoot
	app.getenv = cagyEnv(project, developerName)
	app.now = func() time.Time { return now.Add(time.Hour) }

	// 1. getTaskStatus
	_, _ = app.getTaskStatus(context.Background())
	// 2. getDeveloperStatus
	_, _ = app.getDeveloperStatus(context.Background())
	// 3. ensureNoInterruptedTask
	info, _ := app.context()
	_ = app.ensureNoInterruptedTask(context.Background(), info)
	// 4. reportTaskJournal
	_ = app.reportTaskJournal(context.Background())
	// 5. unconfirmed forgetTask (returns error before calling runner)
	_, _ = app.forgetTask(context.Background(), false)

	runner.assertDone()

	// Verify file is bit-for-bit unchanged
	afterData, err := os.ReadFile(filePath)
	if err != nil {
		t.Fatal(err)
	}
	if string(afterData) != string(data) {
		t.Fatalf("journal file was mutated by inspection!\nbefore: %s\nafter: %s", string(data), string(afterData))
	}
	afterStat, err := os.Stat(filePath)
	if err != nil {
		t.Fatal(err)
	}
	if !afterStat.ModTime().Equal(initialStat.ModTime()) {
		t.Fatalf("journal mtime changed from %v to %v", initialStat.ModTime(), afterStat.ModTime())
	}
}

func TestForgetStaleBackgroundPendingWhenDeveloperIdle(t *testing.T) {
	project, _ := filepath.EvalSymlinks(t.TempDir())
	stateDir := filepath.Join(t.TempDir(), "private-state")
	if err := ensurePrivateStateDir(stateDir); err != nil {
		t.Fatal(err)
	}
	brainRoot := t.TempDir()
	developerName := developerName("w1", "w1:p1")
	task := "Interrupted task with stale pending marker"
	// Write transcript with a background task that never completed
	taskDir := filepath.Join(brainRoot, testConversationID, ".system_generated", "logs")
	_ = os.MkdirAll(taskDir, 0o700)
	transcriptPath := filepath.Join(taskDir, "transcript.jsonl")
	step1 := `{"step_index":1,"source":"USER_EXPLICIT","type":"USER_INPUT","status":"DONE","content":"<USER_REQUEST>\n` + task + `\n</USER_REQUEST>"}` + "\n"
	step2 := fmt.Sprintf("{\"step_index\":2,\"source\":\"MODEL\",\"type\":\"GENERIC\",\"status\":\"RUNNING\",\"content\":\"Tool is running as a background task with task id: %s/task-1\\n\"}\n", testConversationID)
	if err := os.WriteFile(transcriptPath, []byte(step1+step2), 0o600); err != nil {
		t.Fatal(err)
	}

	runner := &scriptedRunner{t: t, steps: []runStep{
		// Steps for inspectTaskJournal during forgetTask
		{want: []string{"herdr", "agent", "get", developerName}, result: agentJSONWithSession("w1:p2", "w1", project, "idle", testConversationID)},
		{want: []string{"herdr", "pane", "get", "w1:p1"}, result: supervisorPaneJSON(project)},
		{want: []string{"herdr", "pane", "get", "w1:p2"}, result: paneJSON("w1:p2", "w1:t1", project, developerPaneLabel, map[string]string{"cagy_owner": developerName, "cagy_role": "developer"})},
		{want: []string{"herdr", "agent", "read", developerName, "--source", "visible", "--lines", "80"}, result: textResult(">\n────────────────────\n? for shortcuts\n")},
	}}
	app := New(runner, &strings.Builder{}, &strings.Builder{})
	app.stateDir = stateDir
	app.agyBrainRoot = brainRoot
	app.getenv = cagyEnv(project, developerName)
	info, _ := app.context()
	developer := herdr.AgentInfo{Agent: "agy", PaneID: "w1:p2", WorkspaceID: "w1", TabID: "w1:t1", ForegroundCWD: project, AgentSession: &herdr.AgentSessionInfo{Source: "herdr:antigravity_cli", Agent: "agy", Kind: "id", Value: testConversationID}}
	if err := app.beginTaskTracking(info, developer, task, transcript.Checkpoint{SessionID: testConversationID}, taskPhaseMonitoring); err != nil {
		t.Fatal(err)
	}
	app.activeTask = nil

	// Forget with confirmation should succeed because the verified developer is idle in the pane
	out, err := app.forgetTask(context.Background(), true)
	if err != nil {
		t.Fatalf("expected successful forget of stale background task, got err: %v", err)
	}
	if out.Status != "forgotten" {
		t.Fatalf("expected status forgotten, got: %s", out.Status)
	}
	if _, exists, _ := app.loadTaskJournal(developerName); exists {
		t.Fatal("journal should be deleted after forgetTask")
	}
	runner.assertDone()
}

type errorWriter struct {
	err error
}

func (w *errorWriter) Write(p []byte) (n int, err error) {
	return 0, w.err
}

func TestRecoverInterruptedTaskPreservesReceiptOnStdoutFailure(t *testing.T) {
	project, _ := filepath.EvalSymlinks(t.TempDir())
	stateDir := filepath.Join(t.TempDir(), "private-state")
	if err := ensurePrivateStateDir(stateDir); err != nil {
		t.Fatal(err)
	}
	brainRoot := t.TempDir()
	developerName := developerName("w1", "w1:p1")
	task := "Completed task awaiting recovery"
	answer := "Important finished work"
	writeAgyTranscript(t, brainRoot, testConversationID, task, answer)

	runner := &scriptedRunner{t: t, steps: []runStep{
		// Step for inspectTaskJournal during first recovery attempt
		{want: []string{"herdr", "agent", "get", developerName}, result: agentJSONWithSession("w1:p2", "w1", project, "idle", testConversationID)},
		{want: []string{"herdr", "pane", "get", "w1:p1"}, result: supervisorPaneJSON(project)},
		{want: []string{"herdr", "pane", "get", "w1:p2"}, result: paneJSON("w1:p2", "w1:t1", project, developerPaneLabel, map[string]string{"cagy_owner": developerName, "cagy_role": "developer"})},
		// Steps for inspectTaskJournal during second recovery attempt
		{want: []string{"herdr", "agent", "get", developerName}, result: agentJSONWithSession("w1:p2", "w1", project, "idle", testConversationID)},
		{want: []string{"herdr", "pane", "get", "w1:p1"}, result: supervisorPaneJSON(project)},
		{want: []string{"herdr", "pane", "get", "w1:p2"}, result: paneJSON("w1:p2", "w1:t1", project, developerPaneLabel, map[string]string{"cagy_owner": developerName, "cagy_role": "developer"})},
	}}

	failOut := &errorWriter{err: errors.New("simulated stdout broken pipe")}
	app := New(runner, failOut, &strings.Builder{})
	app.stateDir = stateDir
	app.agyBrainRoot = brainRoot
	app.getenv = cagyEnv(project, developerName)
	info, _ := app.context()
	developer := herdr.AgentInfo{
		Agent:         "agy",
		PaneID:        "w1:p2",
		WorkspaceID:   "w1",
		TabID:         "w1:t1",
		ForegroundCWD: project,
		AgentSession:  &herdr.AgentSessionInfo{Source: "herdr:antigravity_cli", Agent: "agy", Kind: "id", Value: testConversationID},
	}
	if err := app.beginTaskTracking(info, developer, task, transcript.Checkpoint{SessionID: testConversationID}, taskPhaseCompleted); err != nil {
		t.Fatal(err)
	}
	app.activeTask = nil

	// 1. First recovery attempt fails on stdout write
	err := app.recoverInterruptedTask(context.Background())
	if err == nil || !strings.Contains(err.Error(), "simulated stdout broken pipe") {
		t.Fatalf("expected stdout broken pipe error, got: %v", err)
	}

	// 2. Journal must still exist on disk with the persisted delivery receipt
	journal, exists, err := app.loadTaskJournal(developerName)
	if err != nil || !exists {
		t.Fatalf("journal should exist after stdout failure, exists=%v, err=%v", exists, err)
	}
	if journal.DeliveryReceipt == "" || !isValidDeliveryReceipt(journal.DeliveryReceipt) {
		t.Fatalf("expected valid persisted receipt on disk, got: %q", journal.DeliveryReceipt)
	}
	persistedReceipt := journal.DeliveryReceipt

	// 3. Second recovery attempt with working stdout uses the same persisted receipt
	var goodOut strings.Builder
	app.stdout = &goodOut
	if err := app.recoverInterruptedTask(context.Background()); err != nil {
		t.Fatalf("second recovery attempt failed: %v", err)
	}
	if strings.TrimSpace(goodOut.String()) != answer {
		t.Fatalf("output = %q, want %q", goodOut.String(), answer)
	}

	// 4. Journal must be acknowledged and removed
	if _, exists, _ := app.loadTaskJournal(developerName); exists {
		t.Fatal("journal should be deleted after successful recovery acknowledgment")
	}
	// Verify that the receipt used matched the persisted one
	if subtle.ConstantTimeCompare([]byte(journal.DeliveryReceipt), []byte(persistedReceipt)) != 1 {
		t.Fatal("receipt mismatch")
	}
	runner.assertDone()
}
