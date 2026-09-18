package app

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/kazimshah39/cagy/internal/herdr"
	proc "github.com/kazimshah39/cagy/internal/process"
)

type fakeRunner struct {
	attachedArgs []string
	attachedEnv  []string
}

func (f *fakeRunner) LookPath(name string) (string, error) { return "/bin/" + name, nil }
func (f *fakeRunner) Run(_ context.Context, args ...string) (proc.Result, error) {
	return proc.Result{}, errors.New("unexpected process call: " + strings.Join(args, " "))
}
func (f *fakeRunner) RunAttached(args []string, env []string) error {
	f.attachedArgs = append([]string(nil), args...)
	f.attachedEnv = append([]string(nil), env...)
	return nil
}

func TestCodexArgsAlwaysUseYoloSearchAndInvocationTrust(t *testing.T) {
	project := `/tmp/team.project/quote"and\slash`
	args := codexArgs(project)
	for _, expected := range []string{"--yolo", "--dangerously-bypass-hook-trust", "--search", "-c", "-C", project} {
		if !contains(args, expected) {
			t.Fatalf("missing %q in %#v", expected, args)
		}
	}
	wantTrust := `projects={"/tmp/team.project/quote\"and\\slash"={trust_level="trusted"}}`
	if !contains(args, wantTrust) {
		t.Fatalf("missing safe project trust override %q in %#v", wantTrust, args)
	}
	wantInstructions := codexDeveloperInstructionsOverride()
	if !contains(args, wantInstructions) {
		t.Fatalf("missing developer instructions override in %#v", args)
	}
	if contains(args, supervisorPrompt) {
		t.Fatal("supervisor instructions must not be sent as an initial user task")
	}
}

func TestSupervisorPromptNativeToolWorkflow(t *testing.T) {
	for _, tool := range []string{"delegate_task", "task_status", "acknowledge_task", "recover_task"} {
		if !strings.Contains(supervisorPrompt, tool) {
			t.Fatalf("supervisor prompt missing required native MCP tool %q: %q", tool, supervisorPrompt)
		}
	}
	if strings.Contains(supervisorPrompt, "cat <<'CAGY_TASK'") {
		t.Fatalf("supervisor prompt should not recommend heredoc as primary path: %q", supervisorPrompt)
	}
	if strings.Contains(supervisorPrompt, `cagy ask "<`) {
		t.Fatalf("supervisor prompt recommends unsafe quoted argv delegation: %q", supervisorPrompt)
	}
	if !strings.Contains(supervisorPrompt, "emergency and manual compatibility only") {
		t.Fatalf("supervisor prompt should state CLI commands are for emergency/compatibility only: %q", supervisorPrompt)
	}
}

func TestParseStartArgsDefaultsCompactAndSupportsExpandedMode(t *testing.T) {
	tests := []struct {
		args     []string
		wantPath string
		wantShow bool
		wantErr  bool
	}{
		{args: nil, wantPath: "."},
		{args: []string{"/tmp/project"}, wantPath: "/tmp/project"},
		{args: []string{"--show-agents"}, wantPath: ".", wantShow: true},
		{args: []string{"--show-agents", "/tmp/project"}, wantPath: "/tmp/project", wantShow: true},
		{args: []string{"/tmp/project", "--show-agents"}, wantPath: "/tmp/project", wantShow: true},
		{args: []string{"--unknown"}, wantErr: true},
		{args: []string{"one", "two"}, wantErr: true},
	}
	for _, test := range tests {
		path, show, err := parseStartArgs(test.args)
		if test.wantErr {
			if err == nil {
				t.Fatalf("args=%#v expected error", test.args)
			}
			continue
		}
		if err != nil || path != test.wantPath || show != test.wantShow {
			t.Fatalf("args=%#v path=%q show=%v err=%v", test.args, path, show, err)
		}
	}
}

func TestDeveloperNameIsStableAndValid(t *testing.T) {
	first := developerName("w1", "w1:p1")
	if first != developerName("w1", "w1:p1") {
		t.Fatal("developer name is not stable")
	}
	if !strings.HasPrefix(first, "cagy_dev_") || len(first) > 32 {
		t.Fatalf("invalid developer name %q", first)
	}
}

func TestValidateDeveloperRejectsWrongWorkspaceAndProject(t *testing.T) {
	base := herdr.AgentInfo{Agent: "agy", WorkspaceID: "w1", ForegroundCWD: "/tmp/project"}
	if err := validateDeveloper(base, "w1", "/tmp/project"); err != nil {
		t.Fatal(err)
	}
	wrongWorkspace := base
	wrongWorkspace.WorkspaceID = "w2"
	if validateDeveloper(wrongWorkspace, "w1", "/tmp/project") == nil {
		t.Fatal("expected workspace rejection")
	}
	wrongProject := base
	wrongProject.ForegroundCWD = "/tmp/other"
	if validateDeveloper(wrongProject, "w1", "/tmp/project") == nil {
		t.Fatal("expected project rejection")
	}
}

func TestTaskLockRejectsConcurrentOwner(t *testing.T) {
	dir := t.TempDir()
	first, err := acquireLock(dir, "developer", time.Now())
	if err != nil {
		t.Fatal(err)
	}
	defer first.release()
	if _, err := acquireLock(dir, "developer", time.Now()); err == nil {
		t.Fatal("expected busy lock error")
	}
}

func TestMarkedCommandAndStatus(t *testing.T) {
	command := markedCommand("agm refresh-all", "__MARK__")
	if strings.Contains(command, `\"$__cagy_status\"`) {
		t.Fatalf("status variable was incorrectly escaped: %s", command)
	}
	if !strings.Contains(command, `"$__cagy_status"`) {
		t.Fatalf("status variable is not shell quoted: %s", command)
	}
	status, err := markerStatus("done\n__MARK__:7\n", "__MARK__")
	if err != nil || status != 7 {
		t.Fatalf("status=%d err=%v", status, err)
	}
}

func TestAgySwitchPartialIDESuccess(t *testing.T) {
	output := "  ✓ Antigravity CLI (agy)\n  ✗ Antigravity IDE: not running\npartial switch failure"
	if !agySwitchSucceeded(output) {
		t.Fatal("expected agy success despite IDE failure")
	}
	if agySwitchSucceeded("✗ Antigravity CLI (agy): keychain error") {
		t.Fatal("unexpected agy success")
	}
}

func TestResolveProject(t *testing.T) {
	dir := t.TempDir()
	resolved, err := resolveProject(dir)
	if err != nil {
		t.Fatal(err)
	}
	evaluated, _ := filepath.EvalSymlinks(dir)
	if resolved != evaluated {
		t.Fatalf("resolved=%q want=%q", resolved, evaluated)
	}
	missing := filepath.Join(dir, "missing")
	if _, err := resolveProject(missing); err == nil {
		t.Fatal("expected missing directory error")
	}
}

func TestMergeEnvOverridesValues(t *testing.T) {
	merged := mergeEnv([]string{"A=old", "B=keep"}, map[string]string{"A": "new"})
	if !contains(merged, "A=new") || !contains(merged, "B=keep") || contains(merged, "A=old") {
		t.Fatalf("merged env = %#v", merged)
	}
}

func TestContextDerivesDeveloperWithoutSavedEnvironment(t *testing.T) {
	runner := &fakeRunner{}
	application := New(runner, os.Stdout, os.Stderr)
	values := map[string]string{
		"HERDR_ENV":          "1",
		"HERDR_WORKSPACE_ID": "w1",
		"HERDR_PANE_ID":      "w1:p1",
		"CAGY_PROJECT_DIR":   "/tmp/project",
	}
	application.getenv = func(key string) string { return values[key] }
	info, err := application.context()
	if err != nil {
		t.Fatal(err)
	}
	if info.developer != developerName("w1", "w1:p1") {
		t.Fatalf("developer=%q", info.developer)
	}
}

func contains(values []string, expected string) bool {
	for _, value := range values {
		if value == expected {
			return true
		}
	}
	return false
}

func TestRecoveryPolicyIsFixed(t *testing.T) {
	if maxRecoveryAttempts != 2 {
		t.Fatalf("maxRecoveryAttempts=%d", maxRecoveryAttempts)
	}
	prompt := continuationPrompt("finish the feature")
	if !strings.Contains(prompt, "Inspect the current working tree") || !strings.Contains(prompt, "finish the feature") {
		t.Fatalf("continuation prompt=%q", prompt)
	}
}

type runStep struct {
	want   []string
	result proc.Result
	err    error
	before func()
}

type scriptedRunner struct {
	t            *testing.T
	steps        []runStep
	calls        [][]string
	lookups      []string
	attachedArgs []string
	attachedEnv  []string
}

func (r *scriptedRunner) LookPath(name string) (string, error) {
	r.lookups = append(r.lookups, name)
	return "/usr/bin/" + name, nil
}

func (r *scriptedRunner) Run(_ context.Context, args ...string) (proc.Result, error) {
	r.t.Helper()
	r.calls = append(r.calls, append([]string(nil), args...))
	if len(r.steps) == 0 {
		r.t.Fatalf("unexpected command: %#v", args)
	}
	step := r.steps[0]
	r.steps = r.steps[1:]
	if !slicesEqual(args, step.want) {
		r.t.Fatalf("command mismatch\n got: %#v\nwant: %#v", args, step.want)
	}
	if step.before != nil {
		step.before()
	}
	return step.result, step.err
}

func (r *scriptedRunner) RunAttached(args []string, env []string) error {
	r.attachedArgs = append([]string(nil), args...)
	r.attachedEnv = append([]string(nil), env...)
	return nil
}

func (r *scriptedRunner) assertDone() {
	r.t.Helper()
	if len(r.steps) != 0 {
		r.t.Fatalf("%d scripted commands were not used; next=%#v", len(r.steps), r.steps[0].want)
	}
}

func TestStartCreatesVisibleDeveloperAndLaunchesCodex(t *testing.T) {
	project, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	developer := developerName("w1", "w1:p1")
	runner := &scriptedRunner{t: t, steps: []runStep{
		{want: []string{"herdr", "integration", "status"}, result: textResult("antigravity-cli: current (v3) (/tmp/hook)\n")},
		{want: []string{"herdr", "pane", "current", "--current"}, result: jsonResult(`{"type":"pane_current","pane":{"pane_id":"w1:p1","workspace_id":"w1","tab_id":"w1:t1","cwd":"` + project + `"}}`)},
		{want: []string{"herdr", "pane", "report-metadata", "w1:p1", "--source", paneOwnershipSource, "--token", "cagy_owner=" + developer, "--token", "cagy_role=supervisor"}, result: jsonResult(`{"type":"ok"}`)},
		{want: []string{"herdr", "pane", "report-metadata", "w1:p1", "--source", supervisorDisplaySource, "--agent", "codex", "--display-agent", compactSupervisorDisplayName}, result: jsonResult(`{"type":"ok"}`)},
		{want: []string{"herdr", "agent", "get", developer}, result: jsonError("agent_not_found", "missing")},
		{want: []string{"herdr", "pane", "split", "--current", "--direction", "right", "--cwd", project, "--no-focus"}, result: jsonResult(`{"type":"pane_info","pane":{"pane_id":"w1:p2","workspace_id":"w1","tab_id":"w1:t1","cwd":"` + project + `"}}`)},
		{want: []string{"herdr", "pane", "rename", "w1:p1", "Codex Supervisor"}, result: jsonResult(`{"type":"pane_info"}`)},
		{want: []string{"herdr", "pane", "rename", "w1:p2", "agy Developer"}, result: jsonResult(`{"type":"pane_info"}`)},
		{want: []string{"herdr", "pane", "report-metadata", "w1:p2", "--source", paneOwnershipSource, "--token", "cagy_owner=" + developer, "--token", "cagy_role=developer"}, result: jsonResult(`{"type":"ok"}`)},
		{want: []string{"herdr", "agent", "start", developer, "--kind", "agy", "--pane", "w1:p2", "--timeout", "60000", "--", "--dangerously-skip-permissions", "--mode", "accept-edits"}, result: jsonResult(`{"type":"agent_started","agent":{"agent":"agy","agent_status":"idle","pane_id":"w1:p2","workspace_id":"w1","tab_id":"w1:t1","foreground_cwd":"` + project + `"},"argv":[]}`)},
		{want: []string{"herdr", "pane", "report-metadata", "w1:p2", "--source", paneOwnershipSource, "--token", "cagy_owner=" + developer, "--token", "cagy_role=developer"}, result: jsonResult(`{"type":"ok"}`)},
		{want: []string{"herdr", "pane", "report-metadata", "w1:p2", "--source", developerDisplaySource, "--agent", "agy", "--display-agent", developerDisplayName}, result: jsonResult(`{"type":"ok"}`)},
		pendingSessionStep("w1:p2"),
		{want: []string{"herdr", "pane", "read", "w1:p2", "--source", "recent-unwrapped", "--lines", "200"}, result: textResult("Do you trust the contents of this project?\n> Yes, I trust this folder\n")},
		{want: []string{"herdr", "pane", "send-keys", "w1:p2", "enter"}, result: jsonResult(`{"type":"pane_info"}`)},
		{want: []string{"herdr", "pane", "wait-output", "w1:p2", "--match", "? for shortcuts", "--source", "recent-unwrapped", "--lines", "400", "--timeout", "60000"}, result: jsonResult(`{"type":"output_matched"}`)},
	}}
	application := New(runner, os.Stdout, os.Stderr)
	sidebarCalls := 0
	application.configureSidebar = func(_ context.Context, showAgents bool) error {
		sidebarCalls++
		if showAgents {
			t.Fatal("default startup unexpectedly requested expanded sidebar")
		}
		return nil
	}
	values := map[string]string{
		"HERDR_ENV":          "1",
		"HERDR_WORKSPACE_ID": "w1",
		"HERDR_PANE_ID":      "w1:p1",
		"HERDR_SOCKET_PATH":  "/tmp/herdr.sock",
	}
	application.getenv = func(key string) string { return values[key] }
	application.environ = func() []string { return []string{"PATH=/usr/bin"} }

	if err := application.start(context.Background(), project, false); err != nil {
		t.Fatal(err)
	}
	runner.assertDone()
	if sidebarCalls != 1 {
		t.Fatalf("sidebar calls=%d want=1", sidebarCalls)
	}
	for _, expected := range []string{"codex", "--yolo", "--dangerously-bypass-hook-trust", "--search", "-C", project} {
		if !contains(runner.attachedArgs, expected) {
			t.Fatalf("missing %q in attached args %#v", expected, runner.attachedArgs)
		}
	}
	hasMCPServer := false
	for _, arg := range runner.attachedArgs {
		if strings.Contains(arg, "mcp_servers.cagy=") {
			hasMCPServer = true
			break
		}
	}
	if !hasMCPServer {
		t.Fatalf("missing mcp_servers.cagy config in attached args %#v", runner.attachedArgs)
	}
	for _, expected := range []string{"CAGY_DEVELOPER=" + developer, "CAGY_DEVELOPER_PANE_ID=w1:p2", "CAGY_SUPERVISOR_PANE_ID=w1:p1", "CAGY_PROJECT_DIR=" + project} {
		if !contains(runner.attachedEnv, expected) {
			t.Fatalf("missing %q in attached env %#v", expected, runner.attachedEnv)
		}
	}
}

func TestStartShowAgentsUsesExpandedLabelsWhenReusingDeveloper(t *testing.T) {
	project, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	developer := developerName("w1", "w1:p1")
	runner := &scriptedRunner{t: t, steps: []runStep{
		{want: []string{"herdr", "integration", "status"}, result: textResult("antigravity-cli: current (v3) (/tmp/hook)\n")},
		{want: []string{"herdr", "pane", "current", "--current"}, result: jsonResult(`{"type":"pane_current","pane":{"pane_id":"w1:p1","workspace_id":"w1","tab_id":"w1:t1","cwd":"` + project + `"}}`)},
		{want: []string{"herdr", "pane", "report-metadata", "w1:p1", "--source", paneOwnershipSource, "--token", "cagy_owner=" + developer, "--token", "cagy_role=supervisor"}, result: jsonResult(`{"type":"ok"}`)},
		{want: []string{"herdr", "pane", "report-metadata", "w1:p1", "--source", supervisorDisplaySource, "--agent", "codex", "--display-agent", expandedSupervisorDisplayName}, result: jsonResult(`{"type":"ok"}`)},
		{want: []string{"herdr", "agent", "get", developer}, result: agentJSON("w1:p2", "w1", project, "idle")},
		{want: []string{"herdr", "pane", "get", "w1:p2"}, result: paneJSON("w1:p2", "w1:t1", project, developerPaneLabel, map[string]string{"cagy_owner": developer, "cagy_role": "developer"})},
		{want: []string{"herdr", "pane", "report-metadata", "w1:p2", "--source", paneOwnershipSource, "--token", "cagy_owner=" + developer, "--token", "cagy_role=developer"}, result: jsonResult(`{"type":"ok"}`)},
		{want: []string{"herdr", "pane", "report-metadata", "w1:p2", "--source", developerDisplaySource, "--agent", "agy", "--display-agent", developerDisplayName}, result: jsonResult(`{"type":"ok"}`)},
	}}
	application := New(runner, os.Stdout, os.Stderr)
	sidebarCalls := 0
	application.configureSidebar = func(_ context.Context, showAgents bool) error {
		sidebarCalls++
		if !showAgents {
			t.Fatal("expanded startup requested compact sidebar")
		}
		return nil
	}
	application.getenv = envGetter(map[string]string{
		"HERDR_ENV":          "1",
		"HERDR_WORKSPACE_ID": "w1",
		"HERDR_PANE_ID":      "w1:p1",
		"HERDR_SOCKET_PATH":  "/tmp/herdr.sock",
	})
	application.environ = func() []string { return []string{"PATH=/usr/bin"} }

	if err := application.start(context.Background(), project, true); err != nil {
		t.Fatal(err)
	}
	runner.assertDone()
	if sidebarCalls != 1 {
		t.Fatalf("sidebar calls=%d want=1", sidebarCalls)
	}
	if !contains(runner.attachedEnv, "CAGY_DEVELOPER_PANE_ID=w1:p2") {
		t.Fatalf("missing reused developer pane in %#v", runner.attachedEnv)
	}
	hasMCPServer := false
	for _, arg := range runner.attachedArgs {
		if strings.Contains(arg, "mcp_servers.cagy=") {
			hasMCPServer = true
			break
		}
	}
	if !hasMCPServer {
		t.Fatalf("missing mcp_servers.cagy config in attached args %#v", runner.attachedArgs)
	}
}

