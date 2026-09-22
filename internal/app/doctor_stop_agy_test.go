package app

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/kazimshah39/herdr-tandem/internal/herdr"
	proc "github.com/kazimshah39/herdr-tandem/internal/process"
	"github.com/kazimshah39/herdr-tandem/internal/supervisor"
)

type doctorStopTestRunner struct {
	project        string
	supervisorPane string
	developerPane  string
	developerName  string
	calls          [][]string
}

func (r *doctorStopTestRunner) LookPath(name string) (string, error) { return "/bin/" + name, nil }
func (r *doctorStopTestRunner) Run(_ context.Context, args ...string) (proc.Result, error) {
	r.calls = append(r.calls, append([]string(nil), args...))
	joined := strings.Join(args, " ")
	ok := proc.Result{ExitCode: 0, Stdout: `{"id":"x","result":{"type":"ok"}}`}

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
		return proc.Result{ExitCode: 0, Stdout: `{"id":"x","result":{"type":"pane_list","panes":[]}}`}, nil
	case strings.HasPrefix(joined, "herdr agent get "):
		return proc.Result{ExitCode: 1, Stderr: `{"id":"x","error":{"code":"agent_not_found","message":"missing"}}`}, nil
	case strings.HasPrefix(joined, "herdr pane read "):
		return proc.Result{ExitCode: 0, Stdout: "? for shortcuts"}, nil
	case strings.HasPrefix(joined, "herdr pane wait-output "), strings.HasPrefix(joined, "herdr pane rename "), strings.HasPrefix(joined, "herdr pane report-metadata "), strings.HasPrefix(joined, "herdr pane close "):
		return ok, nil
	case joined == "agy --help":
		return proc.Result{ExitCode: 0, Stdout: "--agent --model --mode --dangerously-skip-permissions accept-edits --conversation"}, nil
	case joined == "agy models":
		return proc.Result{ExitCode: 0, Stdout: "gemini-3.8-flash-high\tGemini 3.8 Flash (High)\nclaude-opus-4-6-thinking\tClaude Opus 4.6 (Thinking)\n"}, nil
	case joined == "herdr agent":
		return proc.Result{ExitCode: 0, Stdout: "agent start agent prompt agent wait agy"}, nil
	case joined == "herdr pane":
		return proc.Result{ExitCode: 0, Stdout: "pane split pane run pane close pane report-metadata"}, nil
	case joined == "herdr pane report-metadata --help":
		return proc.Result{ExitCode: 0, Stdout: "--source --agent --display-agent --token"}, nil
	case joined == "herdr api schema --json":
		return proc.Result{ExitCode: 0, Stdout: "agent.view.set agent.view.clear"}, nil
	case joined == "codex --yolo --help":
		return proc.Result{ExitCode: 0, Stdout: "--yolo"}, nil
	case joined == "codex mcp --help":
		return proc.Result{ExitCode: 0, Stdout: "list"}, nil
	default:
		return proc.Result{}, errors.New("unexpected call: " + joined)
	}
}
func (r *doctorStopTestRunner) RunAttached(string, []string, []string) error { return nil }

func setupDoctorStopApp(t *testing.T, runner *doctorStopTestRunner, stdout io.Writer) (*App, string, string) {
	stateDir := t.TempDir()
	configRoot := t.TempDir()
	var err error
	runner.project, err = resolveProject(runner.project)
	if err != nil {
		t.Fatal(err)
	}
	app := New(runner, stdout, io.Discard)
	app.supervisor = supervisor.Agy{}
	app.stateDir = stateDir
	app.configRoot = configRoot
	app.token = func() (string, error) { return "runtime-doctor-stop", nil }
	app.checkPlatform = func() error { return nil }
	app.resolveExecutable = func() (string, error) { return "/tmp/herdr-tandem", nil }
	app.providerServiceCheck = func(context.Context) error { return nil }
	app.reportSidebarVisibility = func(context.Context, string, herdr.PaneVisibility) error { return nil }
	app.clearSidebarView = func(context.Context) error { return nil }
	app.getenv = func(key string) string {
		switch key {
		case "HERDR_ENV":
			return "1"
		case "HERDR_WORKSPACE_ID":
			return "w1"
		case "HERDR_PANE_ID":
			return runner.supervisorPane
		case "HERDR_SOCKET_PATH":
			return "/tmp/herdr.sock"
		case "HERDR_TANDEM_CONFIG_ROOT":
			return configRoot
		case "HERDR_TANDEM_PROJECT_DIR":
			return runner.project
		default:
			return ""
		}
	}
	return app, stateDir, configRoot
}

