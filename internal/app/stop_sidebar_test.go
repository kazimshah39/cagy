package app

import (
	"context"
	"fmt"
	"io"
	"strings"
	"testing"
	"time"

	"github.com/kazimshah39/herdr-tandem/internal/herdr"
	proc "github.com/kazimshah39/herdr-tandem/internal/process"
)

type stopSidebarRunner struct {
	project      string
	developer    string
	agentRunning bool
	paneExists   bool
	events       []string
}

func (r *stopSidebarRunner) LookPath(name string) (string, error) { return "/bin/" + name, nil }
func (r *stopSidebarRunner) Run(_ context.Context, args ...string) (proc.Result, error) {
	joined := strings.Join(args, " ")
	switch joined {
	case "herdr pane get w1:p1":
		return proc.Result{ExitCode: 0, Stdout: fmt.Sprintf(`{"id":"x","result":{"type":"pane_info","pane":{"pane_id":"w1:p1","workspace_id":"w1","tab_id":"w1:t1","cwd":%q,"tokens":{}}}}`, r.project)}, nil
	case "herdr pane get w1:p2":
		if !r.paneExists {
			return proc.Result{ExitCode: 1, Stderr: `{"id":"x","error":{"code":"pane_not_found","message":"missing"}}`}, nil
		}
		return proc.Result{ExitCode: 0, Stdout: fmt.Sprintf(`{"id":"x","result":{"type":"pane_info","pane":{"pane_id":"w1:p2","workspace_id":"w1","tab_id":"w1:t1","cwd":%q,"tokens":{"herdr_tandem_owner":%q,"herdr_tandem_role":"developer","herdr_tandem_runtime_id":"runtime-stop"}}}}`, r.project, r.developer)}, nil
	case "herdr agent get " + r.developer:
		if !r.agentRunning {
			return proc.Result{ExitCode: 1, Stderr: `{"id":"x","error":{"code":"agent_not_found","message":"missing"}}`}, nil
		}
		return proc.Result{ExitCode: 0, Stdout: fmt.Sprintf(`{"id":"x","result":{"type":"agent_info","agent":{"name":%q,"pane_id":"w1:p2","workspace_id":"w1","tab_id":"w1:t1","cwd":%q,"agent":"agy","agent_status":"idle"}}}`, r.developer, r.project)}, nil
	case "herdr agent send-keys " + r.developer + " ctrl+c":
		r.events = append(r.events, "send-keys")
		r.agentRunning = false
		return proc.Result{ExitCode: 0, Stdout: `{"id":"x","result":{"type":"ok"}}`}, nil
	case "herdr pane close w1:p2":
		r.events = append(r.events, "close-pane")
		r.paneExists = false
		return proc.Result{ExitCode: 0, Stdout: `{"id":"x","result":{"type":"ok"}}`}, nil
	default:
		return proc.Result{}, fmt.Errorf("unexpected stop call: %s", joined)
	}
}
func (r *stopSidebarRunner) RunAttached(string, []string, []string) error { return nil }

func TestStopRestoresCompactSupervisorBeforeDeveloperShutdownAndClearsLastView(t *testing.T) {
	project, err := resolveProject(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	developer := developerName("w1", "w1:p1")
	runner := &stopSidebarRunner{project: project, developer: developer, agentRunning: true, paneExists: true}
	app := New(runner, io.Discard, io.Discard)
	app.stateDir = t.TempDir()
	app.developerPoll = time.Millisecond
	app.agentStopEscalation = 10 * time.Millisecond
	app.agentStopTimeout = 10 * time.Millisecond
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
		case runtimeIDEnv:
			return "runtime-stop"
		case sidebarModeEnv:
			return string(sidebarModeCompact)
		}
		return ""
	}
	if _, err := app.runtimeManager().Prepare(context.Background(), runtimeRecord{RuntimeID: "runtime-stop", SidebarMode: string(sidebarModeCompact), SupervisorKind: "codex", DeveloperKind: "agy", WorkspaceID: "w1", SupervisorPaneID: "w1:p1", Developer: developer, DeveloperPaneID: "w1:p2", Project: project}); err != nil {
		t.Fatal(err)
	}
	app.reportSidebarVisibility = func(_ context.Context, pane string, visibility herdr.PaneVisibility) error {
		runner.events = append(runner.events, pane+"="+string(visibility))
		return nil
	}
	app.clearSidebarView = func(context.Context) error {
		runner.events = append(runner.events, "clear-view")
		return nil
	}
	if err := app.stop(context.Background()); err != nil {
		t.Fatal(err)
	}
	wantPrefix := []string{"w1:p1=visible", "w1:p2=hidden", "send-keys", "close-pane", "clear-view"}
	if strings.Join(runner.events, ",") != strings.Join(wantPrefix, ",") {
		t.Fatalf("events=%v want=%v", runner.events, wantPrefix)
	}
	if _, exists, err := app.runtimeManager().loadByIDUnlocked("runtime-stop"); err != nil || exists {
		t.Fatalf("runtime exists=%t err=%v", exists, err)
	}
}
