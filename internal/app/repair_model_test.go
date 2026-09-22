package app

import (
	"context"
	"errors"
	"fmt"
	"io"
	"strings"
	"testing"

	"github.com/kazimshah39/herdr-tandem/internal/herdr"
	proc "github.com/kazimshah39/herdr-tandem/internal/process"
	"github.com/kazimshah39/herdr-tandem/internal/supervisor"
)

type repairModelTestRunner struct {
	project         string
	supervisorPane  string
	developerPane   string
	developerName   string
	startedSession  string
	splitPaneTokens map[string]string
	existingPanes   []herdr.PaneInfo
	calls           [][]string
}

func (r *repairModelTestRunner) LookPath(name string) (string, error) { return "/bin/" + name, nil }
func (r *repairModelTestRunner) Run(_ context.Context, args ...string) (proc.Result, error) {
	r.calls = append(r.calls, append([]string(nil), args...))
	joined := strings.Join(args, " ")
	ok := proc.Result{ExitCode: 0, Stdout: `{"id":"x","result":{"type":"ok"}}`}

	sessionVal := "11111111-1111-1111-1111-111111111111"
	if r.startedSession != "" {
		sessionVal = r.startedSession
	}

	switch {
	case joined == "herdr integration status":
		return proc.Result{ExitCode: 0, Stdout: "antigravity-cli: current test\n"}, nil
	case joined == "herdr pane current --current":
		return proc.Result{ExitCode: 0, Stdout: fmt.Sprintf(`{"id":"x","result":{"type":"pane_info","pane":{"pane_id":%q,"workspace_id":"w1","tab_id":"w1:t1","cwd":%q,"tokens":{}}}}`, r.supervisorPane, r.project)}, nil
	case joined == "herdr pane get "+r.supervisorPane:
		return proc.Result{ExitCode: 0, Stdout: fmt.Sprintf(`{"id":"x","result":{"type":"pane_info","pane":{"pane_id":%q,"workspace_id":"w1","tab_id":"w1:t1","cwd":%q,"tokens":{"herdr_tandem_owner":%q,"herdr_tandem_role":"supervisor"}}}}`, r.supervisorPane, r.project, r.developerName)}, nil
	case joined == "herdr pane get "+r.developerPane:
		return proc.Result{ExitCode: 0, Stdout: fmt.Sprintf(`{"id":"x","result":{"type":"pane_info","pane":{"pane_id":%q,"workspace_id":"w1","tab_id":"w1:t1","cwd":%q,"tokens":{"herdr_tandem_owner":%q,"herdr_tandem_role":"developer"}}}}`, r.developerPane, r.project, r.developerName)}, nil
	case joined == "herdr pane list --workspace w1":
		if len(r.existingPanes) > 0 {
			var paneJSONs []string
			for _, p := range r.existingPanes {
				var tokenPairs []string
				for k, v := range p.Tokens {
					tokenPairs = append(tokenPairs, fmt.Sprintf("%q:%q", k, v))
				}
				tokensObj := "{" + strings.Join(tokenPairs, ",") + "}"
				paneJSONs = append(paneJSONs, fmt.Sprintf(`{"pane_id":%q,"workspace_id":%q,"tab_id":%q,"cwd":%q,"tokens":%s}`, p.PaneID, p.WorkspaceID, p.TabID, p.CWD, tokensObj))
			}
			return proc.Result{ExitCode: 0, Stdout: fmt.Sprintf(`{"id":"x","result":{"type":"pane_list","panes":[%s]}}`, strings.Join(paneJSONs, ","))}, nil
		}
		return proc.Result{ExitCode: 0, Stdout: `{"id":"x","result":{"type":"pane_list","panes":[]}}`}, nil
	case strings.HasPrefix(joined, "herdr agent get "):
		return proc.Result{ExitCode: 1, Stderr: `{"id":"x","error":{"code":"agent_not_found","message":"missing"}}`}, nil
	case strings.HasPrefix(joined, "herdr pane split "):
		return proc.Result{ExitCode: 0, Stdout: fmt.Sprintf(`{"id":"x","result":{"type":"pane_info","pane":{"pane_id":%q,"workspace_id":"w1","tab_id":"w1:t1","cwd":%q,"tokens":{}}}}`, r.developerPane, r.project)}, nil
	case strings.HasPrefix(joined, "herdr agent start "):
		return proc.Result{ExitCode: 0, Stdout: fmt.Sprintf(`{"id":"x","result":{"type":"agent_started","agent":{"name":%q,"pane_id":%q,"workspace_id":"w1","tab_id":"w1:t1","cwd":%q,"agent":"agy","agent_status":"idle","agent_session":{"source":"herdr:antigravity_cli","agent":"agy","kind":"id","value":%q}}}}`, r.developerName, r.developerPane, r.project, sessionVal)}, nil
	case strings.HasPrefix(joined, "herdr pane process-info "):
		return proc.Result{ExitCode: 0, Stdout: fmt.Sprintf(`{"id":"x","result":{"type":"pane_process_info","process_info":{"pane_id":%q,"foreground_processes":[{"name":"zsh","argv":["-zsh"]}]}}}`, r.developerPane)}, nil
	case strings.HasPrefix(joined, "herdr pane read "):
		return proc.Result{ExitCode: 0, Stdout: "? for shortcuts"}, nil
	case strings.HasPrefix(joined, "herdr pane wait-output "), strings.HasPrefix(joined, "herdr pane rename "), strings.HasPrefix(joined, "herdr pane report-metadata "):
		return ok, nil
	case strings.HasPrefix(joined, "herdr pane close "):
		return ok, nil
	case strings.HasPrefix(joined, "herdr pane send-keys "):
		return ok, nil
	default:
		return proc.Result{}, errors.New("unexpected call: " + joined)
	}
}
func (r *repairModelTestRunner) RunAttached(string, []string, []string) error { return nil }

