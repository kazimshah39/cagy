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
		args     []string
		wantPath string
		wantShow bool
		wantErr  bool
	}{
		{args: nil, wantPath: "."},
		{args: []string{"/tmp/project"}, wantPath: "/tmp/project"},
		{args: []string{"--show-agents", "/tmp/project"}, wantPath: "/tmp/project", wantShow: true},
		{args: []string{"one", "two"}, wantErr: true},
		{args: []string{"--unknown"}, wantErr: true},
	}
	for _, test := range tests {
		path, show, supervisorID, err := parseStartArgs(test.args)
		if test.wantErr {
			if err == nil {
				t.Fatalf("args=%#v: expected error", test.args)
			}
			continue
		}
		if err != nil || path != test.wantPath || show != test.wantShow || supervisorID != "" {
			t.Fatalf("args=%#v path=%q show=%t err=%v", test.args, path, show, err)
		}
	}
	path, show, supervisorID, err := parseStartArgs([]string{"--supervisor", "opencode", "--show-agents", "/tmp/project"})
	if err != nil || path != "/tmp/project" || !show || supervisorID != "opencode" {
		t.Fatalf("supervisor parse path=%q show=%t id=%q err=%v", path, show, supervisorID, err)
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
