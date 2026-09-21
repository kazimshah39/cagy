package app

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/kazimshah39/herdr-tandem/internal/herdr"
	proc "github.com/kazimshah39/herdr-tandem/internal/process"
	"github.com/kazimshah39/herdr-tandem/internal/transcript"
)

type delegateSidebarRunner struct {
	project        string
	developer      string
	conversationID string
	transcriptRoot string
	task           string
	calls          [][]string
}

func (r *delegateSidebarRunner) LookPath(name string) (string, error) { return "/bin/" + name, nil }
func (r *delegateSidebarRunner) Run(_ context.Context, args ...string) (proc.Result, error) {
	r.calls = append(r.calls, append([]string(nil), args...))
	joined := strings.Join(args, " ")
	agentJSON := fmt.Sprintf(`{"name":%q,"pane_id":"w1:p2","workspace_id":"w1","tab_id":"w1:t1","cwd":%q,"agent":"agy","agent_status":"idle","agent_session":{"source":"herdr:antigravity_cli","agent":"agy","kind":"id","value":%q}}`, r.developer, r.project, r.conversationID)
	switch {
	case joined == "herdr pane get w1:p1":
		return proc.Result{ExitCode: 0, Stdout: fmt.Sprintf(`{"id":"x","result":{"type":"pane_info","pane":{"pane_id":"w1:p1","workspace_id":"w1","tab_id":"w1:t1","cwd":%q,"tokens":{}}}}`, r.project)}, nil
	case joined == "herdr pane get w1:p2":
		return proc.Result{ExitCode: 0, Stdout: fmt.Sprintf(`{"id":"x","result":{"type":"pane_info","pane":{"pane_id":"w1:p2","workspace_id":"w1","tab_id":"w1:t1","cwd":%q,"tokens":{"herdr_tandem_owner":%q,"herdr_tandem_role":"developer","herdr_tandem_session":%q,"herdr_tandem_session_state":"ready"}}}}`, r.project, r.developer, r.conversationID)}, nil
	case joined == "herdr agent get "+r.developer:
		return proc.Result{ExitCode: 0, Stdout: `{"id":"x","result":{"type":"agent_info","agent":` + agentJSON + `}}`}, nil
	case strings.HasPrefix(joined, "herdr agent prompt "+r.developer+" "):
		if err := r.writeCompletedTranscript(); err != nil {
			return proc.Result{}, err
		}
		return proc.Result{ExitCode: 0, Stdout: `{"id":"x","result":{"type":"agent_info","agent":` + agentJSON + `}}`}, nil
	case strings.HasPrefix(joined, "herdr agent wait "+r.developer+" "):
		return proc.Result{ExitCode: 1, Stderr: `{"id":"x","error":{"code":"timeout","message":"not blocked"}}`}, nil
	case strings.HasPrefix(joined, "herdr agent read "+r.developer+" "):
		return proc.Result{ExitCode: 0, Stdout: "────────\n? for shortcuts\n"}, nil
	case strings.HasPrefix(joined, "herdr pane report-metadata w1:p2 "):
		return proc.Result{ExitCode: 0, Stdout: `{"id":"x","result":{"type":"ok"}}`}, nil
	default:
		return proc.Result{}, fmt.Errorf("unexpected delegate call: %s", joined)
	}
}
func (r *delegateSidebarRunner) RunAttached([]string, []string) error { return nil }

func (r *delegateSidebarRunner) writeCompletedTranscript() error {
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
		line("MODEL", "PLANNER_RESPONSE", "completed answer")
	return os.WriteFile(paths.Full, []byte(content), 0o600)
}