func setupRepairApp(t *testing.T, runner *repairModelTestRunner, model string) (*App, runtimeContext) {
	project, err := resolveProject(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	runner.project = project
	info := runtimeContext{
		supervisorKind: supervisor.CodexID,
		developerKind:  "agy",
		workspaceID:    "w1",
		supervisor:     runner.supervisorPane,
		developer:      runner.developerName,
		project:        project,
	}
	app := New(runner, io.Discard, io.Discard)
	app.supervisor = supervisor.Codex{}
	app.stateDir = t.TempDir()
	app.token = func() (string, error) { return "runtime-repair-model", nil }
	app.reportSidebarVisibility = func(context.Context, string, herdr.PaneVisibility) error { return nil }

	_, err = app.runtimeManager().Prepare(context.Background(), runtimeRecord{
		SidebarMode:      string(sidebarModeCompact),
		SupervisorKind:   supervisor.CodexID,
		DeveloperKind:    "agy",
		WorkspaceID:      "w1",
		SupervisorPaneID: info.supervisor,
		Developer:        info.developer,
		Project:          project,
		DeveloperModel:   model,
	})
	if err != nil {
		t.Fatal(err)
	}
	return app, info
}

func TestRepairIncludesStoredDeveloperModelFresh(t *testing.T) {
	runner := &repairModelTestRunner{
		supervisorPane: "w1:p1",
		developerPane:  "w1:p2",
		developerName:  developerName("w1", "w1:p1"),
	}
	app, info := setupRepairApp(t, runner, "gemini-2.5-flash")

	agent, err := app.repairMissingDeveloper(context.Background(), info)
	if err != nil {
		t.Fatalf("repair failed: %v", err)
	}
	if agent.PaneID != "w1:p2" {
		t.Fatalf("unexpected pane: %v", agent)
	}

	var startCall []string
	for _, call := range runner.calls {
		if len(call) >= 3 && call[0] == "herdr" && call[1] == "agent" && call[2] == "start" {
			startCall = call
			break
		}
	}
	if startCall == nil {
		t.Fatal("herdr agent start not called")
	}
	joined := strings.Join(startCall, " ")
	if !strings.Contains(joined, "--model gemini-2.5-flash") {
		t.Fatalf("agent start missing stored developer model: %s", joined)
	}
}

func TestRepairIncludesStoredDeveloperModelExactResume(t *testing.T) {
	sessionID := "11111111-1111-1111-1111-111111111111"
	runner := &repairModelTestRunner{
		supervisorPane: "w1:p1",
		developerPane:  "w1:p2",
		developerName:  developerName("w1", "w1:p1"),
		startedSession: sessionID,
	}
	// Configure an existing repair pane with saved session token
	app, info := setupRepairApp(t, runner, "gemini-2.5-pro")
	runner.existingPanes = []herdr.PaneInfo{
		{
			PaneID:      "w1:p2",
			WorkspaceID: "w1",
			TabID:       "w1:t1",
			CWD:         runner.project,
			Tokens: map[string]string{
				"herdr_tandem_owner":   info.developer,
				"herdr_tandem_role":    "repair",
				"herdr_tandem_session": sessionID,
				agySessionStateToken:   agySessionStateReady,
			},
		},
	}

	agent, err := app.repairMissingDeveloper(context.Background(), info)
	if err != nil {
		t.Fatalf("repair failed: %v", err)
	}
	if agent.PaneID != "w1:p2" {
		t.Fatalf("unexpected pane: %v", agent)
	}

	var startCall []string
	for _, call := range runner.calls {
		if len(call) >= 3 && call[0] == "herdr" && call[1] == "agent" && call[2] == "start" {
			startCall = call
			break
		}
	}
	if startCall == nil {
		t.Fatal("herdr agent start not called")
	}
	joined := strings.Join(startCall, " ")
	// Resume ordering check: --conversation <id> must be first arg before other flags, and --model must be present
	if !strings.Contains(joined, "--conversation "+sessionID) {
		t.Fatalf("resume start missing conversation ID: %s", joined)
	}
	if !strings.Contains(joined, "--model gemini-2.5-pro") {
		t.Fatalf("resume start missing stored developer model: %s", joined)
	}
}

func TestRepairLegacyRecordOmitsModel(t *testing.T) {
	runner := &repairModelTestRunner{
		supervisorPane: "w1:p1",
		developerPane:  "w1:p2",
		developerName:  developerName("w1", "w1:p1"),
	}
	// Model is empty string (legacy record)
	app, info := setupRepairApp(t, runner, "")

	agent, err := app.repairMissingDeveloper(context.Background(), info)
	if err != nil {
		t.Fatalf("repair failed: %v", err)
	}
	if agent.PaneID != "w1:p2" {
		t.Fatalf("unexpected pane: %v", agent)
	}

	var startCall []string
	for _, call := range runner.calls {
		if len(call) >= 3 && call[0] == "herdr" && call[1] == "agent" && call[2] == "start" {
			startCall = call
			break
		}
	}
	if startCall == nil {
		t.Fatal("herdr agent start not called")
	}
	joined := strings.Join(startCall, " ")
	if strings.Contains(joined, "--model") {
		t.Fatalf("legacy start should not contain --model, got: %s", joined)
	}
}

func TestRepairRejectsMismatchedResumeSession(t *testing.T) {
	expectedSession := "11111111-1111-1111-1111-111111111111"
	differentSession := "22222222-2222-2222-2222-222222222222"
	runner := &repairModelTestRunner{
		supervisorPane: "w1:p1",
		developerPane:  "w1:p2",
		developerName:  developerName("w1", "w1:p1"),
		startedSession: differentSession, // agy returns different session ID
	}
	app, info := setupRepairApp(t, runner, "gemini-2.5-flash")
	runner.existingPanes = []herdr.PaneInfo{
		{
			PaneID:      "w1:p2",
			WorkspaceID: "w1",
			TabID:       "w1:t1",
			CWD:         runner.project,
			Tokens: map[string]string{
				"herdr_tandem_owner":   info.developer,
				"herdr_tandem_role":    "repair",
				"herdr_tandem_session": expectedSession,
				agySessionStateToken:   agySessionStateReady,
			},
		},
	}

	_, err := app.repairMissingDeveloper(context.Background(), info)
	if err == nil {
		t.Fatal("expected error on session mismatch")
	}
	if !strings.Contains(err.Error(), "resumed a different conversation") {
		t.Fatalf("expected continuity error, got: %v", err)
	}
}
