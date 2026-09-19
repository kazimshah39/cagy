package herdr

import (
	"context"
	"encoding/json"
	"errors"
	"reflect"
	"strings"
	"testing"
	"time"

	proc "github.com/kazimshah39/cagy/internal/process"
)

type fakeRunner struct {
	results []proc.Result
	calls   [][]string
}

func (f *fakeRunner) LookPath(name string) (string, error) { return "/bin/" + name, nil }
func (f *fakeRunner) Run(_ context.Context, args ...string) (proc.Result, error) {
	f.calls = append(f.calls, append([]string(nil), args...))
	if len(f.results) == 0 {
		return proc.Result{}, errors.New("no fake result")
	}
	result := f.results[0]
	f.results = f.results[1:]
	return result, nil
}
func (f *fakeRunner) RunAttached(_ []string, _ []string) error { return nil }

func TestSplitRightParsesPaneAndUsesSafeArguments(t *testing.T) {
	runner := &fakeRunner{results: []proc.Result{{
		ExitCode: 0,
		Stdout:   `{"id":"x","result":{"type":"pane_info","pane":{"pane_id":"w1:p2","workspace_id":"w1","cwd":"/tmp/My Project"}}}`,
	}}}
	client := New(runner)
	pane, err := client.SplitRight(context.Background(), "/tmp/My Project")
	if err != nil {
		t.Fatal(err)
	}
	if pane.PaneID != "w1:p2" {
		t.Fatalf("pane ID = %q", pane.PaneID)
	}
	want := []string{"herdr", "pane", "split", "--current", "--direction", "right", "--cwd", "/tmp/My Project", "--no-focus"}
	if !reflect.DeepEqual(runner.calls[0], want) {
		t.Fatalf("args = %#v", runner.calls[0])
	}
}

func TestReportAgentDisplayUsesGuardedPresentationMetadata(t *testing.T) {
	runner := &fakeRunner{results: []proc.Result{{
		ExitCode: 0,
		Stdout:   `{"id":"x","result":{"type":"ok"}}`,
	}}}
	client := New(runner)
	if err := client.ReportAgentDisplay(context.Background(), "w1:p2", "cagy:developer-display", "agy", "cagy Developer"); err != nil {
		t.Fatal(err)
	}
	want := []string{"herdr", "pane", "report-metadata", "w1:p2", "--source", "cagy:developer-display", "--agent", "agy", "--display-agent", "cagy Developer"}
	if !reflect.DeepEqual(runner.calls[0], want) {
		t.Fatalf("args = %#v", runner.calls[0])
	}
}

func TestGetAndListPanesParseOwnershipMetadata(t *testing.T) {
	runner := &fakeRunner{results: []proc.Result{
		{ExitCode: 0, Stdout: `{"id":"x","result":{"type":"pane_info","pane":{"pane_id":"w1:p2","workspace_id":"w1","tab_id":"w1:t1","cwd":"/tmp/project","label":"agy Repair","tokens":{"cagy_owner":"cagy_dev_abc","cagy_role":"repair"}}}}`},
		{ExitCode: 0, Stdout: `{"id":"x","result":{"type":"pane_list","panes":[{"pane_id":"w1:p2","workspace_id":"w1","tab_id":"w1:t1","cwd":"/tmp/project","label":"agy Repair","tokens":{"cagy_owner":"cagy_dev_abc","cagy_role":"repair"}}]}}`},
	}}
	client := New(runner)
	pane, err := client.GetPane(context.Background(), "w1:p2")
	if err != nil {
		t.Fatal(err)
	}
	if pane.Label != "agy Repair" || pane.Tokens["cagy_owner"] != "cagy_dev_abc" {
		t.Fatalf("pane=%+v", pane)
	}
	panes, err := client.ListPanes(context.Background(), "w1")
	if err != nil {
		t.Fatal(err)
	}
	if len(panes) != 1 || panes[0].Tokens["cagy_role"] != "repair" {
		t.Fatalf("panes=%+v", panes)
	}
	wants := [][]string{
		{"herdr", "pane", "get", "w1:p2"},
		{"herdr", "pane", "list", "--workspace", "w1"},
	}
	if !reflect.DeepEqual(runner.calls, wants) {
		t.Fatalf("calls=%#v", runner.calls)
	}
}