func TestDelegateTaskSwitchesCompactSidebarToDeveloperThenBackToSupervisor(t *testing.T) {
	project, err := resolveProject(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	conversationID := "22222222-2222-2222-2222-222222222222"
	developer := developerName("w1", "w1:p1")
	task := "implement the requested change"
	runner := &delegateSidebarRunner{project: project, developer: developer, conversationID: conversationID, transcriptRoot: t.TempDir(), task: task}
	app := New(runner, io.Discard, io.Discard)
	app.stateDir = t.TempDir()
	app.transcriptRoot = runner.transcriptRoot
	app.developerPoll = time.Millisecond
	app.transcriptWait = time.Millisecond
	app.missingTranscriptWait = 10 * time.Millisecond
	app.taskDeadline = 100 * time.Millisecond
	app.healthyStallWindow = time.Second
	app.heartbeatInterval = time.Second
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
	prepared, err := app.runtimeManager().Prepare(context.Background(), runtimeRecord{RuntimeID: "runtime-delegate", SidebarMode: string(sidebarModeCompact), SupervisorKind: "codex", DeveloperKind: "agy", WorkspaceID: "w1", SupervisorPaneID: "w1:p1", Developer: developer, DeveloperPaneID: "w1:p2", Project: project})
	if err != nil {
		t.Fatal(err)
	}
	_ = prepared
	var visibility []string
	app.reportSidebarVisibility = func(_ context.Context, pane string, value herdr.PaneVisibility) error {
		visibility = append(visibility, pane+"="+string(value))
		return nil
	}
	delivery, err := app.delegateTask(context.Background(), task)
	if err != nil {
		t.Fatal(err)
	}
	if delivery.output != "completed answer" || delivery.receipt == "" {
		t.Fatalf("delivery=%+v", delivery)
	}
	want := []string{"w1:p2=visible", "w1:p1=hidden", "w1:p1=visible", "w1:p2=hidden"}
	if strings.Join(visibility, ",") != strings.Join(want, ",") {
		t.Fatalf("visibility=%v want=%v", visibility, want)
	}
}

func newDelegateSidebarFixture(t *testing.T, mode sidebarMode, task string, stderr io.Writer) *App {
	t.Helper()
	project, err := resolveProject(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	conversationID := "33333333-3333-3333-3333-333333333333"
	developer := developerName("w1", "w1:p1")
	runner := &delegateSidebarRunner{project: project, developer: developer, conversationID: conversationID, transcriptRoot: t.TempDir(), task: task}
	app := New(runner, io.Discard, stderr)
	app.stateDir = t.TempDir()
	app.transcriptRoot = runner.transcriptRoot
	app.developerPoll = time.Millisecond
	app.transcriptWait = time.Millisecond
	app.missingTranscriptWait = 10 * time.Millisecond
	app.taskDeadline = 100 * time.Millisecond
	app.healthyStallWindow = time.Second
	app.heartbeatInterval = time.Second
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
			return string(mode)
		}
		return ""
	}
	if _, err := app.runtimeManager().Prepare(context.Background(), runtimeRecord{RuntimeID: "runtime-delegate", SidebarMode: string(mode), SupervisorKind: "codex", DeveloperKind: "agy", WorkspaceID: "w1", SupervisorPaneID: "w1:p1", Developer: developer, DeveloperPaneID: "w1:p2", Project: project}); err != nil {
		t.Fatal(err)
	}
	return app
}

func TestDelegateTaskExpandedSidebarNeverHidesEitherAgent(t *testing.T) {
	task := "expanded task"
	app := newDelegateSidebarFixture(t, sidebarModeExpanded, task, io.Discard)
	var visibility []string
	app.reportSidebarVisibility = func(_ context.Context, pane string, value herdr.PaneVisibility) error {
		if value == herdr.PaneHidden {
			t.Fatalf("expanded task hid %s", pane)
		}
		visibility = append(visibility, pane+"="+string(value))
		return nil
	}
	if _, err := app.delegateTask(context.Background(), task); err != nil {
		t.Fatal(err)
	}
	want := []string{"w1:p1=visible", "w1:p2=visible", "w1:p1=visible", "w1:p2=visible"}
	if strings.Join(visibility, ",") != strings.Join(want, ",") {
		t.Fatalf("visibility=%v want=%v", visibility, want)
	}
}

func TestDelegateTaskContinuesWhenSidebarPresentationFailsWithoutLeakingTask(t *testing.T) {
	task := "secret prompt must not appear in diagnostics"
	var stderr strings.Builder
	var diagnostics strings.Builder
	app := newDelegateSidebarFixture(t, sidebarModeCompact, task, &stderr)
	app.diagnosticSink = func(line string) { diagnostics.WriteString(line) }
	app.reportSidebarVisibility = func(context.Context, string, herdr.PaneVisibility) error {
		return fmt.Errorf("metadata write failed")
	}
	delivery, err := app.delegateTask(context.Background(), task)
	if err != nil {
		t.Fatal(err)
	}
	if delivery.output != "completed answer" {
		t.Fatalf("delivery=%+v", delivery)
	}
	combined := stderr.String() + diagnostics.String()
	if strings.Contains(combined, task) || strings.Contains(combined, delivery.output) {
		t.Fatalf("presentation diagnostics leaked task content: %q", combined)
	}
	if !strings.Contains(stderr.String(), "sidebar state could not be updated") {
		t.Fatalf("missing presentation warning: %q", stderr.String())
	}
}

