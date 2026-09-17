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
	first, err := acquireLock(dir, "developer")
	if err != nil {
		t.Fatal(err)
	}
	defer first.release()
	if _, err := acquireLock(dir, "developer"); err == nil {
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
	return step.result, nil
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
		{want: []string{"herdr", "pane", "current", "--current"}, result: jsonResult(`{"type":"pane_current","pane":{"pane_id":"w1:p1","workspace_id":"w1","cwd":"` + project + `"}}`)},
		{want: []string{"herdr", "pane", "report-metadata", "w1:p1", "--source", supervisorDisplaySource, "--agent", "codex", "--display-agent", compactSupervisorDisplayName, "--token", "cagy_role=supervisor"}, result: jsonResult(`{"type":"ok"}`)},
		{want: []string{"herdr", "agent", "get", developer}, result: jsonError("agent_not_found", "missing")},
		{want: []string{"herdr", "pane", "split", "--current", "--direction", "right", "--cwd", project, "--no-focus"}, result: jsonResult(`{"type":"pane_info","pane":{"pane_id":"w1:p2","workspace_id":"w1","cwd":"` + project + `"}}`)},
		{want: []string{"herdr", "pane", "rename", "w1:p1", "Codex Supervisor"}, result: jsonResult(`{"type":"pane_info"}`)},
		{want: []string{"herdr", "pane", "rename", "w1:p2", "agy Developer"}, result: jsonResult(`{"type":"pane_info"}`)},
		{want: []string{"herdr", "agent", "start", developer, "--kind", "agy", "--pane", "w1:p2", "--timeout", "60000", "--", "--dangerously-skip-permissions", "--mode", "accept-edits"}, result: jsonResult(`{"type":"agent_started","agent":{"agent":"agy","agent_status":"idle","pane_id":"w1:p2","workspace_id":"w1","foreground_cwd":"` + project + `"},"argv":[]}`)},
		{want: []string{"herdr", "pane", "report-metadata", "w1:p2", "--source", developerDisplaySource, "--agent", "agy", "--display-agent", developerDisplayName, "--token", "cagy_role=developer"}, result: jsonResult(`{"type":"ok"}`)},
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
		{want: []string{"herdr", "pane", "current", "--current"}, result: jsonResult(`{"type":"pane_current","pane":{"pane_id":"w1:p1","workspace_id":"w1","cwd":"` + project + `"}}`)},
		{want: []string{"herdr", "pane", "report-metadata", "w1:p1", "--source", supervisorDisplaySource, "--agent", "codex", "--display-agent", expandedSupervisorDisplayName, "--token", "cagy_role=supervisor"}, result: jsonResult(`{"type":"ok"}`)},
		{want: []string{"herdr", "agent", "get", developer}, result: agentJSON("w1:p2", "w1", project, "idle")},
		{want: []string{"herdr", "pane", "report-metadata", "w1:p2", "--source", developerDisplaySource, "--agent", "agy", "--display-agent", developerDisplayName, "--token", "cagy_role=developer"}, result: jsonResult(`{"type":"ok"}`)},
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
}