func TestStartFailsWhenExecutableUnsafe(t *testing.T) {
	project, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	developer := developerName("w1", "w1:p1")
	runner := &scriptedRunner{t: t, steps: []runStep{
		{want: []string{"herdr", "integration", "status"}, result: textResult("antigravity-cli: current (v3) (/tmp/hook)\n")},
		{want: []string{"herdr", "pane", "current", "--current"}, result: jsonResult(`{"type":"pane_current","pane":{"pane_id":"w1:p1","workspace_id":"w1","tab_id":"w1:t1","cwd":"` + project + `"}}`)},
		{want: []string{"herdr", "pane", "report-metadata", "w1:p1", "--source", paneOwnershipSource, "--token", "cagy_owner=" + developer, "--token", "cagy_role=supervisor"}, result: jsonResult(`{"type":"ok"}`)},
		{want: []string{"herdr", "pane", "report-metadata", "w1:p1", "--source", supervisorDisplaySource, "--agent", "codex", "--display-agent", compactSupervisorDisplayName}, result: jsonResult(`{"type":"ok"}`)},
		{want: []string{"herdr", "agent", "get", developer}, result: agentJSON("w1:p2", "w1", project, "idle")},
		{want: []string{"herdr", "pane", "get", "w1:p2"}, result: paneJSON("w1:p2", "w1:t1", project, developerPaneLabel, map[string]string{"cagy_owner": developer, "cagy_role": "developer"})},
		{want: []string{"herdr", "pane", "report-metadata", "w1:p2", "--source", paneOwnershipSource, "--token", "cagy_owner=" + developer, "--token", "cagy_role=developer"}, result: jsonResult(`{"type":"ok"}`)},
		{want: []string{"herdr", "pane", "report-metadata", "w1:p2", "--source", developerDisplaySource, "--agent", "agy", "--display-agent", developerDisplayName}, result: jsonResult(`{"type":"ok"}`)},
	}}
	application := New(runner, os.Stdout, os.Stderr)
	application.getenv = envGetter(map[string]string{
		"HERDR_ENV":          "1",
		"HERDR_WORKSPACE_ID": "w1",
		"HERDR_PANE_ID":      "w1:p1",
		"HERDR_SOCKET_PATH":  "/tmp/herdr.sock",
	})
	application.resolveExecutable = func() (string, error) {
		return "", errors.New("unsafe binary")
	}
	err = application.start(context.Background(), project, false)
	if err == nil || !strings.Contains(err.Error(), "resolve cagy executable: unsafe binary") {
		t.Fatalf("expected unsafe binary error, got: %v", err)
	}
	if runner.attachedArgs != nil {
		t.Fatal("runner.RunAttached must not be called when executable resolution fails")
	}
}

func TestStartRequiresHerdrSocketPath(t *testing.T) {
	project, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	developer := developerName("w1", "w1:p1")
	runner := &scriptedRunner{t: t, steps: []runStep{
		{want: []string{"herdr", "integration", "status"}, result: textResult("antigravity-cli: current (v3) (/tmp/hook)\n")},
		{want: []string{"herdr", "pane", "current", "--current"}, result: jsonResult(`{"type":"pane_current","pane":{"pane_id":"w1:p1","workspace_id":"w1","tab_id":"w1:t1","cwd":"` + project + `"}}`)},
		{want: []string{"herdr", "pane", "report-metadata", "w1:p1", "--source", paneOwnershipSource, "--token", "cagy_owner=" + developer, "--token", "cagy_role=supervisor"}, result: jsonResult(`{"type":"ok"}`)},
		{want: []string{"herdr", "pane", "report-metadata", "w1:p1", "--source", supervisorDisplaySource, "--agent", "codex", "--display-agent", compactSupervisorDisplayName}, result: jsonResult(`{"type":"ok"}`)},
		{want: []string{"herdr", "agent", "get", developer}, result: agentJSON("w1:p2", "w1", project, "idle")},
		{want: []string{"herdr", "pane", "get", "w1:p2"}, result: paneJSON("w1:p2", "w1:t1", project, developerPaneLabel, map[string]string{"cagy_owner": developer, "cagy_role": "developer"})},
		{want: []string{"herdr", "pane", "report-metadata", "w1:p2", "--source", paneOwnershipSource, "--token", "cagy_owner=" + developer, "--token", "cagy_role=developer"}, result: jsonResult(`{"type":"ok"}`)},
		{want: []string{"herdr", "pane", "report-metadata", "w1:p2", "--source", developerDisplaySource, "--agent", "agy", "--display-agent", developerDisplayName}, result: jsonResult(`{"type":"ok"}`)},
	}}
	application := New(runner, os.Stdout, os.Stderr)
	application.getenv = envGetter(map[string]string{
		"HERDR_ENV":          "1",
		"HERDR_WORKSPACE_ID": "w1",
		"HERDR_PANE_ID":      "w1:p1",
		"HERDR_SOCKET_PATH":  "",
	})
	application.resolveExecutable = func() (string, error) {
		return "/usr/local/bin/cagy", nil
	}
	err = application.start(context.Background(), project, false)
	if err == nil || !strings.Contains(err.Error(), "herdr socket path is missing") {
		t.Fatalf("expected missing socket path error, got: %v", err)
	}
	if runner.attachedArgs != nil {
		t.Fatal("runner.RunAttached must not be called when socket path is missing")
	}
}

func TestBuildMCPEnvAllowlistAndNoSecrets(t *testing.T) {
	app := New(nil, nil, nil)
	envVars := map[string]string{
		"HERDR_SOCKET_PATH":     "/tmp/herdr.sock",
		"ANTHROPIC_API_KEY":     "secret-key",
		"OPENAI_API_KEY":        "secret-key-2",
		"AWS_SECRET_ACCESS_KEY": "super-secret",
		"PASSWORD":              "secret-pass",
		"USER":                  "testuser",
		"HOME":                  "/Users/testuser",
	}
	app.getenv = func(k string) string { return envVars[k] }
	current := herdr.PaneInfo{
		PaneID:      "w1:p1",
		WorkspaceID: "w1",
		TabID:       "w1:t1",
	}

	mcpEnv, err := app.buildMCPEnv(current, "cagy_dev_123", "w1:p2", "/Users/test/project")
	if err != nil {
		t.Fatalf("unexpected buildMCPEnv error: %v", err)
	}

	expectedKeys := map[string]string{
		"HERDR_ENV":               "1",
		"HERDR_WORKSPACE_ID":      "w1",
		"HERDR_PANE_ID":           "w1:p1",
		"HERDR_SOCKET_PATH":       "/tmp/herdr.sock",
		"HERDR_TAB_ID":            "w1:t1",
		"CAGY_SUPERVISOR_PANE_ID": "w1:p1",
		"CAGY_DEVELOPER":          "cagy_dev_123",
		"CAGY_DEVELOPER_PANE_ID":  "w1:p2",
		"CAGY_PROJECT_DIR":        "/Users/test/project",
	}

	if len(mcpEnv) != len(expectedKeys) {
		t.Fatalf("mcpEnv has %d keys, want %d: %#v", len(mcpEnv), len(expectedKeys), mcpEnv)
	}
	for k, wantVal := range expectedKeys {
		if gotVal, ok := mcpEnv[k]; !ok || gotVal != wantVal {
			t.Fatalf("key %q: got %q (ok=%v), want %q", k, gotVal, ok, wantVal)
		}
	}

	for _, forbidden := range []string{"ANTHROPIC_API_KEY", "OPENAI_API_KEY", "AWS_SECRET_ACCESS_KEY", "PASSWORD", "USER", "HOME"} {
		if _, ok := mcpEnv[forbidden]; ok {
			t.Fatalf("forbidden/secret variable %q leaked into mcpEnv", forbidden)
		}
	}
}

func TestAskReturnsOnlyNewDeveloperOutput(t *testing.T) {
	project, _ := filepath.EvalSymlinks(t.TempDir())
	developer := developerName("w1", "w1:p1")
	task := "Implement the feature safely"
	brainRoot := t.TempDir()
	writeAgyTranscript(t, brainRoot, testConversationID, task, "Implemented.")
	runner := &scriptedRunner{t: t, steps: []runStep{
		{want: []string{"herdr", "pane", "get", "w1:p1"}, result: supervisorPaneJSON(project)},
		{want: []string{"herdr", "agent", "get", developer}, result: agentJSON("w1:p2", "w1", project, "idle")},
		{want: []string{"herdr", "pane", "get", "w1:p2"}, result: paneJSON("w1:p2", "w1:t1", project, developerPaneLabel, map[string]string{"cagy_owner": developer, "cagy_role": "developer"})},
		{want: agyProbeArgs("/model"), result: agyModelResult("gemini-3.8-flash-high", "Gemini 3.8 Flash (High)")},
		{want: agyProbeArgs("/quota"), result: agyQuotaResult("Gemini Models", 0.8, 0.9)},
		{want: []string{"herdr", "agent", "read", developer, "--source", "recent-unwrapped", "--lines", "400"}, result: textResult("old output\n")},
		{want: []string{"herdr", "agent", "prompt", developer, task, "--wait", "--timeout", "300000"}, result: agentJSONWithSession("w1:p2", "w1", project, "done", testConversationID)},
		{want: []string{"herdr", "agent", "read", developer, "--source", "recent-unwrapped", "--lines", "400"}, result: textResult("old output\nImplemented.\n")},
		{want: []string{"herdr", "agent", "wait", developer, "--until", "blocked", "--timeout", "1000"}, result: jsonError("timeout", "timed out")},
		{want: []string{"herdr", "agent", "read", developer, "--source", "visible", "--lines", "80"}, result: textResult(">\n────────────────────\n? for shortcuts\n")},
		sessionOwnershipStep("w1:p2", testConversationID),
	}}
	var stdout, stderr strings.Builder
	application := New(runner, &stdout, &stderr)
	application.stateDir = t.TempDir()
	application.agyBrainRoot = brainRoot
	application.transcriptWait = time.Second
	application.getenv = envGetter(map[string]string{
		"HERDR_ENV":               "1",
		"HERDR_WORKSPACE_ID":      "w1",
		"HERDR_PANE_ID":           "w1:p1",
		"CAGY_SUPERVISOR_PANE_ID": "w1:p1",
		"CAGY_DEVELOPER":          developer,
		"CAGY_PROJECT_DIR":        project,
	})
	application.stdin = strings.NewReader(task + "\n")
	if err := application.Run(context.Background(), []string{"ask", "--stdin"}); err != nil {
		t.Fatal(err)
	}
	runner.assertDone()
	if got := stdout.String(); got != "Implemented.\n" {
		t.Fatalf("stdout=%q", got)
	}
	if !strings.Contains(stderr.String(), "submitting one task") {
		t.Fatalf("stderr lacks immediate lifecycle status: %q", stderr.String())
	}
	if _, err := os.Stat(application.taskJournalPath(developer)); !os.IsNotExist(err) {
		t.Fatalf("delivered task journal still exists: %v", err)
	}
}

func TestDelegateTaskDoesNotWriteFinalAnswerToStdout(t *testing.T) {
	project, _ := filepath.EvalSymlinks(t.TempDir())
	developer := developerName("w1", "w1:p1")
	task := "Implement without stdout side effect"
	brainRoot := t.TempDir()
	writeAgyTranscript(t, brainRoot, testConversationID, task, "Direct delivery result.")
	runner := &scriptedRunner{t: t, steps: []runStep{
		{want: []string{"herdr", "pane", "get", "w1:p1"}, result: supervisorPaneJSON(project)},
		{want: []string{"herdr", "agent", "get", developer}, result: agentJSON("w1:p2", "w1", project, "idle")},
		{want: []string{"herdr", "pane", "get", "w1:p2"}, result: paneJSON("w1:p2", "w1:t1", project, developerPaneLabel, map[string]string{"cagy_owner": developer, "cagy_role": "developer"})},
		{want: agyProbeArgs("/model"), result: agyModelResult("gemini-3.8-flash-high", "Gemini 3.8 Flash (High)")},
		{want: agyProbeArgs("/quota"), result: agyQuotaResult("Gemini Models", 0.8, 0.9)},
		{want: []string{"herdr", "agent", "read", developer, "--source", "recent-unwrapped", "--lines", "400"}, result: textResult("old output\n")},
		{want: []string{"herdr", "agent", "prompt", developer, task, "--wait", "--timeout", "300000"}, result: agentJSONWithSession("w1:p2", "w1", project, "done", testConversationID)},
		{want: []string{"herdr", "agent", "read", developer, "--source", "recent-unwrapped", "--lines", "400"}, result: textResult("old output\nDirect delivery result.\n")},
		{want: []string{"herdr", "agent", "wait", developer, "--until", "blocked", "--timeout", "1000"}, result: jsonError("timeout", "timed out")},
		{want: []string{"herdr", "agent", "read", developer, "--source", "visible", "--lines", "80"}, result: textResult(">\n────────────────────\n? for shortcuts\n")},
		sessionOwnershipStep("w1:p2", testConversationID),
	}}
	var stdout, stderr strings.Builder
	application := New(runner, &stdout, &stderr)
	application.stateDir = t.TempDir()
	application.agyBrainRoot = brainRoot
	application.transcriptWait = time.Second
	application.getenv = envGetter(map[string]string{
		"HERDR_ENV":               "1",
		"HERDR_WORKSPACE_ID":      "w1",
		"HERDR_PANE_ID":           "w1:p1",
		"CAGY_SUPERVISOR_PANE_ID": "w1:p1",
		"CAGY_DEVELOPER":          developer,
		"CAGY_PROJECT_DIR":        project,
	})

	delivery, err := application.delegateTask(context.Background(), task)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	runner.assertDone()
	if delivery.output != "Direct delivery result." {
		t.Fatalf("expected delivery output %q, got %q", "Direct delivery result.", delivery.output)
	}
	if got := stdout.String(); got != "" {
		t.Fatalf("expected stdout to be empty, got: %q", got)
	}
}

