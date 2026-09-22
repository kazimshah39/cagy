package app

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"

	buildmeta "github.com/kazimshah39/herdr-tandem/internal/buildinfo"
	proc "github.com/kazimshah39/herdr-tandem/internal/process"
	"github.com/kazimshah39/herdr-tandem/internal/supervisor"
)

type lifecycleTestRunner struct {
	workspaceID    string
	project        string
	supervisorPane string
	developerPane  string
	developerName  string
	runAttachedErr error
	runAttachedFn  func(dir string, args, env []string) error
	lastDir        string
	lastArgs       []string
	lastEnv        []string
	calls          [][]string
}

func (r *lifecycleTestRunner) LookPath(name string) (string, error) { return "/bin/" + name, nil }
func (r *lifecycleTestRunner) Run(_ context.Context, args ...string) (proc.Result, error) {
	r.calls = append(r.calls, append([]string(nil), args...))
	joined := strings.Join(args, " ")
	ok := proc.Result{ExitCode: 0, Stdout: `{"id":"x","result":{"type":"ok"}}`}
	ws := r.workspaceID
	if ws == "" {
		ws = "w1"
	}
	switch {
	case joined == "herdr integration status":
		return proc.Result{ExitCode: 0, Stdout: "antigravity-cli: current test\n"}, nil
	case joined == "herdr pane current --current":
		return proc.Result{ExitCode: 0, Stdout: fmt.Sprintf(`{"id":"x","result":{"type":"pane_info","pane":{"pane_id":%q,"workspace_id":%q,"tab_id":%q,"cwd":%q,"tokens":{}}}}`, r.supervisorPane, ws, ws+":t1", r.project)}, nil
	case joined == "herdr pane get "+r.supervisorPane:
		return proc.Result{ExitCode: 0, Stdout: fmt.Sprintf(`{"id":"x","result":{"type":"pane_info","pane":{"pane_id":%q,"workspace_id":%q,"tab_id":%q,"cwd":%q,"tokens":{"herdr_tandem_owner":%q,"herdr_tandem_role":"supervisor"}}}}`, r.supervisorPane, ws, ws+":t1", r.project, r.developerName)}, nil
	case strings.HasPrefix(joined, "herdr pane list --workspace "):
		return proc.Result{ExitCode: 0, Stdout: `{"id":"x","result":{"type":"pane_list","panes":[]}}`}, nil
	case strings.HasPrefix(joined, "herdr agent get "):
		return proc.Result{ExitCode: 1, Stderr: `{"id":"x","error":{"code":"agent_not_found","message":"missing"}}`}, nil
	case strings.HasPrefix(joined, "herdr pane split "):
		return proc.Result{ExitCode: 0, Stdout: fmt.Sprintf(`{"id":"x","result":{"type":"pane_info","pane":{"pane_id":%q,"workspace_id":%q,"tab_id":%q,"cwd":%q,"tokens":{}}}}`, r.developerPane, ws, ws+":t1", r.project)}, nil
	case strings.HasPrefix(joined, "herdr agent start "):
		return proc.Result{ExitCode: 0, Stdout: fmt.Sprintf(`{"id":"x","result":{"type":"agent_started","agent":{"name":%q,"pane_id":%q,"workspace_id":%q,"tab_id":%q,"cwd":%q,"agent":"agy","agent_status":"idle","agent_session":{"source":"herdr:antigravity_cli","agent":"agy","kind":"id","value":"11111111-1111-1111-1111-111111111111"}}}}`, r.developerName, r.developerPane, ws, ws+":t1", r.project)}, nil
	case strings.HasPrefix(joined, "herdr pane read "):
		return proc.Result{ExitCode: 0, Stdout: "? for shortcuts"}, nil
	case strings.HasPrefix(joined, "herdr pane wait-output "), strings.HasPrefix(joined, "herdr pane rename "), strings.HasPrefix(joined, "herdr pane report-metadata "):
		return ok, nil
	case joined == "agy --help":
		return proc.Result{ExitCode: 0, Stdout: "--agent --model --mode --dangerously-skip-permissions accept-edits --conversation"}, nil
	default:
		return proc.Result{}, errors.New("unexpected call: " + joined)
	}
}

func (r *lifecycleTestRunner) RunAttached(dir string, args []string, env []string) error {
	r.lastDir = dir
	r.lastArgs = append([]string(nil), args...)
	r.lastEnv = append([]string(nil), env...)
	if r.runAttachedFn != nil {
		return r.runAttachedFn(dir, args, env)
	}
	return r.runAttachedErr
}