func TestAskReturnsOnlyNewDeveloperOutput(t *testing.T) {
	project, _ := filepath.EvalSymlinks(t.TempDir())
	developer := "cagy_dev_test"
	task := "Implement the feature safely"
	brainRoot := t.TempDir()
	writeAgyTranscript(t, brainRoot, testConversationID, task, "Implemented.")
	runner := &scriptedRunner{t: t, steps: []runStep{
		{want: []string{"herdr", "agent", "get", developer}, result: agentJSON("w1:p2", "w1", project, "idle")},
		{want: agyProbeArgs("/model"), result: agyModelResult("gemini-3.8-flash-high", "Gemini 3.8 Flash (High)")},
		{want: agyProbeArgs("/quota"), result: agyQuotaResult("Gemini Models", 0.8, 0.9)},
		{want: []string{"herdr", "agent", "read", developer, "--source", "recent-unwrapped", "--lines", "400"}, result: textResult("old output\n")},
		{want: []string{"herdr", "agent", "prompt", developer, task, "--wait", "--timeout", "300000"}, result: agentJSONWithSession("w1:p2", "w1", project, "done", testConversationID)},
		{want: []string{"herdr", "agent", "read", developer, "--source", "recent-unwrapped", "--lines", "400"}, result: textResult("old output\nImplemented.\n")},
		{want: []string{"herdr", "agent", "wait", developer, "--until", "blocked", "--timeout", "1000"}, result: jsonError("timeout", "timed out")},
		{want: []string{"herdr", "agent", "read", developer, "--source", "visible", "--lines", "80"}, result: textResult(">\n────────────────────\n? for shortcuts\n")},
	}}
	var stdout strings.Builder
	application := New(runner, &stdout, os.Stderr)
	application.tempDir = t.TempDir()
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
	if err := application.ask(context.Background(), task); err != nil {
		t.Fatal(err)
	}
	runner.assertDone()
	if got := stdout.String(); got != "Implemented.\n" {
		t.Fatalf("stdout=%q", got)
	}
}

