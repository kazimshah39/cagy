package app

import (
	"context"
	"strings"
	"testing"
)

func TestCodexArgsAlwaysUseYoloInvocationTrustAndMCP(t *testing.T) {
	project := `/tmp/team.project/quote"and\slash`
	mcpOverride := `mcp_servers.cagy={command="/tmp/cagy",args=["mcp-server"]}`
	args := codexArgs(project, mcpOverride)
	for _, expected := range []string{"--yolo", "--dangerously-bypass-hook-trust", "--search", "-c", "-C", project, mcpOverride} {
		if !contains(args, expected) {
			t.Fatalf("missing %q in %#v", expected, args)
		}
	}
	wantTrust := `projects={"/tmp/team.project/quote\"and\\slash"={trust_level="trusted"}}`
	if !contains(args, wantTrust) {
		t.Fatalf("missing trust override %q in %#v", wantTrust, args)
	}
	if contains(args, supervisorPrompt) {
		t.Fatal("supervisor instructions must be a config override, not an initial task")
	}
}

func TestSupervisorPromptUsesRouterModeAndNativeTools(t *testing.T) {
	for _, tool := range []string{"delegate_task", "task_status", "acknowledge_task", "recover_task"} {
		if !strings.Contains(supervisorPrompt, tool) {
			t.Fatalf("supervisor prompt missing %q", tool)
		}
	}
	for _, forbidden := range []string{"cagy accounts", "agy -p /quota", "agy -p /model"} {
		if strings.Contains(strings.ToLower(supervisorPrompt), strings.ToLower(forbidden)) {
			t.Fatalf("supervisor prompt still mentions removed native account behavior: %q", forbidden)
		}
	}
}

func TestParseStartArgsAndAccountsMigrationMessage(t *testing.T) {
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
		path, show, err := parseStartArgs(test.args)
		if test.wantErr {
			if err == nil {
				t.Fatalf("args=%#v: expected error", test.args)
			}
			continue
		}
		if err != nil || path != test.wantPath || show != test.wantShow {
			t.Fatalf("args=%#v path=%q show=%t err=%v", test.args, path, show, err)
		}
	}
	app := New(fakeRunner{}, nil, nil)
	err := app.Run(context.Background(), []string{"accounts"})
	if err == nil || !strings.Contains(err.Error(), "was removed") || !strings.Contains(err.Error(), "9Router") {
		t.Fatalf("accounts migration error=%v", err)
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
	if !strings.HasPrefix(first, "cagy_dev_") || len(first) > 32 {
		t.Fatalf("developer=%q", first)
	}
}