func TestAskRecoversQuotaWithVisibleAGMPartialIDESuccess(t *testing.T) {
	project, _ := filepath.EvalSymlinks(t.TempDir())
	developer := developerName("w1", "w1:p1")
	task := "Finish the feature"
	continuation := continuationPrompt(task)
	brainRoot := t.TempDir()
	writeAgyTranscript(t, brainRoot, testConversationID, continuation, "Finished after account switch.")
	refreshMarker := "__CAGY_REFRESH_refresh1__"
	switchMarker := "__CAGY_SWITCH_switch1__"
	confirmRegex := `Switch to this account\? \[y/N\]:|` + completionPattern(switchMarker)
	steps := []runStep{
		{want: []string{"herdr", "pane", "get", "w1:p1"}, result: supervisorPaneJSON(project)},
		{want: []string{"herdr", "agent", "get", developer}, result: agentJSONWithSession("w1:p2", "w1", project, "idle", testConversationID)},
		{want: []string{"herdr", "pane", "get", "w1:p2"}, result: paneJSON("w1:p2", "w1:t1", project, developerPaneLabel, map[string]string{"cagy_owner": developer, "cagy_role": "developer"})},
		{want: agyProbeArgs("/model"), result: agyModelResult("gemini-3.8-flash-high", "Gemini 3.8 Flash (High)")},
		{want: agyProbeArgs("/quota"), result: agyQuotaResult("Gemini Models", 0.8, 0.9)},
		{want: []string{"herdr", "agent", "read", developer, "--source", "recent-unwrapped", "--lines", "400"}, result: textResult("old\n")},
		{want: []string{"herdr", "agent", "prompt", developer, task, "--wait", "--timeout", "300000"}, result: agentJSONWithSession("w1:p2", "w1", project, "done", testConversationID)},
		{want: []string{"herdr", "agent", "read", developer, "--source", "recent-unwrapped", "--lines", "400"}, result: textResult("old\nRESOURCE_EXHAUSTED: quota exceeded\n")},
		{want: []string{"herdr", "pane", "get", "w1:p1"}, result: supervisorPaneJSON(project)},
		{want: []string{"herdr", "agent", "get", developer}, result: agentJSONWithSession("w1:p2", "w1", project, "idle", testConversationID)},
		{want: []string{"herdr", "pane", "get", "w1:p2"}, result: paneJSON("w1:p2", "w1:t1", project, developerPaneLabel, map[string]string{"cagy_owner": developer, "cagy_role": "developer"})},
		sessionOwnershipStep("w1:p2", testConversationID),
		{want: []string{"herdr", "pane", "get", "w1:p1"}, result: supervisorPaneJSON(project)},
		{want: []string{"herdr", "pane", "split", "--current", "--direction", "right", "--cwd", project, "--no-focus"}, result: paneJSON("w1:p3", "w1:t1", project, recoveryPaneLabel, nil)},
		{want: []string{"herdr", "pane", "rename", "w1:p3", recoveryPaneLabel}, result: jsonResult(`{"type":"pane_info"}`)},
		ownershipStep("w1:p3", developer, "recovery"),
		{want: []string{"herdr", "pane", "run", "w1:p3", markedCommand("agm refresh-all", refreshMarker)}, result: jsonResult(`{"type":"pane_info"}`)},
		{want: []string{"herdr", "pane", "wait-output", "w1:p3", "--regex", completionPattern(refreshMarker), "--source", "recent-unwrapped", "--lines", "400", "--timeout", "1800000"}, result: jsonResult(`{"type":"output_matched"}`)},
		{want: []string{"herdr", "pane", "read", "w1:p3", "--source", "recent-unwrapped", "--lines", "400"}, result: textResult("Completed: 2 successful\n" + refreshMarker + ":0\n")},
		{want: []string{"herdr", "pane", "run", "w1:p3", markedCommand("agm auto-switch --min 5", switchMarker)}, result: jsonResult(`{"type":"pane_info"}`)},
		{want: []string{"herdr", "pane", "wait-output", "w1:p3", "--regex", confirmRegex, "--source", "recent-unwrapped", "--lines", "400", "--timeout", "300000"}, result: jsonResult(`{"type":"output_matched"}`)},
		{want: []string{"herdr", "pane", "read", "w1:p3", "--source", "recent-unwrapped", "--lines", "400"}, result: textResult("Best account: test@example.com\n" + agmConfirmation)},
		{want: []string{"herdr", "pane", "send-text", "w1:p3", "y"}, result: jsonResult(`{"type":"pane_info"}`)},
		{want: []string{"herdr", "pane", "send-keys", "w1:p3", "enter"}, result: jsonResult(`{"type":"pane_info"}`)},
		{want: []string{"herdr", "pane", "wait-output", "w1:p3", "--regex", completionPattern(switchMarker), "--source", "recent-unwrapped", "--lines", "400", "--timeout", "300000"}, result: jsonResult(`{"type":"output_matched"}`)},
		{want: []string{"herdr", "pane", "read", "w1:p3", "--source", "recent-unwrapped", "--lines", "400"}, result: textResult("  ✓ Antigravity CLI (agy)\n  ✗ Antigravity IDE: unavailable\n" + switchMarker + ":1\n")},
		{want: agyProbeArgs("/model"), result: agyModelResult("gemini-3.8-flash-high", "Gemini 3.8 Flash (High)")},
		{want: agyProbeArgs("/quota"), result: agyQuotaResult("Gemini Models", 0.8, 0.9)},
	}
	steps = append(steps, confirmedCloseSteps("w1:p3")...)
	steps = append(steps, restartInPlaceSteps(developer, project, "w1:p2")...)
	steps = append(steps,
		runStep{want: []string{"herdr", "agent", "read", developer, "--source", "recent-unwrapped", "--lines", "400"}, result: textResult("resumed\n")},
		runStep{want: []string{"herdr", "agent", "prompt", developer, continuation, "--wait", "--timeout", "300000"}, before: func() {
			appendAgyTranscript(t, brainRoot, testConversationID, continuation, "Finished after account switch.")
		}, result: agentJSONWithSession("w1:p2", "w1", project, "done", testConversationID)},
		runStep{want: []string{"herdr", "agent", "read", developer, "--source", "recent-unwrapped", "--lines", "400"}, result: textResult("resumed\nFinished after account switch.\n")},
		runStep{want: []string{"herdr", "agent", "wait", developer, "--until", "blocked", "--timeout", "1000"}, result: jsonError("timeout", "timed out")},
		runStep{want: []string{"herdr", "agent", "read", developer, "--source", "visible", "--lines", "80"}, result: textResult(">\n────────────────────\n? for shortcuts\n")},
		sessionOwnershipStep("w1:p2", testConversationID),
	)
	runner := &scriptedRunner{t: t, steps: steps}
	var stdout strings.Builder
	application := New(runner, &stdout, os.Stderr)
	application.stateDir = t.TempDir()
	application.agyBrainRoot = brainRoot
	application.transcriptWait = time.Second
	tokens := []string{"refresh1", "switch1"}
	application.token = func() (string, error) { token := tokens[0]; tokens = tokens[1:]; return token, nil }
	application.getenv = cagyEnv(project, developer)
	if err := application.ask(context.Background(), task); err != nil {
		t.Fatal(err)
	}
	runner.assertDone()
	if got := stdout.String(); got != "Finished after account switch.\n" {
		t.Fatalf("stdout=%q", got)
	}
}
func envGetter(values map[string]string) func(string) string {
	return func(key string) string { return values[key] }
}

func jsonResult(result string) proc.Result {
	return proc.Result{ExitCode: 0, Stdout: `{"id":"test","result":` + result + `}`}
}

func jsonError(code, message string) proc.Result {
	return proc.Result{ExitCode: 1, Stderr: `{"id":"test","error":{"code":"` + code + `","message":"` + message + `"}}`}
}

func cagyEnv(project, developer string) func(string) string {
	return envGetter(map[string]string{
		"HERDR_ENV":               "1",
		"HERDR_WORKSPACE_ID":      "w1",
		"HERDR_PANE_ID":           "w1:p1",
		"CAGY_SUPERVISOR_PANE_ID": "w1:p1",
		"CAGY_DEVELOPER":          developer,
		"CAGY_DEVELOPER_PANE_ID":  "w1:p2",
		"CAGY_PROJECT_DIR":        project,
	})
}

func paneJSON(paneID, tabID, project, label string, tokens map[string]string) proc.Result {
	tokenJSON := "{}"
	if len(tokens) != 0 {
		encoded, _ := json.Marshal(tokens)
		tokenJSON = string(encoded)
	}
	return jsonResult(`{"type":"pane_info","pane":{"pane_id":"` + paneID + `","workspace_id":"w1","tab_id":"` + tabID + `","cwd":"` + project + `","label":"` + label + `","tokens":` + tokenJSON + `}}`)
}

func supervisorPaneJSON(project string) proc.Result {
	return paneJSON("w1:p1", "w1:t1", project, "Codex Supervisor", nil)
}

func ownershipStep(paneID, developer, role string) runStep {
	return runStep{
		want:   []string{"herdr", "pane", "report-metadata", paneID, "--source", paneOwnershipSource, "--token", "cagy_owner=" + developer, "--token", "cagy_role=" + role},
		result: jsonResult(`{"type":"ok"}`),
	}
}

func sessionOwnershipStep(paneID, sessionID string) runStep {
	return runStep{
		want:   []string{"herdr", "pane", "report-metadata", paneID, "--source", paneOwnershipSource, "--token", "cagy_session=" + sessionID, "--token", agySessionStateToken + "=" + agySessionStateReady},
		result: jsonResult(`{"type":"ok"}`),
	}
}

func pendingSessionStep(paneID string) runStep {
	return runStep{
		want:   []string{"herdr", "pane", "report-metadata", paneID, "--source", paneOwnershipSource, "--token", agySessionStateToken + "=" + agySessionStatePending},
		result: jsonResult(`{"type":"ok"}`),
	}
}

func exactSessionCaptureSteps(developer, project, paneID, sessionID string) []runStep {
	return []runStep{
		{want: []string{"herdr", "pane", "get", "w1:p1"}, result: supervisorPaneJSON(project)},
		{want: []string{"herdr", "agent", "get", developer}, result: agentJSONWithSession(paneID, "w1", project, "idle", sessionID)},
		{want: []string{"herdr", "pane", "get", paneID}, result: paneJSON(paneID, "w1:t1", project, developerPaneLabel, map[string]string{"cagy_owner": developer, "cagy_role": "developer"})},
		sessionOwnershipStep(paneID, sessionID),
	}
}

func pendingSessionCaptureSteps(developer, project, paneID string) []runStep {
	return []runStep{
		{want: []string{"herdr", "pane", "get", "w1:p1"}, result: supervisorPaneJSON(project)},
		{want: []string{"herdr", "agent", "get", developer}, result: agentJSON(paneID, "w1", project, "idle")},
		{want: []string{"herdr", "pane", "get", paneID}, result: paneJSON(paneID, "w1:t1", project, developerPaneLabel, map[string]string{"cagy_owner": developer, "cagy_role": "developer", agySessionStateToken: agySessionStatePending})},
		pendingSessionStep(paneID),
	}
}

func restartFreshInPlaceSteps(developer, project, paneID string) []runStep {
	return []runStep{
		{want: []string{"herdr", "pane", "get", "w1:p1"}, result: supervisorPaneJSON(project)},
		{want: []string{"herdr", "agent", "get", developer}, result: agentJSON(paneID, "w1", project, "idle")},
		{want: []string{"herdr", "pane", "get", paneID}, result: paneJSON(paneID, "w1:t1", project, developerPaneLabel, map[string]string{"cagy_owner": developer, "cagy_role": "developer", agySessionStateToken: agySessionStatePending})},
		ownershipStep(paneID, developer, "developer"),
		{want: []string{"herdr", "pane", "report-metadata", paneID, "--source", developerDisplaySource, "--agent", "agy", "--display-agent", developerDisplayName}, result: jsonResult(`{"type":"ok"}`)},
		{want: []string{"herdr", "agent", "send-keys", developer, "ctrl+c"}, result: jsonResult(`{"type":"agent_info"}`)},
		{want: []string{"herdr", "agent", "get", developer}, result: jsonError("agent_not_found", "stopped")},
		{want: []string{"herdr", "agent", "start", developer, "--kind", "agy", "--pane", paneID, "--timeout", "60000", "--", "--dangerously-skip-permissions", "--mode", "accept-edits"}, result: agentJSON(paneID, "w1", project, "idle")},
		{want: []string{"herdr", "pane", "rename", paneID, developerPaneLabel}, result: jsonResult(`{"type":"pane_info"}`)},
		ownershipStep(paneID, developer, "developer"),
		{want: []string{"herdr", "pane", "report-metadata", paneID, "--source", developerDisplaySource, "--agent", "agy", "--display-agent", developerDisplayName}, result: jsonResult(`{"type":"ok"}`)},
		pendingSessionStep(paneID),
		{want: []string{"herdr", "pane", "read", paneID, "--source", "recent-unwrapped", "--lines", "200"}, result: textResult("? for shortcuts\n")},
		{want: []string{"herdr", "pane", "wait-output", paneID, "--match", "? for shortcuts", "--source", "recent-unwrapped", "--lines", "400", "--timeout", "60000"}, result: jsonResult(`{"type":"output_matched"}`)},
	}
}

func restartInPlaceSteps(developer, project, paneID string) []runStep {
	return []runStep{
		{want: []string{"herdr", "pane", "get", "w1:p1"}, result: supervisorPaneJSON(project)},
		{want: []string{"herdr", "agent", "get", developer}, result: agentJSONWithSession(paneID, "w1", project, "idle", testConversationID)},
		{want: []string{"herdr", "pane", "get", paneID}, result: paneJSON(paneID, "w1:t1", project, developerPaneLabel, map[string]string{"cagy_owner": developer, "cagy_role": "developer"})},
		ownershipStep(paneID, developer, "developer"),
		{want: []string{"herdr", "pane", "report-metadata", paneID, "--source", developerDisplaySource, "--agent", "agy", "--display-agent", developerDisplayName}, result: jsonResult(`{"type":"ok"}`)},
		{want: []string{"herdr", "agent", "send-keys", developer, "ctrl+c"}, result: jsonResult(`{"type":"agent_info"}`)},
		{want: []string{"herdr", "agent", "get", developer}, result: jsonError("agent_not_found", "stopped")},
		{want: []string{"herdr", "agent", "start", developer, "--kind", "agy", "--pane", paneID, "--timeout", "60000", "--", "--conversation", testConversationID, "--dangerously-skip-permissions", "--mode", "accept-edits"}, result: agentJSONWithSession(paneID, "w1", project, "idle", testConversationID)},
		{want: []string{"herdr", "pane", "rename", paneID, developerPaneLabel}, result: jsonResult(`{"type":"pane_info"}`)},
		ownershipStep(paneID, developer, "developer"),
		{want: []string{"herdr", "pane", "report-metadata", paneID, "--source", developerDisplaySource, "--agent", "agy", "--display-agent", developerDisplayName}, result: jsonResult(`{"type":"ok"}`)},
		sessionOwnershipStep(paneID, testConversationID),
		{want: []string{"herdr", "pane", "read", paneID, "--source", "recent-unwrapped", "--lines", "200"}, result: textResult("? for shortcuts\n")},
		{want: []string{"herdr", "pane", "wait-output", paneID, "--match", "? for shortcuts", "--source", "recent-unwrapped", "--lines", "400", "--timeout", "60000"}, result: jsonResult(`{"type":"output_matched"}`)},
	}
}

func paneListJSON(panes ...herdr.PaneInfo) proc.Result {
	encoded, _ := json.Marshal(map[string]any{"type": "pane_list", "panes": panes})
	return jsonResult(string(encoded))
}

func confirmedCloseSteps(paneID string) []runStep {
	return []runStep{
		{want: []string{"herdr", "pane", "close", paneID}, result: jsonResult(`{"type":"pane_info"}`)},
		{want: []string{"herdr", "pane", "get", paneID}, result: jsonError("pane_not_found", "pane not found")},
	}
}

func paneProcessJSON(paneID string, processes ...string) proc.Result {
	var procs []herdr.PaneProcess
	for _, p := range processes {
		procs = append(procs, herdr.PaneProcess{Name: p, Argv: []string{p}})
	}
	encoded, _ := json.Marshal(map[string]any{
		"type": "pane_process_info",
		"process_info": herdr.PaneProcessInfo{
			PaneID:              paneID,
			ForegroundProcesses: procs,
		},
	})
	return jsonResult(string(encoded))
}

func developerValidationSteps(developer, project string) []runStep {
	return []runStep{
		{want: []string{"herdr", "pane", "get", "w1:p1"}, result: supervisorPaneJSON(project)},
		{want: []string{"herdr", "agent", "get", developer}, result: agentJSONWithSession("w1:p2", "w1", project, "idle", testConversationID)},
		{want: []string{"herdr", "pane", "get", "w1:p2"}, result: paneJSON("w1:p2", "w1:t1", project, developerPaneLabel, map[string]string{"cagy_owner": developer, "cagy_role": "developer"})},
	}
}

func textResult(text string) proc.Result {
	return proc.Result{ExitCode: 0, Stdout: text}
}

func agentJSON(paneID, workspaceID, project, status string) proc.Result {
	return jsonResult(`{"type":"agent_info","agent":{"agent":"agy","agent_status":"` + status + `","pane_id":"` + paneID + `","workspace_id":"` + workspaceID + `","tab_id":"w1:t1","foreground_cwd":"` + project + `"}}`)
}

const testConversationID = "512995cf-e151-4934-9dad-c43327869bf1"

func agentJSONWithSession(paneID, workspaceID, project, status, sessionID string) proc.Result {
	return jsonResult(`{"type":"agent_info","agent":{"agent":"agy","agent_status":"` + status + `","pane_id":"` + paneID + `","workspace_id":"` + workspaceID + `","tab_id":"w1:t1","foreground_cwd":"` + project + `","agent_session":{"source":"herdr:antigravity_cli","agent":"agy","kind":"id","value":"` + sessionID + `"}}}`)
}

func writeAgyTranscript(t *testing.T, brainRoot, sessionID, task, answer string) {
	t.Helper()
	path := filepath.Join(brainRoot, sessionID, ".system_generated", "logs", "transcript.jsonl")
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		t.Fatal(err)
	}
	events := []map[string]string{
		{"source": "USER_EXPLICIT", "type": "USER_INPUT", "status": "DONE", "content": "<USER_REQUEST>\n" + task + "\n</USER_REQUEST>"},
		{"source": "MODEL", "type": "PLANNER_RESPONSE", "status": "DONE", "content": answer},
	}
	file, err := os.Create(path)
	if err != nil {
		t.Fatal(err)
	}
	defer file.Close()
	encoder := json.NewEncoder(file)
	for _, event := range events {
		if err := encoder.Encode(event); err != nil {
			t.Fatal(err)
		}
	}
}

func appendAgyTranscript(t *testing.T, brainRoot, sessionID, task, answer string) {
	t.Helper()
	path := filepath.Join(brainRoot, sessionID, ".system_generated", "logs", "transcript.jsonl")
	file, err := os.OpenFile(path, os.O_WRONLY|os.O_APPEND, 0o600)
	if err != nil {
		t.Fatal(err)
	}
	defer file.Close()
	encoder := json.NewEncoder(file)
	for _, event := range []map[string]string{
		{"source": "USER_EXPLICIT", "type": "USER_INPUT", "status": "DONE", "content": "<USER_REQUEST>\n" + task + "\n</USER_REQUEST>"},
		{"source": "MODEL", "type": "PLANNER_RESPONSE", "status": "DONE", "content": answer},
	} {
		if err := encoder.Encode(event); err != nil {
			t.Fatal(err)
		}
	}
}

func slicesEqual(left, right []string) bool {
	if len(left) != len(right) {
		return false
	}
	for i := range left {
		if left[i] != right[i] {
			return false
		}
	}
	return true
}

func TestRunHelpAndUsageErrors(t *testing.T) {
	runner := &fakeRunner{}
	var stdout strings.Builder
	application := New(runner, &stdout, os.Stderr)
	if err := application.Run(context.Background(), []string{"--help"}); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(stdout.String(), "cagy doctor") {
		t.Fatalf("help output=%q", stdout.String())
	}
	for _, flag := range []string{"--help", "-h"} {
		stdout.Reset()
		if err := application.Run(context.Background(), []string{"ask", flag}); err != nil {
			t.Fatal(err)
		}
		if !strings.Contains(stdout.String(), `cagy ask "<development task>"`) {
			t.Fatalf("ask help output=%q", stdout.String())
		}
	}
	if err := application.Run(context.Background(), []string{"ask"}); err == nil {
		t.Fatal("expected missing-task usage error")
	}
	if err := application.Run(context.Background(), []string{"one", "two"}); err == nil {
		t.Fatal("expected too-many-arguments usage error")
	}
}

