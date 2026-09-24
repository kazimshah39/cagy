package app

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/kazimshah39/herdr-tandem/internal/herdr"
	proc "github.com/kazimshah39/herdr-tandem/internal/process"
	"github.com/kazimshah39/herdr-tandem/internal/transcript"
)

type asyncDelegateRunner struct {
	mu                    sync.Mutex
	project               string
	developer             string
	conversationID        string
	transcriptRoot        string
	task                  string
	submitted             bool
	complete              bool
	blocked               bool
	promptCount           int
	supervisorPromptCount int
	supervisorPromptText  string
	supervisorStatus      string
}

func (r *asyncDelegateRunner) LookPath(name string) (string, error) { return "/bin/" + name, nil }
func (r *asyncDelegateRunner) Run(_ context.Context, args ...string) (proc.Result, error) {
	joined := strings.Join(args, " ")
	if strings.HasPrefix(joined, "herdr agent wait ") {
		time.Sleep(time.Millisecond)
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	status := "idle"
	if r.submitted && !r.complete && !r.blocked {
		status = "working"
	}
	if r.blocked {
		status = "blocked"
	}
	agentJSON := fmt.Sprintf(`{"name":%q,"pane_id":"w1:p2","workspace_id":"w1","tab_id":"w1:t1","cwd":%q,"agent":"agy","agent_status":%q,"agent_session":{"source":"herdr:antigravity_cli","agent":"agy","kind":"id","value":%q}}`, r.developer, r.project, status, r.conversationID)
	switch {
	case joined == "herdr pane get w1:p1":
		return proc.Result{ExitCode: 0, Stdout: fmt.Sprintf(`{"id":"x","result":{"type":"pane_info","pane":{"pane_id":"w1:p1","workspace_id":"w1","tab_id":"w1:t1","cwd":%q,"tokens":{"herdr_tandem_owner":%q,"herdr_tandem_role":"supervisor"}}}}`, r.project, r.developer)}, nil
	case joined == "herdr pane get w1:p2":
		return proc.Result{ExitCode: 0, Stdout: fmt.Sprintf(`{"id":"x","result":{"type":"pane_info","pane":{"pane_id":"w1:p2","workspace_id":"w1","tab_id":"w1:t1","cwd":%q,"tokens":{"herdr_tandem_owner":%q,"herdr_tandem_role":"developer","herdr_tandem_session":%q,"herdr_tandem_session_state":"ready"}}}}`, r.project, r.developer, r.conversationID)}, nil
	case joined == "herdr agent get "+r.developer:
		return proc.Result{ExitCode: 0, Stdout: `{"id":"x","result":{"type":"agent_info","agent":` + agentJSON + `}}`}, nil
	case joined == "herdr agent get w1:p1":
		supervisorStatus := r.supervisorStatus
		if supervisorStatus == "" {
			supervisorStatus = "idle"
		}
		supervisorJSON := fmt.Sprintf(`{"name":"supervisor","pane_id":"w1:p1","workspace_id":"w1","tab_id":"w1:t1","cwd":%q,"agent":"codex","agent_status":%q}`, r.project, supervisorStatus)
		return proc.Result{ExitCode: 0, Stdout: `{"id":"x","result":{"type":"agent_info","agent":` + supervisorJSON + `}}`}, nil
	case strings.HasPrefix(joined, "herdr agent prompt "+r.developer+" "):
		r.promptCount++
		r.submitted = true
		return proc.Result{ExitCode: 1, Stderr: `{"id":"x","error":{"code":"timeout","message":"still working"}}`}, nil
	case strings.HasPrefix(joined, "herdr agent prompt w1:p1 "):
		r.supervisorPromptCount++
		r.supervisorPromptText = joined
		return proc.Result{ExitCode: 1, Stderr: `{"id":"x","error":{"code":"timeout","message":"supervisor accepted wake prompt"}}`}, nil
	case strings.HasPrefix(joined, "herdr agent wait "+r.developer+" "):
		if r.blocked {
			return proc.Result{ExitCode: 0, Stdout: `{"id":"x","result":{"type":"agent_info","agent":` + agentJSON + `}}`}, nil
		}
		return proc.Result{ExitCode: 1, Stderr: `{"id":"x","error":{"code":"timeout","message":"not blocked"}}`}, nil
	case strings.HasPrefix(joined, "herdr agent read "+r.developer+" "):
		if r.complete {
			if err := r.writeCompletedTranscriptLocked(); err != nil {
				return proc.Result{}, err
			}
			return proc.Result{ExitCode: 0, Stdout: "────────\n? for shortcuts\n"}, nil
		}
		if r.blocked {
			return proc.Result{ExitCode: 0, Stdout: "developer needs input\n"}, nil
		}
		return proc.Result{ExitCode: 0, Stdout: "────────\nesc to cancel\n"}, nil
	case strings.HasPrefix(joined, "herdr pane report-metadata w1:p2 "), strings.HasPrefix(joined, "herdr pane report-metadata w1:p1 "):
		return proc.Result{ExitCode: 0, Stdout: `{"id":"x","result":{"type":"ok"}}`}, nil
	default:
		return proc.Result{}, fmt.Errorf("unexpected async delegate call: %s", joined)
	}
}
func (r *asyncDelegateRunner) RunAttached(string, []string, []string) error { return nil }

func (r *asyncDelegateRunner) writeCompletedTranscriptLocked() error {
	paths, err := transcript.PathsFor(r.transcriptRoot, transcript.Ref{Source: "herdr:antigravity_cli", Agent: "agy", Kind: "id", Value: r.conversationID})
	if err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(paths.Full), 0o700); err != nil {
		return err
	}
	line := func(source, kind, content string) string {
		data, _ := json.Marshal(map[string]string{"source": source, "type": kind, "status": "DONE", "content": content})
		return string(data) + "\n"
	}
	content := line("USER_EXPLICIT", "USER_INPUT", "<USER_REQUEST>\n"+r.task+"\n</USER_REQUEST>") +
		line("MODEL", "PLANNER_RESPONSE", "async completed answer")
	return os.WriteFile(paths.Full, []byte(content), 0o600)
}

