package app

import (
	"context"
	"encoding/json"
	"errors"
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
		{want: []string{"herdr", "agent", "prompt", developerName, task, "--wait", "--timeout", "300000"}, result: jsonError("transport_closed", "caller disappeared")},
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
		{want: []string{"herdr", "agent", "prompt", developerName, task, "--wait", "--timeout", "300000"}, err: context.Canceled},
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