func createValidAgentArtifact(t *testing.T, configRoot, artifactName string) string {
	dir := filepath.Join(configRoot, "agents", artifactName)
	if err := os.MkdirAll(dir, 0700); err != nil {
		t.Fatal(err)
	}
	file := filepath.Join(dir, "agent.md")
	content := fmt.Sprintf("---\nname: %s\n---\n# System Prompt\nHello\n", artifactName)
	if err := os.WriteFile(file, []byte(content), 0600); err != nil {
		t.Fatal(err)
	}
	return file
}

func TestDoctorInspectsAgySupervisorArtifactWithoutMutation(t *testing.T) {
	var stdout bytes.Buffer
	runner := &doctorStopTestRunner{
		project:        t.TempDir(),
		supervisorPane: "w1:p1",
		developerPane:  "w1:p2",
		developerName:  developerName("w1", "w1:p1"),
	}
	app, _, configRoot := setupDoctorStopApp(t, runner, &stdout)

	// Save an interrupted agy runtime record
	artifactName := "herdr-tandem-runtime-doctor-stop"
	_, err := app.runtimeManager().Prepare(context.Background(), runtimeRecord{
		SidebarMode:         string(sidebarModeCompact),
		SupervisorKind:      supervisor.AgyID,
		DeveloperKind:       "agy",
		WorkspaceID:         "w1",
		SupervisorPaneID:    runner.supervisorPane,
		Developer:           runner.developerName,
		Project:             runner.project,
		SupervisorAgentName: artifactName,
	})
	if err != nil {
		t.Fatal(err)
	}

	// Create valid artifact on disk
	agentFile := createValidAgentArtifact(t, configRoot, artifactName)

	// Run doctor
	err = app.doctor(context.Background())
	if err == nil {
		t.Fatal("expected doctor to report problems when interrupted supervisor artifact is active")
	}

	out := stdout.String()
	if !strings.Contains(out, "supervisor custom agent") || !strings.Contains(out, "run herdr-tandem stop") {
		t.Fatalf("expected supervisor custom agent stop recommendation in doctor output: %s", out)
	}

	// Read-only verification: artifact file and directory still exist on disk!
	if _, err := os.Stat(agentFile); err != nil {
		t.Fatalf("doctor mutated or deleted artifact file: %v", err)
	}
}

func TestDoctorRejectsInvalidSupervisorArtifactWithoutMutation(t *testing.T) {
	var stdout bytes.Buffer
	runner := &doctorStopTestRunner{
		project:        t.TempDir(),
		supervisorPane: "w1:p1",
		developerPane:  "w1:p2",
		developerName:  developerName("w1", "w1:p1"),
	}
	app, _, configRoot := setupDoctorStopApp(t, runner, &stdout)

	artifactName := "herdr-tandem-runtime-doctor-stop"
	_, err := app.runtimeManager().Prepare(context.Background(), runtimeRecord{
		SidebarMode:         string(sidebarModeCompact),
		SupervisorKind:      supervisor.AgyID,
		DeveloperKind:       "agy",
		WorkspaceID:         "w1",
		SupervisorPaneID:    runner.supervisorPane,
		Developer:           runner.developerName,
		Project:             runner.project,
		SupervisorAgentName: artifactName,
	})
	if err != nil {
		t.Fatal(err)
	}

	// Create artifact with invalid file permissions (0644 instead of 0600)
	dir := filepath.Join(configRoot, "agents", artifactName)
	if err := os.MkdirAll(dir, 0700); err != nil {
		t.Fatal(err)
	}
	file := filepath.Join(dir, "agent.md")
	content := fmt.Sprintf("---\nname: %s\n---\n# System Prompt\nHello\n", artifactName)
	if err := os.WriteFile(file, []byte(content), 0644); err != nil {
		t.Fatal(err)
	}

	err = app.doctor(context.Background())
	if err == nil {
		t.Fatal("expected doctor to report problems for invalid permissions")
	}

	out := stdout.String()
	if !strings.Contains(out, "permissions") && !strings.Contains(out, "mismatch") {
		t.Fatalf("expected permissions mismatch in doctor output: %s", out)
	}

	// Read-only verification: file remains unchanged
	if _, err := os.Stat(file); err != nil {
		t.Fatalf("doctor mutated or deleted invalid file: %v", err)
	}
}