func (r *asyncDelegateRunner) setComplete() {
	r.mu.Lock()
	r.complete = true
	r.mu.Unlock()
}

func (r *asyncDelegateRunner) setBlocked() {
	r.mu.Lock()
	r.blocked = true
	r.mu.Unlock()
}

func (r *asyncDelegateRunner) setSupervisorStatus(status string) {
	r.mu.Lock()
	r.supervisorStatus = status
	r.mu.Unlock()
}

func (r *asyncDelegateRunner) prompts() int {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.promptCount
}

func (r *asyncDelegateRunner) supervisorPrompts() (int, string) {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.supervisorPromptCount, r.supervisorPromptText
}

func newAsyncDelegateApp(t *testing.T, task string) (*App, *asyncDelegateRunner) {
	t.Helper()
	project, err := resolveProject(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	developer := developerName("w1", "w1:p1")
	runner := &asyncDelegateRunner{
		project:        project,
		developer:      developer,
		conversationID: "44444444-4444-4444-4444-444444444444",
		transcriptRoot: t.TempDir(),
		task:           task,
	}
	app := New(runner, io.Discard, io.Discard)
	app.stateDir = t.TempDir()
	app.transcriptRoot = runner.transcriptRoot
	app.developerPoll = time.Millisecond
	app.transcriptWait = time.Millisecond
	app.missingTranscriptWait = 25 * time.Millisecond
	app.taskDeadline = 500 * time.Millisecond
	app.healthyStallWindow = time.Second
	app.heartbeatInterval = time.Second
	app.mcpSubmissionTimeout = 100 * time.Millisecond
	app.mcpInitialPromptWait = 5 * time.Millisecond
	app.supervisorNotifyTimeout = 100 * time.Millisecond
	app.supervisorNotifyPoll = time.Millisecond
	app.supervisorNotifyWait = 5 * time.Millisecond
	app.providerServiceCheck = func(context.Context) error { return nil }
	app.getenv = func(key string) string {
		switch key {
		case "HERDR_ENV":
			return "1"
		case "HERDR_WORKSPACE_ID":
			return "w1"
		case "HERDR_PANE_ID", "HERDR_TANDEM_SUPERVISOR_PANE_ID":
			return "w1:p1"
		case "HERDR_TANDEM_DEVELOPER":
			return developer
		case "HERDR_TANDEM_DEVELOPER_PANE_ID":
			return "w1:p2"
		case "HERDR_TANDEM_PROJECT_DIR":
			return project
		case sidebarModeEnv:
			return string(sidebarModeCompact)
		}
		return ""
	}
	if _, err := app.runtimeManager().Prepare(context.Background(), runtimeRecord{RuntimeID: "runtime-async", SidebarMode: string(sidebarModeCompact), SupervisorKind: "codex", DeveloperKind: "agy", WorkspaceID: "w1", SupervisorPaneID: "w1:p1", Developer: developer, DeveloperPaneID: "w1:p2", Project: project}); err != nil {
		t.Fatal(err)
	}
	app.reportSidebarVisibility = func(context.Context, string, herdr.PaneVisibility) error { return nil }
	return app, runner
}

func TestMCPStartupWakesSupervisorForPendingTerminalTask(t *testing.T) {
	app, runner := newAsyncDelegateApp(t, "task completed before MCP restart")
	info, err := app.context()
	if err != nil {
		t.Fatal(err)
	}
	now := time.Now().UTC()
	record := taskJournal{
		Version:          taskJournalVersion,
		SupervisorKind:   info.supervisorKind,
		DeveloperKind:    info.developerKind,
		WorkspaceID:      info.workspaceID,
		SupervisorPaneID: info.supervisor,
		Developer:        info.developer,
		DeveloperPaneID:  "w1:p2",
		Project:          info.project,
		TaskHash:         transcript.TaskHash(runner.task),
		Phase:            taskPhaseCompleted,
		StartedAt:        now,
		UpdatedAt:        now,
	}
	if err := app.writeTaskJournal(record); err != nil {
		t.Fatal(err)
	}
	app.supervisorNotifyDelay = 0
	app.schedulePendingSupervisorWake()
	waitForAsyncMonitors(t, app)
	if count, prompt := runner.supervisorPrompts(); count != 1 || !strings.Contains(prompt, "Call task_status now") {
		t.Fatalf("startup supervisor wake count=%d prompt=%q", count, prompt)
	}
}

func TestMCPDelegateTaskReturnsAfterOneSubmissionAndMonitoringSurvivesRequestCancellation(t *testing.T) {
	task := "private async task text"
	app, runner := newAsyncDelegateApp(t, task)
	var diagnostics strings.Builder
	var diagnosticsMu sync.Mutex
	app.diagnosticSink = func(line string) {
		diagnosticsMu.Lock()
		diagnostics.WriteString(line + "\n")
		diagnosticsMu.Unlock()
	}

	requestCtx, cancelRequest := context.WithCancel(context.Background())
	_, out, err := app.handleMCPDelegateTask(requestCtx, nil, DelegateTaskInput{Task: task})
	if err != nil {
		t.Fatal(err)
	}
	if out.Status != "running" || !strings.Contains(out.Message, "wait for the local monitor to wake you") || !strings.Contains(out.Message, "do not poll task_status") {
		t.Fatalf("delegate output=%+v", out)
	}
	if runner.prompts() != 1 {
		t.Fatalf("prompt count=%d, want 1", runner.prompts())
	}
	cancelRequest()

	status, err := app.getTaskStatus(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if status.Status != "running" && status.Status != "submitting" {
		t.Fatalf("status immediately after submission=%+v", status)
	}
	if _, _, err := app.handleMCPDelegateTask(context.Background(), nil, DelegateTaskInput{Task: task}); err == nil {
		t.Fatal("second delegate_task unexpectedly succeeded")
	}
	if runner.prompts() != 1 {
		t.Fatalf("task was submitted more than once: %d", runner.prompts())
	}

	runner.setComplete()
	waitForTaskStatus(t, app, "completed_unacknowledged")
	waitForAsyncMonitors(t, app)
	notifyCount, notifyPrompt := runner.supervisorPrompts()
	if notifyCount != 1 {
		t.Fatalf("supervisor notification count=%d, want 1", notifyCount)
	}
	if !strings.Contains(notifyPrompt, "Call task_status now") {
		t.Fatalf("supervisor notification prompt=%q", notifyPrompt)
	}
	if strings.Contains(notifyPrompt, task) {
		t.Fatalf("supervisor notification leaked task text: %q", notifyPrompt)
	}
	recovered, err := app.recoverTask(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if recovered.Answer != "async completed answer" || recovered.Receipt == "" || !recovered.AcknowledgementRequired {
		t.Fatalf("recovered=%+v", recovered)
	}
	if err := app.acknowledgeTask(context.Background(), recovered.Receipt); err != nil {
		t.Fatal(err)
	}
	waitForTaskStatus(t, app, "none")

	diagnosticsMu.Lock()
	logged := diagnostics.String()
	diagnosticsMu.Unlock()
	for _, code := range []string{"HTD-MCP-001", "HTD-MCP-002", "HTD-MON-001", "HTD-MON-003", "HTD-SUP-001", "HTD-SUP-002"} {
		if !strings.Contains(logged, code) {
			t.Fatalf("missing diagnostic code %s in %q", code, logged)
		}
	}
	if strings.Contains(logged, task) || strings.Contains(logged, recovered.Answer) || strings.Contains(logged, recovered.Receipt) {
		t.Fatalf("diagnostics leaked protected content: %q", logged)
	}
}

func TestAsyncMonitorWaitsForIdleSupervisorBeforeTerminalWake(t *testing.T) {
	app, runner := newAsyncDelegateApp(t, "task completed while supervisor is active")
	app.supervisorNotifyTimeout = 250 * time.Millisecond
	runner.setSupervisorStatus("working")
	if _, _, err := app.handleMCPDelegateTask(context.Background(), nil, DelegateTaskInput{Task: runner.task}); err != nil {
		t.Fatal(err)
	}
	runner.setComplete()
	waitForTaskStatus(t, app, "completed_unacknowledged")
	time.Sleep(10 * time.Millisecond)
	if count, _ := runner.supervisorPrompts(); count != 0 {
		t.Fatalf("supervisor was prompted while working: count=%d", count)
	}
	runner.setSupervisorStatus("idle")
	waitForAsyncMonitors(t, app)
	if count, _ := runner.supervisorPrompts(); count != 1 {
		t.Fatalf("supervisor notification count=%d, want 1", count)
	}
}

func TestAsyncMonitorDoesNotSendStaleWakeAfterAcknowledgement(t *testing.T) {
	app, runner := newAsyncDelegateApp(t, "task acknowledged before supervisor becomes idle")
	app.supervisorNotifyTimeout = 250 * time.Millisecond
	runner.setSupervisorStatus("working")
	if _, _, err := app.handleMCPDelegateTask(context.Background(), nil, DelegateTaskInput{Task: runner.task}); err != nil {
		t.Fatal(err)
	}
	runner.setComplete()
	waitForTaskStatus(t, app, "completed_unacknowledged")
	recovered, err := app.recoverTask(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if err := app.acknowledgeTask(context.Background(), recovered.Receipt); err != nil {
		t.Fatal(err)
	}
	waitForAsyncMonitors(t, app)
	if count, prompt := runner.supervisorPrompts(); count != 0 {
		t.Fatalf("stale supervisor notification count=%d prompt=%q", count, prompt)
	}
}

func TestAsyncMonitorRecordsBlockedTask(t *testing.T) {
	app, runner := newAsyncDelegateApp(t, "task that blocks")
	if _, _, err := app.handleMCPDelegateTask(context.Background(), nil, DelegateTaskInput{Task: runner.task}); err != nil {
		t.Fatal(err)
	}
	runner.setBlocked()
	waitForTaskStatus(t, app, "blocked")
	waitForAsyncMonitors(t, app)
}

func TestAsyncMonitorRecordsUncertainAfterDeadline(t *testing.T) {
	app, runner := newAsyncDelegateApp(t, "task that times out")
	app.taskDeadline = 15 * time.Millisecond
	app.missingTranscriptWait = 5 * time.Millisecond
	if _, _, err := app.handleMCPDelegateTask(context.Background(), nil, DelegateTaskInput{Task: runner.task}); err != nil {
		t.Fatal(err)
	}
	waitForTaskStatus(t, app, "uncertain")
	waitForAsyncMonitors(t, app)
	if runner.prompts() != 1 {
		t.Fatalf("prompt count=%d, want 1", runner.prompts())
	}
}

func waitForAsyncMonitors(t *testing.T, app *App) {
	t.Helper()
	done := make(chan struct{})
	go func() {
		app.monitorWG.Wait()
		close(done)
	}()
	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("background monitor did not finish")
	}
}

func waitForTaskStatus(t *testing.T, app *App, want string) *TaskStatusOutput {
	t.Helper()
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		status, err := app.getTaskStatus(context.Background())
		if err != nil {
			t.Fatal(err)
		}
		if status.Status == want {
			return status
		}
		time.Sleep(2 * time.Millisecond)
	}
	status, err := app.getTaskStatus(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	t.Fatalf("task status=%+v, want %q", status, want)
	return nil
}