func setupLifecycleApp(t *testing.T, runner *lifecycleTestRunner, supervisorAdapter supervisor.Adapter) (*App, string, string) {
	stateDir := t.TempDir()
	configRoot := t.TempDir()
	var err error
	runner.project, err = resolveProject(runner.project)
	if err != nil {
		t.Fatal(err)
	}
	app := New(runner, io.Discard, io.Discard)
	app.supervisor = supervisorAdapter
	app.stateDir = stateDir
	app.configRoot = configRoot
	app.token = func() (string, error) { return "test-runtime-1234", nil }
	app.checkPlatform = func() error { return nil }
	app.resolveExecutable = func() (string, error) { return "/tmp/herdr-tandem", nil }
	app.providerServiceCheck = func(context.Context) error { return nil }
	app.runningBuild = func() buildmeta.Identity { return buildmeta.Identity{} }
	app.getenv = func(key string) string {
		switch key {
		case "HERDR_ENV":
			return "1"
		case "HERDR_PANE_ID":
			return runner.supervisorPane
		case "HERDR_WORKSPACE_ID":
			return "w1"
		case "HERDR_TAB_ID":
			return "w1:t1"
		case "HERDR_SOCKET_PATH":
			return "/tmp/herdr.sock"
		case "HERDR_TANDEM_CONFIG_ROOT":
			return configRoot
		default:
			return ""
		}
	}
	return app, stateDir, configRoot
}

func TestStartupWithAgySupervisorAndModels(t *testing.T) {
	project, err := resolveProject(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	runner := &lifecycleTestRunner{
		project:        project,
		supervisorPane: "w1:p1",
		developerPane:  "w1:p2",
		developerName:  developerName("w1", "w1:p1"),
	}
	app, stateDir, configRoot := setupLifecycleApp(t, runner, supervisor.Agy{})
	app.supervisorModel = "gemini-2.5-pro"
	app.developerModel = "gemini-2.5-flash"

	runAttachedCalled := false
	runner.runAttachedFn = func(dir string, args, env []string) error {
		runAttachedCalled = true
		// Verify supervisor agent.md was prepared while attached
		agentFile := filepath.Join(configRoot, "agents", "herdr-tandem-test-runtime-1234", "agent.md")
		data, err := os.ReadFile(agentFile)
		if err != nil {
			t.Fatalf("expected agent.md to exist during RunAttached: %v", err)
		}
		if !strings.Contains(string(data), "name: herdr-tandem-test-runtime-1234") {
			t.Fatalf("agent.md content mismatch: %s", string(data))
		}

		// Verify runtime record has SupervisorAgentName while supervisor is running
		record, found, err := app.runtimeManager().Load()
		if err != nil || !found {
			t.Fatalf("expected 1 runtime record, found=%t, err=%v", found, err)
		}
		if record.SupervisorAgentName != "herdr-tandem-test-runtime-1234" {
			t.Fatalf("SupervisorAgentName=%q, want herdr-tandem-test-runtime-1234", record.SupervisorAgentName)
		}
		if record.SupervisorModel != "gemini-2.5-pro" {
			t.Fatalf("SupervisorModel=%q, want gemini-2.5-pro", record.SupervisorModel)
		}
		if record.DeveloperModel != "gemini-2.5-flash" {
			t.Fatalf("DeveloperModel=%q, want gemini-2.5-flash", record.DeveloperModel)
		}

		return nil
	}

	err = app.start(context.Background(), project, sidebarModeCompact)
	if err != nil {
		t.Fatalf("start failed: %v", err)
	}
	if !runAttachedCalled {
		t.Fatal("RunAttached was not called")
	}

	// Verify developer start call included developer model
	var devStartCall []string
	for _, call := range runner.calls {
		if len(call) >= 3 && call[0] == "herdr" && call[1] == "agent" && call[2] == "start" {
			devStartCall = call
			break
		}
	}
	if devStartCall == nil {
		t.Fatal("herdr agent start not found in calls")
	}
	joinedStart := strings.Join(devStartCall, " ")
	if !strings.Contains(joinedStart, "--model gemini-2.5-flash") {
		t.Fatalf("developer start call missing model: %s", joinedStart)
	}

	// Verify supervisor launch args included supervisor model and no project path
	if runner.lastDir != project {
		t.Fatalf("attached dir = %q, want %q", runner.lastDir, project)
	}
	for _, arg := range runner.lastArgs {
		if arg == project {
			t.Fatalf("project path %q leaked into supervisor argv: %v", arg, runner.lastArgs)
		}
	}
	joinedSupervisorArgs := strings.Join(runner.lastArgs, " ")
	if !strings.Contains(joinedSupervisorArgs, "--model gemini-2.5-pro") {
		t.Fatalf("supervisor args missing model: %s", joinedSupervisorArgs)
	}
	if !strings.Contains(joinedSupervisorArgs, "--agent herdr-tandem-test-runtime-1234") {
		t.Fatalf("supervisor args missing agent: %s", joinedSupervisorArgs)
	}

	// Verify cleanup: agent.md and its directory must be removed after exit
	agentDir := filepath.Join(configRoot, "agents", "herdr-tandem-test-runtime-1234")
	if _, err := os.Stat(agentDir); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("agent dir was not cleaned up: %v", err)
	}

	// Verify SupervisorAgentName was cleared from the preserved runtime record
	record, found, err := app.runtimeManager().Load()
	if err != nil || !found {
		t.Fatalf("expected runtime record to remain after supervisor exit, found=%t, err=%v", found, err)
	}
	if record.SupervisorAgentName != "" {
		t.Fatalf("SupervisorAgentName was not cleared after cleanup: %q", record.SupervisorAgentName)
	}
	_ = stateDir
}