func TestStopDiscoversAgyRuntimeWhenSupervisorKindEnvAbsent(t *testing.T) {
	runner := &doctorStopTestRunner{
		project:        t.TempDir(),
		supervisorPane: "w1:p1",
		developerPane:  "w1:p2",
		developerName:  developerName("w1", "w1:p1"),
	}
	app, _, configRoot := setupDoctorStopApp(t, runner, io.Discard)

	// In this test, HERDR_TANDEM_SUPERVISOR_KIND is NOT in env
	// app.supervisor initially defaults to Codex
	app.supervisor = supervisor.Codex{}

	artifactName := "herdr-tandem-runtime-doctor-stop"
	rec, err := app.runtimeManager().Prepare(context.Background(), runtimeRecord{
		SidebarMode:         string(sidebarModeCompact),
		SupervisorKind:      supervisor.AgyID,
		DeveloperKind:       "agy",
		WorkspaceID:         "w1",
		SupervisorPaneID:    runner.supervisorPane,
		Developer:           runner.developerName,
		Project:             runner.project,
		SupervisorAgentName: artifactName,
	})
	if err != nil {
		t.Fatal(err)
	}

	// Create valid artifact on disk
	agentFile := createValidAgentArtifact(t, configRoot, artifactName)

	// Stop must discover the agy runtime using FindForSupervisorPane, resolve agy supervisor, and clean up
	if err := app.stop(context.Background()); err != nil {
		t.Fatalf("stop failed: %v", err)
	}

	// Verify artifact on disk was cleaned up
	if _, err := os.Stat(agentFile); !os.IsNotExist(err) {
		t.Fatalf("agent file was not cleaned up: %v", err)
	}
	agentDir := filepath.Join(configRoot, "agents", artifactName)
	if _, err := os.Stat(agentDir); !os.IsNotExist(err) {
		t.Fatalf("agent dir was not cleaned up: %v", err)
	}

	// Verify runtime record was removed
	_, found, err := app.runtimeManager().loadByIDUnlocked(rec.RuntimeID)
	if err != nil {
		t.Fatal(err)
	}
	if found {
		t.Fatal("expected runtime record to be removed after successful stop")
	}
}

func TestStopCleanupFailurePreservesRuntimeRecord(t *testing.T) {
	runner := &doctorStopTestRunner{
		project:        t.TempDir(),
		supervisorPane: "w1:p1",
		developerPane:  "w1:p2",
		developerName:  developerName("w1", "w1:p1"),
	}
	app, _, configRoot := setupDoctorStopApp(t, runner, io.Discard)

	artifactName := "herdr-tandem-runtime-doctor-stop"
	rec, err := app.runtimeManager().Prepare(context.Background(), runtimeRecord{
		SidebarMode:         string(sidebarModeCompact),
		SupervisorKind:      supervisor.AgyID,
		DeveloperKind:       "agy",
		WorkspaceID:         "w1",
		SupervisorPaneID:    runner.supervisorPane,
		Developer:           runner.developerName,
		Project:             runner.project,
		SupervisorAgentName: artifactName,
	})
	if err != nil {
		t.Fatal(err)
	}

	// Create artifact with an unexpected file inside so CleanupLaunch fails
	createValidAgentArtifact(t, configRoot, artifactName)
	unexpectedFile := filepath.Join(configRoot, "agents", artifactName, "unexpected.txt")
	if err := os.WriteFile(unexpectedFile, []byte("surprise"), 0600); err != nil {
		t.Fatal(err)
	}

	err = app.stop(context.Background())
	if err == nil {
		t.Fatal("expected stop to fail when cleanup encounters unexpected file")
	}
	if !strings.Contains(err.Error(), "unexpected file") {
		t.Fatalf("unexpected error message: %v", err)
	}

	// Runtime record must be PRESERVED on cleanup failure
	_, found, loadErr := app.runtimeManager().loadByIDUnlocked(rec.RuntimeID)
	if loadErr != nil || !found {
		t.Fatalf("expected runtime record to be preserved, found=%t, err=%v", found, loadErr)
	}
}