func TestPaneProcessInfoParsesForegroundProcesses(t *testing.T) {
	runner := &fakeRunner{results: []proc.Result{{
		ExitCode: 0,
		Stdout:   `{"id":"x","result":{"type":"pane_process_info","process_info":{"pane_id":"w1:p2","foreground_processes":[{"name":"zsh","argv":["-zsh"]}]}}}`,
	}}}
	info, err := New(runner).PaneProcessInfo(context.Background(), "w1:p2")
	if err != nil {
		t.Fatal(err)
	}
	if len(info.ForegroundProcesses) != 1 || info.ForegroundProcesses[0].Name != "zsh" {
		t.Fatalf("info=%+v", info)
	}
	want := []string{"herdr", "pane", "process-info", "--pane", "w1:p2"}
	if !reflect.DeepEqual(runner.calls[0], want) {
		t.Fatalf("args=%#v", runner.calls[0])
	}
}

func TestReportPaneOwnershipUsesPersistentUnguardedTokens(t *testing.T) {
	runner := &fakeRunner{results: []proc.Result{{ExitCode: 0, Stdout: `{"id":"x","result":{"type":"ok"}}`}}}
	client := New(runner)
	if err := client.ReportPaneOwnership(context.Background(), "w1:p2", "cagy:pane-owner", "cagy_dev_abc", "repair"); err != nil {
		t.Fatal(err)
	}
	want := []string{"herdr", "pane", "report-metadata", "w1:p2", "--source", "cagy:pane-owner", "--token", "cagy_owner=cagy_dev_abc", "--token", "cagy_role=repair"}
	if !reflect.DeepEqual(runner.calls[0], want) {
		t.Fatalf("args=%#v", runner.calls[0])
	}
}

func TestSetCagySidebarCompactUsesDeveloperTokenFilter(t *testing.T) {
	runner := &fakeRunner{}
	client := New(runner)
	var method string
	var params any
	client.apiCall = func(_ context.Context, gotMethod string, gotParams any) error {
		method = gotMethod
		params = gotParams
		return nil
	}
	if err := client.SetCagySidebarCompact(context.Background()); err != nil {
		t.Fatal(err)
	}
	if method != "agent.view.set" {
		t.Fatalf("method=%q", method)
	}
	encoded, err := json.Marshal(params)
	if err != nil {
		t.Fatal(err)
	}
	want := `{"filter":{"filter":{"field":{"token":"cagy_role"},"op":"eq","value":"developer"},"op":"not"},"label":"cagy","source":"cagy:sidebar"}`
	if string(encoded) != want {
		t.Fatalf("params=%s", encoded)
	}
}

func TestClearCagySidebarViewClearsOnlyOwnedProjection(t *testing.T) {
	client := New(&fakeRunner{})
	var method string
	var params any
	client.apiCall = func(_ context.Context, gotMethod string, gotParams any) error {
		method = gotMethod
		params = gotParams
		return nil
	}
	if err := client.ClearCagySidebarView(context.Background()); err != nil {
		t.Fatal(err)
	}
	if method != "agent.view.clear" {
		t.Fatalf("method=%q", method)
	}
	encoded, err := json.Marshal(params)
	if err != nil {
		t.Fatal(err)
	}
	if string(encoded) != `{"source":"cagy:sidebar"}` {
		t.Fatalf("params=%s", encoded)
	}
}

func TestStartAgyAlwaysUsesYoloFlags(t *testing.T) {
	for range 1 {
		runner := &fakeRunner{results: []proc.Result{{
			ExitCode: 0,
			Stdout:   `{"id":"x","result":{"type":"agent_started","agent":{"agent":"agy","agent_status":"idle","pane_id":"w1:p2","workspace_id":"w1"},"argv":[]}}`,
		}}}
		client := New(runner)
		if _, err := client.StartAgy(context.Background(), "cagy_dev_abc", "w1:p2"); err != nil {
			t.Fatal(err)
		}
		args := runner.calls[0]
		assertContains(t, args, "--dangerously-skip-permissions")
		assertContains(t, args, "accept-edits")
		for _, forbidden := range []string{"--continue", "--conversation"} {
			if containsArg(args, forbidden) {
				t.Fatalf("fresh agy start contains %q: %#v", forbidden, args)
			}
		}
	}
}