func TestDoctorChecksInstalledCapabilities(t *testing.T) {
	runner := &scriptedRunner{t: t, steps: []runStep{
		{want: []string{"codex", "--yolo", "--help"}, result: proc.Result{ExitCode: 0}},
		{want: []string{"codex", "mcp", "--help"}, result: textResult("Commands:\n  list\n")},
		{want: []string{"agy", "--help"}, result: textResult("--dangerously-skip-permissions --mode accept-edits --conversation --print --output-format --print-timeout")},
		{want: []string{"herdr", "agent"}, result: proc.Result{ExitCode: 2, Stderr: "agent start agent prompt agent wait kinds: agy"}},
		{want: []string{"herdr", "pane"}, result: proc.Result{ExitCode: 2, Stderr: "pane split pane run pane close pane report-metadata"}},
		{want: []string{"herdr", "pane", "report-metadata", "--help"}, result: textResult("--source --agent --display-agent --token")},
		{want: []string{"herdr", "api", "schema", "--json"}, result: textResult("agent.view.set agent.view.clear")},
		{want: []string{"agm", "help"}, result: textResult("refresh-all auto-switch")},
		{want: []string{"agm", "auto-switch", "--help"}, result: textResult("--min")},
		{want: []string{"herdr", "integration", "status"}, result: textResult("antigravity-cli: current (v3) (/tmp/hook)\n")},
		{want: []string{"herdr", "pane", "current", "--current"}, result: jsonResult(`{"type":"pane_current","pane":{"pane_id":"w1:p1","workspace_id":"w1"}}`)},
	}}
	var stdout strings.Builder
	application := New(runner, &stdout, os.Stderr)
	application.getenv = envGetter(map[string]string{
		"HERDR_ENV":          "1",
		"HERDR_WORKSPACE_ID": "w1",
		"HERDR_PANE_ID":      "w1:p1",
	})
	if err := application.doctor(context.Background()); err != nil {
		t.Fatal(err)
	}
	runner.assertDone()
	if len(runner.lookups) != 4 {
		t.Fatalf("lookups=%#v", runner.lookups)
	}
	if !strings.Contains(stdout.String(), "cagy is ready") {
		t.Fatalf("doctor output=%q", stdout.String())
	}
}

func TestDoctorFailsWhenCodexLacksMCPSupport(t *testing.T) {
	runner := &scriptedRunner{t: t, steps: []runStep{
		{want: []string{"codex", "--yolo", "--help"}, result: proc.Result{ExitCode: 0}},
		{want: []string{"codex", "mcp", "--help"}, result: proc.Result{ExitCode: 1, Stderr: "error: unrecognized subcommand 'mcp'"}},
		{want: []string{"agy", "--help"}, result: textResult("--dangerously-skip-permissions --mode accept-edits --conversation --print --output-format --print-timeout")},
		{want: []string{"herdr", "agent"}, result: proc.Result{ExitCode: 2, Stderr: "agent start agent prompt agent wait kinds: agy"}},
		{want: []string{"herdr", "pane"}, result: proc.Result{ExitCode: 2, Stderr: "pane split pane run pane close pane report-metadata"}},
		{want: []string{"herdr", "pane", "report-metadata", "--help"}, result: textResult("--source --agent --display-agent --token")},
		{want: []string{"herdr", "api", "schema", "--json"}, result: textResult("agent.view.set agent.view.clear")},
		{want: []string{"agm", "help"}, result: textResult("refresh-all auto-switch")},
		{want: []string{"agm", "auto-switch", "--help"}, result: textResult("--min")},
		{want: []string{"herdr", "integration", "status"}, result: textResult("antigravity-cli: current (v3) (/tmp/hook)\n")},
		{want: []string{"herdr", "pane", "current", "--current"}, result: jsonResult(`{"type":"pane_current","pane":{"pane_id":"w1:p1","workspace_id":"w1"}}`)},
	}}
	var stdout strings.Builder
	application := New(runner, &stdout, os.Stderr)
	application.getenv = envGetter(map[string]string{
		"HERDR_ENV":          "1",
		"HERDR_WORKSPACE_ID": "w1",
		"HERDR_PANE_ID":      "w1:p1",
	})
	err := application.doctor(context.Background())
	if err == nil {
		t.Fatal("expected doctor to fail when Codex lacks MCP support")
	}
	if !strings.Contains(stdout.String(), "✗ Codex MCP support") {
		t.Fatalf("expected doctor output to show failed MCP check: %s", stdout.String())
	}
	runner.assertDone()
}

func TestStopClosesOnlyVerifiedDeveloperPane(t *testing.T) {
	project, _ := filepath.EvalSymlinks(t.TempDir())
	developer := developerName("w1", "w1:p1")
	runner := &scriptedRunner{t: t, steps: []runStep{
		{want: []string{"herdr", "pane", "get", "w1:p1"}, result: supervisorPaneJSON(project)},
		{want: []string{"herdr", "agent", "get", developer}, result: agentJSON("w1:p2", "w1", project, "idle")},
		{want: []string{"herdr", "pane", "get", "w1:p2"}, result: paneJSON("w1:p2", "w1:t1", project, developerPaneLabel, map[string]string{"cagy_owner": developer, "cagy_role": "developer"})},
		{want: []string{"herdr", "agent", "send-keys", developer, "ctrl+c"}, result: jsonResult(`{"type":"agent_info"}`)},
		{want: []string{"herdr", "agent", "get", developer}, result: jsonError("agent_not_found", "stopped")},
		{want: []string{"herdr", "pane", "close", "w1:p2"}, result: jsonResult(`{"type":"pane_info"}`)},
	}}
	var stdout strings.Builder
	application := New(runner, &stdout, os.Stderr)
	application.getenv = envGetter(map[string]string{
		"HERDR_ENV":               "1",
		"HERDR_WORKSPACE_ID":      "w1",
		"HERDR_PANE_ID":           "w1:p1",
		"CAGY_SUPERVISOR_PANE_ID": "w1:p1",
		"CAGY_DEVELOPER":          developer,
		"CAGY_PROJECT_DIR":        project,
	})
	if err := application.stop(context.Background()); err != nil {
		t.Fatal(err)
	}
	runner.assertDone()
	if !strings.Contains(stdout.String(), "stopped the agy developer") {
		t.Fatalf("stop output=%q", stdout.String())
	}
}

func TestStopAbortsIfAgentReleaseTimesOut(t *testing.T) {
	project, _ := filepath.EvalSymlinks(t.TempDir())
	developer := developerName("w1", "w1:p1")
	runner := &scriptedRunner{t: t, steps: []runStep{
		{want: []string{"herdr", "pane", "get", "w1:p1"}, result: supervisorPaneJSON(project)},
		{want: []string{"herdr", "agent", "get", developer}, result: agentJSON("w1:p2", "w1", project, "idle")},
		{want: []string{"herdr", "pane", "get", "w1:p2"}, result: paneJSON("w1:p2", "w1:t1", project, developerPaneLabel, map[string]string{"cagy_owner": developer, "cagy_role": "developer"})},
		{want: []string{"herdr", "agent", "send-keys", developer, "ctrl+c"}, result: jsonResult(`{"type":"agent_info"}`)},
		{want: []string{"herdr", "agent", "get", developer}, result: agentJSON("w1:p2", "w1", project, "working")},
	}}
	app := New(runner, &strings.Builder{}, &strings.Builder{})
	app.agentStopTimeout = 1 * time.Millisecond
	app.developerPoll = time.Second
	app.getenv = envGetter(map[string]string{
		"HERDR_ENV":               "1",
		"HERDR_WORKSPACE_ID":      "w1",
		"HERDR_PANE_ID":           "w1:p1",
		"CAGY_SUPERVISOR_PANE_ID": "w1:p1",
		"CAGY_DEVELOPER":          developer,
		"CAGY_PROJECT_DIR":        project,
	})
	err := app.stop(context.Background())
	if err == nil || !strings.Contains(err.Error(), "wait for agy developer to stop") || !strings.Contains(err.Error(), "developer pane was kept") {
		t.Fatalf("error=%v", err)
	}
	runner.assertDone()
	for _, call := range runner.calls {
		if strings.Contains(strings.Join(call, " "), "pane close") {
			t.Fatalf("pane was closed despite agent release timeout: %v", call)
		}
	}
}
func TestRecoveryStopsAfterTwoAttemptsAndKeepsOriginalDeveloper(t *testing.T) {
	project, _ := filepath.EvalSymlinks(t.TempDir())
	developer := developerName("w1", "w1:p1")
	info := runtimeContext{workspaceID: "w1", supervisor: "w1:p1", developer: developer, developerPane: "w1:p2", project: project}
	agent := herdr.AgentInfo{Agent: "agy", AgentStatus: "done", PaneID: "w1:p2", WorkspaceID: "w1", ForegroundCWD: project, AgentSession: &herdr.AgentSessionInfo{Source: "herdr:antigravity_cli", Agent: "agy", Kind: "id", Value: testConversationID}}
	refreshMarker := "__CAGY_REFRESH_refresh1__"
	firstMarker := "__CAGY_SWITCH_switch1__"
	secondMarker := "__CAGY_SWITCH_switch2__"
	steps := exactSessionCaptureSteps(developer, project, "w1:p2", testConversationID)
	steps = append(steps, recoveryAttemptStartSteps("w1:p3", project, developer)...)
	steps = append(steps,
		runStep{want: []string{"herdr", "pane", "run", "w1:p3", markedCommand("agm refresh-all", refreshMarker)}, result: jsonResult(`{"type":"pane_info"}`)},
		runStep{want: []string{"herdr", "pane", "wait-output", "w1:p3", "--regex", completionPattern(refreshMarker), "--source", "recent-unwrapped", "--lines", "400", "--timeout", "1800000"}, result: jsonResult(`{"type":"output_matched"}`)},
		runStep{want: []string{"herdr", "pane", "read", "w1:p3", "--source", "recent-unwrapped", "--lines", "400"}, result: textResult("Completed: 2 successful\n" + refreshMarker + ":0\n")},
	)
	steps = append(steps, failedAutoSwitchSteps("w1:p3", firstMarker)...)
	steps = append(steps, runStep{want: []string{"herdr", "pane", "close", "w1:p3"}, result: jsonResult(`{"type":"pane_info"}`)})
	steps = append(steps, exactSessionCaptureSteps(developer, project, "w1:p2", testConversationID)...)
	steps = append(steps, recoveryAttemptStartSteps("w1:p4", project, developer)...)
	steps = append(steps, failedAutoSwitchSteps("w1:p4", secondMarker)...)

	runner := &scriptedRunner{t: t, steps: steps}
	application := New(runner, &strings.Builder{}, &strings.Builder{})
	application.stateDir = t.TempDir()
	tokens := []string{"refresh1", "switch1", "switch2"}
	application.token = func() (string, error) { token := tokens[0]; tokens = tokens[1:]; return token, nil }
	_, _, err := application.recover(context.Background(), info, agent, "task", true)
	if err == nil || !strings.Contains(err.Error(), "original developer was kept") {
		t.Fatalf("error=%v", err)
	}
	runner.assertDone()
	for _, call := range runner.calls {
		joined := strings.Join(call, " ")
		if strings.Contains(joined, "agent send-keys") || strings.Contains(joined, "agent start") || (strings.Contains(joined, "pane close") && strings.Contains(joined, "w1:p2")) {
			t.Fatalf("original developer was mutated before a healthy replacement: %q", joined)
		}
	}
}

func TestAskPreflightQuotaRecoverySendsOriginalTask(t *testing.T) {
	project, _ := filepath.EvalSymlinks(t.TempDir())
	developer := developerName("w1", "w1:p1")
	task := "Implement the original task"
	brainRoot := t.TempDir()
	writeAgyTranscript(t, brainRoot, testConversationID, task, "Completed original task.")
	marker := "__CAGY_SWITCH_switch1__"
	steps := append(developerValidationSteps(developer, project),
		runStep{want: agyProbeArgs("/model"), result: agyModelResult("gemini-3.8-flash-high", "Gemini 3.8 Flash (High)")},
		runStep{want: agyProbeArgs("/quota"), result: agyQuotaResult("Gemini Models", 0.03, 0.9)},
	)
	steps = append(steps, exactSessionCaptureSteps(developer, project, "w1:p2", testConversationID)...)
	steps = append(steps, recoveryAttemptStartSteps("w1:p3", project, developer)...)
	steps = append(steps, successfulAutoSwitchSteps("w1:p3", marker)...)
	steps = append(steps,
		runStep{want: agyProbeArgs("/model"), result: agyModelResult("gemini-3.8-flash-high", "Gemini 3.8 Flash (High)")},
		runStep{want: agyProbeArgs("/quota"), result: agyQuotaResult("Gemini Models", 0.8, 0.9)},
	)
	steps = append(steps, confirmedCloseSteps("w1:p3")...)
	steps = append(steps, restartInPlaceSteps(developer, project, "w1:p2")...)
	steps = append(steps,
		runStep{want: []string{"herdr", "agent", "read", developer, "--source", "recent-unwrapped", "--lines", "400"}, result: textResult("resumed\n")},
		runStep{want: []string{"herdr", "agent", "prompt", developer, task, "--wait", "--timeout", "300000"}, before: func() { appendAgyTranscript(t, brainRoot, testConversationID, task, "Completed original task.") }, result: agentJSONWithSession("w1:p2", "w1", project, "done", testConversationID)},
		runStep{want: []string{"herdr", "agent", "read", developer, "--source", "recent-unwrapped", "--lines", "400"}, result: textResult("resumed\nCompleted original task.\n")},
		runStep{want: []string{"herdr", "agent", "wait", developer, "--until", "blocked", "--timeout", "1000"}, result: jsonError("timeout", "timed out")},
		runStep{want: []string{"herdr", "agent", "read", developer, "--source", "visible", "--lines", "80"}, result: textResult(">\n────────────────────\n? for shortcuts\n")},
		sessionOwnershipStep("w1:p2", testConversationID),
	)
	runner := &scriptedRunner{t: t, steps: steps}
	var stdout strings.Builder
	application := New(runner, &stdout, &strings.Builder{})
	application.stateDir = t.TempDir()
	application.agyBrainRoot = brainRoot
	application.transcriptWait = time.Second
	application.now = func() time.Time { return time.Date(2026, time.September, 17, 12, 0, 0, 0, time.UTC) }
	if err := application.recordAGMRefresh(application.now().Add(-30 * time.Minute)); err != nil {
		t.Fatal(err)
	}
	application.token = func() (string, error) { return "switch1", nil }
	application.getenv = cagyEnv(project, developer)
	if err := application.ask(context.Background(), task); err != nil {
		t.Fatal(err)
	}
	runner.assertDone()
	if got := stdout.String(); got != "Completed original task.\n" {
		t.Fatalf("stdout=%q", got)
	}
}

func TestRecoveryRejectsUnhealthyOrUnreadableSwitchedAccountsWithoutStoppingDeveloper(t *testing.T) {
	project, _ := filepath.EvalSymlinks(t.TempDir())
	developer := developerName("w1", "w1:p1")
	info := runtimeContext{workspaceID: "w1", supervisor: "w1:p1", developer: developer, developerPane: "w1:p2", project: project}
	agent := herdr.AgentInfo{Agent: "agy", AgentStatus: "done", PaneID: "w1:p2", WorkspaceID: "w1", ForegroundCWD: project, AgentSession: &herdr.AgentSessionInfo{Source: "herdr:antigravity_cli", Agent: "agy", Kind: "id", Value: testConversationID}}
	firstMarker := "__CAGY_SWITCH_switch1__"
	secondMarker := "__CAGY_SWITCH_switch2__"
	steps := exactSessionCaptureSteps(developer, project, "w1:p2", testConversationID)
	steps = append(steps, recoveryAttemptStartSteps("w1:p3", project, developer)...)
	steps = append(steps, successfulAutoSwitchSteps("w1:p3", firstMarker)...)
	steps = append(steps,
		runStep{want: agyProbeArgs("/model"), result: agyModelResult("gemini-3.8-flash-high", "Gemini 3.8 Flash (High)")},
		runStep{want: agyProbeArgs("/quota"), result: agyQuotaResult("Gemini Models", 0.03, 0.9)},
		runStep{want: []string{"herdr", "pane", "close", "w1:p3"}, result: jsonResult(`{"type":"pane_info"}`)},
	)
	steps = append(steps, exactSessionCaptureSteps(developer, project, "w1:p2", testConversationID)...)
	steps = append(steps, recoveryAttemptStartSteps("w1:p4", project, developer)...)
	steps = append(steps, successfulAutoSwitchSteps("w1:p4", secondMarker)...)
	steps = append(steps, runStep{want: agyProbeArgs("/model"), result: textResult("not-json")})

	runner := &scriptedRunner{t: t, steps: steps}
	application := New(runner, &strings.Builder{}, &strings.Builder{})
	application.stateDir = t.TempDir()
	application.now = func() time.Time { return time.Date(2026, time.September, 17, 12, 0, 0, 0, time.UTC) }
	if err := application.recordAGMRefresh(application.now().Add(-30 * time.Minute)); err != nil {
		t.Fatal(err)
	}
	tokens := []string{"switch1", "switch2"}
	application.token = func() (string, error) { token := tokens[0]; tokens = tokens[1:]; return token, nil }
	_, _, err := application.recover(context.Background(), info, agent, "task", true)
	if err == nil || !strings.Contains(err.Error(), "verify switched agy account") {
		t.Fatalf("error=%v", err)
	}
	runner.assertDone()
	for _, call := range runner.calls {
		joined := strings.Join(call, " ")
		if strings.Contains(joined, "agent send-keys") || strings.Contains(joined, "agent start") {
			t.Fatalf("developer changed before quota verification: %q", joined)
		}
	}
}

func recoveryAttemptStartSteps(paneID, project, developer string) []runStep {
	return []runStep{
		{want: []string{"herdr", "pane", "get", "w1:p1"}, result: supervisorPaneJSON(project)},
		{want: []string{"herdr", "pane", "split", "--current", "--direction", "right", "--cwd", project, "--no-focus"}, result: paneJSON(paneID, "w1:t1", project, "", nil)},
		{want: []string{"herdr", "pane", "rename", paneID, recoveryPaneLabel}, result: jsonResult(`{"type":"pane_info"}`)},
		ownershipStep(paneID, developer, "recovery"),
	}
}

