package app

import (
	"context"
	"io"
	"strings"
	"testing"
	"time"

	"github.com/kazimshah39/herdr-tandem/internal/herdr"
	"github.com/kazimshah39/herdr-tandem/internal/supervisor"
)

func TestSupervisorPromptUsesProviderManagedModeAndNativeTools(t *testing.T) {
	for _, tool := range []string{"delegate_task", "task_status", "acknowledge_task", "recover_task"} {
		if !strings.Contains(supervisorPrompt, tool) {
			t.Fatalf("supervisor prompt missing %q", tool)
		}
	}
	for _, forbidden := range []string{"herdr-tandem accounts", "agy -p /quota", "agy -p /model"} {
		if strings.Contains(strings.ToLower(supervisorPrompt), strings.ToLower(forbidden)) {
			t.Fatalf("supervisor prompt still mentions removed native account behavior: %q", forbidden)
		}
	}
}

func TestParseStartArgsAndSupervisorSelection(t *testing.T) {
	tests := []struct {
		name                string
		args                []string
		wantPath            string
		wantMode            sidebarMode
		wantSupervisor      string
		wantSupervisorModel string
		wantDeveloperModel  string
		wantErr             bool
	}{
		{name: "nil args default compact", args: nil, wantPath: ".", wantMode: sidebarModeCompact, wantDeveloperModel: DefaultAgyDeveloperModel},
		{name: "empty args default compact", args: []string{}, wantPath: ".", wantMode: sidebarModeCompact, wantDeveloperModel: DefaultAgyDeveloperModel},
		{name: "path only default compact", args: []string{"/tmp/project"}, wantPath: "/tmp/project", wantMode: sidebarModeCompact, wantDeveloperModel: DefaultAgyDeveloperModel},
		{name: "expanded alone", args: []string{"--expanded"}, wantPath: ".", wantMode: sidebarModeExpanded, wantDeveloperModel: DefaultAgyDeveloperModel},
		{name: "expanded with path", args: []string{"--expanded", "/tmp/project"}, wantPath: "/tmp/project", wantMode: sidebarModeExpanded, wantDeveloperModel: DefaultAgyDeveloperModel},
		{name: "path then expanded", args: []string{"/tmp/project", "--expanded"}, wantPath: "/tmp/project", wantMode: sidebarModeExpanded, wantDeveloperModel: DefaultAgyDeveloperModel},
		{name: "supervisor only default compact", args: []string{"--supervisor", "opencode"}, wantPath: ".", wantMode: sidebarModeCompact, wantSupervisor: "opencode", wantDeveloperModel: DefaultAgyDeveloperModel},
		{name: "supervisor with path default compact", args: []string{"--supervisor", "opencode", "/tmp/project"}, wantPath: "/tmp/project", wantMode: sidebarModeCompact, wantSupervisor: "opencode", wantDeveloperModel: DefaultAgyDeveloperModel},
		{name: "supervisor equal with path default compact", args: []string{"--supervisor=opencode", "/tmp/project"}, wantPath: "/tmp/project", wantMode: sidebarModeCompact, wantSupervisor: "opencode", wantDeveloperModel: DefaultAgyDeveloperModel},
		{name: "supervisor and expanded with path", args: []string{"--supervisor", "opencode", "--expanded", "/tmp/project"}, wantPath: "/tmp/project", wantMode: sidebarModeExpanded, wantSupervisor: "opencode", wantDeveloperModel: DefaultAgyDeveloperModel},
		{name: "expanded and supervisor with path", args: []string{"--expanded", "--supervisor", "opencode", "/tmp/project"}, wantPath: "/tmp/project", wantMode: sidebarModeExpanded, wantSupervisor: "opencode", wantDeveloperModel: DefaultAgyDeveloperModel},
		{name: "expanded and supervisor equal with path", args: []string{"--expanded", "--supervisor=opencode", "/tmp/project"}, wantPath: "/tmp/project", wantMode: sidebarModeExpanded, wantSupervisor: "opencode", wantDeveloperModel: DefaultAgyDeveloperModel},
		{name: "path then expanded then supervisor", args: []string{"/tmp/project", "--expanded", "--supervisor=opencode"}, wantPath: "/tmp/project", wantMode: sidebarModeExpanded, wantSupervisor: "opencode", wantDeveloperModel: DefaultAgyDeveloperModel},
		{name: "supervisor agy default compact", args: []string{"--supervisor", "agy"}, wantPath: ".", wantMode: sidebarModeCompact, wantSupervisor: "agy", wantSupervisorModel: DefaultAgySupervisorModel, wantDeveloperModel: DefaultAgyDeveloperModel},
		{name: "supervisor agy equal with path", args: []string{"--supervisor=agy", "/tmp/project"}, wantPath: "/tmp/project", wantMode: sidebarModeCompact, wantSupervisor: "agy", wantSupervisorModel: DefaultAgySupervisorModel, wantDeveloperModel: DefaultAgyDeveloperModel},
		{name: "supervisor agy with supervisor-model split", args: []string{"--supervisor", "agy", "--supervisor-model", "gemini-2.5-pro"}, wantPath: ".", wantMode: sidebarModeCompact, wantSupervisor: "agy", wantSupervisorModel: "gemini-2.5-pro", wantDeveloperModel: DefaultAgyDeveloperModel},
		{name: "supervisor agy with supervisor-model equal", args: []string{"--supervisor=agy", "--supervisor-model=gemini-2.5-pro"}, wantPath: ".", wantMode: sidebarModeCompact, wantSupervisor: "agy", wantSupervisorModel: "gemini-2.5-pro", wantDeveloperModel: DefaultAgyDeveloperModel},
		{name: "developer-model split with default supervisor", args: []string{"--developer-model", "gemini-2.5-flash"}, wantPath: ".", wantMode: sidebarModeCompact, wantDeveloperModel: "gemini-2.5-flash"},
		{name: "developer-model equal with opencode", args: []string{"--supervisor=opencode", "--developer-model=gemini-2.5-flash"}, wantPath: ".", wantMode: sidebarModeCompact, wantSupervisor: "opencode", wantDeveloperModel: "gemini-2.5-flash"},
		{name: "supervisor-model and developer-model with agy", args: []string{"--supervisor=agy", "--supervisor-model", "gemini-2.5-pro", "--developer-model=gemini-2.5-flash", "--expanded", "/tmp/project"}, wantPath: "/tmp/project", wantMode: sidebarModeExpanded, wantSupervisor: "agy", wantSupervisorModel: "gemini-2.5-pro", wantDeveloperModel: "gemini-2.5-flash"},
		{name: "supervisor-model with default codex supervisor rejected", args: []string{"--supervisor-model", "gemini-2.5-pro"}, wantErr: true},
		{name: "supervisor-model with explicit codex rejected", args: []string{"--supervisor", "codex", "--supervisor-model", "gemini-2.5-pro"}, wantErr: true},
		{name: "supervisor-model with opencode rejected", args: []string{"--supervisor", "opencode", "--supervisor-model", "gemini-2.5-pro"}, wantErr: true},
		{name: "supervisor-model duplicate rejected", args: []string{"--supervisor=agy", "--supervisor-model=a", "--supervisor-model=b"}, wantErr: true},
		{name: "developer-model duplicate rejected", args: []string{"--developer-model=a", "--developer-model=b"}, wantErr: true},
		{name: "supervisor-model missing argument", args: []string{"--supervisor=agy", "--supervisor-model"}, wantErr: true},
		{name: "supervisor-model flag as argument", args: []string{"--supervisor=agy", "--supervisor-model", "--expanded"}, wantErr: true},
		{name: "developer-model missing argument", args: []string{"--developer-model"}, wantErr: true},
		{name: "developer-model flag as argument", args: []string{"--developer-model", "--expanded"}, wantErr: true},
		{name: "supervisor-model empty rejected", args: []string{"--supervisor=agy", "--supervisor-model="}, wantErr: true},
		{name: "developer-model empty rejected", args: []string{"--developer-model="}, wantErr: true},
		{name: "supervisor-model invalid characters rejected", args: []string{"--supervisor=agy", "--supervisor-model=invalid model"}, wantErr: true},
		{name: "developer-model invalid characters rejected", args: []string{"--developer-model=invalid\nmodel"}, wantErr: true},
		{name: "legacy compact rejected", args: []string{"--compact"}, wantErr: true},
		{name: "legacy compact with path rejected", args: []string{"--compact", "/tmp/project"}, wantErr: true},
		{name: "legacy show-agents rejected", args: []string{"--show-agents", "/tmp/project"}, wantErr: true},
		{name: "legacy show-agents alone rejected", args: []string{"--show-agents"}, wantErr: true},
		{name: "duplicate expanded rejected", args: []string{"--expanded", "--expanded"}, wantErr: true},
		{name: "duplicate expanded with path rejected", args: []string{"--expanded", "/tmp/project", "--expanded"}, wantErr: true},
		{name: "duplicate supervisor space-separated rejected", args: []string{"--supervisor", "codex", "--supervisor", "opencode"}, wantErr: true},
		{name: "duplicate supervisor equal rejected", args: []string{"--supervisor=codex", "--supervisor=opencode"}, wantErr: true},
		{name: "duplicate supervisor mixed equal-first rejected", args: []string{"--supervisor=codex", "--supervisor", "opencode"}, wantErr: true},
		{name: "duplicate supervisor mixed space-first rejected", args: []string{"--supervisor", "codex", "--supervisor=opencode"}, wantErr: true},
		{name: "supervisor missing argument rejected", args: []string{"--supervisor"}, wantErr: true},
		{name: "supervisor followed by flag rejected", args: []string{"--supervisor", "--expanded"}, wantErr: true},
		{name: "supervisor equal empty rejected", args: []string{"--supervisor="}, wantErr: true},
		{name: "multiple paths rejected", args: []string{"one", "two"}, wantErr: true},
		{name: "multiple paths with expanded rejected", args: []string{"--expanded", "one", "two"}, wantErr: true},
		{name: "unknown flag rejected", args: []string{"--unknown"}, wantErr: true},
		{name: "expanded flag with value rejected", args: []string{"--expanded=true"}, wantErr: true},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			path, mode, supervisorID, supervisorModel, developerModel, err := parseStartArgs(test.args)
			if test.wantErr {
				if err == nil {
					t.Fatalf("args=%#v: expected error", test.args)
				}
				return
			}
			if err != nil {
				t.Fatalf("args=%#v unexpected error: %v", test.args, err)
			}
			if path != test.wantPath || mode != test.wantMode || supervisorID != test.wantSupervisor || supervisorModel != test.wantSupervisorModel || developerModel != test.wantDeveloperModel {
				t.Fatalf("args=%#v got path=%q mode=%q supervisor=%q supervisorModel=%q developerModel=%q, want path=%q mode=%q supervisor=%q supervisorModel=%q developerModel=%q", test.args, path, mode, supervisorID, supervisorModel, developerModel, test.wantPath, test.wantMode, test.wantSupervisor, test.wantSupervisorModel, test.wantDeveloperModel)
			}
		})
	}
}