func TestStartAgyWithSessionRejectsMissingConversation(t *testing.T) {
	runner := &fakeRunner{}
	if _, err := New(runner).StartAgyWithSession(context.Background(), "developer", "w1:p2", "  "); err == nil {
		t.Fatal("expected missing conversation ID error")
	}
	if len(runner.calls) != 0 {
		t.Fatalf("agy was started without an exact conversation: %#v", runner.calls)
	}
}

func TestStartAgyWithSessionUsesExactConversation(t *testing.T) {
	runner := &fakeRunner{results: []proc.Result{{
		ExitCode: 0,
		Stdout:   `{"id":"x","result":{"type":"agent_started","agent":{"agent":"agy","agent_status":"idle","pane_id":"w1:p2","workspace_id":"w1","agent_session":{"source":"herdr:antigravity_cli","agent":"agy","kind":"id","value":"session-123"}}}}`,
	}}}
	if _, err := New(runner).StartAgyWithSession(context.Background(), "developer", "w1:p2", "session-123"); err != nil {
		t.Fatal(err)
	}
	want := []string{"herdr", "agent", "start", "developer", "--kind", "agy", "--pane", "w1:p2", "--timeout", "60000", "--", "--conversation", "session-123", "--dangerously-skip-permissions", "--mode", "accept-edits"}
	if !reflect.DeepEqual(runner.calls[0], want) {
		t.Fatalf("args=%#v want=%#v", runner.calls[0], want)
	}
}

func TestStartAgyWithSessionPreservesExactRequestedConversationWhenHerdrOmitsIt(t *testing.T) {
	started := proc.Result{ExitCode: 0, Stdout: `{"id":"x","result":{"type":"agent_started","agent":{"agent":"agy","name":"developer","agent_status":"idle","pane_id":"w1:p2","workspace_id":"w1"}}}`}
	runner := &fakeRunner{results: []proc.Result{started}}

	agent, err := New(runner).StartAgyWithSession(context.Background(), "developer", "w1:p2", "session-123")
	if err != nil {
		t.Fatal(err)
	}
	if agent.AgentSession == nil || agent.AgentSession.Value != "session-123" {
		t.Fatalf("session=%+v", agent.AgentSession)
	}
	if len(runner.calls) != 1 {
		t.Fatalf("calls=%#v", runner.calls)
	}
}

func TestStartAgyWithSessionRejectsDifferentReportedConversation(t *testing.T) {
	started := proc.Result{ExitCode: 0, Stdout: `{"id":"x","result":{"type":"agent_started","agent":{"agent":"agy","name":"developer","agent_status":"idle","pane_id":"w1:p2","workspace_id":"w1","agent_session":{"source":"herdr:antigravity_cli","agent":"agy","kind":"id","value":"other-session"}}}}`}
	runner := &fakeRunner{results: []proc.Result{started}}

	_, err := New(runner).StartAgyWithSession(context.Background(), "developer", "w1:p2", "session-123")
	if err == nil || !strings.Contains(err.Error(), "different conversation") {
		t.Fatalf("error=%v", err)
	}
}

func TestReportPaneSessionUsesUnguardedToken(t *testing.T) {
	runner := &fakeRunner{results: []proc.Result{{ExitCode: 0, Stdout: `{"id":"x","result":{"type":"ok"}}`}}}
	if err := New(runner).ReportPaneSession(context.Background(), "w1:p2", "cagy:pane-owner", "session-123"); err != nil {
		t.Fatal(err)
	}
	want := []string{"herdr", "pane", "report-metadata", "w1:p2", "--source", "cagy:pane-owner", "--token", "cagy_session=session-123", "--token", "cagy_session_state=ready"}
	if !reflect.DeepEqual(runner.calls[0], want) {
		t.Fatalf("args=%#v want=%#v", runner.calls[0], want)
	}
}

func TestReportPaneSessionPendingUsesUnguardedToken(t *testing.T) {
	runner := &fakeRunner{results: []proc.Result{{ExitCode: 0, Stdout: `{"id":"x","result":{"type":"ok"}}`}}}
	if err := New(runner).ReportPaneSessionPending(context.Background(), "w1:p2", "cagy:pane-owner"); err != nil {
		t.Fatal(err)
	}
	want := []string{"herdr", "pane", "report-metadata", "w1:p2", "--source", "cagy:pane-owner", "--token", "cagy_session_state=pending"}
	if !reflect.DeepEqual(runner.calls[0], want) {
		t.Fatalf("args=%#v want=%#v", runner.calls[0], want)
	}
}