func successfulAutoSwitchSteps(paneID, marker string) []runStep {
	confirmRegex := `Switch to this account\? \[y/N\]:|` + completionPattern(marker)
	return []runStep{
		{want: []string{"herdr", "pane", "run", paneID, markedCommand("agm auto-switch --min 5", marker)}, result: jsonResult(`{"type":"pane_info"}`)},
		{want: []string{"herdr", "pane", "wait-output", paneID, "--regex", confirmRegex, "--source", "recent-unwrapped", "--lines", "400", "--timeout", "300000"}, result: jsonResult(`{"type":"output_matched"}`)},
		{want: []string{"herdr", "pane", "read", paneID, "--source", "recent-unwrapped", "--lines", "400"}, result: textResult("Best account: candidate@example.com\n" + agmConfirmation)},
		{want: []string{"herdr", "pane", "send-text", paneID, "y"}, result: jsonResult(`{"type":"pane_info"}`)},
		{want: []string{"herdr", "pane", "send-keys", paneID, "enter"}, result: jsonResult(`{"type":"pane_info"}`)},
		{want: []string{"herdr", "pane", "wait-output", paneID, "--regex", completionPattern(marker), "--source", "recent-unwrapped", "--lines", "400", "--timeout", "300000"}, result: jsonResult(`{"type":"output_matched"}`)},
		{want: []string{"herdr", "pane", "read", paneID, "--source", "recent-unwrapped", "--lines", "400"}, result: textResult("  ✓ Antigravity CLI (agy)\n" + marker + ":0\n")},
	}
}

func failedAutoSwitchSteps(paneID, marker string) []runStep {
	return []runStep{
		{want: []string{"herdr", "pane", "run", paneID, markedCommand("agm auto-switch --min 5", marker)}, result: jsonResult(`{"type":"pane_info"}`)},
		{want: []string{"herdr", "pane", "wait-output", paneID, "--regex", `Switch to this account\? \[y/N\]:|` + completionPattern(marker), "--source", "recent-unwrapped", "--lines", "400", "--timeout", "300000"}, result: jsonResult(`{"type":"output_matched"}`)},
		{want: []string{"herdr", "pane", "read", paneID, "--source", "recent-unwrapped", "--lines", "400"}, result: textResult("no account available\n" + marker + ":1\n")},
	}
}

func TestContextRejectsStaleDeveloperIdentity(t *testing.T) {
	application := New(&fakeRunner{}, &strings.Builder{}, &strings.Builder{})
	application.getenv = envGetter(map[string]string{
		"HERDR_ENV":               "1",
		"HERDR_WORKSPACE_ID":      "w1",
		"HERDR_PANE_ID":           "w1:p1",
		"CAGY_SUPERVISOR_PANE_ID": "w1:p1",
		"CAGY_DEVELOPER":          "cagy_dev_stale",
		"CAGY_PROJECT_DIR":        "/tmp/project",
	})
	if _, err := application.context(); err == nil || !strings.Contains(err.Error(), "identity is stale") {
		t.Fatalf("error=%v", err)
	}
}

func TestEnsureDeveloperRepairsOwnedRecoveryPane(t *testing.T) {
	project, _ := filepath.EvalSymlinks(t.TempDir())
	developer := developerName("w1", "w1:p1")
	info := runtimeContext{workspaceID: "w1", supervisor: "w1:p1", developer: developer, developerPane: "w1:p2", project: project}
	runner := &scriptedRunner{t: t, steps: []runStep{
		{want: []string{"herdr", "pane", "get", "w1:p1"}, result: supervisorPaneJSON(project)},
		{want: []string{"herdr", "agent", "get", developer}, result: jsonError("agent_not_found", "missing")},
		{want: []string{"herdr", "pane", "get", "w1:p1"}, result: supervisorPaneJSON(project)},
		{want: []string{"herdr", "pane", "list", "--workspace", "w1"}, result: paneListJSON(
			herdr.PaneInfo{PaneID: "w1:p1", WorkspaceID: "w1", TabID: "w1:t1", CWD: project, Label: "Codex Supervisor", Agent: "codex"},
			herdr.PaneInfo{PaneID: "w1:p2", WorkspaceID: "w1", TabID: "w1:t1", CWD: project, Label: recoveryPaneLabel, Tokens: map[string]string{"cagy_owner": developer, "cagy_role": "recovery", "cagy_session": testConversationID, agySessionStateToken: agySessionStateReady}},
		)},
		{want: []string{"herdr", "pane", "process-info", "--pane", "w1:p2"}, result: paneProcessJSON("w1:p2", "zsh")},
		{want: []string{"herdr", "pane", "rename", "w1:p2", recoveryPaneLabel}, result: jsonResult(`{"type":"pane_info"}`)},
		ownershipStep("w1:p2", developer, "recovery"),
		{want: []string{"herdr", "pane", "rename", "w1:p2", developerPaneLabel}, result: jsonResult(`{"type":"pane_info"}`)},
		{want: []string{"herdr", "agent", "start", developer, "--kind", "agy", "--pane", "w1:p2", "--timeout", "60000", "--", "--conversation", testConversationID, "--dangerously-skip-permissions", "--mode", "accept-edits"}, result: agentJSONWithSession("w1:p2", "w1", project, "idle", testConversationID)},
		ownershipStep("w1:p2", developer, "developer"),
		{want: []string{"herdr", "pane", "report-metadata", "w1:p2", "--source", developerDisplaySource, "--agent", "agy", "--display-agent", developerDisplayName}, result: jsonResult(`{"type":"ok"}`)},
		sessionOwnershipStep("w1:p2", testConversationID),
		{want: []string{"herdr", "pane", "read", "w1:p2", "--source", "recent-unwrapped", "--lines", "200"}, result: textResult("? for shortcuts\n")},
		{want: []string{"herdr", "pane", "wait-output", "w1:p2", "--match", "? for shortcuts", "--source", "recent-unwrapped", "--lines", "400", "--timeout", "60000"}, result: jsonResult(`{"type":"output_matched"}`)},
	}}
	application := New(runner, &strings.Builder{}, &strings.Builder{})
	got, err := application.ensureDeveloper(context.Background(), info)
	if err != nil {
		t.Fatal(err)
	}
	if got.PaneID != "w1:p2" {
		t.Fatalf("developer=%+v", got)
	}
	runner.assertDone()
}

func TestEnsureDeveloperRejectsMultipleOwnedRecoveryPanes(t *testing.T) {
	project, _ := filepath.EvalSymlinks(t.TempDir())
	developer := developerName("w1", "w1:p1")
	info := runtimeContext{workspaceID: "w1", supervisor: "w1:p1", developer: developer, project: project}
	runner := &scriptedRunner{t: t, steps: []runStep{
		{want: []string{"herdr", "pane", "get", "w1:p1"}, result: supervisorPaneJSON(project)},
		{want: []string{"herdr", "agent", "get", developer}, result: jsonError("agent_not_found", "missing")},
		{want: []string{"herdr", "pane", "get", "w1:p1"}, result: supervisorPaneJSON(project)},
		{want: []string{"herdr", "pane", "list", "--workspace", "w1"}, result: paneListJSON(
			herdr.PaneInfo{PaneID: "w1:p2", WorkspaceID: "w1", TabID: "w1:t1", CWD: project, Label: recoveryPaneLabel, Tokens: map[string]string{"cagy_owner": developer, "cagy_role": "recovery"}},
			herdr.PaneInfo{PaneID: "w1:p3", WorkspaceID: "w1", TabID: "w1:t1", CWD: project, Label: recoveryPaneLabel, Tokens: map[string]string{"cagy_owner": developer, "cagy_role": "recovery"}},
		)},
	}}
	application := New(runner, &strings.Builder{}, &strings.Builder{})
	_, err := application.ensureDeveloper(context.Background(), info)
	if err == nil || !strings.Contains(err.Error(), "multiple cagy-owned repair panes found") {
		t.Fatalf("error=%v", err)
	}
	runner.assertDone()
}

func TestSessionHealthDetectsRepairableMissingDeveloper(t *testing.T) {
	project, _ := filepath.EvalSymlinks(t.TempDir())
	developer := developerName("w1", "w1:p1")
	runner := &scriptedRunner{t: t, steps: []runStep{
		{want: []string{"herdr", "pane", "get", "w1:p1"}, result: supervisorPaneJSON(project)},
		{want: []string{"herdr", "agent", "get", developer}, result: jsonError("agent_not_found", "missing")},
		{want: []string{"herdr", "pane", "list", "--workspace", "w1"}, result: paneListJSON(
			herdr.PaneInfo{PaneID: "w1:p2", WorkspaceID: "w1", TabID: "w1:t1", CWD: project, Label: recoveryPaneLabel, Tokens: map[string]string{"cagy_owner": developer, "cagy_role": "recovery"}},
		)},
		{want: []string{"herdr", "pane", "process-info", "--pane", "w1:p2"}, result: paneProcessJSON("w1:p2", "zsh")},
	}}
	application := New(runner, &strings.Builder{}, &strings.Builder{})
	application.getenv = cagyEnv(project, developer)
	err := application.checkSessionHealth(context.Background())
	if err == nil || !strings.Contains(err.Error(), "can repair pane w1:p2") {
		t.Fatalf("error=%v", err)
	}
	runner.assertDone()
}

type neverStopsRunner struct {
	calls int
}

func (r *neverStopsRunner) LookPath(name string) (string, error) { return "/bin/" + name, nil }
func (r *neverStopsRunner) Run(_ context.Context, args ...string) (proc.Result, error) {
	r.calls++
	if slicesEqual(args, []string{"herdr", "agent", "get", "developer"}) {
		return agentJSON("w1:p2", "w1", "/tmp/project", "working"), nil
	}
	return proc.Result{}, errors.New("unexpected command: " + strings.Join(args, " "))
}
func (r *neverStopsRunner) RunAttached(_ []string, _ []string) error { return nil }

func TestWaitAgentReleasedIsBounded(t *testing.T) {
	runner := &neverStopsRunner{}
	application := New(runner, &strings.Builder{}, &strings.Builder{})
	application.agentStopTimeout = 8 * time.Millisecond
	application.developerPoll = time.Millisecond
	err := application.waitAgentReleased(context.Background(), "developer")
	if err == nil || !strings.Contains(err.Error(), "developer pane was kept") {
		t.Fatalf("error=%v", err)
	}
	if runner.calls < 2 {
		t.Fatalf("poll calls=%d", runner.calls)
	}
}

func TestSessionHealthRejectsLeftoverOwnedRecoveryPane(t *testing.T) {
	project, _ := filepath.EvalSymlinks(t.TempDir())
	developer := developerName("w1", "w1:p1")
	runner := &scriptedRunner{t: t, steps: []runStep{
		{want: []string{"herdr", "pane", "get", "w1:p1"}, result: supervisorPaneJSON(project)},
		{want: []string{"herdr", "agent", "get", developer}, result: agentJSONWithSession("w1:p2", "w1", project, "idle", testConversationID)},
		{want: []string{"herdr", "pane", "get", "w1:p2"}, result: paneJSON("w1:p2", "w1:t1", project, developerPaneLabel, map[string]string{"cagy_owner": developer, "cagy_role": "developer", "cagy_session": testConversationID, agySessionStateToken: agySessionStateReady})},
		{want: []string{"herdr", "pane", "list", "--workspace", "w1"}, result: paneListJSON(
			herdr.PaneInfo{PaneID: "w1:p2", WorkspaceID: "w1", TabID: "w1:t1", CWD: project, Label: developerPaneLabel, Agent: "agy"},
			herdr.PaneInfo{PaneID: "w1:p3", WorkspaceID: "w1", TabID: "w1:t1", CWD: project, Label: recoveryPaneLabel, Tokens: map[string]string{"cagy_owner": developer, "cagy_role": "recovery"}},
		)},
	}}
	application := New(runner, &strings.Builder{}, &strings.Builder{})
	application.getenv = cagyEnv(project, developer)
	err := application.checkSessionHealth(context.Background())
	if err == nil || !strings.Contains(err.Error(), "recovery pane w1:p3 remains") {
		t.Fatalf("error=%v", err)
	}
	runner.assertDone()
}

func TestValidateExistingDeveloperScenarios(t *testing.T) {
	project, _ := filepath.EvalSymlinks(t.TempDir())
	developer := developerName("w1", "w1:p1")
	info := runtimeContext{workspaceID: "w1", supervisor: "w1:p1", developer: developer, project: project}
	supervisor := herdr.PaneInfo{PaneID: "w1:p1", WorkspaceID: "w1", TabID: "w1:t1", CWD: project}

	t.Run("valid owned developer passes", func(t *testing.T) {
		runner := &scriptedRunner{t: t, steps: []runStep{
			{want: []string{"herdr", "pane", "get", "w1:p2"}, result: paneJSON("w1:p2", "w1:t1", project, developerPaneLabel, map[string]string{"cagy_owner": developer, "cagy_role": "developer"})},
		}}
		app := New(runner, &strings.Builder{}, &strings.Builder{})
		agent := herdr.AgentInfo{Agent: "agy", PaneID: "w1:p2", WorkspaceID: "w1", TabID: "w1:t1", ForegroundCWD: project}
		if err := app.validateExistingDeveloper(context.Background(), agent, info, supervisor); err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		runner.assertDone()
	})

	t.Run("unowned running session fails closed", func(t *testing.T) {
		runner := &scriptedRunner{t: t, steps: []runStep{
			{want: []string{"herdr", "pane", "get", "w1:p2"}, result: paneJSON("w1:p2", "w1:t1", project, developerPaneLabel, nil)},
		}}
		app := New(runner, &strings.Builder{}, &strings.Builder{})
		agent := herdr.AgentInfo{Agent: "agy", PaneID: "w1:p2", WorkspaceID: "w1", TabID: "w1:t1", ForegroundCWD: project}
		err := app.validateExistingDeveloper(context.Background(), agent, info, supervisor)
		if err == nil || !strings.Contains(err.Error(), "ownership is incomplete") {
			t.Fatalf("error=%v", err)
		}
		runner.assertDone()
	})

	t.Run("owner mismatch fails closed", func(t *testing.T) {
		runner := &scriptedRunner{t: t, steps: []runStep{
			{want: []string{"herdr", "pane", "get", "w1:p2"}, result: paneJSON("w1:p2", "w1:t1", project, developerPaneLabel, map[string]string{"cagy_owner": "other_dev", "cagy_role": "developer"})},
		}}
		app := New(runner, &strings.Builder{}, &strings.Builder{})
		agent := herdr.AgentInfo{Agent: "agy", PaneID: "w1:p2", WorkspaceID: "w1", TabID: "w1:t1", ForegroundCWD: project}
		err := app.validateExistingDeveloper(context.Background(), agent, info, supervisor)
		if err == nil || !strings.Contains(err.Error(), "owned by another developer: other_dev") {
			t.Fatalf("error=%v", err)
		}
		runner.assertDone()
	})

	t.Run("role mismatch fails closed", func(t *testing.T) {
		runner := &scriptedRunner{t: t, steps: []runStep{
			{want: []string{"herdr", "pane", "get", "w1:p2"}, result: paneJSON("w1:p2", "w1:t1", project, developerPaneLabel, map[string]string{"cagy_owner": developer, "cagy_role": "supervisor"})},
		}}
		app := New(runner, &strings.Builder{}, &strings.Builder{})
		agent := herdr.AgentInfo{Agent: "agy", PaneID: "w1:p2", WorkspaceID: "w1", TabID: "w1:t1", ForegroundCWD: project}
		err := app.validateExistingDeveloper(context.Background(), agent, info, supervisor)
		if err == nil || !strings.Contains(err.Error(), "has invalid role: supervisor") {
			t.Fatalf("error=%v", err)
		}
		runner.assertDone()
	})

	t.Run("empty owner with role fails closed", func(t *testing.T) {
		runner := &scriptedRunner{t: t, steps: []runStep{
			{want: []string{"herdr", "pane", "get", "w1:p2"}, result: paneJSON("w1:p2", "w1:t1", project, developerPaneLabel, map[string]string{"cagy_role": "developer"})},
		}}
		app := New(runner, &strings.Builder{}, &strings.Builder{})
		agent := herdr.AgentInfo{Agent: "agy", PaneID: "w1:p2", WorkspaceID: "w1", TabID: "w1:t1", ForegroundCWD: project}
		err := app.validateExistingDeveloper(context.Background(), agent, info, supervisor)
		if err == nil || !strings.Contains(err.Error(), "ownership is incomplete") {
			t.Fatalf("error=%v", err)
		}
		runner.assertDone()
	})

	t.Run("agent tab mismatch fails closed", func(t *testing.T) {
		app := New(&scriptedRunner{t: t}, &strings.Builder{}, &strings.Builder{})
		agent := herdr.AgentInfo{Agent: "agy", PaneID: "w1:p2", WorkspaceID: "w1", TabID: "w1:t2", ForegroundCWD: project}
		err := app.validateExistingDeveloper(context.Background(), agent, info, supervisor)
		if err == nil || !strings.Contains(err.Error(), "belongs to another Herdr tab") {
			t.Fatalf("error=%v", err)
		}
	})

	t.Run("pane tab mismatch fails closed", func(t *testing.T) {
		runner := &scriptedRunner{t: t, steps: []runStep{
			{want: []string{"herdr", "pane", "get", "w1:p2"}, result: paneJSON("w1:p2", "w1:t2", project, developerPaneLabel, map[string]string{"cagy_owner": developer, "cagy_role": "developer"})},
		}}
		app := New(runner, &strings.Builder{}, &strings.Builder{})
		agent := herdr.AgentInfo{Agent: "agy", PaneID: "w1:p2", WorkspaceID: "w1", TabID: "w1:t1", ForegroundCWD: project}
		err := app.validateExistingDeveloper(context.Background(), agent, info, supervisor)
		if err == nil || !strings.Contains(err.Error(), "pane belongs to another Herdr tab") {
			t.Fatalf("error=%v", err)
		}
		runner.assertDone()
	})

	t.Run("unknown agent CWD fails closed", func(t *testing.T) {
		app := New(&scriptedRunner{t: t}, &strings.Builder{}, &strings.Builder{})
		agent := herdr.AgentInfo{Agent: "agy", PaneID: "w1:p2", WorkspaceID: "w1", TabID: "w1:t1", ForegroundCWD: "", CWD: ""}
		err := app.validateExistingDeveloper(context.Background(), agent, info, supervisor)
		if err == nil || !strings.Contains(err.Error(), "cagy developer working directory is unknown") {
			t.Fatalf("error=%v", err)
		}
	})

	t.Run("unknown pane CWD fails closed", func(t *testing.T) {
		runner := &scriptedRunner{t: t, steps: []runStep{
			{want: []string{"herdr", "pane", "get", "w1:p2"}, result: jsonResult(`{"type":"pane_info","pane":{"pane_id":"w1:p2","workspace_id":"w1","tab_id":"w1:t1","cwd":"","foreground_cwd":""}}`)},
		}}
		app := New(runner, &strings.Builder{}, &strings.Builder{})
		agent := herdr.AgentInfo{Agent: "agy", PaneID: "w1:p2", WorkspaceID: "w1", TabID: "w1:t1", ForegroundCWD: project}
		err := app.validateExistingDeveloper(context.Background(), agent, info, supervisor)
		if err == nil || !strings.Contains(err.Error(), "working directory is unknown") {
			t.Fatalf("error=%v", err)
		}
		runner.assertDone()
	})
}