func TestStartupSupervisorCleanupOnRunAttachedError(t *testing.T) {
	project := t.TempDir()
	runner := &lifecycleTestRunner{
		project:        project,
		supervisorPane: "w1:p1",
		developerPane:  "w1:p2",
		developerName:  developerName("w1", "w1:p1"),
		runAttachedErr: errors.New("simulated supervisor crash"),
	}
	app, _, configRoot := setupLifecycleApp(t, runner, supervisor.Agy{})

	err := app.start(context.Background(), project, sidebarModeCompact)
	if err == nil || !strings.Contains(err.Error(), "simulated supervisor crash") {
		t.Fatalf("expected crash error, got: %v", err)
	}

	// Even on error, artifact was cleaned up
	agentDir := filepath.Join(configRoot, "agents", "herdr-tandem-test-runtime-1234")
	if _, err := os.Stat(agentDir); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("agent dir was not cleaned up on supervisor crash: %v", err)
	}

	// Runtime record has SupervisorAgentName cleared
	record, found, err := app.runtimeManager().Load()
	if err != nil || !found {
		t.Fatalf("expected runtime record to remain, found=%t, err=%v", found, err)
	}
	if record.SupervisorAgentName != "" {
		t.Fatalf("SupervisorAgentName was not cleared: %q", record.SupervisorAgentName)
	}
}

func TestStartupSupervisorCleanupOnContextCancellation(t *testing.T) {
	project := t.TempDir()
	runner := &lifecycleTestRunner{
		project:        project,
		supervisorPane: "w1:p1",
		developerPane:  "w1:p2",
		developerName:  developerName("w1", "w1:p1"),
	}
	app, _, configRoot := setupLifecycleApp(t, runner, supervisor.Agy{})

	ctx, cancel := context.WithCancel(context.Background())
	runner.runAttachedFn = func(dir string, args, env []string) error {
		cancel() // Cancel the parent context!
		return ctx.Err()
	}

	err := app.start(ctx, project, sidebarModeCompact)
	if err == nil || !errors.Is(err, context.Canceled) {
		t.Fatalf("expected context.Canceled error, got: %v", err)
	}

	// Artifact must still be cleaned up using bounded context.WithoutCancel
	agentDir := filepath.Join(configRoot, "agents", "herdr-tandem-test-runtime-1234")
	if _, err := os.Stat(agentDir); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("agent dir was not cleaned up on cancellation: %v", err)
	}
}

type failingCleanupSupervisor struct {
	supervisor.Agy
	cleanupErr error
}

func (f failingCleanupSupervisor) CleanupLaunch(ctx context.Context, runner proc.Runner, launch supervisor.LaunchContext, artifact string) error {
	if f.cleanupErr != nil {
		return f.cleanupErr
	}
	return f.Agy.CleanupLaunch(ctx, runner, launch, artifact)
}

func TestStartupSupervisorCleanupFailureCombinesErrorAndPreservesRecord(t *testing.T) {
	project := t.TempDir()
	runner := &lifecycleTestRunner{
		project:        project,
		supervisorPane: "w1:p1",
		developerPane:  "w1:p2",
		developerName:  developerName("w1", "w1:p1"),
		runAttachedErr: errors.New("primary supervisor error"),
	}
	sup := failingCleanupSupervisor{
		Agy:        supervisor.Agy{},
		cleanupErr: errors.New("disk permission denied during cleanup"),
	}
	app, _, _ := setupLifecycleApp(t, runner, sup)

	err := app.start(context.Background(), project, sidebarModeCompact)
	if err == nil {
		t.Fatal("expected error, got nil")
	}
	if !strings.Contains(err.Error(), "primary supervisor error") {
		t.Fatalf("error missing primary error: %v", err)
	}
	if !strings.Contains(err.Error(), "disk permission denied during cleanup") {
		t.Fatalf("error missing cleanup error: %v", err)
	}

	// Because cleanup failed, SupervisorAgentName must be retained in the runtime record for recovery
	record, found, listErr := app.runtimeManager().Load()
	if listErr != nil || !found {
		t.Fatalf("expected 1 runtime record, found=%t, err=%v", found, listErr)
	}
	if record.SupervisorAgentName != "herdr-tandem-test-runtime-1234" {
		t.Fatalf("SupervisorAgentName should be preserved on cleanup failure, got: %q", record.SupervisorAgentName)
	}
}