func TestAPIErrorIsDecodedFromStderr(t *testing.T) {
	runner := &fakeRunner{results: []proc.Result{{
		ExitCode: 1,
		Stderr:   `{"id":"x","error":{"code":"agent_not_found","message":"missing"}}`,
	}}}
	_, err := New(runner).GetAgent(context.Background(), "developer")
	if !IsCode(err, "agent_not_found") {
		t.Fatalf("error = %v", err)
	}
}

func TestReadAgentReturnsPlainText(t *testing.T) {
	runner := &fakeRunner{results: []proc.Result{{ExitCode: 0, Stdout: "visible response\n"}}}
	text, err := New(runner).ReadAgent(context.Background(), "developer", 120)
	if err != nil {
		t.Fatal(err)
	}
	if text != "visible response\n" {
		t.Fatalf("text = %q", text)
	}
}

func containsArg(values []string, expected string) bool {
	for _, value := range values {
		if value == expected {
			return true
		}
	}
	return false
}

func assertContains(t *testing.T, values []string, expected string) {
	t.Helper()
	for _, value := range values {
		if value == expected {
			return
		}
	}
	t.Fatalf("%q not found in %#v", expected, values)
}

func TestWaitAgentUsesBoundedWaitCommand(t *testing.T) {
	runner := &fakeRunner{results: []proc.Result{{
		ExitCode: 0,
		Stdout:   `{"id":"x","result":{"type":"agent_info","agent":{"agent":"agy","agent_status":"done","pane_id":"w1:p2","workspace_id":"w1"}}}`,
	}}}
	client := New(runner)
	agent, err := client.WaitAgent(context.Background(), "developer", 120000)
	if err != nil {
		t.Fatal(err)
	}
	if agent.AgentStatus != "done" {
		t.Fatalf("status = %q", agent.AgentStatus)
	}
	want := []string{"herdr", "agent", "wait", "developer", "--timeout", "120000"}
	if !reflect.DeepEqual(runner.calls[0], want) {
		t.Fatalf("args = %#v", runner.calls[0])
	}
}

func TestWaitAgentBlockedUsesBlockedStateBeforeTimeout(t *testing.T) {
	runner := &fakeRunner{results: []proc.Result{{
		ExitCode: 0,
		Stdout:   `{"id":"x","result":{"type":"agent_info","agent":{"agent":"agy","agent_status":"blocked"}}}`,
	}}}
	agent, err := New(runner).WaitAgentBlocked(context.Background(), "developer", 1234)
	if err != nil {
		t.Fatal(err)
	}
	if agent.AgentStatus != "blocked" {
		t.Fatalf("status = %q", agent.AgentStatus)
	}
	want := []string{"herdr", "agent", "wait", "developer", "--until", "blocked", "--timeout", "1234"}
	if !reflect.DeepEqual(runner.calls[0], want) {
		t.Fatalf("args = %#v", runner.calls[0])
	}
}

func TestReadAgentVisibleUsesVisibleSource(t *testing.T) {
	runner := &fakeRunner{results: []proc.Result{{ExitCode: 0, Stdout: "screen\n"}}}
	text, err := New(runner).ReadAgentVisible(context.Background(), "developer", 80)
	if err != nil {
		t.Fatal(err)
	}
	if text != "screen\n" {
		t.Fatalf("text = %q", text)
	}
	want := []string{"herdr", "agent", "read", "developer", "--source", "visible", "--lines", "80"}
	if !reflect.DeepEqual(runner.calls[0], want) {
		t.Fatalf("args = %#v", runner.calls[0])
	}
}

func TestMutationAcceptsEmptySuccessfulAcknowledgment(t *testing.T) {
	runner := &fakeRunner{results: []proc.Result{{ExitCode: 0}}}
	if err := New(runner).SendPaneKeys(context.Background(), "w1:p2", "enter"); err != nil {
		t.Fatal(err)
	}
}