func TestValidateAgySessionContinuity(t *testing.T) {
	agent := herdr.AgentInfo{AgentSession: &herdr.AgentSessionInfo{Source: "herdr:antigravity_cli", Agent: "agy", Kind: "id", Value: testConversationID}}
	if err := validateAgySessionContinuity(testConversationID, agent); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if err := validateAgySessionContinuity("11111111-1111-1111-1111-111111111111", agent); err == nil || !strings.Contains(err.Error(), "different conversation") {
		t.Fatalf("error=%v", err)
	}
	missing := herdr.AgentInfo{}
	if err := validateAgySessionContinuity(testConversationID, missing); err == nil || !strings.Contains(err.Error(), "identity is missing") {
		t.Fatalf("error=%v", err)
	}
}

func TestValidateShellPaneScenarios(t *testing.T) {
	t.Run("interactive shell succeeds", func(t *testing.T) {
		for _, shell := range []string{"zsh", "-zsh", "/bin/bash", "sh", "fish", "dash"} {
			runner := &scriptedRunner{t: t, steps: []runStep{
				{want: []string{"herdr", "pane", "process-info", "--pane", "w1:p2"}, result: paneProcessJSON("w1:p2", shell)},
			}}
			app := New(runner, &strings.Builder{}, &strings.Builder{})
			if err := app.validateShellPane(context.Background(), "w1:p2"); err != nil {
				t.Fatalf("shell %q rejected: %v", shell, err)
			}
			runner.assertDone()
		}
	})

	t.Run("non-shell process fails closed", func(t *testing.T) {
		runner := &scriptedRunner{t: t, steps: []runStep{
			{want: []string{"herdr", "pane", "process-info", "--pane", "w1:p2"}, result: paneProcessJSON("w1:p2", "python3")},
		}}
		app := New(runner, &strings.Builder{}, &strings.Builder{})
		err := app.validateShellPane(context.Background(), "w1:p2")
		if err == nil || !strings.Contains(err.Error(), "running a non-shell process: python3") {
			t.Fatalf("error=%v", err)
		}
		runner.assertDone()
	})

	t.Run("empty foreground processes fails closed", func(t *testing.T) {
		runner := &scriptedRunner{t: t, steps: []runStep{
			{want: []string{"herdr", "pane", "process-info", "--pane", "w1:p2"}, result: jsonResult(`{"type":"pane_process_info","process_info":{"pane_id":"w1:p2","foreground_processes":[]}}`)},
		}}
		app := New(runner, &strings.Builder{}, &strings.Builder{})
		err := app.validateShellPane(context.Background(), "w1:p2")
		if err == nil || !strings.Contains(err.Error(), "no readable foreground process") {
			t.Fatalf("error=%v", err)
		}
		runner.assertDone()
	})

	t.Run("process info error fails closed", func(t *testing.T) {
		runner := &scriptedRunner{t: t, steps: []runStep{
			{want: []string{"herdr", "pane", "process-info", "--pane", "w1:p2"}, result: jsonError("internal_error", "pane process info failed")},
		}}
		app := New(runner, &strings.Builder{}, &strings.Builder{})
		err := app.validateShellPane(context.Background(), "w1:p2")
		if err == nil || !strings.Contains(err.Error(), "read process info for repair pane") {
			t.Fatalf("error=%v", err)
		}
		runner.assertDone()
	})
}

func TestRepairMissingDeveloperIgnoresUnownedLabelOnlyPane(t *testing.T) {
	project, _ := filepath.EvalSymlinks(t.TempDir())
	developer := developerName("w1", "w1:p1")
	info := runtimeContext{workspaceID: "w1", supervisor: "w1:p1", developer: developer, project: project}
	runner := &scriptedRunner{t: t, steps: []runStep{
		{want: []string{"herdr", "pane", "get", "w1:p1"}, result: supervisorPaneJSON(project)},
		// findRepairPane lists panes; w1:p2 has label "agy Recovery" but NO tokens -> must NOT be adopted!
		{want: []string{"herdr", "pane", "list", "--workspace", "w1"}, result: paneListJSON(
			herdr.PaneInfo{PaneID: "w1:p1", WorkspaceID: "w1", TabID: "w1:t1", CWD: project, Label: "Codex Supervisor", Agent: "codex"},
			herdr.PaneInfo{PaneID: "w1:p2", WorkspaceID: "w1", TabID: "w1:t1", CWD: project, Label: recoveryPaneLabel},
		)},
		// Therefore SplitRight is called to create a new pane w1:p3!
		{want: []string{"herdr", "pane", "split", "--current", "--direction", "right", "--cwd", project, "--no-focus"}, result: paneJSON("w1:p3", "w1:t1", project, "", nil)},
		{want: []string{"herdr", "pane", "rename", "w1:p3", recoveryPaneLabel}, result: jsonResult(`{"type":"pane_info"}`)},
		ownershipStep("w1:p3", developer, "recovery"),
		{want: []string{"herdr", "pane", "rename", "w1:p3", developerPaneLabel}, result: jsonResult(`{"type":"pane_info"}`)},
		{want: []string{"herdr", "agent", "start", developer, "--kind", "agy", "--pane", "w1:p3", "--timeout", "60000", "--", "--dangerously-skip-permissions", "--mode", "accept-edits"}, result: agentJSONWithSession("w1:p3", "w1", project, "idle", testConversationID)},
		ownershipStep("w1:p3", developer, "developer"),
		{want: []string{"herdr", "pane", "report-metadata", "w1:p3", "--source", developerDisplaySource, "--agent", "agy", "--display-agent", developerDisplayName}, result: jsonResult(`{"type":"ok"}`)},
		sessionOwnershipStep("w1:p3", testConversationID),
		{want: []string{"herdr", "pane", "read", "w1:p3", "--source", "recent-unwrapped", "--lines", "200"}, result: textResult("? for shortcuts\n")},
		{want: []string{"herdr", "pane", "wait-output", "w1:p3", "--match", "? for shortcuts", "--source", "recent-unwrapped", "--lines", "400", "--timeout", "60000"}, result: jsonResult(`{"type":"output_matched"}`)},
	}}
	app := New(runner, &strings.Builder{}, &strings.Builder{})
	got, err := app.repairMissingDeveloper(context.Background(), info)
	if err != nil {
		t.Fatal(err)
	}
	if got.PaneID != "w1:p3" {
		t.Fatalf("paneID=%s want w1:p3", got.PaneID)
	}
	runner.assertDone()
}

func TestRepairMissingDeveloperAdoptsExistingOnAgentNameTaken(t *testing.T) {
	project, _ := filepath.EvalSymlinks(t.TempDir())
	developer := developerName("w1", "w1:p1")
	info := runtimeContext{workspaceID: "w1", supervisor: "w1:p1", developer: developer, project: project}
	runner := &scriptedRunner{t: t, steps: []runStep{
		{want: []string{"herdr", "pane", "get", "w1:p1"}, result: supervisorPaneJSON(project)},
		{want: []string{"herdr", "pane", "list", "--workspace", "w1"}, result: paneListJSON(
			herdr.PaneInfo{PaneID: "w1:p1", WorkspaceID: "w1", TabID: "w1:t1", CWD: project, Label: "Codex Supervisor", Agent: "codex"},
		)},
		{want: []string{"herdr", "pane", "split", "--current", "--direction", "right", "--cwd", project, "--no-focus"}, result: paneJSON("w1:p3", "w1:t1", project, "", nil)},
		{want: []string{"herdr", "pane", "rename", "w1:p3", recoveryPaneLabel}, result: jsonResult(`{"type":"pane_info"}`)},
		ownershipStep("w1:p3", developer, "recovery"),
		{want: []string{"herdr", "pane", "rename", "w1:p3", developerPaneLabel}, result: jsonResult(`{"type":"pane_info"}`)},
		// StartAgy returns agent_name_taken
		{want: []string{"herdr", "agent", "start", developer, "--kind", "agy", "--pane", "w1:p3", "--timeout", "60000", "--", "--dangerously-skip-permissions", "--mode", "accept-edits"}, result: jsonError("agent_name_taken", "agent name taken")},
		// cagy looks up existing agent
		{want: []string{"herdr", "agent", "get", developer}, result: agentJSONWithSession("w1:p2", "w1", project, "idle", testConversationID)},
		// validates it: checks pane w1:p2
		{want: []string{"herdr", "pane", "get", "w1:p2"}, result: paneJSON("w1:p2", "w1:t1", project, developerPaneLabel, map[string]string{"cagy_owner": developer, "cagy_role": "developer"})},
		// closes the newly created repair pane w1:p3 and confirms it is gone
		{want: []string{"herdr", "pane", "close", "w1:p3"}, result: jsonResult(`{"type":"pane_info"}`)},
		{want: []string{"herdr", "pane", "get", "w1:p3"}, result: jsonError("pane_not_found", "not found")},
	}}
	app := New(runner, &strings.Builder{}, &strings.Builder{})
	got, err := app.repairMissingDeveloper(context.Background(), info)
	if err != nil {
		t.Fatal(err)
	}
	if got.PaneID != "w1:p2" {
		t.Fatalf("paneID=%s want w1:p2", got.PaneID)
	}
	runner.assertDone()
}

func TestRepairMissingDeveloperRejectsInvalidExistingOnAgentNameTaken(t *testing.T) {
	project, _ := filepath.EvalSymlinks(t.TempDir())
	developer := developerName("w1", "w1:p1")
	info := runtimeContext{workspaceID: "w1", supervisor: "w1:p1", developer: developer, project: project}
	runner := &scriptedRunner{t: t, steps: []runStep{
		{want: []string{"herdr", "pane", "get", "w1:p1"}, result: supervisorPaneJSON(project)},
		{want: []string{"herdr", "pane", "list", "--workspace", "w1"}, result: paneListJSON(
			herdr.PaneInfo{PaneID: "w1:p1", WorkspaceID: "w1", TabID: "w1:t1", CWD: project, Label: "Codex Supervisor", Agent: "codex"},
		)},
		{want: []string{"herdr", "pane", "split", "--current", "--direction", "right", "--cwd", project, "--no-focus"}, result: paneJSON("w1:p3", "w1:t1", project, "", nil)},
		{want: []string{"herdr", "pane", "rename", "w1:p3", recoveryPaneLabel}, result: jsonResult(`{"type":"pane_info"}`)},
		ownershipStep("w1:p3", developer, "recovery"),
		{want: []string{"herdr", "pane", "rename", "w1:p3", developerPaneLabel}, result: jsonResult(`{"type":"pane_info"}`)},
		// StartAgy returns agent_name_taken
		{want: []string{"herdr", "agent", "start", developer, "--kind", "agy", "--pane", "w1:p3", "--timeout", "60000", "--", "--dangerously-skip-permissions", "--mode", "accept-edits"}, result: jsonError("agent_name_taken", "agent name taken")},
		// Existing agent belongs to another tab!
		{want: []string{"herdr", "agent", "get", developer}, result: jsonResult(`{"type":"agent_info","agent":{"agent":"agy","agent_status":"idle","pane_id":"w1:p2","workspace_id":"w1","tab_id":"w1:other_tab","foreground_cwd":"` + project + `"}}`)},
		// Mark pane w1:p3 as recovery and leave visible
		{want: []string{"herdr", "pane", "rename", "w1:p3", recoveryPaneLabel}, result: jsonResult(`{"type":"pane_info"}`)},
		ownershipStep("w1:p3", developer, "recovery"),
	}}
	app := New(runner, &strings.Builder{}, &strings.Builder{})
	_, err := app.repairMissingDeveloper(context.Background(), info)
	if err == nil || !strings.Contains(err.Error(), "agent_name_taken") || !strings.Contains(err.Error(), "recovery pane left visible") {
		t.Fatalf("error=%v", err)
	}
	runner.assertDone()
}

func TestRollbackStartedAgentOnPostStartFailure(t *testing.T) {
	project, _ := filepath.EvalSymlinks(t.TempDir())
	developer := developerName("w1", "w1:p1")
	info := runtimeContext{workspaceID: "w1", supervisor: "w1:p1", developer: developer, project: project}

	t.Run("readiness failure stops agent and leaves recovery pane", func(t *testing.T) {
		runner := &scriptedRunner{t: t, steps: []runStep{
			{want: []string{"herdr", "pane", "get", "w1:p1"}, result: supervisorPaneJSON(project)},
			{want: []string{"herdr", "pane", "list", "--workspace", "w1"}, result: paneListJSON(
				herdr.PaneInfo{PaneID: "w1:p1", WorkspaceID: "w1", TabID: "w1:t1", CWD: project, Label: "Codex Supervisor", Agent: "codex"},
			)},
			{want: []string{"herdr", "pane", "split", "--current", "--direction", "right", "--cwd", project, "--no-focus"}, result: paneJSON("w1:p2", "w1:t1", project, "", nil)},
			{want: []string{"herdr", "pane", "rename", "w1:p2", recoveryPaneLabel}, result: jsonResult(`{"type":"pane_info"}`)},
			ownershipStep("w1:p2", developer, "recovery"),
			{want: []string{"herdr", "pane", "rename", "w1:p2", developerPaneLabel}, result: jsonResult(`{"type":"pane_info"}`)},
			{want: []string{"herdr", "agent", "start", developer, "--kind", "agy", "--pane", "w1:p2", "--timeout", "60000", "--", "--dangerously-skip-permissions", "--mode", "accept-edits"}, result: agentJSONWithSession("w1:p2", "w1", project, "idle", testConversationID)},
			ownershipStep("w1:p2", developer, "developer"),
			{want: []string{"herdr", "pane", "report-metadata", "w1:p2", "--source", developerDisplaySource, "--agent", "agy", "--display-agent", developerDisplayName}, result: jsonResult(`{"type":"ok"}`)},
			sessionOwnershipStep("w1:p2", testConversationID),
			// readiness check fails:
			{want: []string{"herdr", "pane", "read", "w1:p2", "--source", "recent-unwrapped", "--lines", "200"}, result: jsonError("read_error", "unreadable pane")},
			// rollback:
			{want: []string{"herdr", "agent", "send-keys", developer, "ctrl+c"}, result: jsonResult(`{"type":"agent_info"}`)},
			{want: []string{"herdr", "agent", "get", developer}, result: jsonError("agent_not_found", "stopped")},
			{want: []string{"herdr", "pane", "rename", "w1:p2", recoveryPaneLabel}, result: jsonResult(`{"type":"pane_info"}`)},
			ownershipStep("w1:p2", developer, "recovery"),
		}}
		app := New(runner, &strings.Builder{}, &strings.Builder{})
		_, err := app.repairMissingDeveloper(context.Background(), info)
		if err == nil || !strings.Contains(err.Error(), "read agy startup") || !strings.Contains(err.Error(), "recovery pane left visible") {
			t.Fatalf("error=%v", err)
		}
		runner.assertDone()
	})

	t.Run("cleanup failure during rollback reports both errors", func(t *testing.T) {
		runner := &scriptedRunner{t: t, steps: []runStep{
			// SendAgentKeys fails during rollback; do not relabel a live agent as recovery.
			{want: []string{"herdr", "agent", "send-keys", developer, "ctrl+c"}, result: jsonError("network_error", "connection refused")},
			ownershipStep("w1:p2", developer, "developer"),
		}}
		app := New(runner, &strings.Builder{}, &strings.Builder{})
		origErr := errors.New("original startup failure")
		err := app.rollbackStartedAgent(context.Background(), info, "w1:p2", origErr)
		if err == nil || !strings.Contains(err.Error(), "original startup failure") || !strings.Contains(err.Error(), "rollback cleanup failed: stop agent") {
			t.Fatalf("error=%v", err)
		}
		runner.assertDone()
	})
}