func TestRecoverTaskRestoresCompactSupervisor(t *testing.T) {
	task := "recoverable task"
	app := newDelegateSidebarFixture(t, sidebarModeCompact, task, io.Discard)
	app.reportSidebarVisibility = func(context.Context, string, herdr.PaneVisibility) error { return nil }
	if _, err := app.delegateTask(context.Background(), task); err != nil {
		t.Fatal(err)
	}
	var visibility []string
	app.reportSidebarVisibility = func(_ context.Context, pane string, value herdr.PaneVisibility) error {
		visibility = append(visibility, pane+"="+string(value))
		return nil
	}
	out, err := app.recoverTask(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if out.Answer != "completed answer" || out.Receipt == "" {
		t.Fatalf("recovery=%+v", out)
	}
	want := []string{"w1:p1=visible", "w1:p2=hidden"}
	if strings.Join(visibility, ",") != strings.Join(want, ",") {
		t.Fatalf("visibility=%v want=%v", visibility, want)
	}
}

func TestForgetTaskRestoresCompactSupervisor(t *testing.T) {
	task := "forgettable task"
	app := newDelegateSidebarFixture(t, sidebarModeCompact, task, io.Discard)
	app.reportSidebarVisibility = func(context.Context, string, herdr.PaneVisibility) error { return nil }
	if _, err := app.delegateTask(context.Background(), task); err != nil {
		t.Fatal(err)
	}
	var visibility []string
	app.reportSidebarVisibility = func(_ context.Context, pane string, value herdr.PaneVisibility) error {
		visibility = append(visibility, pane+"="+string(value))
		return nil
	}
	out, err := app.forgetTask(context.Background(), true)
	if err != nil {
		t.Fatal(err)
	}
	if out.Status != "forgotten" {
		t.Fatalf("forget=%+v", out)
	}
	want := []string{"w1:p1=visible", "w1:p2=hidden"}
	if strings.Join(visibility, ",") != strings.Join(want, ",") {
		t.Fatalf("visibility=%v want=%v", visibility, want)
	}
}

func TestRecoverTaskRecoversUncertainTaskWhenDeveloperFinishedVisiblyIdleWithTranscriptAnswer(t *testing.T) {
	task := "interrupted task that finished later"
	app := newDelegateSidebarFixture(t, sidebarModeCompact, task, io.Discard)
	app.reportSidebarVisibility = func(context.Context, string, herdr.PaneVisibility) error { return nil }

	info, err := app.context()
	if err != nil {
		t.Fatal(err)
	}
	developer, err := app.herdr.GetAgent(context.Background(), info.developer)
	if err != nil {
		t.Fatal(err)
	}
	checkpoint, err := app.transcriptCheckpoint(developer)
	if err != nil {
		t.Fatal(err)
	}

	// 1. Begin tracking as if a task was submitted
	if err := app.beginTaskTracking(info, developer, task, checkpoint, taskPhaseSubmitting); err != nil {
		t.Fatal(err)
	}
	// 2. Caller interrupted (e.g. Ctrl-C), journal transitions to phase uncertain
	if err := app.setTrackedPhase(taskPhaseUncertain, developer); err != nil {
		t.Fatal(err)
	}

	// 3. agy kept working and writes completed transcript
	runner := app.runner.(*delegateSidebarRunner)
	if err := runner.writeCompletedTranscript(); err != nil {
		t.Fatal(err)
	}

	// 4. task_status should now report completed_unacknowledged (recoverable)
	status, err := app.getTaskStatus(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if status.Status != "completed_unacknowledged" || !status.Recoverable || !status.AcknowledgementRequired {
		t.Fatalf("unexpected status: %+v", status)
	}

	// 5. recoverTask returns answer and receipt without resubmitting
	startCallCount := len(runner.calls)
	recovered, err := app.recoverTask(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if recovered.Status != "completed_unacknowledged" || recovered.Answer != "completed answer" || recovered.Receipt == "" || !recovered.AcknowledgementRequired {
		t.Fatalf("unexpected recovered result: %+v", recovered)
	}

	// Verify no new prompts were submitted
	for _, call := range runner.calls[startCallCount:] {
		if len(call) >= 3 && call[0] == "herdr" && call[1] == "agent" && call[2] == "prompt" {
			t.Fatalf("recover_task unexpectedly resubmitted prompt: %#v", call)
		}
	}

	// 6. acknowledgeTask succeeds and clears journal
	if err := app.acknowledgeTask(context.Background(), recovered.Receipt); err != nil {
		t.Fatal(err)
	}
	finalStatus, err := app.getTaskStatus(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if finalStatus.Status != "none" {
		t.Fatalf("expected status none after acknowledge, got %q", finalStatus.Status)
	}
}

func TestRecoverTaskRefusesUncertainTaskWhileTranscriptNotComplete(t *testing.T) {
	task := "still incomplete task"
	app := newDelegateSidebarFixture(t, sidebarModeCompact, task, io.Discard)
	app.reportSidebarVisibility = func(context.Context, string, herdr.PaneVisibility) error { return nil }

	info, err := app.context()
	if err != nil {
		t.Fatal(err)
	}
	developer, err := app.herdr.GetAgent(context.Background(), info.developer)
	if err != nil {
		t.Fatal(err)
	}
	checkpoint, err := app.transcriptCheckpoint(developer)
	if err != nil {
		t.Fatal(err)
	}

	if err := app.beginTaskTracking(info, developer, task, checkpoint, taskPhaseUncertain); err != nil {
		t.Fatal(err)
	}

	// Transcript does not have completed answer
	status, err := app.getTaskStatus(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if status.Status != "uncertain" || status.Recoverable {
		t.Fatalf("expected uncertain and non-recoverable, got: %+v", status)
	}
	_, err = app.recoverTask(context.Background())
	if err == nil || !strings.Contains(err.Error(), "not safely recoverable yet") {
		t.Fatalf("expected recovery refusal, got %v", err)
	}
}
