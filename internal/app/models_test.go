package app

import (
	"bytes"
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

// modelsTestRunner is a mock runner for model-related tests.
type modelsTestRunner struct {
	project         string
	supervisorPane  string
	developerPane   string
	developerName   string
	startedSession  string
	agyModelsOutput string
	agyModelsErr    error
	agyModelsCount  int
	calls           [][]string
}

func (r *modelsTestRunner) LookPath(name string) (string, error) { return "/bin/" + name, nil }

func (r *modelsTestRunner) Run(ctx context.Context, args ...string) (proc.Result, error) {
	if err := ctx.Err(); err != nil {
		return proc.Result{}, err
	}
	r.calls = append(r.calls, append([]string(nil), args...))
	joined := strings.Join(args, " ")
	ok := proc.Result{ExitCode: 0, Stdout: `{"id":"x","result":{"type":"ok"}}`}

	switch {
	case joined == "agy models":
		r.agyModelsCount++
		if r.agyModelsErr != nil {
			return proc.Result{ExitCode: 1, Stderr: "failed to list models"}, r.agyModelsErr
		}
		output := r.agyModelsOutput
		if output == "" {
			output = fmt.Sprintf("%s\tClaude Opus 4.6 (Thinking)\n%s\tGemini 3.8 Flash (High)\n", DefaultAgySupervisorModel, DefaultAgyDeveloperModel)
		}
		return proc.Result{ExitCode: 0, Stdout: output}, nil
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
	case strings.HasPrefix(joined, "herdr pane split "):
		return proc.Result{ExitCode: 0, Stdout: fmt.Sprintf(`{"id":"x","result":{"type":"pane_info","pane":{"pane_id":%q,"workspace_id":"w1","tab_id":"w1:t1","cwd":%q,"tokens":{}}}}`, r.developerPane, r.project)}, nil
	case strings.HasPrefix(joined, "herdr agent start "):
		sessionVal := "11111111-1111-1111-1111-111111111111"
		if r.startedSession != "" {
			sessionVal = r.startedSession
		}
		return proc.Result{ExitCode: 0, Stdout: fmt.Sprintf(`{"id":"x","result":{"type":"agent_started","agent":{"name":%q,"pane_id":%q,"workspace_id":"w1","tab_id":"w1:t1","cwd":%q,"agent":"agy","agent_status":"idle","agent_session":{"source":"herdr:antigravity_cli","agent":"agy","kind":"id","value":%q}}}}`, r.developerName, r.developerPane, r.project, sessionVal)}, nil
	case strings.HasPrefix(joined, "herdr pane read "):
		return proc.Result{ExitCode: 0, Stdout: "? for shortcuts"}, nil
	case joined == "agy --help":
		return proc.Result{ExitCode: 0, Stdout: "--agent --model --mode --dangerously-skip-permissions accept-edits --conversation"}, nil
	case joined == "herdr agent":
		return proc.Result{ExitCode: 0, Stdout: "agent start agent prompt agent wait agy"}, nil
	case joined == "herdr pane":
		return proc.Result{ExitCode: 0, Stdout: "pane split pane run pane close pane report-metadata"}, nil
	case joined == "herdr pane report-metadata --help":
		return proc.Result{ExitCode: 0, Stdout: "--source --agent --display-agent --token"}, nil
	case strings.HasPrefix(joined, "herdr pane wait-output "), strings.HasPrefix(joined, "herdr pane rename "), strings.HasPrefix(joined, "herdr pane report-metadata "), strings.HasPrefix(joined, "herdr pane close "):
		return ok, nil
	case joined == "herdr api schema --json":
		return proc.Result{ExitCode: 0, Stdout: "agent.view.set agent.view.clear"}, nil
	case joined == "codex --yolo --help":
		return proc.Result{ExitCode: 0, Stdout: "--yolo"}, nil
	case joined == "codex mcp --help":
		return proc.Result{ExitCode: 0, Stdout: "list"}, nil
	case joined == "opencode --dangerously-skip-permissions --help":
		return proc.Result{ExitCode: 0, Stdout: "--session --continue"}, nil
	case joined == "opencode mcp --help":
		return proc.Result{ExitCode: 0, Stdout: "mcp"}, nil
	default:
		return proc.Result{}, errors.New("unexpected process call: " + joined)
	}
}

func (r *modelsTestRunner) RunAttached(string, []string, []string) error { return nil }

func setupModelsTestApp(t *testing.T, runner *modelsTestRunner, stdout, stderr io.Writer) *App {
	t.Helper()
	stateDir := t.TempDir()
	configRoot := t.TempDir()
	var err error
	runner.project, err = resolveProject(runner.project)
	if err != nil {
		t.Fatal(err)
	}
	app := New(runner, stdout, stderr)
	app.stateDir = stateDir
	app.configRoot = configRoot
	app.token = func() (string, error) { return "runtime-models-test", nil }
	app.checkPlatform = func() error { return nil }
	app.resolveExecutable = func() (string, error) { return "/tmp/herdr-tandem", nil }
	app.providerServiceCheck = func(context.Context) error { return nil }
	app.reportSidebarVisibility = func(context.Context, string, herdr.PaneVisibility) error { return nil }
	app.clearSidebarView = func(context.Context) error { return nil }
	app.setSidebarView = func(context.Context) error { return nil }
	app.getenv = func(key string) string {
		switch key {
		case "HERDR_ENV":
			return "1"
		case "HERDR_WORKSPACE_ID":
			return "w1"
		case "HERDR_PANE_ID":
			return runner.supervisorPane
		case "HERDR_TAB_ID":
			return "w1:t1"
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
	return app
}

func TestModelDefaultsAndOverrides(t *testing.T) {
	tests := []struct {
		name                string
		args                []string
		wantSupervisor      string
		wantSupervisorModel string
		wantDeveloperModel  string
		wantErr             bool
	}{
		{
			name:                "no args defaults to codex with dev default",
			args:                nil,
			wantSupervisor:      "",
			wantSupervisorModel: "",
			wantDeveloperModel:  DefaultAgyDeveloperModel,
		},
		{
			name:                "explicit developer model override",
			args:                []string{"--developer-model", "custom-dev-model"},
			wantSupervisor:      "",
			wantSupervisorModel: "",
			wantDeveloperModel:  "custom-dev-model",
		},
		{
			name:                "explicit developer model with default value",
			args:                []string{"--developer-model", DefaultAgyDeveloperModel},
			wantSupervisor:      "",
			wantSupervisorModel: "",
			wantDeveloperModel:  DefaultAgyDeveloperModel,
		},
		{
			name:                "opencode supervisor gets developer default but no supervisor model",
			args:                []string{"--supervisor", "opencode"},
			wantSupervisor:      "opencode",
			wantSupervisorModel: "",
			wantDeveloperModel:  DefaultAgyDeveloperModel,
		},
		{
			name:                "agy supervisor gets both supervisor and developer defaults",
			args:                []string{"--supervisor", "agy"},
			wantSupervisor:      "agy",
			wantSupervisorModel: DefaultAgySupervisorModel,
			wantDeveloperModel:  DefaultAgyDeveloperModel,
		},
		{
			name:                "agy supervisor with supervisor-model override",
			args:                []string{"--supervisor", "agy", "--supervisor-model", "custom-sup-model"},
			wantSupervisor:      "agy",
			wantSupervisorModel: "custom-sup-model",
			wantDeveloperModel:  DefaultAgyDeveloperModel,
		},
		{
			name:                "agy supervisor with explicit default supervisor-model",
			args:                []string{"--supervisor", "agy", "--supervisor-model", DefaultAgySupervisorModel},
			wantSupervisor:      "agy",
			wantSupervisorModel: DefaultAgySupervisorModel,
			wantDeveloperModel:  DefaultAgyDeveloperModel,
		},
		{
			name:                "agy supervisor with both models overridden",
			args:                []string{"--supervisor", "agy", "--supervisor-model", "custom-sup", "--developer-model", "custom-dev"},
			wantSupervisor:      "agy",
			wantSupervisorModel: "custom-sup",
			wantDeveloperModel:  "custom-dev",
		},
		{
			name:    "supervisor-model with codex rejected",
			args:    []string{"--supervisor", "codex", "--supervisor-model", "some-model"},
			wantErr: true,
		},
		{
			name:    "supervisor-model with opencode rejected",
			args:    []string{"--supervisor", "opencode", "--supervisor-model", "some-model"},
			wantErr: true,
		},
		{
			name:    "supervisor-model alone without agy supervisor rejected",
			args:    []string{"--supervisor-model", "some-model"},
			wantErr: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			_, _, supID, supModel, devModel, err := parseStartArgs(tt.args)
			if tt.wantErr {
				if err == nil {
					t.Fatalf("expected error for args %#v", tt.args)
				}
				return
			}
			if err != nil {
				t.Fatalf("unexpected error for args %#v: %v", tt.args, err)
			}
			if supID != tt.wantSupervisor {
				t.Errorf("supervisor = %q, want %q", supID, tt.wantSupervisor)
			}
			if supModel != tt.wantSupervisorModel {
				t.Errorf("supervisorModel = %q, want %q", supModel, tt.wantSupervisorModel)
			}
			if devModel != tt.wantDeveloperModel {
				t.Errorf("developerModel = %q, want %q", devModel, tt.wantDeveloperModel)
			}
		})
	}
}

func TestCodexOpenCodeModelIsolation(t *testing.T) {
	// 1. Codex LaunchSpec must never contain model flags or environment
	codexAdapter := supervisor.Codex{}
	codexSpec, err := codexAdapter.BuildLaunch(supervisor.LaunchContext{
		ProjectDir:   "/tmp/test-proj",
		Executable:   "/tmp/herdr-tandem",
		Instructions: "do work",
	})
	if err != nil {
		t.Fatalf("Codex BuildLaunch failed: %v", err)
	}
	for _, arg := range codexSpec.Args {
		if arg == "--model" || strings.HasPrefix(arg, "--model=") {
			t.Fatalf("Codex args unexpectedly contain model flag: %v", codexSpec.Args)
		}
	}

	// 2. OpenCode LaunchSpec must never contain model flags or environment
	openCodeAdapter := supervisor.OpenCode{}
	openCodeSpec, err := openCodeAdapter.BuildLaunch(supervisor.LaunchContext{
		ProjectDir:   "/tmp/test-proj",
		Executable:   "/tmp/herdr-tandem",
		Instructions: "do work",
	})
	if err != nil {
		t.Fatalf("OpenCode BuildLaunch failed: %v", err)
	}
	for _, arg := range openCodeSpec.Args {
		if arg == "--model" || strings.HasPrefix(arg, "--model=") {
			t.Fatalf("OpenCode args unexpectedly contain model flag: %v", openCodeSpec.Args)
		}
	}

	// 3. Verify start for Codex persists empty SupervisorModel and default DeveloperModel
	runner := &modelsTestRunner{
		project:        t.TempDir(),
		supervisorPane: "w1:p1",
		developerPane:  "w1:p2",
		developerName:  developerName("w1", "w1:p1"),
	}
	app := setupModelsTestApp(t, runner, io.Discard, io.Discard)
	app.supervisor = supervisor.Codex{}

	err = app.start(context.Background(), runner.project, sidebarModeCompact)
	if err != nil {
		t.Fatalf("start with Codex failed: %v", err)
	}

	rec, found, err := app.runtimeManager().Load()
	if err != nil || !found {
		t.Fatalf("runtime record not found: %v", err)
	}
	if rec.SupervisorModel != "" {
		t.Fatalf("Codex runtime record has SupervisorModel=%q, want empty", rec.SupervisorModel)
	}
	if rec.DeveloperModel != DefaultAgyDeveloperModel {
		t.Fatalf("Codex runtime record has DeveloperModel=%q, want %q", rec.DeveloperModel, DefaultAgyDeveloperModel)
	}
}

func TestRuntimePersistenceAndRepairWithDefaultModel(t *testing.T) {
	// Test agy supervisor start persists both defaults
	runner := &modelsTestRunner{
		project:        t.TempDir(),
		supervisorPane: "w1:p1",
		developerPane:  "w1:p2",
		developerName:  developerName("w1", "w1:p1"),
	}
	app := setupModelsTestApp(t, runner, io.Discard, io.Discard)
	app.supervisor = supervisor.Agy{}

	err := app.start(context.Background(), runner.project, sidebarModeCompact)
	if err != nil {
		t.Fatalf("start with agy failed: %v", err)
	}

	rec, found, err := app.runtimeManager().Load()
	if err != nil || !found {
		t.Fatalf("runtime record not found: %v", err)
	}
	if rec.SupervisorModel != DefaultAgySupervisorModel {
		t.Fatalf("SupervisorModel=%q, want %q", rec.SupervisorModel, DefaultAgySupervisorModel)
	}
	if rec.DeveloperModel != DefaultAgyDeveloperModel {
		t.Fatalf("DeveloperModel=%q, want %q", rec.DeveloperModel, DefaultAgyDeveloperModel)
	}

	// Verify developer start call received --model gemini-3.8-flash-high
	var devStartCall []string
	for _, call := range runner.calls {
		if len(call) >= 3 && call[0] == "herdr" && call[1] == "agent" && call[2] == "start" {
			devStartCall = call
			break
		}
	}
	if devStartCall == nil {
		t.Fatal("herdr agent start was not called")
	}
	devStartStr := strings.Join(devStartCall, " ")
	if !strings.Contains(devStartStr, "--model "+DefaultAgyDeveloperModel) {
		t.Fatalf("herdr agent start missing default model: %s", devStartStr)
	}

	// Verify repair retains the stored default developer model
	info := runtimeContext{
		supervisorKind: supervisor.AgyID,
		developerKind:  "agy",
		workspaceID:    "w1",
		supervisor:     runner.supervisorPane,
		developer:      runner.developerName,
		project:        runner.project,
	}
	runner.calls = nil
	agent, err := app.repairMissingDeveloper(context.Background(), info)
	if err != nil {
		t.Fatalf("repair failed: %v", err)
	}
	if agent.PaneID != runner.developerPane {
		t.Fatalf("repaired agent pane=%q, want %q", agent.PaneID, runner.developerPane)
	}

	var repairStartCall []string
	for _, call := range runner.calls {
		if len(call) >= 3 && call[0] == "herdr" && call[1] == "agent" && call[2] == "start" {
			repairStartCall = call
			break
		}
	}
	if repairStartCall == nil {
		t.Fatal("repair did not call herdr agent start")
	}
	repairStartStr := strings.Join(repairStartCall, " ")
	if !strings.Contains(repairStartStr, "--model "+DefaultAgyDeveloperModel) {
		t.Fatalf("repair missing stored default developer model: %s", repairStartStr)
	}
}

func TestParseAgyModelsOutput(t *testing.T) {
	tests := []struct {
		name       string
		output     string
		wantModels []string
		wantAbsent []string
	}{
		{
			name: "tab separated tabular output",
			output: "gemini-3.8-flash-high\tGemini 3.8 Flash (High)\n" +
				"gemini-3.8-flash-medium\tGemini 3.8 Flash (Medium)\n" +
				"claude-opus-4-6-thinking\tClaude Opus 4.6 (Thinking)\n",
			wantModels: []string{"gemini-3.8-flash-high", "gemini-3.8-flash-medium", "claude-opus-4-6-thinking"},
		},
		{
			name: "space separated tabular output",
			output: "gemini-3.8-flash-high     Gemini 3.8 Flash (High)\n" +
				"claude-opus-4-6-thinking  Claude Opus 4.6 (Thinking)\n",
			wantModels: []string{"gemini-3.8-flash-high", "claude-opus-4-6-thinking"},
		},
		{
			name: "output with fetching headers and carriage returns",
			output: "\rFetching available models...\r" +
				"gemini-3.8-flash-high     Gemini 3.8 Flash (High)\r\n" +
				"claude-opus-4-6-thinking  Claude Opus 4.6 (Thinking)\r\n",
			wantModels: []string{"gemini-3.8-flash-high", "claude-opus-4-6-thinking"},
		},
		{
			name: "output with spinner characters",
			output: "⠋ Fetching available models...\n" +
				"⠸ gemini-3.8-flash-high     Gemini 3.8 Flash (High)\n" +
				"⠸claude-opus-4-6-thinking  Claude Opus 4.6 (Thinking)\n",
			wantModels: []string{"gemini-3.8-flash-high", "claude-opus-4-6-thinking"},
		},
		{
			name: "plain list of model IDs",
			output: "gemini-3.8-flash-high\n" +
				"claude-opus-4-6-thinking\n" +
				"gpt-oss-120b-medium\n",
			wantModels: []string{"gemini-3.8-flash-high", "claude-opus-4-6-thinking", "gpt-oss-120b-medium"},
		},
		{
			name:   "empty output",
			output: "",
		},
		{
			name:       "model absent",
			output:     "gemini-2.5-flash\tGemini 2.5 Flash\n",
			wantModels: []string{"gemini-2.5-flash"},
			wantAbsent: []string{"gemini-3.8-flash-high", "claude-opus-4-6-thinking"},
		},
		{
			name: "rejects error line mentioning model ID",
			output: "error: model gemini-3.8-flash-high is unavailable\n" +
				"claude-opus-4-6-thinking\tClaude Opus 4.6 (Thinking)\n",
			wantModels: []string{"claude-opus-4-6-thinking"},
			wantAbsent: []string{"gemini-3.8-flash-high"},
		},
		{
			name: "rejects warning line mentioning model ID",
			output: "warning: gemini-3.8-flash-high is deprecated\n" +
				"claude-opus-4-6-thinking\tClaude Opus 4.6 (Thinking)\n",
			wantModels: []string{"claude-opus-4-6-thinking"},
			wantAbsent: []string{"gemini-3.8-flash-high"},
		},
		{
			name: "rejects status line mentioning model ID",
			output: "status: gemini-3.8-flash-high unavailable\n" +
				"claude-opus-4-6-thinking\tClaude Opus 4.6 (Thinking)\n",
			wantModels: []string{"claude-opus-4-6-thinking"},
			wantAbsent: []string{"gemini-3.8-flash-high"},
		},
		{
			name: "rejects description mentioning a model slug",
			output: "other-model\tFallback for gemini-3.8-flash-high\n" +
				"claude-opus-4-6-thinking\tClaude Opus 4.6 (Thinking)\n",
			wantModels: []string{"other-model", "claude-opus-4-6-thinking"},
			wantAbsent: []string{"gemini-3.8-flash-high"},
		},
		{
			name:       "rejects arbitrary tokens elsewhere on a row",
			output:     "gemini-3.8-flash-high\tGemini 3.8 Flash (High)\tclaude-opus-4-6-thinking\n",
			wantModels: []string{"gemini-3.8-flash-high"},
			wantAbsent: []string{"claude-opus-4-6-thinking"},
		},
		{
			name:       "rejects single-space prose mentioning model",
			output:     "The following model gemini-3.8-flash-high is unavailable\n",
			wantAbsent: []string{"gemini-3.8-flash-high"},
		},
		{
			name:       "rejects prose starting with model name",
			output:     "gemini-3.8-flash-high is unavailable\n",
			wantAbsent: []string{"gemini-3.8-flash-high"},
		},
		{
			name: "rejects headers and reserved labels",
			output: "MODEL ID\tDISPLAY NAME\n" +
				"MODEL\tNAME\n" +
				"model-id\tdisplay-name\n" +
				"name\tdescription\n" +
				"gemini-3.8-flash-high\tGemini 3.8 Flash (High)\n",
			wantModels: []string{"gemini-3.8-flash-high"},
			wantAbsent: []string{"model-id", "display-name", "name", "description", "MODEL", "ID"},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			models := parseAgyModelsOutput(tt.output)
			for _, want := range tt.wantModels {
				if _, ok := models[want]; !ok {
					t.Errorf("model %q missing from parsed models: %v", want, models)
				}
			}
			for _, absent := range tt.wantAbsent {
				if _, ok := models[absent]; ok {
					t.Errorf("model %q should NOT be present in parsed models: %v", absent, models)
				}
			}
			if len(tt.wantModels) == 0 && len(models) != 0 && len(tt.wantAbsent) == 0 {
				t.Errorf("expected empty map, got: %v", models)
			}
		})
	}
}

func TestModelCapabilityPreflight(t *testing.T) {
	t.Run("start succeeds when models are available", func(t *testing.T) {
		runner := &modelsTestRunner{
			project:        t.TempDir(),
			supervisorPane: "w1:p1",
			developerPane:  "w1:p2",
			developerName:  developerName("w1", "w1:p1"),
		}
		app := setupModelsTestApp(t, runner, io.Discard, io.Discard)
		app.supervisor = supervisor.Agy{}

		if err := app.start(context.Background(), runner.project, sidebarModeCompact); err != nil {
			t.Fatalf("start failed: %v", err)
		}
	})

	t.Run("start fails fast when developer model is absent", func(t *testing.T) {
		runner := &modelsTestRunner{
			project:         t.TempDir(),
			supervisorPane:  "w1:p1",
			developerPane:   "w1:p2",
			developerName:   developerName("w1", "w1:p1"),
			agyModelsOutput: "claude-opus-4-6-thinking\tClaude Opus 4.6\n", // missing developer model
		}
		app := setupModelsTestApp(t, runner, io.Discard, io.Discard)
		app.supervisor = supervisor.Agy{}

		err := app.start(context.Background(), runner.project, sidebarModeCompact)
		if err == nil {
			t.Fatal("expected start to fail when developer model is absent")
		}
		if !strings.Contains(err.Error(), "developer model is not available") || !strings.Contains(err.Error(), "agy models") {
			t.Fatalf("unexpected error message: %v", err)
		}
		// Verify no panes were split and no runtime record created
		rec, found, _ := app.runtimeManager().Load()
		if found || rec.RuntimeID != "" {
			t.Fatal("runtime record unexpectedly created on failed preflight")
		}
	})

	t.Run("start fails fast when supervisor model is absent", func(t *testing.T) {
		runner := &modelsTestRunner{
			project:         t.TempDir(),
			supervisorPane:  "w1:p1",
			developerPane:   "w1:p2",
			developerName:   developerName("w1", "w1:p1"),
			agyModelsOutput: "gemini-3.8-flash-high\tGemini 3.8 Flash\n", // missing supervisor model
		}
		app := setupModelsTestApp(t, runner, io.Discard, io.Discard)
		app.supervisor = supervisor.Agy{}

		err := app.start(context.Background(), runner.project, sidebarModeCompact)
		if err == nil {
			t.Fatal("expected start to fail when supervisor model is absent")
		}
		if !strings.Contains(err.Error(), "supervisor model is not available") || !strings.Contains(err.Error(), "agy models") {
			t.Fatalf("unexpected error message: %v", err)
		}
	})

	t.Run("start fails fast when model only appears in error line", func(t *testing.T) {
		runner := &modelsTestRunner{
			project:         t.TempDir(),
			supervisorPane:  "w1:p1",
			developerPane:   "w1:p2",
			developerName:   developerName("w1", "w1:p1"),
			agyModelsOutput: "claude-opus-4-6-thinking\tClaude Opus 4.6\nerror: model gemini-3.8-flash-high is unavailable\n",
		}
		app := setupModelsTestApp(t, runner, io.Discard, io.Discard)
		app.supervisor = supervisor.Agy{}

		err := app.start(context.Background(), runner.project, sidebarModeCompact)
		if err == nil {
			t.Fatal("expected start to fail when developer model only appears in error line")
		}
		if !strings.Contains(err.Error(), "developer model is not available") {
			t.Fatalf("unexpected error message: %v", err)
		}
		if strings.Contains(err.Error(), "gemini-3.8-flash-high") {
			t.Fatalf("error message leaked model value: %v", err)
		}
	})

	t.Run("start fails fast when model only appears in description of another model", func(t *testing.T) {
		runner := &modelsTestRunner{
			project:         t.TempDir(),
			supervisorPane:  "w1:p1",
			developerPane:   "w1:p2",
			developerName:   developerName("w1", "w1:p1"),
			agyModelsOutput: "claude-opus-4-6-thinking\tClaude Opus 4.6\nother-model\tFallback for gemini-3.8-flash-high\n",
		}
		app := setupModelsTestApp(t, runner, io.Discard, io.Discard)
		app.supervisor = supervisor.Agy{}

		err := app.start(context.Background(), runner.project, sidebarModeCompact)
		if err == nil {
			t.Fatal("expected start to fail when developer model only appears in description")
		}
		if !strings.Contains(err.Error(), "developer model is not available") {
			t.Fatalf("unexpected error message: %v", err)
		}
		if strings.Contains(err.Error(), "gemini-3.8-flash-high") {
			t.Fatalf("error message leaked model value: %v", err)
		}
	})

	t.Run("start fails fast when model appears in prose", func(t *testing.T) {
		runner := &modelsTestRunner{
			project:         t.TempDir(),
			supervisorPane:  "w1:p1",
			developerPane:   "w1:p2",
			developerName:   developerName("w1", "w1:p1"),
			agyModelsOutput: "claude-opus-4-6-thinking\tClaude Opus 4.6\ngemini-3.8-flash-high is unavailable\n",
		}
		app := setupModelsTestApp(t, runner, io.Discard, io.Discard)
		app.supervisor = supervisor.Agy{}

		err := app.start(context.Background(), runner.project, sidebarModeCompact)
		if err == nil {
			t.Fatal("expected start to fail when developer model appears in prose")
		}
		if !strings.Contains(err.Error(), "developer model is not available") {
			t.Fatalf("unexpected error message: %v", err)
		}
		if strings.Contains(err.Error(), "gemini-3.8-flash-high") {
			t.Fatalf("error message leaked model value: %v", err)
		}
	})

	t.Run("start fails fast when supervisor model only appears as arbitrary token on row", func(t *testing.T) {
		runner := &modelsTestRunner{
			project:         t.TempDir(),
			supervisorPane:  "w1:p1",
			developerPane:   "w1:p2",
			developerName:   developerName("w1", "w1:p1"),
			agyModelsOutput: "gemini-3.8-flash-high\tGemini 3.8 Flash (High)\textra\tclaude-opus-4-6-thinking\n",
		}
		app := setupModelsTestApp(t, runner, io.Discard, io.Discard)
		app.supervisor = supervisor.Agy{}

		err := app.start(context.Background(), runner.project, sidebarModeCompact)
		if err == nil {
			t.Fatal("expected start to fail when supervisor model is only an arbitrary token")
		}
		if !strings.Contains(err.Error(), "supervisor model is not available") {
			t.Fatalf("unexpected error message: %v", err)
		}
		if strings.Contains(err.Error(), "claude-opus-4-6-thinking") {
			t.Fatalf("error message leaked model value: %v", err)
		}
	})

	t.Run("preflight bounded by context", func(t *testing.T) {
		runner := &modelsTestRunner{
			project:        t.TempDir(),
			supervisorPane: "w1:p1",
			developerPane:  "w1:p2",
			developerName:  developerName("w1", "w1:p1"),
		}
		app := setupModelsTestApp(t, runner, io.Discard, io.Discard)
		ctx, cancel := context.WithCancel(context.Background())
		cancel()

		err := app.start(ctx, runner.project, sidebarModeCompact)
		if err == nil {
			t.Fatal("expected start to fail with cancelled context")
		}
	})
}

func TestSingleAgyModelsInvocationWhenBothRolesUseAgy(t *testing.T) {
	t.Run("start invokes agy models exactly once", func(t *testing.T) {
		runner := &modelsTestRunner{
			project:        t.TempDir(),
			supervisorPane: "w1:p1",
			developerPane:  "w1:p2",
			developerName:  developerName("w1", "w1:p1"),
		}
		app := setupModelsTestApp(t, runner, io.Discard, io.Discard)
		app.supervisor = supervisor.Agy{}

		if err := app.start(context.Background(), runner.project, sidebarModeCompact); err != nil {
			t.Fatalf("start failed: %v", err)
		}
		if runner.agyModelsCount != 1 {
			t.Fatalf("agy models called %d times, want exactly 1", runner.agyModelsCount)
		}
	})

	t.Run("doctor invokes agy models exactly once", func(t *testing.T) {
		runner := &modelsTestRunner{
			project:        t.TempDir(),
			supervisorPane: "w1:p1",
			developerPane:  "w1:p2",
			developerName:  developerName("w1", "w1:p1"),
		}
		var stdout bytes.Buffer
		app := setupModelsTestApp(t, runner, &stdout, io.Discard)
		app.supervisor = supervisor.Agy{}

		if err := app.doctor(context.Background()); err != nil {
			t.Fatalf("doctor failed: %v\noutput:\n%s", err, stdout.String())
		}
		if runner.agyModelsCount != 1 {
			t.Fatalf("doctor called agy models %d times, want exactly 1", runner.agyModelsCount)
		}
		out := stdout.String()
		if !strings.Contains(out, "✓ agy supervisor model capability") {
			t.Fatalf("doctor missing supervisor model capability check: %s", out)
		}
		if !strings.Contains(out, "✓ agy developer model capability") {
			t.Fatalf("doctor missing developer model capability check: %s", out)
		}
	})
}

func TestDoctorModelValidation(t *testing.T) {
	t.Run("default codex supervisor validates only developer model", func(t *testing.T) {
		runner := &modelsTestRunner{
			project:        t.TempDir(),
			supervisorPane: "w1:p1",
			developerPane:  "w1:p2",
			developerName:  developerName("w1", "w1:p1"),
		}
		var stdout bytes.Buffer
		app := setupModelsTestApp(t, runner, &stdout, io.Discard)
		app.supervisor = supervisor.Codex{}

		if err := app.doctor(context.Background()); err != nil {
			t.Fatalf("doctor failed: %v", err)
		}
		out := stdout.String()
		if strings.Contains(out, "agy supervisor model capability") {
			t.Fatalf("codex doctor unexpectedly checked agy supervisor model: %s", out)
		}
		if !strings.Contains(out, "✓ agy developer model capability") {
			t.Fatalf("codex doctor missing developer model capability: %s", out)
		}
		if runner.agyModelsCount != 1 {
			t.Fatalf("agy models called %d times, want 1", runner.agyModelsCount)
		}
	})

	t.Run("opencode supervisor validates only developer model", func(t *testing.T) {
		runner := &modelsTestRunner{
			project:        t.TempDir(),
			supervisorPane: "w1:p1",
			developerPane:  "w1:p2",
			developerName:  developerName("w1", "w1:p1"),
		}
		var stdout bytes.Buffer
		app := setupModelsTestApp(t, runner, &stdout, io.Discard)
		app.supervisor = supervisor.OpenCode{}

		if err := app.doctor(context.Background()); err != nil {
			t.Fatalf("doctor failed: %v\noutput:\n%s", err, stdout.String())
		}
		out := stdout.String()
		if strings.Contains(out, "agy supervisor model capability") {
			t.Fatalf("opencode doctor unexpectedly checked agy supervisor model: %s", out)
		}
		if !strings.Contains(out, "✓ agy developer model capability") {
			t.Fatalf("opencode doctor missing developer model capability: %s", out)
		}
	})

	t.Run("doctor reports failure when developer model missing", func(t *testing.T) {
		runner := &modelsTestRunner{
			project:         t.TempDir(),
			supervisorPane:  "w1:p1",
			developerPane:   "w1:p2",
			developerName:   developerName("w1", "w1:p1"),
			agyModelsOutput: "claude-opus-4-6-thinking\tClaude Opus 4.6\n",
		}
		var stdout bytes.Buffer
		app := setupModelsTestApp(t, runner, &stdout, io.Discard)
		app.supervisor = supervisor.Codex{}

		err := app.doctor(context.Background())
		if err == nil {
			t.Fatal("expected doctor to fail when developer model missing")
		}
		out := stdout.String()
		if !strings.Contains(out, "✗ agy developer model capability") {
			t.Fatalf("doctor did not report developer model failure: %s", out)
		}
	})

	t.Run("doctor --supervisor agy reports failure when supervisor model missing", func(t *testing.T) {
		runner := &modelsTestRunner{
			project:         t.TempDir(),
			supervisorPane:  "w1:p1",
			developerPane:   "w1:p2",
			developerName:   developerName("w1", "w1:p1"),
			agyModelsOutput: "gemini-3.8-flash-high\tGemini 3.8 Flash\n",
		}
		var stdout bytes.Buffer
		app := setupModelsTestApp(t, runner, &stdout, io.Discard)
		app.supervisor = supervisor.Agy{}

		err := app.doctor(context.Background())
		if err == nil {
			t.Fatal("expected doctor to fail when supervisor model missing")
		}
		out := stdout.String()
		if !strings.Contains(out, "✗ agy supervisor model capability") {
			t.Fatalf("doctor did not report supervisor model failure: %s", out)
		}
	})
}

func TestDiagnosticsExcludeModelValues(t *testing.T) {
	var diagnostics []string
	sink := func(msg string) {
		diagnostics = append(diagnostics, msg)
	}

	runner := &modelsTestRunner{
		project:         t.TempDir(),
		supervisorPane:  "w1:p1",
		developerPane:   "w1:p2",
		developerName:   developerName("w1", "w1:p1"),
		agyModelsOutput: "other-model-1\tOther Model 1\n", // will trigger preflight failure
	}
	app := setupModelsTestApp(t, runner, io.Discard, io.Discard)
	app.supervisor = supervisor.Agy{}
	app.diagnosticSink = sink
	app.supervisorModel = "custom-sup-secret-999"
	app.developerModel = "custom-dev-secret-888"

	// Run start (which fails during preflight)
	_ = app.start(context.Background(), runner.project, sidebarModeCompact)

	// Run doctor (which fails during model check)
	_ = app.doctor(context.Background())

	// Verify all captured diagnostic messages
	forbiddenValues := []string{
		"custom-sup-secret-999",
		"custom-dev-secret-888",
		DefaultAgySupervisorModel,
		DefaultAgyDeveloperModel,
		"other-model-1", // model listed in command output
	}

	allLogged := strings.Join(diagnostics, "\n")
	for _, forbidden := range forbiddenValues {
		if strings.Contains(allLogged, forbidden) {
			t.Fatalf("diagnostic log leaked model value %q:\n%s", forbidden, allLogged)
		}
	}

	// Verify diagnostic messages do not contain raw command output
	if strings.Contains(allLogged, "Other Model 1") {
		t.Fatalf("diagnostic log contains raw command output:\n%s", allLogged)
	}
}