func TestReadStillRejectsEmptySuccessfulResponse(t *testing.T) {
	runner := &fakeRunner{results: []proc.Result{{ExitCode: 0}}}
	if _, err := New(runner).GetAgent(context.Background(), "developer"); err == nil {
		t.Fatal("expected an empty response error")
	}
}

func TestGetAgentParsesAgySessionReference(t *testing.T) {
	runner := &fakeRunner{results: []proc.Result{{
		ExitCode: 0,
		Stdout:   `{"id":"x","result":{"type":"agent_info","agent":{"agent":"agy","name":"developer","agent_session":{"source":"herdr:antigravity_cli","agent":"agy","kind":"id","value":"512995cf-e151-4934-9dad-c43327869bf1"}}}}`,
	}}}
	agent, err := New(runner).GetAgent(context.Background(), "developer")
	if err != nil {
		t.Fatal(err)
	}
	if agent.AgentSession == nil || agent.AgentSession.Value != "512995cf-e151-4934-9dad-c43327869bf1" {
		t.Fatalf("session=%+v", agent.AgentSession)
	}
}

func TestStartAgyRetriesOnceAfterNewPaneBecomesAvailableShell(t *testing.T) {
	busy := proc.Result{ExitCode: 1, Stderr: `{"error":{"code":"agent_pane_busy","message":"agent target pane w1:p2 is not an available shell"},"id":"x"}`}
	process := proc.Result{ExitCode: 0, Stdout: `{"id":"x","result":{"type":"pane_process_info","process_info":{"pane_id":"w1:p2","foreground_processes":[{"name":"zsh","argv":["-zsh"]}]}}}`}
	success := proc.Result{ExitCode: 0, Stdout: `{"id":"x","result":{"type":"agent_info","agent":{"name":"developer","agent":"agy","agent_status":"idle","pane_id":"w1:p2","workspace_id":"w1"}}}`}
	runner := &fakeRunner{results: []proc.Result{busy, process, success}}
	client := New(runner)
	agent, err := client.StartAgy(context.Background(), "developer", "w1:p2")
	if err != nil {
		t.Fatal(err)
	}
	if agent.PaneID != "w1:p2" {
		t.Fatalf("agent=%+v", agent)
	}
	if len(runner.calls) != 3 {
		t.Fatalf("calls=%#v", runner.calls)
	}
	if !reflect.DeepEqual(runner.calls[0], runner.calls[2]) {
		t.Fatalf("start retry changed args: first=%#v retry=%#v", runner.calls[0], runner.calls[2])
	}
	wantProcess := []string{"herdr", "pane", "process-info", "--pane", "w1:p2"}
	if !reflect.DeepEqual(runner.calls[1], wantProcess) {
		t.Fatalf("process args=%#v", runner.calls[1])
	}
}

func TestStartAgyPaneBusyHonorsContextCancellationWithoutRetry(t *testing.T) {
	busy := proc.Result{ExitCode: 1, Stderr: `{"error":{"code":"agent_pane_busy","message":"agent target pane w1:p2 is not an available shell"},"id":"x"}`}
	process := proc.Result{ExitCode: 0, Stdout: `{"id":"x","result":{"type":"pane_process_info","process_info":{"pane_id":"w1:p2","foreground_processes":[{"name":"cloudflared","argv":["cloudflared"]}]}}}`}
	runner := &fakeRunner{results: []proc.Result{busy, process}}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	_, err := New(runner).StartAgy(ctx, "developer", "w1:p2")
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("error=%v, want context canceled", err)
	}
	if len(runner.calls) != 2 {
		t.Fatalf("calls=%#v, want initial start and one process-info check", runner.calls)
	}
}

func TestWaitForAvailableShellTimesOutWithLastForegroundProcess(t *testing.T) {
	process := proc.Result{ExitCode: 0, Stdout: `{"id":"x","result":{"type":"pane_process_info","process_info":{"pane_id":"w1:p2","foreground_processes":[{"name":"cloudflared","argv":["cloudflared"]}]}}}`}
	runner := &fakeRunner{results: []proc.Result{process}}
	err := New(runner).waitForAvailableShell(context.Background(), "w1:p2", time.Millisecond)
	if err == nil || !strings.Contains(err.Error(), "last=cloudflared") {
		t.Fatalf("error=%v, want timeout with last foreground process", err)
	}
}