func TestStopCodexSupervisorUnaffected(t *testing.T) {
	runner := &doctorStopTestRunner{
		project:        t.TempDir(),
		supervisorPane: "w1:p1",
		developerPane:  "w1:p2",
		developerName:  developerName("w1", "w1:p1"),
	}
	app, _, _ := setupDoctorStopApp(t, runner, io.Discard)
	app.supervisor = supervisor.Codex{}

	rec, err := app.runtimeManager().Prepare(context.Background(), runtimeRecord{
		SidebarMode:      string(sidebarModeCompact),
		SupervisorKind:   supervisor.CodexID,
		DeveloperKind:    "agy",
		WorkspaceID:      "w1",
		SupervisorPaneID: runner.supervisorPane,
		Developer:        runner.developerName,
		Project:          runner.project,
	})
	if err != nil {
		t.Fatal(err)
	}

	if err := app.stop(context.Background()); err != nil {
		t.Fatalf("stop codex failed: %v", err)
	}

	// Runtime record was removed
	_, found, err := app.runtimeManager().loadByIDUnlocked(rec.RuntimeID)
	if err != nil {
		t.Fatal(err)
	}
	if found {
		t.Fatal("expected codex runtime record to be removed after stop")
	}
}

func TestDoctorReportsProblemWhenSupervisorAgentDirectoryMissing(t *testing.T) {
	var stdout bytes.Buffer
	runner := &doctorStopTestRunner{
		project:        t.TempDir(),
		supervisorPane: "w1:p1",
		developerPane:  "w1:p2",
		developerName:  developerName("w1", "w1:p1"),
	}
	app, _, _ := setupDoctorStopApp(t, runner, &stdout)

	artifactName := "herdr-tandem-runtime-doctor-stop"
	_, err := app.runtimeManager().Prepare(context.Background(), runtimeRecord{
		SidebarMode:         string(sidebarModeCompact),
		SupervisorKind:      supervisor.AgyID,
		DeveloperKind:       "agy",
		WorkspaceID:         "w1",
		SupervisorPaneID:    runner.supervisorPane,
		Developer:           runner.developerName,
		Project:             runner.project,
		SupervisorAgentName: artifactName,
	})
	if err != nil {
		t.Fatal(err)
	}

	// Do NOT create artifact on disk (directory is missing!)
	err = app.doctor(context.Background())
	if err == nil {
		t.Fatal("expected doctor to report problems when supervisor agent directory is missing")
	}

	out := stdout.String()
	if !strings.Contains(out, "custom agent directory") || !strings.Contains(out, "missing; run herdr-tandem stop") {
		t.Fatalf("expected missing custom agent directory error in doctor output: %s", out)
	}
}

func TestDoctorRejectsCommentMatchingSupervisorAgentName(t *testing.T) {
	var stdout bytes.Buffer
	runner := &doctorStopTestRunner{
		project:        t.TempDir(),
		supervisorPane: "w1:p1",
		developerPane:  "w1:p2",
		developerName:  developerName("w1", "w1:p1"),
	}
	app, _, configRoot := setupDoctorStopApp(t, runner, &stdout)

	artifactName := "herdr-tandem-runtime-doctor-stop"
	_, err := app.runtimeManager().Prepare(context.Background(), runtimeRecord{
		SidebarMode:         string(sidebarModeCompact),
		SupervisorKind:      supervisor.AgyID,
		DeveloperKind:       "agy",
		WorkspaceID:         "w1",
		SupervisorPaneID:    runner.supervisorPane,
		Developer:           runner.developerName,
		Project:             runner.project,
		SupervisorAgentName: artifactName,
	})
	if err != nil {
		t.Fatal(err)
	}

	// Create artifact where expected name only appears in a comment
	dir := filepath.Join(configRoot, "agents", artifactName)
	if err := os.MkdirAll(dir, 0700); err != nil {
		t.Fatal(err)
	}
	file := filepath.Join(dir, "agent.md")
	content := fmt.Sprintf("---\nname: other-agent\n# name: %s\n---\n# System Prompt\nHello\n", artifactName)
	if err := os.WriteFile(file, []byte(content), 0600); err != nil {
		t.Fatal(err)
	}

	err = app.doctor(context.Background())
	if err == nil {
		t.Fatal("expected doctor to report problems when expected name only appears in comment")
	}

	out := stdout.String()
	if !strings.Contains(out, "ownership mismatch") {
		t.Fatalf("expected ownership mismatch error in doctor output: %s", out)
	}

	// Read-only verification: file remains unchanged
	if _, err := os.Stat(file); err != nil {
		t.Fatalf("doctor mutated or deleted file: %v", err)
	}
}