func TestConfirmClosePaneRetriesAndAbortsOnFailure(t *testing.T) {
	t.Run("close confirmed after retry", func(t *testing.T) {
		runner := &scriptedRunner{t: t, steps: []runStep{
			// Attempt 1: ClosePane ok, but GetPane still finds pane
			{want: []string{"herdr", "pane", "close", "w1:p3"}, result: jsonResult(`{"type":"pane_info"}`)},
			{want: []string{"herdr", "pane", "get", "w1:p3"}, result: paneJSON("w1:p3", "w1:t1", "/tmp/project", "", nil)},
			// Attempt 2: ClosePane ok, GetPane returns pane_not_found
			{want: []string{"herdr", "pane", "close", "w1:p3"}, result: jsonResult(`{"type":"pane_info"}`)},
			{want: []string{"herdr", "pane", "get", "w1:p3"}, result: jsonError("pane_not_found", "not found")},
		}}
		app := New(runner, &strings.Builder{}, &strings.Builder{})
		if err := app.confirmClosePane(context.Background(), "w1:p3"); err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		runner.assertDone()
	})

	t.Run("close failure aborts after 3 attempts without stopping developer", func(t *testing.T) {
		runner := &scriptedRunner{t: t, steps: []runStep{
			// 3 attempts where pane remains present
			{want: []string{"herdr", "pane", "close", "w1:p3"}, result: jsonResult(`{"type":"pane_info"}`)},
			{want: []string{"herdr", "pane", "get", "w1:p3"}, result: paneJSON("w1:p3", "w1:t1", "/tmp/project", "", nil)},
			{want: []string{"herdr", "pane", "close", "w1:p3"}, result: jsonResult(`{"type":"pane_info"}`)},
			{want: []string{"herdr", "pane", "get", "w1:p3"}, result: paneJSON("w1:p3", "w1:t1", "/tmp/project", "", nil)},
			{want: []string{"herdr", "pane", "close", "w1:p3"}, result: jsonResult(`{"type":"pane_info"}`)},
			{want: []string{"herdr", "pane", "get", "w1:p3"}, result: paneJSON("w1:p3", "w1:t1", "/tmp/project", "", nil)},
		}}
		app := New(runner, &strings.Builder{}, &strings.Builder{})
		err := app.confirmClosePane(context.Background(), "w1:p3")
		if err == nil || !strings.Contains(err.Error(), "could not be confirmed") {
			t.Fatalf("error=%v", err)
		}
		runner.assertDone()
	})

	t.Run("context cancellation during retries returns context error", func(t *testing.T) {
		ctx, cancel := context.WithCancel(context.Background())
		runner := &scriptedRunner{t: t, steps: []runStep{
			{want: []string{"herdr", "pane", "close", "w1:p3"}, result: jsonResult(`{"type":"pane_info"}`)},
			{want: []string{"herdr", "pane", "get", "w1:p3"}, result: paneJSON("w1:p3", "w1:t1", "/tmp/project", "", nil), before: func() {
				cancel()
			}},
		}}
		app := New(runner, &strings.Builder{}, &strings.Builder{})
		err := app.confirmClosePane(ctx, "w1:p3")
		if !errors.Is(err, context.Canceled) {
			t.Fatalf("expected context.Canceled, got %v", err)
		}
		runner.assertDone()
	})
}

func TestStopFailsIfCtrlCFailsWithoutClosingPane(t *testing.T) {
	project, _ := filepath.EvalSymlinks(t.TempDir())
	developer := developerName("w1", "w1:p1")
	runner := &scriptedRunner{t: t, steps: []runStep{
		{want: []string{"herdr", "pane", "get", "w1:p1"}, result: supervisorPaneJSON(project)},
		{want: []string{"herdr", "agent", "get", developer}, result: agentJSON("w1:p2", "w1", project, "idle")},
		{want: []string{"herdr", "pane", "get", "w1:p2"}, result: paneJSON("w1:p2", "w1:t1", project, developerPaneLabel, map[string]string{"cagy_owner": developer, "cagy_role": "developer"})},
		{want: []string{"herdr", "agent", "send-keys", developer, "ctrl+c"}, result: jsonError("herdr_error", "failed to send keys")},
	}}
	app := New(runner, &strings.Builder{}, &strings.Builder{})
	app.getenv = envGetter(map[string]string{
		"HERDR_ENV":               "1",
		"HERDR_WORKSPACE_ID":      "w1",
		"HERDR_PANE_ID":           "w1:p1",
		"CAGY_SUPERVISOR_PANE_ID": "w1:p1",
		"CAGY_DEVELOPER":          developer,
		"CAGY_PROJECT_DIR":        project,
	})
	err := app.stop(context.Background())
	if err == nil || !strings.Contains(err.Error(), "stop agy developer") || !strings.Contains(err.Error(), "failed to send keys") {
		t.Fatalf("error=%v", err)
	}
	runner.assertDone()
	for _, call := range runner.calls {
		if strings.Contains(strings.Join(call, " "), "pane close") {
			t.Fatalf("pane was closed despite ctrl+c failure: %v", call)
		}
	}
}

func TestStopFailsOnInvalidDeveloperOwnership(t *testing.T) {
	project, _ := filepath.EvalSymlinks(t.TempDir())
	developer := developerName("w1", "w1:p1")
	runner := &scriptedRunner{t: t, steps: []runStep{
		{want: []string{"herdr", "pane", "get", "w1:p1"}, result: supervisorPaneJSON(project)},
		{want: []string{"herdr", "agent", "get", developer}, result: agentJSON("w1:p2", "w1", project, "idle")},
		// Pane is owned by another developer!
		{want: []string{"herdr", "pane", "get", "w1:p2"}, result: paneJSON("w1:p2", "w1:t1", project, developerPaneLabel, map[string]string{"cagy_owner": "rogue_dev", "cagy_role": "developer"})},
	}}
	app := New(runner, &strings.Builder{}, &strings.Builder{})
	app.getenv = envGetter(map[string]string{
		"HERDR_ENV":               "1",
		"HERDR_WORKSPACE_ID":      "w1",
		"HERDR_PANE_ID":           "w1:p1",
		"CAGY_SUPERVISOR_PANE_ID": "w1:p1",
		"CAGY_DEVELOPER":          developer,
		"CAGY_PROJECT_DIR":        project,
	})
	err := app.stop(context.Background())
	if err == nil || !strings.Contains(err.Error(), "owned by another developer: rogue_dev") {
		t.Fatalf("error=%v", err)
	}
	runner.assertDone()
	for _, call := range runner.calls {
		joined := strings.Join(call, " ")
		if strings.Contains(joined, "send-keys") || strings.Contains(joined, "pane close") {
			t.Fatalf("mutated developer despite ownership violation: %v", call)
		}
	}
}

func TestSupervisorPaneValidation(t *testing.T) {
	project, _ := filepath.EvalSymlinks(t.TempDir())
	developer := developerName("w1", "w1:p1")
	info := runtimeContext{workspaceID: "w1", supervisor: "w1:p1", developer: developer, project: project}

	t.Run("supervisor in different workspace fails closed", func(t *testing.T) {
		runner := &scriptedRunner{t: t, steps: []runStep{
			{want: []string{"herdr", "pane", "get", "w1:p1"}, result: jsonResult(`{"type":"pane_info","pane":{"pane_id":"w1:p1","workspace_id":"w2","tab_id":"w1:t1","cwd":"` + project + `"}}`)},
		}}
		app := New(runner, &strings.Builder{}, &strings.Builder{})
		_, err := app.supervisorPane(context.Background(), info)
		if err == nil || !strings.Contains(err.Error(), "belongs to another Herdr workspace") {
			t.Fatalf("error=%v", err)
		}
		runner.assertDone()
	})

	t.Run("supervisor with missing tab fails closed", func(t *testing.T) {
		runner := &scriptedRunner{t: t, steps: []runStep{
			{want: []string{"herdr", "pane", "get", "w1:p1"}, result: jsonResult(`{"type":"pane_info","pane":{"pane_id":"w1:p1","workspace_id":"w1","tab_id":"","cwd":"` + project + `"}}`)},
		}}
		app := New(runner, &strings.Builder{}, &strings.Builder{})
		_, err := app.supervisorPane(context.Background(), info)
		if err == nil || !strings.Contains(err.Error(), "cagy supervisor tab is missing") {
			t.Fatalf("error=%v", err)
		}
		runner.assertDone()
	})

	t.Run("supervisor with unknown CWD fails closed", func(t *testing.T) {
		runner := &scriptedRunner{t: t, steps: []runStep{
			{want: []string{"herdr", "pane", "get", "w1:p1"}, result: jsonResult(`{"type":"pane_info","pane":{"pane_id":"w1:p1","workspace_id":"w1","tab_id":"w1:t1","cwd":"","foreground_cwd":""}}`)},
		}}
		app := New(runner, &strings.Builder{}, &strings.Builder{})
		_, err := app.supervisorPane(context.Background(), info)
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		runner.assertDone()
	})

	t.Run("supervisor project is not used for scope", func(t *testing.T) {
		runner := &scriptedRunner{t: t, steps: []runStep{
			{want: []string{"herdr", "pane", "get", "w1:p1"}, result: jsonResult(`{"type":"pane_info","pane":{"pane_id":"w1:p1","workspace_id":"w1","tab_id":"w1:t1","cwd":"/tmp/other"}}`)},
		}}
		app := New(runner, &strings.Builder{}, &strings.Builder{})
		_, err := app.supervisorPane(context.Background(), info)
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		runner.assertDone()
	})
}

func TestExactAgySessionIDRejectsUntrustedIdentity(t *testing.T) {
	tests := []struct {
		name    string
		session *herdr.AgentSessionInfo
		want    string
	}{
		{name: "missing", want: "identity is missing"},
		{name: "wrong source", session: &herdr.AgentSessionInfo{Source: "other", Agent: "agy", Kind: "id", Value: testConversationID}, want: "source is unsupported"},
		{name: "wrong agent", session: &herdr.AgentSessionInfo{Source: "herdr:antigravity_cli", Agent: "codex", Kind: "id", Value: testConversationID}, want: "another agent"},
		{name: "wrong kind", session: &herdr.AgentSessionInfo{Source: "herdr:antigravity_cli", Agent: "agy", Kind: "slug", Value: testConversationID}, want: "kind is unsupported"},
		{name: "invalid UUID", session: &herdr.AgentSessionInfo{Source: "herdr:antigravity_cli", Agent: "agy", Kind: "id", Value: "latest"}, want: "identity is invalid"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			_, err := exactAgySessionID(herdr.AgentInfo{AgentSession: test.session})
			if err == nil || !strings.Contains(err.Error(), test.want) {
				t.Fatalf("error=%v", err)
			}
		})
	}
}

func TestRecoveryFailsBeforeAGMMutationWhenExactSessionIsUnsafe(t *testing.T) {
	project, _ := filepath.EvalSymlinks(t.TempDir())
	developer := developerName("w1", "w1:p1")
	info := runtimeContext{workspaceID: "w1", supervisor: "w1:p1", developer: developer, developerPane: "w1:p2", project: project}
	expected := herdr.AgentInfo{
		Agent:         "agy",
		PaneID:        "w1:p2",
		WorkspaceID:   "w1",
		TabID:         "w1:t1",
		ForegroundCWD: project,
		AgentSession:  &herdr.AgentSessionInfo{Source: "herdr:antigravity_cli", Agent: "agy", Kind: "id", Value: testConversationID},
	}
	otherConversationID := "11111111-1111-1111-1111-111111111111"
	tests := []struct {
		name       string
		liveResult proc.Result
		want       string
	}{
		{name: "missing live identity", liveResult: agentJSON("w1:p2", "w1", project, "idle"), want: "identity is missing"},
		{name: "invalid live source", liveResult: jsonResult(`{"type":"agent_info","agent":{"agent":"agy","agent_status":"idle","pane_id":"w1:p2","workspace_id":"w1","tab_id":"w1:t1","foreground_cwd":"` + project + `","agent_session":{"source":"other","agent":"agy","kind":"id","value":"` + testConversationID + `"}}}`), want: "source is unsupported"},
		{name: "changed live identity", liveResult: agentJSONWithSession("w1:p2", "w1", project, "idle", otherConversationID), want: "conversation changed before recovery"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			runner := &scriptedRunner{t: t, steps: []runStep{
				{want: []string{"herdr", "pane", "get", "w1:p1"}, result: supervisorPaneJSON(project)},
				{want: []string{"herdr", "agent", "get", developer}, result: test.liveResult},
				{want: []string{"herdr", "pane", "get", "w1:p2"}, result: paneJSON("w1:p2", "w1:t1", project, developerPaneLabel, map[string]string{"cagy_owner": developer, "cagy_role": "developer"})},
			}}
			app := New(runner, &strings.Builder{}, &strings.Builder{})
			_, _, err := app.recover(context.Background(), info, expected, "task", true)
			if err == nil || !strings.Contains(err.Error(), test.want) {
				t.Fatalf("error=%v", err)
			}
			runner.assertDone()
			for _, call := range runner.calls {
				joined := strings.Join(call, " ")
				if strings.Contains(joined, "agm ") || strings.Contains(joined, "pane split") || strings.Contains(joined, "agent send-keys") {
					t.Fatalf("unsafe recovery mutated external state: %q", joined)
				}
			}
		})
	}
}

func TestRecoveryRequiresSessionMetadataBeforeAGMMutation(t *testing.T) {
	project, _ := filepath.EvalSymlinks(t.TempDir())
	developer := developerName("w1", "w1:p1")
	info := runtimeContext{workspaceID: "w1", supervisor: "w1:p1", developer: developer, developerPane: "w1:p2", project: project}
	agent := herdr.AgentInfo{Agent: "agy", PaneID: "w1:p2", WorkspaceID: "w1", TabID: "w1:t1", ForegroundCWD: project, AgentSession: &herdr.AgentSessionInfo{Source: "herdr:antigravity_cli", Agent: "agy", Kind: "id", Value: testConversationID}}
	steps := exactSessionCaptureSteps(developer, project, "w1:p2", testConversationID)
	steps[len(steps)-1].result = jsonError("metadata_error", "metadata unavailable")
	runner := &scriptedRunner{t: t, steps: steps}
	app := New(runner, &strings.Builder{}, &strings.Builder{})
	_, _, err := app.recover(context.Background(), info, agent, "task", true)
	if err == nil || !strings.Contains(err.Error(), "save agy conversation before quota recovery") {
		t.Fatalf("error=%v", err)
	}
	runner.assertDone()
	for _, call := range runner.calls {
		if strings.Contains(strings.Join(call, " "), "pane split") {
			t.Fatalf("recovery pane was created before session metadata was safe: %v", call)
		}
	}
}

func TestAskWarnsButReturnsCompletedOutputWhenSessionMetadataFails(t *testing.T) {
	project, _ := filepath.EvalSymlinks(t.TempDir())
	developer := developerName("w1", "w1:p1")
	task := "Implement the feature safely"
	brainRoot := t.TempDir()
	writeAgyTranscript(t, brainRoot, testConversationID, task, "Implemented.")
	runner := &scriptedRunner{t: t, steps: []runStep{
		{want: []string{"herdr", "pane", "get", "w1:p1"}, result: supervisorPaneJSON(project)},
		{want: []string{"herdr", "agent", "get", developer}, result: agentJSON("w1:p2", "w1", project, "idle")},
		{want: []string{"herdr", "pane", "get", "w1:p2"}, result: paneJSON("w1:p2", "w1:t1", project, developerPaneLabel, map[string]string{"cagy_owner": developer, "cagy_role": "developer"})},
		{want: agyProbeArgs("/model"), result: agyModelResult("gemini-3.8-flash-high", "Gemini 3.8 Flash (High)")},
		{want: agyProbeArgs("/quota"), result: agyQuotaResult("Gemini Models", 0.8, 0.9)},
		{want: []string{"herdr", "agent", "read", developer, "--source", "recent-unwrapped", "--lines", "400"}, result: textResult("old output\n")},
		{want: []string{"herdr", "agent", "prompt", developer, task, "--wait", "--timeout", "300000"}, result: agentJSONWithSession("w1:p2", "w1", project, "done", testConversationID)},
		{want: []string{"herdr", "agent", "read", developer, "--source", "recent-unwrapped", "--lines", "400"}, result: textResult("old output\nImplemented.\n")},
		{want: []string{"herdr", "agent", "wait", developer, "--until", "blocked", "--timeout", "1000"}, result: jsonError("timeout", "timed out")},
		{want: []string{"herdr", "agent", "read", developer, "--source", "visible", "--lines", "80"}, result: textResult(">\n────────────────────\n? for shortcuts\n")},
		{want: []string{"herdr", "pane", "report-metadata", "w1:p2", "--source", paneOwnershipSource, "--token", "cagy_session=" + testConversationID, "--token", agySessionStateToken + "=" + agySessionStateReady}, result: jsonError("metadata_error", "metadata unavailable")},
	}}
	var stdout, stderr strings.Builder
	app := New(runner, &stdout, &stderr)
	app.stateDir = t.TempDir()
	app.agyBrainRoot = brainRoot
	app.transcriptWait = time.Second
	app.getenv = cagyEnv(project, developer)
	if err := app.ask(context.Background(), task); err != nil {
		t.Fatal(err)
	}
	runner.assertDone()
	if stdout.String() != "Implemented.\n" {
		t.Fatalf("stdout=%q", stdout.String())
	}
	if !strings.Contains(stderr.String(), "warning: could not save agy conversation identity") {
		t.Fatalf("stderr=%q", stderr.String())
	}
}

func TestSessionHealthRejectsSavedLiveConversationMismatch(t *testing.T) {
	project, _ := filepath.EvalSymlinks(t.TempDir())
	developer := developerName("w1", "w1:p1")
	otherConversationID := "11111111-1111-1111-1111-111111111111"
	runner := &scriptedRunner{t: t, steps: []runStep{
		{want: []string{"herdr", "pane", "get", "w1:p1"}, result: supervisorPaneJSON(project)},
		{want: []string{"herdr", "agent", "get", developer}, result: agentJSONWithSession("w1:p2", "w1", project, "idle", testConversationID)},
		{want: []string{"herdr", "pane", "get", "w1:p2"}, result: paneJSON("w1:p2", "w1:t1", project, developerPaneLabel, map[string]string{"cagy_owner": developer, "cagy_role": "developer", "cagy_session": otherConversationID, agySessionStateToken: agySessionStateReady})},
	}}
	app := New(runner, &strings.Builder{}, &strings.Builder{})
	app.getenv = cagyEnv(project, developer)
	err := app.checkSessionHealth(context.Background())
	if err == nil || !strings.Contains(err.Error(), "does not match the live conversation") {
		t.Fatalf("error=%v", err)
	}
	runner.assertDone()
}