func TestDeveloperNameIsStableAndScoped(t *testing.T) {
	first := developerName("workspace-one", "pane-one")
	if first != developerName("workspace-one", "pane-one") {
		t.Fatal("developer identity is not stable")
	}
	if first == developerName("workspace-one", "pane-two") {
		t.Fatal("different supervisor panes share a developer identity")
	}
	if !strings.HasPrefix(first, "herdr_tandem_dev_") || len(first) > 32 {
		t.Fatalf("developer=%q", first)
	}
}

func TestRunResolvesSupervisorFromRuntimeEnvironment(t *testing.T) {
	app := New(fakeRunner{}, io.Discard, io.Discard)
	app.getenv = func(key string) string {
		if key == supervisorKindEnv {
			return "opencode"
		}
		if key == developerKindEnv {
			return "agy"
		}
		return ""
	}
	if err := app.Run(context.Background(), []string{"--help"}); err != nil {
		t.Fatal(err)
	}
	if app.supervisor.ID() != "opencode" || app.developerAdapter.ID() != "agy" {
		t.Fatalf("supervisor=%s developer=%s", app.supervisor.ID(), app.developerAdapter.ID())
	}
}

func TestRunResolvesAgySupervisorFromRuntimeEnvironment(t *testing.T) {
	app := New(fakeRunner{}, io.Discard, io.Discard)
	app.getenv = func(key string) string {
		if key == supervisorKindEnv {
			return "agy"
		}
		if key == developerKindEnv {
			return "agy"
		}
		return ""
	}
	if err := app.Run(context.Background(), []string{"--help"}); err != nil {
		t.Fatal(err)
	}
	if app.supervisor.ID() != "agy" || app.developerAdapter.ID() != "agy" {
		t.Fatalf("supervisor=%s developer=%s", app.supervisor.ID(), app.developerAdapter.ID())
	}
}

func TestSupervisorInstructionsIncludeCurrentLocalDate(t *testing.T) {
	got := supervisorInstructions(time.Date(2026, 9, 20, 12, 0, 0, 0, time.Local))
	if !strings.Contains(got, "Sunday, September 20, 2026") {
		t.Fatalf("instructions=%q", got)
	}
}

func TestMCPEnvironmentCarriesWorkflowProfile(t *testing.T) {
	app := New(fakeRunner{}, io.Discard, io.Discard)
	app.supervisor = supervisor.OpenCode{}
	app.getenv = func(key string) string {
		if key == "HERDR_SOCKET_PATH" {
			return "/tmp/herdr.sock"
		}
		return ""
	}
	env, err := app.buildMCPEnvForRuntime(herdr.PaneInfo{WorkspaceID: "w1", PaneID: "w1:p1", TabID: "w1:t1"}, "developer", "w1:p2", "/tmp/project", "runtime", sidebarModeExpanded)
	if err != nil {
		t.Fatal(err)
	}
	if env[supervisorKindEnv] != "opencode" || env[developerKindEnv] != "agy" || env[sidebarModeEnv] != "expanded" {
		t.Fatalf("env=%#v", env)
	}
}