func TestAskRecoversQuotaWithVisibleAGMPartialIDESuccess(t *testing.T) {
	project, _ := filepath.EvalSymlinks(t.TempDir())
	developer := "cagy_dev_test"
	task := "Finish the feature"
	continuation := continuationPrompt(task)
	brainRoot := t.TempDir()
	writeAgyTranscript(t, brainRoot, testConversationID, continuation, "Finished after account switch.")
	refreshMarker := "__CAGY_REFRESH_refresh1__"
	switchMarker := "__CAGY_SWITCH_switch1__"
	refreshCommand := markedCommand("agm refresh-all", refreshMarker)
	switchCommand := markedCommand("agm auto-switch --min 5", switchMarker)
	confirmRegex := `Switch to this account\? \[y/N\]:|` + completionPattern(switchMarker)
	runner := &scriptedRunner{t: t, steps: []runStep{
		{want: []string{"herdr", "agent", "get", developer}, result: agentJSON("w1:p2", "w1", project, "idle")},
		{want: agyProbeArgs("/model"), result: agyModelResult("gemini-3.8-flash-high", "Gemini 3.8 Flash (High)")},
		{want: agyProbeArgs("/quota"), result: agyQuotaResult("Gemini Models", 0.8, 0.9)},
		{want: []string{"herdr", "agent", "read", developer, "--source", "recent-unwrapped", "--lines", "400"}, result: textResult("old\n")},
		{want: []string{"herdr", "agent", "prompt", developer, task, "--wait", "--timeout", "300000"}, result: agentJSONWithSession("w1:p2", "w1", project, "done", testConversationID)},
		{want: []string{"herdr", "agent", "read", developer, "--source", "recent-unwrapped", "--lines", "400"}, result: textResult("old\nRESOURCE_EXHAUSTED: quota exceeded\n")},
		{want: []string{"herdr", "agent", "send-keys", developer, "ctrl+c"}, result: jsonResult(`{"type":"agent_info"}`)},
		{want: []string{"herdr", "pane", "close", "w1:p2"}, result: jsonResult(`{"type":"pane_info"}`)},
		{want: []string{"herdr", "pane", "split", "--current", "--direction", "right", "--cwd", project, "--no-focus"}, result: jsonResult(`{"type":"pane_info","pane":{"pane_id":"w1:p3","workspace_id":"w1","cwd":"` + project + `"}}`)},
		{want: []string{"herdr", "pane", "rename", "w1:p3", "agy Recovery"}, result: jsonResult(`{"type":"pane_info"}`)},
		{want: []string{"herdr", "pane", "run", "w1:p3", refreshCommand}, result: jsonResult(`{"type":"pane_info"}`)},
		{want: []string{"herdr", "pane", "wait-output", "w1:p3", "--regex", completionPattern(refreshMarker), "--source", "recent-unwrapped", "--lines", "400", "--timeout", "1800000"}, result: jsonResult(`{"type":"output_matched"}`)},
		{want: []string{"herdr", "pane", "read", "w1:p3", "--source", "recent-unwrapped", "--lines", "400"}, result: textResult("Starting bulk refresh for 2 accounts...\nCompleted: 2 successful\n" + refreshMarker + ":0\n")},
		{want: []string{"herdr", "pane", "run", "w1:p3", switchCommand}, result: jsonResult(`{"type":"pane_info"}`)},
		{want: []string{"herdr", "pane", "wait-output", "w1:p3", "--regex", confirmRegex, "--source", "recent-unwrapped", "--lines", "400", "--timeout", "300000"}, result: jsonResult(`{"type":"output_matched"}`)},
		{want: []string{"herdr", "pane", "read", "w1:p3", "--source", "recent-unwrapped", "--lines", "400"}, result: textResult("Best account: test@example.com\n" + agmConfirmation)},
		{want: []string{"herdr", "pane", "send-text", "w1:p3", "y"}, result: jsonResult(`{"type":"pane_info"}`)},
		{want: []string{"herdr", "pane", "send-keys", "w1:p3", "enter"}, result: jsonResult(`{"type":"pane_info"}`)},
		{want: []string{"herdr", "pane", "wait-output", "w1:p3", "--regex", completionPattern(switchMarker), "--source", "recent-unwrapped", "--lines", "400", "--timeout", "300000"}, result: jsonResult(`{"type":"output_matched"}`)},
		{want: []string{"herdr", "pane", "read", "w1:p3", "--source", "recent-unwrapped", "--lines", "400"}, result: textResult("  ✓ Antigravity CLI (agy)\n  ✗ Antigravity IDE: unavailable\n" + switchMarker + ":1\n")},
		{want: agyProbeArgs("/model"), result: agyModelResult("gemini-3.8-flash-high", "Gemini 3.8 Flash (High)")},
		{want: agyProbeArgs("/quota"), result: agyQuotaResult("Gemini Models", 0.8, 0.9)},
		{want: []string{"herdr", "pane", "rename", "w1:p3", "agy Developer"}, result: jsonResult(`{"type":"pane_info"}`)},
		{want: []string{"herdr", "agent", "start", developer, "--kind", "agy", "--pane", "w1:p3", "--timeout", "60000", "--", "--continue", "--dangerously-skip-permissions", "--mode", "accept-edits"}, result: agentJSON("w1:p3", "w1", project, "idle")},
		{want: []string{"herdr", "pane", "report-metadata", "w1:p3", "--source", developerDisplaySource, "--agent", "agy", "--display-agent", developerDisplayName, "--token", "cagy_role=developer"}, result: jsonResult(`{"type":"ok"}`)},
		{want: []string{"herdr", "pane", "read", "w1:p3", "--source", "recent-unwrapped", "--lines", "200"}, result: textResult("? for shortcuts\n")},
		{want: []string{"herdr", "pane", "wait-output", "w1:p3", "--match", "? for shortcuts", "--source", "recent-unwrapped", "--lines", "400", "--timeout", "60000"}, result: jsonResult(`{"type":"output_matched"}`)},
		{want: []string{"herdr", "agent", "read", developer, "--source", "recent-unwrapped", "--lines", "400"}, result: textResult("resumed\n")},
		{want: []string{"herdr", "agent", "prompt", developer, continuation, "--wait", "--timeout", "300000"}, result: agentJSONWithSession("w1:p3", "w1", project, "done", testConversationID)},
		{want: []string{"herdr", "agent", "read", developer, "--source", "recent-unwrapped", "--lines", "400"}, result: textResult("resumed\nFinished after account switch.\n")},
		{want: []string{"herdr", "agent", "wait", developer, "--until", "blocked", "--timeout", "1000"}, result: jsonError("timeout", "timed out")},
		{want: []string{"herdr", "agent", "read", developer, "--source", "visible", "--lines", "80"}, result: textResult(">\n────────────────────\n? for shortcuts\n")},
	}}
	var stdout strings.Builder
	application := New(runner, &stdout, os.Stderr)
	application.tempDir = t.TempDir()
	application.agyBrainRoot = brainRoot
	application.transcriptWait = time.Second
	tokens := []string{"refresh1", "switch1"}
	application.token = func() (string, error) {
		if len(tokens) == 0 {
			return "", errors.New("no token")
		}
		token := tokens[0]
		tokens = tokens[1:]
		return token, nil
	}
	application.getenv = envGetter(map[string]string{
		"HERDR_ENV":               "1",
		"HERDR_WORKSPACE_ID":      "w1",
		"HERDR_PANE_ID":           "w1:p1",
		"CAGY_SUPERVISOR_PANE_ID": "w1:p1",
		"CAGY_DEVELOPER":          developer,
		"CAGY_PROJECT_DIR":        project,
	})
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

func textResult(text string) proc.Result {
	return proc.Result{ExitCode: 0, Stdout: text}
}

func agentJSON(paneID, workspaceID, project, status string) proc.Result {
	return jsonResult(`{"type":"agent_info","agent":{"agent":"agy","agent_status":"` + status + `","pane_id":"` + paneID + `","workspace_id":"` + workspaceID + `","foreground_cwd":"` + project + `"}}`)
}

const testConversationID = "512995cf-e151-4934-9dad-c43327869bf1"

func agentJSONWithSession(paneID, workspaceID, project, status, sessionID string) proc.Result {
	return jsonResult(`{"type":"agent_info","agent":{"agent":"agy","agent_status":"` + status + `","pane_id":"` + paneID + `","workspace_id":"` + workspaceID + `","foreground_cwd":"` + project + `","agent_session":{"source":"herdr:antigravity_cli","agent":"agy","kind":"id","value":"` + sessionID + `"}}}`)
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
		{want: []string{"agy", "--help"}, result: textResult("--dangerously-skip-permissions --mode accept-edits --continue --print --output-format --print-timeout")},
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

func TestStopClosesOnlyVerifiedDeveloperPane(t *testing.T) {
	project, _ := filepath.EvalSymlinks(t.TempDir())
	developer := "cagy_dev_test"
	runner := &scriptedRunner{t: t, steps: []runStep{
		{want: []string{"herdr", "agent", "get", developer}, result: agentJSON("w1:p2", "w1", project, "idle")},
		{want: []string{"herdr", "agent", "send-keys", developer, "ctrl+c"}, result: jsonResult(`{"type":"agent_info"}`)},
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

func TestRecoveryStopsAfterTwoAttemptsAndLeavesFinalPaneVisible(t *testing.T) {
	project, _ := filepath.EvalSymlinks(t.TempDir())
	developerName := "cagy_dev_test"
	info := runtimeContext{workspaceID: "w1", supervisor: "w1:p1", developer: developerName, project: project}
	developer := herdr.AgentInfo{Agent: "agy", AgentStatus: "done", PaneID: "w1:p2", WorkspaceID: "w1", ForegroundCWD: project}
	refreshMarker := "__CAGY_REFRESH_refresh1__"
	firstSwitchMarker := "__CAGY_SWITCH_switch1__"
	secondSwitchMarker := "__CAGY_SWITCH_switch2__"
	steps := []runStep{
		{want: []string{"herdr", "agent", "send-keys", developerName, "ctrl+c"}, result: jsonResult(`{"type":"agent_info"}`)},
		{want: []string{"herdr", "pane", "close", "w1:p2"}, result: jsonResult(`{"type":"pane_info"}`)},
		{want: []string{"herdr", "pane", "split", "--current", "--direction", "right", "--cwd", project, "--no-focus"}, result: jsonResult(`{"type":"pane_info","pane":{"pane_id":"w1:p3","workspace_id":"w1","cwd":"` + project + `"}}`)},
		{want: []string{"herdr", "pane", "rename", "w1:p3", "agy Recovery"}, result: jsonResult(`{"type":"pane_info"}`)},
		{want: []string{"herdr", "pane", "run", "w1:p3", markedCommand("agm refresh-all", refreshMarker)}, result: jsonResult(`{"type":"pane_info"}`)},
		{want: []string{"herdr", "pane", "wait-output", "w1:p3", "--regex", completionPattern(refreshMarker), "--source", "recent-unwrapped", "--lines", "400", "--timeout", "1800000"}, result: jsonResult(`{"type":"output_matched"}`)},
		{want: []string{"herdr", "pane", "read", "w1:p3", "--source", "recent-unwrapped", "--lines", "400"}, result: textResult("Completed: 2 successful\n" + refreshMarker + ":0\n")},
		{want: []string{"herdr", "pane", "run", "w1:p3", markedCommand("agm auto-switch --min 5", firstSwitchMarker)}, result: jsonResult(`{"type":"pane_info"}`)},
		{want: []string{"herdr", "pane", "wait-output", "w1:p3", "--regex", `Switch to this account\? \[y/N\]:|` + completionPattern(firstSwitchMarker), "--source", "recent-unwrapped", "--lines", "400", "--timeout", "300000"}, result: jsonResult(`{"type":"output_matched"}`)},
		{want: []string{"herdr", "pane", "read", "w1:p3", "--source", "recent-unwrapped", "--lines", "400"}, result: textResult("no account available\n" + firstSwitchMarker + ":1\n")},
		{want: []string{"herdr", "pane", "close", "w1:p3"}, result: jsonResult(`{"type":"pane_info"}`)},
		{want: []string{"herdr", "pane", "split", "--current", "--direction", "right", "--cwd", project, "--no-focus"}, result: jsonResult(`{"type":"pane_info","pane":{"pane_id":"w1:p4","workspace_id":"w1","cwd":"` + project + `"}}`)},
		{want: []string{"herdr", "pane", "rename", "w1:p4", "agy Recovery"}, result: jsonResult(`{"type":"pane_info"}`)},
		{want: []string{"herdr", "pane", "run", "w1:p4", markedCommand("agm auto-switch --min 5", secondSwitchMarker)}, result: jsonResult(`{"type":"pane_info"}`)},
		{want: []string{"herdr", "pane", "wait-output", "w1:p4", "--regex", `Switch to this account\? \[y/N\]:|` + completionPattern(secondSwitchMarker), "--source", "recent-unwrapped", "--lines", "400", "--timeout", "300000"}, result: jsonResult(`{"type":"output_matched"}`)},
		{want: []string{"herdr", "pane", "read", "w1:p4", "--source", "recent-unwrapped", "--lines", "400"}, result: textResult("no account available\n" + secondSwitchMarker + ":1\n")},
	}

	runner := &scriptedRunner{t: t, steps: steps}
	application := New(runner, os.Stdout, os.Stderr)
	application.tempDir = t.TempDir()
	tokens := []string{"refresh1", "switch1", "switch2"}
	application.token = func() (string, error) {
		token := tokens[0]
		tokens = tokens[1:]
		return token, nil
	}
	_, err := application.recover(context.Background(), info, developer, "task", true)
	if err == nil || !strings.Contains(err.Error(), "failed after 2 attempts") {
		t.Fatalf("error=%v", err)
	}
	runner.assertDone()
	refreshCalls := 0
	for _, call := range runner.calls {
		joined := strings.Join(call, " ")
		if strings.Contains(joined, "agm refresh-all") {
			refreshCalls++
		}
		if strings.Contains(joined, "--model") {
			t.Fatalf("unexpected model filter: %q", joined)
		}
	}
	if refreshCalls != 1 {
		t.Fatalf("refresh-all calls=%d want=1", refreshCalls)
	}
}

func TestAskPreflightQuotaRecoverySendsOriginalTask(t *testing.T) {
	project, _ := filepath.EvalSymlinks(t.TempDir())
	developer := "cagy_dev_test"
	task := "Implement the original task"
	brainRoot := t.TempDir()
	writeAgyTranscript(t, brainRoot, testConversationID, task, "Completed original task.")
	switchMarker := "__CAGY_SWITCH_switch1__"
	confirmRegex := `Switch to this account\? \[y/N\]:|` + completionPattern(switchMarker)
	runner := &scriptedRunner{t: t, steps: []runStep{
		{want: []string{"herdr", "agent", "get", developer}, result: agentJSON("w1:p2", "w1", project, "idle")},
		{want: agyProbeArgs("/model"), result: agyModelResult("gemini-3.8-flash-high", "Gemini 3.8 Flash (High)")},
		{want: agyProbeArgs("/quota"), result: agyQuotaResult("Gemini Models", 0.03, 0.9)},
		{want: []string{"herdr", "agent", "send-keys", developer, "ctrl+c"}, result: jsonResult(`{"type":"agent_info"}`)},
		{want: []string{"herdr", "pane", "close", "w1:p2"}, result: jsonResult(`{"type":"pane_info"}`)},
		{want: []string{"herdr", "pane", "split", "--current", "--direction", "right", "--cwd", project, "--no-focus"}, result: jsonResult(`{"type":"pane_info","pane":{"pane_id":"w1:p3","workspace_id":"w1","cwd":"` + project + `"}}`)},
		{want: []string{"herdr", "pane", "rename", "w1:p3", "agy Recovery"}, result: jsonResult(`{"type":"pane_info"}`)},
		{want: []string{"herdr", "pane", "run", "w1:p3", markedCommand("agm auto-switch --min 5", switchMarker)}, result: jsonResult(`{"type":"pane_info"}`)},
		{want: []string{"herdr", "pane", "wait-output", "w1:p3", "--regex", confirmRegex, "--source", "recent-unwrapped", "--lines", "400", "--timeout", "300000"}, result: jsonResult(`{"type":"output_matched"}`)},
		{want: []string{"herdr", "pane", "read", "w1:p3", "--source", "recent-unwrapped", "--lines", "400"}, result: textResult("Best account: healthy@example.com\n" + agmConfirmation)},
		{want: []string{"herdr", "pane", "send-text", "w1:p3", "y"}, result: jsonResult(`{"type":"pane_info"}`)},
		{want: []string{"herdr", "pane", "send-keys", "w1:p3", "enter"}, result: jsonResult(`{"type":"pane_info"}`)},
		{want: []string{"herdr", "pane", "wait-output", "w1:p3", "--regex", completionPattern(switchMarker), "--source", "recent-unwrapped", "--lines", "400", "--timeout", "300000"}, result: jsonResult(`{"type":"output_matched"}`)},
		{want: []string{"herdr", "pane", "read", "w1:p3", "--source", "recent-unwrapped", "--lines", "400"}, result: textResult("  ✓ Antigravity CLI (agy)\n" + switchMarker + ":0\n")},
		{want: agyProbeArgs("/model"), result: agyModelResult("gemini-3.8-flash-high", "Gemini 3.8 Flash (High)")},
		{want: agyProbeArgs("/quota"), result: agyQuotaResult("Gemini Models", 0.8, 0.9)},
		{want: []string{"herdr", "pane", "rename", "w1:p3", "agy Developer"}, result: jsonResult(`{"type":"pane_info"}`)},
		{want: []string{"herdr", "agent", "start", developer, "--kind", "agy", "--pane", "w1:p3", "--timeout", "60000", "--", "--continue", "--dangerously-skip-permissions", "--mode", "accept-edits"}, result: agentJSON("w1:p3", "w1", project, "idle")},
		{want: []string{"herdr", "pane", "report-metadata", "w1:p3", "--source", developerDisplaySource, "--agent", "agy", "--display-agent", developerDisplayName, "--token", "cagy_role=developer"}, result: jsonResult(`{"type":"ok"}`)},
		{want: []string{"herdr", "pane", "read", "w1:p3", "--source", "recent-unwrapped", "--lines", "200"}, result: textResult("? for shortcuts\n")},
		{want: []string{"herdr", "pane", "wait-output", "w1:p3", "--match", "? for shortcuts", "--source", "recent-unwrapped", "--lines", "400", "--timeout", "60000"}, result: jsonResult(`{"type":"output_matched"}`)},
		{want: []string{"herdr", "agent", "read", developer, "--source", "recent-unwrapped", "--lines", "400"}, result: textResult("resumed\n")},
		{want: []string{"herdr", "agent", "prompt", developer, task, "--wait", "--timeout", "300000"}, result: agentJSONWithSession("w1:p3", "w1", project, "done", testConversationID)},
		{want: []string{"herdr", "agent", "read", developer, "--source", "recent-unwrapped", "--lines", "400"}, result: textResult("resumed\nCompleted original task.\n")},
		{want: []string{"herdr", "agent", "wait", developer, "--until", "blocked", "--timeout", "1000"}, result: jsonError("timeout", "timed out")},
		{want: []string{"herdr", "agent", "read", developer, "--source", "visible", "--lines", "80"}, result: textResult(">\n────────────────────\n? for shortcuts\n")},
	}}
	var stdout strings.Builder
	application := New(runner, &stdout, &strings.Builder{})
	application.tempDir = t.TempDir()
	application.agyBrainRoot = brainRoot
	application.transcriptWait = time.Second
	application.now = func() time.Time { return time.Date(2026, time.September, 17, 12, 0, 0, 0, time.UTC) }
	if err := application.recordAGMRefresh(application.now().Add(-30 * time.Minute)); err != nil {
		t.Fatal(err)
	}
	application.token = func() (string, error) { return "switch1", nil }
	application.getenv = envGetter(map[string]string{
		"HERDR_ENV":               "1",
		"HERDR_WORKSPACE_ID":      "w1",
		"HERDR_PANE_ID":           "w1:p1",
		"CAGY_SUPERVISOR_PANE_ID": "w1:p1",
		"CAGY_DEVELOPER":          developer,
		"CAGY_PROJECT_DIR":        project,
	})

	if err := application.ask(context.Background(), task); err != nil {
		t.Fatal(err)
	}
	runner.assertDone()
	if got := stdout.String(); got != "Completed original task.\n" {
		t.Fatalf("stdout=%q", got)
	}
}

func TestRecoveryRejectsUnhealthyOrUnreadableSwitchedAccounts(t *testing.T) {
	project, _ := filepath.EvalSymlinks(t.TempDir())
	developerName := "cagy_dev_test"
	info := runtimeContext{workspaceID: "w1", supervisor: "w1:p1", developer: developerName, project: project}
	developer := herdr.AgentInfo{Agent: "agy", AgentStatus: "done", PaneID: "w1:p2", WorkspaceID: "w1", ForegroundCWD: project}
	firstMarker := "__CAGY_SWITCH_switch1__"
	secondMarker := "__CAGY_SWITCH_switch2__"
	steps := []runStep{
		{want: []string{"herdr", "agent", "send-keys", developerName, "ctrl+c"}, result: jsonResult(`{"type":"agent_info"}`)},
		{want: []string{"herdr", "pane", "close", "w1:p2"}, result: jsonResult(`{"type":"pane_info"}`)},
	}
	steps = append(steps, successfulSwitchSteps("w1:p3", project, firstMarker)...)
	steps = append(steps,
		runStep{want: agyProbeArgs("/model"), result: agyModelResult("gemini-3.8-flash-high", "Gemini 3.8 Flash (High)")},
		runStep{want: agyProbeArgs("/quota"), result: agyQuotaResult("Gemini Models", 0.03, 0.9)},
		runStep{want: []string{"herdr", "pane", "close", "w1:p3"}, result: jsonResult(`{"type":"pane_info"}`)},
	)
	steps = append(steps, successfulSwitchSteps("w1:p4", project, secondMarker)...)
	steps = append(steps, runStep{want: agyProbeArgs("/model"), result: textResult("not-json")})

	runner := &scriptedRunner{t: t, steps: steps}
	application := New(runner, &strings.Builder{}, &strings.Builder{})
	application.tempDir = t.TempDir()
	application.now = func() time.Time { return time.Date(2026, time.September, 17, 12, 0, 0, 0, time.UTC) }
	if err := application.recordAGMRefresh(application.now().Add(-30 * time.Minute)); err != nil {
		t.Fatal(err)
	}
	tokens := []string{"switch1", "switch2"}
	application.token = func() (string, error) {
		token := tokens[0]
		tokens = tokens[1:]
		return token, nil
	}

	_, err := application.recover(context.Background(), info, developer, "task", true)
	if err == nil || !strings.Contains(err.Error(), "verify switched agy account") {
		t.Fatalf("error=%v", err)
	}
	runner.assertDone()
	for _, call := range runner.calls {
		joined := strings.Join(call, " ")
		if strings.Contains(joined, "agent start") {
			t.Fatalf("agy started before quota verification: %q", joined)
		}
		if strings.Contains(joined, "--model") {
			t.Fatalf("unexpected model filter: %q", joined)
		}
	}
}

func successfulSwitchSteps(paneID, project, marker string) []runStep {
	confirmRegex := `Switch to this account\? \[y/N\]:|` + completionPattern(marker)
	return []runStep{
		{want: []string{"herdr", "pane", "split", "--current", "--direction", "right", "--cwd", project, "--no-focus"}, result: jsonResult(`{"type":"pane_info","pane":{"pane_id":"` + paneID + `","workspace_id":"w1","cwd":"` + project + `"}}`)},
		{want: []string{"herdr", "pane", "rename", paneID, "agy Recovery"}, result: jsonResult(`{"type":"pane_info"}`)},
		{want: []string{"herdr", "pane", "run", paneID, markedCommand("agm auto-switch --min 5", marker)}, result: jsonResult(`{"type":"pane_info"}`)},
		{want: []string{"herdr", "pane", "wait-output", paneID, "--regex", confirmRegex, "--source", "recent-unwrapped", "--lines", "400", "--timeout", "300000"}, result: jsonResult(`{"type":"output_matched"}`)},
		{want: []string{"herdr", "pane", "read", paneID, "--source", "recent-unwrapped", "--lines", "400"}, result: textResult("Best account: candidate@example.com\n" + agmConfirmation)},
		{want: []string{"herdr", "pane", "send-text", paneID, "y"}, result: jsonResult(`{"type":"pane_info"}`)},
		{want: []string{"herdr", "pane", "send-keys", paneID, "enter"}, result: jsonResult(`{"type":"pane_info"}`)},
		{want: []string{"herdr", "pane", "wait-output", paneID, "--regex", completionPattern(marker), "--source", "recent-unwrapped", "--lines", "400", "--timeout", "300000"}, result: jsonResult(`{"type":"output_matched"}`)},
		{want: []string{"herdr", "pane", "read", paneID, "--source", "recent-unwrapped", "--lines", "400"}, result: textResult("  ✓ Antigravity CLI (agy)\n" + marker + ":0\n")},
	}
}