func TestRepairMissingDeveloperRejectsOwnedPaneWithoutSavedSession(t *testing.T) {
	project, _ := filepath.EvalSymlinks(t.TempDir())
	developer := developerName("w1", "w1:p1")
	info := runtimeContext{workspaceID: "w1", supervisor: "w1:p1", developer: developer, project: project}
	runner := &scriptedRunner{t: t, steps: []runStep{
		{want: []string{"herdr", "pane", "get", "w1:p1"}, result: supervisorPaneJSON(project)},
		{want: []string{"herdr", "pane", "list", "--workspace", "w1"}, result: paneListJSON(
			herdr.PaneInfo{PaneID: "w1:p2", WorkspaceID: "w1", TabID: "w1:t1", CWD: project, Label: recoveryPaneLabel, Tokens: map[string]string{"cagy_owner": developer, "cagy_role": "recovery"}},
		)},
		{want: []string{"herdr", "pane", "process-info", "--pane", "w1:p2"}, result: paneProcessJSON("w1:p2", "zsh")},
	}}
	app := New(runner, &strings.Builder{}, &strings.Builder{})
	_, err := app.repairMissingDeveloper(context.Background(), info)
	if err == nil || !strings.Contains(err.Error(), "no saved agy conversation identity") {
		t.Fatalf("error=%v", err)
	}
	runner.assertDone()
	for _, call := range runner.calls {
		if strings.Contains(strings.Join(call, " "), "agent start") {
			t.Fatalf("repair guessed a fresh or recent session: %v", call)
		}
	}
}

func TestFreshReplacementAllowsSessionIdentityToAppearAfterFirstTask(t *testing.T) {
	project, _ := filepath.EvalSymlinks(t.TempDir())
	developer := developerName("w1", "w1:p1")
	info := runtimeContext{workspaceID: "w1", supervisor: "w1:p1", developer: developer, project: project}
	runner := &scriptedRunner{t: t, steps: []runStep{
		{want: []string{"herdr", "pane", "get", "w1:p1"}, result: supervisorPaneJSON(project)},
		{want: []string{"herdr", "pane", "list", "--workspace", "w1"}, result: paneListJSON(
			herdr.PaneInfo{PaneID: "w1:p1", WorkspaceID: "w1", TabID: "w1:t1", CWD: project, Label: "Codex Supervisor", Agent: "codex"},
		)},
		{want: []string{"herdr", "pane", "split", "--current", "--direction", "right", "--cwd", project, "--no-focus"}, result: paneJSON("w1:p2", "w1:t1", project, "", nil)},
		{want: []string{"herdr", "pane", "rename", "w1:p2", recoveryPaneLabel}, result: jsonResult(`{"type":"pane_info"}`)},
		ownershipStep("w1:p2", developer, "recovery"),
		{want: []string{"herdr", "pane", "rename", "w1:p2", developerPaneLabel}, result: jsonResult(`{"type":"pane_info"}`)},
		{want: []string{"herdr", "agent", "start", developer, "--kind", "agy", "--pane", "w1:p2", "--timeout", "60000", "--", "--dangerously-skip-permissions", "--mode", "accept-edits"}, result: agentJSON("w1:p2", "w1", project, "idle")},
		ownershipStep("w1:p2", developer, "developer"),
		{want: []string{"herdr", "pane", "report-metadata", "w1:p2", "--source", developerDisplaySource, "--agent", "agy", "--display-agent", developerDisplayName}, result: jsonResult(`{"type":"ok"}`)},
		pendingSessionStep("w1:p2"),
		{want: []string{"herdr", "pane", "read", "w1:p2", "--source", "recent-unwrapped", "--lines", "200"}, result: textResult("? for shortcuts\n")},
		{want: []string{"herdr", "pane", "wait-output", "w1:p2", "--match", "? for shortcuts", "--source", "recent-unwrapped", "--lines", "400", "--timeout", "60000"}, result: jsonResult(`{"type":"output_matched"}`)},
	}}
	app := New(runner, &strings.Builder{}, &strings.Builder{})
	got, err := app.repairMissingDeveloper(context.Background(), info)
	if err != nil {
		t.Fatal(err)
	}
	if got.PaneID != "w1:p2" {
		t.Fatalf("developer=%+v", got)
	}
	runner.assertDone()
	for _, call := range runner.calls {
		joined := strings.Join(call, " ")
		if strings.Contains(joined, "--continue") || strings.Contains(joined, "--conversation") {
			t.Fatalf("fresh replacement unexpectedly resumed a prior session: %q", joined)
		}
	}
}

func TestRestartRefusesToStopDeveloperIfConversationChangedAfterAccountSwitch(t *testing.T) {
	project, _ := filepath.EvalSymlinks(t.TempDir())
	developerName := developerName("w1", "w1:p1")
	info := runtimeContext{workspaceID: "w1", supervisor: "w1:p1", developer: developerName, developerPane: "w1:p2", project: project}
	expected := herdr.AgentInfo{
		Agent:         "agy",
		PaneID:        "w1:p2",
		WorkspaceID:   "w1",
		TabID:         "w1:t1",
		ForegroundCWD: project,
		AgentSession:  &herdr.AgentSessionInfo{Source: "herdr:antigravity_cli", Agent: "agy", Kind: "id", Value: testConversationID},
	}
	newerConversationID := "11111111-1111-1111-1111-111111111111"
	runner := &scriptedRunner{t: t, steps: []runStep{
		{want: []string{"herdr", "pane", "get", "w1:p1"}, result: supervisorPaneJSON(project)},
		{want: []string{"herdr", "agent", "get", developerName}, result: agentJSONWithSession("w1:p2", "w1", project, "idle", newerConversationID)},
		{want: []string{"herdr", "pane", "get", "w1:p2"}, result: paneJSON("w1:p2", "w1:t1", project, developerPaneLabel, map[string]string{"cagy_owner": developerName, "cagy_role": "developer", "cagy_session": testConversationID, agySessionStateToken: agySessionStateReady})},
	}}
	app := New(runner, &strings.Builder{}, &strings.Builder{})
	_, err := app.restartDeveloperInPlace(context.Background(), info, expected)
	if err == nil || !strings.Contains(err.Error(), "conversation changed before recovery") {
		t.Fatalf("error=%v", err)
	}
	runner.assertDone()
	for _, call := range runner.calls {
		if strings.Contains(strings.Join(call, " "), "agent send-keys") {
			t.Fatalf("changed developer was stopped: %v", call)
		}
	}
}

func TestAskPreflightQuotaRecoveryRestartsFreshUnreportedSession(t *testing.T) {
	project, _ := filepath.EvalSymlinks(t.TempDir())
	developer := developerName("w1", "w1:p1")
	task := "Implement the first task"
	brainRoot := t.TempDir()
	marker := "__CAGY_SWITCH_switch1__"
	steps := []runStep{
		{want: []string{"herdr", "pane", "get", "w1:p1"}, result: supervisorPaneJSON(project)},
		{want: []string{"herdr", "agent", "get", developer}, result: agentJSON("w1:p2", "w1", project, "idle")},
		{want: []string{"herdr", "pane", "get", "w1:p2"}, result: paneJSON("w1:p2", "w1:t1", project, developerPaneLabel, map[string]string{"cagy_owner": developer, "cagy_role": "developer", agySessionStateToken: agySessionStatePending})},
		{want: agyProbeArgs("/model"), result: agyModelResult("gemini-3.8-flash-high", "Gemini 3.8 Flash (High)")},
		{want: agyProbeArgs("/quota"), result: agyQuotaResult("Gemini Models", 0.03, 0.9)},
	}
	steps = append(steps, pendingSessionCaptureSteps(developer, project, "w1:p2")...)
	steps = append(steps, recoveryAttemptStartSteps("w1:p3", project, developer)...)
	steps = append(steps, successfulAutoSwitchSteps("w1:p3", marker)...)
	steps = append(steps,
		runStep{want: agyProbeArgs("/model"), result: agyModelResult("gemini-3.8-flash-high", "Gemini 3.8 Flash (High)")},
		runStep{want: agyProbeArgs("/quota"), result: agyQuotaResult("Gemini Models", 0.8, 0.9)},
	)
	steps = append(steps, confirmedCloseSteps("w1:p3")...)
	steps = append(steps, restartFreshInPlaceSteps(developer, project, "w1:p2")...)
	steps = append(steps,
		runStep{want: []string{"herdr", "agent", "read", developer, "--source", "recent-unwrapped", "--lines", "400"}, result: textResult("fresh\n")},
		runStep{want: []string{"herdr", "agent", "prompt", developer, task, "--wait", "--timeout", "300000"}, before: func() {
			writeAgyTranscript(t, brainRoot, testConversationID, task, "Completed first task.")
		}, result: agentJSONWithSession("w1:p2", "w1", project, "done", testConversationID)},
		runStep{want: []string{"herdr", "agent", "read", developer, "--source", "recent-unwrapped", "--lines", "400"}, result: textResult("fresh\nCompleted first task.\n")},
		runStep{want: []string{"herdr", "agent", "wait", developer, "--until", "blocked", "--timeout", "1000"}, result: jsonError("timeout", "timed out")},
		runStep{want: []string{"herdr", "agent", "read", developer, "--source", "visible", "--lines", "80"}, result: textResult(">\n────────────────────\n? for shortcuts\n")},
		sessionOwnershipStep("w1:p2", testConversationID),
	)
	runner := &scriptedRunner{t: t, steps: steps}
	var stdout strings.Builder
	app := New(runner, &stdout, &strings.Builder{})
	app.stateDir = t.TempDir()
	app.agyBrainRoot = brainRoot
	app.transcriptWait = time.Second
	app.now = func() time.Time { return time.Date(2026, time.September, 18, 12, 0, 0, 0, time.UTC) }
	if err := app.recordAGMRefresh(app.now().Add(-30 * time.Minute)); err != nil {
		t.Fatal(err)
	}
	app.token = func() (string, error) { return "switch1", nil }
	app.getenv = cagyEnv(project, developer)
	if err := app.ask(context.Background(), task); err != nil {
		t.Fatal(err)
	}
	runner.assertDone()
	if stdout.String() != "Completed first task.\n" {
		t.Fatalf("stdout=%q", stdout.String())
	}
	for _, call := range runner.calls {
		if contains(call, "--continue") {
			t.Fatalf("ambiguous resume was used: %v", call)
		}
	}
}

func TestSessionHealthAcceptsVerifiedFreshPendingConversation(t *testing.T) {
	project, _ := filepath.EvalSymlinks(t.TempDir())
	developer := developerName("w1", "w1:p1")
	runner := &scriptedRunner{t: t, steps: []runStep{
		{want: []string{"herdr", "pane", "get", "w1:p1"}, result: supervisorPaneJSON(project)},
		{want: []string{"herdr", "agent", "get", developer}, result: agentJSON("w1:p2", "w1", project, "idle")},
		{want: []string{"herdr", "pane", "get", "w1:p2"}, result: paneJSON("w1:p2", "w1:t1", project, developerPaneLabel, map[string]string{"cagy_owner": developer, "cagy_role": "developer", agySessionStateToken: agySessionStatePending})},
		{want: []string{"herdr", "pane", "list", "--workspace", "w1"}, result: paneListJSON(
			herdr.PaneInfo{PaneID: "w1:p2", WorkspaceID: "w1", TabID: "w1:t1", CWD: project, Label: developerPaneLabel, Agent: "agy"},
		)},
	}}
	app := New(runner, &strings.Builder{}, &strings.Builder{})
	app.getenv = cagyEnv(project, developer)
	if err := app.checkSessionHealth(context.Background()); err != nil {
		t.Fatal(err)
	}
	runner.assertDone()
}

func TestDelegateTaskPreservesResumedSessionIdentityAfterQuotaRecovery(t *testing.T) {
	project, _ := filepath.EvalSymlinks(t.TempDir())
	developer := developerName("w1", "w1:p1")
	task := "Implement the first task with recovery"
	brainRoot := t.TempDir()
	marker := "__CAGY_SWITCH_switch1__"
	steps := []runStep{
		{want: []string{"herdr", "pane", "get", "w1:p1"}, result: supervisorPaneJSON(project)},
		{want: []string{"herdr", "agent", "get", developer}, result: agentJSON("w1:p2", "w1", project, "idle")},
		{want: []string{"herdr", "pane", "get", "w1:p2"}, result: paneJSON("w1:p2", "w1:t1", project, developerPaneLabel, map[string]string{"cagy_owner": developer, "cagy_role": "developer", agySessionStateToken: agySessionStatePending})},
		{want: agyProbeArgs("/model"), result: agyModelResult("gemini-3.8-flash-high", "Gemini 3.8 Flash (High)")},
		{want: agyProbeArgs("/quota"), result: agyQuotaResult("Gemini Models", 0.03, 0.9)},
	}
	steps = append(steps, pendingSessionCaptureSteps(developer, project, "w1:p2")...)
	steps = append(steps, recoveryAttemptStartSteps("w1:p3", project, developer)...)
	steps = append(steps, successfulAutoSwitchSteps("w1:p3", marker)...)
	steps = append(steps,
		runStep{want: agyProbeArgs("/model"), result: agyModelResult("gemini-3.8-flash-high", "Gemini 3.8 Flash (High)")},
		runStep{want: agyProbeArgs("/quota"), result: agyQuotaResult("Gemini Models", 0.8, 0.9)},
	)
	steps = append(steps, confirmedCloseSteps("w1:p3")...)
	steps = append(steps, restartFreshInPlaceSteps(developer, project, "w1:p2")...)
	steps = append(steps,
		runStep{want: []string{"herdr", "agent", "read", developer, "--source", "recent-unwrapped", "--lines", "400"}, result: textResult("fresh\n")},
		runStep{want: []string{"herdr", "agent", "prompt", developer, task, "--wait", "--timeout", "300000"}, before: func() {
			writeAgyTranscript(t, brainRoot, testConversationID, task, "Completed first task after recovery.")
		}, result: agentJSONWithSession("w1:p2", "w1", project, "done", testConversationID)},
		runStep{want: []string{"herdr", "agent", "read", developer, "--source", "recent-unwrapped", "--lines", "400"}, result: textResult("fresh\nCompleted first task after recovery.\n")},
		runStep{want: []string{"herdr", "agent", "wait", developer, "--until", "blocked", "--timeout", "1000"}, result: jsonError("timeout", "timed out")},
		runStep{want: []string{"herdr", "agent", "read", developer, "--source", "visible", "--lines", "80"}, result: textResult(">\n────────────────────\n? for shortcuts\n")},
		sessionOwnershipStep("w1:p2", testConversationID),
		// Steps for inspectTaskJournal during recoverTask
		runStep{want: []string{"herdr", "agent", "get", developer}, result: agentJSONWithSession("w1:p2", "w1", project, "idle", testConversationID)},
		runStep{want: []string{"herdr", "pane", "get", "w1:p1"}, result: supervisorPaneJSON(project)},
		runStep{want: []string{"herdr", "pane", "get", "w1:p2"}, result: paneJSON("w1:p2", "w1:t1", project, developerPaneLabel, map[string]string{"cagy_owner": developer, "cagy_role": "developer"})},
	)

	runner := &scriptedRunner{t: t, steps: steps}
	var stdout strings.Builder
	app := New(runner, &stdout, &strings.Builder{})
	app.stateDir = t.TempDir()
	app.agyBrainRoot = brainRoot
	app.transcriptWait = time.Second
	app.now = func() time.Time { return time.Date(2026, time.September, 18, 12, 0, 0, 0, time.UTC) }
	if err := app.recordAGMRefresh(app.now().Add(-30 * time.Minute)); err != nil {
		t.Fatal(err)
	}
	app.token = func() (string, error) { return "switch1", nil }
	app.getenv = cagyEnv(project, developer)

	delivery, err := app.delegateTask(context.Background(), task)
	if err != nil {
		t.Fatalf("delegateTask failed: %v", err)
	}
	if delivery.output != "Completed first task after recovery." {
		t.Fatalf("unexpected delivery output: %q", delivery.output)
	}

	// Verify journal on disk has the RESUMED session, NOT the initial empty one!
	record, exists, err := app.loadTaskJournal(developer)
	if err != nil || !exists {
		t.Fatalf("journal should exist on disk, exists=%v, err=%v", exists, err)
	}
	if record.SessionID != testConversationID {
		t.Fatalf("journal SessionID = %q, want resumed session %q (was overwritten by pre-recovery agent!)", record.SessionID, testConversationID)
	}
	if record.Phase != taskPhaseCompleted {
		t.Fatalf("journal Phase = %q, want %q", record.Phase, taskPhaseCompleted)
	}
	if record.DeliveryReceipt == "" || !isValidDeliveryReceipt(record.DeliveryReceipt) {
		t.Fatalf("expected valid delivery receipt in journal, got: %q", record.DeliveryReceipt)
	}

	// Verify recoverTask succeeds using the journal and matches the answer and receipt
	app.activeTask = nil
	recOut, err := app.recoverTask(context.Background())
	if err != nil {
		t.Fatalf("recoverTask failed: %v", err)
	}
	if recOut.Answer != "Completed first task after recovery." {
		t.Fatalf("recoverTask Answer = %q, want %q", recOut.Answer, "Completed first task after recovery.")
	}
	if recOut.Receipt != record.DeliveryReceipt {
		t.Fatalf("recoverTask Receipt = %q, want journal receipt %q", recOut.Receipt, record.DeliveryReceipt)
	}

	// Verify acknowledgeTask clears the journal
	if err := app.acknowledgeTask(context.Background(), recOut.Receipt); err != nil {
		t.Fatalf("acknowledgeTask failed: %v", err)
	}
	if _, exists, _ := app.loadTaskJournal(developer); exists {
		t.Fatal("journal should be deleted after acknowledgeTask")
	}

	runner.assertDone()
}
