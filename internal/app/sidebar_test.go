package app

import (
	"context"
	"errors"
	"fmt"
	"io"
	"reflect"
	"strings"
	"testing"

	"github.com/kazimshah39/herdr-tandem/internal/herdr"
	proc "github.com/kazimshah39/herdr-tandem/internal/process"
)

func TestSidebarModeParsingLabelsAndFlagMapping(t *testing.T) {
	if sidebarModeFromShowAgents(false) != sidebarModeCompact || sidebarModeFromShowAgents(true) != sidebarModeExpanded {
		t.Fatal("show-agents flag mapping is wrong")
	}
	for _, value := range []string{"", "legacy", "COMPACT", " compact " + "extra"} {
		if _, err := parseSidebarMode(value); err == nil {
			t.Fatalf("invalid mode %q was accepted", value)
		}
	}
	compact, err := parseSidebarMode(" compact ")
	if err != nil || compact.supervisorDisplayName() != "herdr-tandem" || compact.developerDisplayName() != "herdr-tandem" {
		t.Fatalf("compact=%q err=%v", compact, err)
	}
	expanded, err := parseSidebarMode("expanded")
	if err != nil || expanded.supervisorDisplayName() != "herdr-tandem Supervisor" || expanded.developerDisplayName() != "agy Developer" {
		t.Fatalf("expanded=%q err=%v", expanded, err)
	}
}

func TestSidebarTransitionShowsDestinationBeforeHidingSource(t *testing.T) {
	for _, test := range []struct {
		name           string
		representative sidebarRepresentative
		want           []string
	}{
		{name: "developer", representative: sidebarRepresentativeDeveloper, want: []string{"developer=visible", "supervisor=hidden"}},
		{name: "supervisor", representative: sidebarRepresentativeSupervisor, want: []string{"supervisor=visible", "developer=hidden"}},
	} {
		t.Run(test.name, func(t *testing.T) {
			app := New(fakeRunner{}, nil, nil)
			var calls []string
			app.reportSidebarVisibility = func(_ context.Context, pane string, visibility herdr.PaneVisibility) error {
				calls = append(calls, pane+"="+string(visibility))
				return nil
			}
			if err := app.setSidebarRepresentative(context.Background(), sidebarModeCompact, "supervisor", "developer", test.representative); err != nil {
				t.Fatal(err)
			}
			if !reflect.DeepEqual(calls, test.want) {
				t.Fatalf("calls=%v want=%v", calls, test.want)
			}
		})
	}
}

func TestSidebarTransitionFailuresNeverHideBothRows(t *testing.T) {
	firstFailure := New(fakeRunner{}, nil, nil)
	var firstCalls []string
	firstFailure.reportSidebarVisibility = func(_ context.Context, pane string, visibility herdr.PaneVisibility) error {
		firstCalls = append(firstCalls, pane+"="+string(visibility))
		return errors.New("first write failed")
	}
	if err := firstFailure.setSidebarRepresentative(context.Background(), sidebarModeCompact, "supervisor", "developer", sidebarRepresentativeDeveloper); err == nil {
		t.Fatal("first-write failure was ignored")
	}
	if len(firstCalls) != 1 || firstCalls[0] != "developer=visible" {
		t.Fatalf("second write ran after first failure: %v", firstCalls)
	}

	secondFailure := New(fakeRunner{}, nil, nil)
	var secondCalls []string
	secondFailure.reportSidebarVisibility = func(_ context.Context, pane string, visibility herdr.PaneVisibility) error {
		secondCalls = append(secondCalls, pane+"="+string(visibility))
		if len(secondCalls) == 2 {
			return errors.New("second write failed")
		}
		return nil
	}
	if err := secondFailure.setSidebarRepresentative(context.Background(), sidebarModeCompact, "supervisor", "developer", sidebarRepresentativeDeveloper); err == nil {
		t.Fatal("second-write failure was ignored")
	}
	want := []string{"developer=visible", "supervisor=hidden"}
	if !reflect.DeepEqual(secondCalls, want) {
		t.Fatalf("calls=%v want=%v", secondCalls, want)
	}
}

func TestSidebarExpandedModeNeverHidesRows(t *testing.T) {
	app := New(fakeRunner{}, nil, nil)
	var calls []string
	app.reportSidebarVisibility = func(_ context.Context, pane string, visibility herdr.PaneVisibility) error {
		calls = append(calls, pane+"="+string(visibility))
		if visibility == herdr.PaneHidden {
			t.Fatal("expanded mode issued a hide")
		}
		return nil
	}
	for _, representative := range []sidebarRepresentative{sidebarRepresentativeDeveloper, sidebarRepresentativeSupervisor} {
		if err := app.setSidebarRepresentative(context.Background(), sidebarModeExpanded, "supervisor", "developer", representative); err != nil {
			t.Fatal(err)
		}
	}
	want := []string{"supervisor=visible", "developer=visible", "supervisor=visible", "developer=visible"}
	if !reflect.DeepEqual(calls, want) {
		t.Fatalf("calls=%v want=%v", calls, want)
	}
}

func TestSidebarCompactRepresentativeStatusPrecedence(t *testing.T) {
	for _, test := range []struct {
		status  string
		managed bool
		want    sidebarRepresentative
	}{
		{status: "working", managed: true, want: sidebarRepresentativeDeveloper},
		{status: "blocked", managed: true, want: sidebarRepresentativeDeveloper},
		{status: "unknown", managed: true, want: sidebarRepresentativeDeveloper},
		{status: "idle", managed: true, want: sidebarRepresentativeSupervisor},
		{status: "done", managed: true, want: sidebarRepresentativeSupervisor},
		{status: "absent", managed: true, want: sidebarRepresentativeSupervisor},
		{status: "working", managed: false, want: sidebarRepresentativeSupervisor},
	} {
		if got := compactRepresentativeForStatus(test.status, test.managed); got != test.want {
			t.Fatalf("status=%q managed=%t got=%q want=%q", test.status, test.managed, got, test.want)
		}
	}
}

func TestSidebarIndependentRuntimeModesInBothStartOrders(t *testing.T) {
	for _, order := range [][]sidebarMode{{sidebarModeExpanded, sidebarModeCompact}, {sidebarModeCompact, sidebarModeExpanded}} {
		state := map[string]herdr.PaneVisibility{}
		app := New(fakeRunner{}, nil, nil)
		app.reportSidebarVisibility = func(_ context.Context, pane string, visibility herdr.PaneVisibility) error {
			state[pane] = visibility
			return nil
		}
		for index, mode := range order {
			prefix := string(rune('a' + index))
			representative := sidebarRepresentativeSupervisor
			if err := app.setSidebarRepresentative(context.Background(), mode, prefix+"-supervisor", prefix+"-developer", representative); err != nil {
				t.Fatal(err)
			}
		}
		for index, mode := range order {
			prefix := string(rune('a' + index))
			if state[prefix+"-supervisor"] != herdr.PaneVisible {
				t.Fatalf("mode=%q supervisor state=%q", mode, state[prefix+"-supervisor"])
			}
			wantDeveloper := herdr.PaneHidden
			if mode == sidebarModeExpanded {
				wantDeveloper = herdr.PaneVisible
			}
			if state[prefix+"-developer"] != wantDeveloper {
				t.Fatalf("mode=%q developer state=%q want=%q", mode, state[prefix+"-developer"], wantDeveloper)
			}
		}
		before := map[string]herdr.PaneVisibility{}
		for key, value := range state {
			before[key] = value
		}
		if err := app.setSidebarRepresentative(context.Background(), sidebarModeCompact, "c-supervisor", "c-developer", sidebarRepresentativeSupervisor); err != nil {
			t.Fatal(err)
		}
		for key, value := range before {
			if state[key] != value {
				t.Fatalf("third runtime changed %s from %q to %q", key, value, state[key])
			}
		}
	}
}

func TestSidebarWarningsDoNotContainTaskContent(t *testing.T) {
	var stderr strings.Builder
	app := New(fakeRunner{}, nil, &stderr)
	secret := "private task prompt and answer"
	app.reportSidebarVisibility = func(context.Context, string, herdr.PaneVisibility) error {
		return errors.New("presentation unavailable")
	}
	app.warnSidebarTransition(context.Background(), sidebarModeCompact, runtimeContext{supervisor: "supervisor"}, "developer", sidebarRepresentativeDeveloper)
	if strings.Contains(stderr.String(), secret) {
		t.Fatal("sidebar warning exposed task content")
	}
}

type readOnlySidebarRunner struct {
	project      string
	developer    string
	status       string
	agentMissing bool
}

func (r readOnlySidebarRunner) LookPath(name string) (string, error) { return "/bin/" + name, nil }
func (r readOnlySidebarRunner) Run(_ context.Context, args ...string) (proc.Result, error) {
	joined := strings.Join(args, " ")
	switch joined {
	case "herdr agent get " + r.developer:
		if r.agentMissing {
			return proc.Result{ExitCode: 1, Stderr: `{"id":"x","error":{"code":"agent_not_found","message":"missing"}}`}, nil
		}
		status := r.status
		if status == "" {
			status = "idle"
		}
		return proc.Result{ExitCode: 0, Stdout: `{"id":"x","result":{"type":"agent_info","agent":{"name":"` + r.developer + `","pane_id":"w1:p2","workspace_id":"w1","tab_id":"w1:t1","cwd":"` + r.project + `","agent":"agy","agent_status":"` + status + `"}}}`}, nil
	case "herdr pane get w1:p1":
		return proc.Result{ExitCode: 0, Stdout: `{"id":"x","result":{"type":"pane_info","pane":{"pane_id":"w1:p1","workspace_id":"w1","tab_id":"w1:t1","cwd":"` + r.project + `","tokens":{}}}}`}, nil
	case "herdr pane get w1:p2":
		return proc.Result{ExitCode: 0, Stdout: `{"id":"x","result":{"type":"pane_info","pane":{"pane_id":"w1:p2","workspace_id":"w1","tab_id":"w1:t1","cwd":"` + r.project + `","tokens":{"herdr_tandem_owner":"` + r.developer + `","herdr_tandem_role":"developer","herdr_tandem_session_state":"pending"}}}}`}, nil
	default:
		return proc.Result{}, fmt.Errorf("unexpected read-only status call: %s", joined)
	}
}
func (readOnlySidebarRunner) RunAttached([]string, []string) error { return nil }

func TestTaskAndDeveloperStatusRemainSidebarReadOnly(t *testing.T) {
	project, err := resolveProject(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	developer := developerName("w1", "w1:p1")
	app := New(readOnlySidebarRunner{project: project, developer: developer}, io.Discard, io.Discard)
	app.stateDir = t.TempDir()
	app.providerServiceCheck = func(context.Context) error { return nil }
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
		}
		return ""
	}
	writes := 0
	app.reportSidebarVisibility = func(context.Context, string, herdr.PaneVisibility) error {
		writes++
		return nil
	}
	if _, err := app.getTaskStatus(context.Background()); err != nil {
		t.Fatal(err)
	}
	if _, err := app.getDeveloperStatus(context.Background()); err != nil {
		t.Fatal(err)
	}
	if writes != 0 {
		t.Fatalf("read-only status tools wrote sidebar metadata %d times", writes)
	}
}

func TestSidebarReconcilesManagedTaskInterruptionFromLiveDeveloperStatus(t *testing.T) {
	for _, test := range []struct {
		name         string
		status       string
		agentMissing bool
		want         []string
	}{
		{name: "working", status: "working", want: []string{"w1:p2=visible", "w1:p1=hidden"}},
		{name: "blocked", status: "blocked", want: []string{"w1:p2=visible", "w1:p1=hidden"}},
		{name: "unknown", status: "unknown", want: []string{"w1:p2=visible", "w1:p1=hidden"}},
		{name: "idle", status: "idle", want: []string{"w1:p1=visible", "w1:p2=hidden"}},
		{name: "done", status: "done", want: []string{"w1:p1=visible", "w1:p2=hidden"}},
		{name: "absent", agentMissing: true, want: []string{"w1:p1=visible", "w1:p2=hidden"}},
	} {
		t.Run(test.name, func(t *testing.T) {
			project, err := resolveProject(t.TempDir())
			if err != nil {
				t.Fatal(err)
			}
			developer := developerName("w1", "w1:p1")
			runner := readOnlySidebarRunner{project: project, developer: developer, status: test.status, agentMissing: test.agentMissing}
			app := New(runner, io.Discard, io.Discard)
			var calls []string
			app.reportSidebarVisibility = func(_ context.Context, pane string, visibility herdr.PaneVisibility) error {
				calls = append(calls, pane+"="+string(visibility))
				return nil
			}
			info := runtimeContext{workspaceID: "w1", supervisor: "w1:p1", developer: developer, developerPane: "w1:p2", project: project}
			app.reconcileSidebarAfterManagedTask(context.Background(), sidebarModeCompact, info, "w1:p2", false)
			if !reflect.DeepEqual(calls, test.want) {
				t.Fatalf("calls=%v want=%v", calls, test.want)
			}
		})
	}
}

func TestSidebarConfirmedCompletionRestoresSupervisor(t *testing.T) {
	project, err := resolveProject(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	developer := developerName("w1", "w1:p1")
	app := New(readOnlySidebarRunner{project: project, developer: developer, status: "working"}, io.Discard, io.Discard)
	var calls []string
	app.reportSidebarVisibility = func(_ context.Context, pane string, visibility herdr.PaneVisibility) error {
		calls = append(calls, pane+"="+string(visibility))
		return nil
	}
	info := runtimeContext{workspaceID: "w1", supervisor: "w1:p1", developer: developer, developerPane: "w1:p2", project: project}
	app.reconcileSidebarAfterManagedTask(context.Background(), sidebarModeCompact, info, "w1:p2", true)
	want := []string{"w1:p1=visible", "w1:p2=hidden"}
	if !reflect.DeepEqual(calls, want) {
		t.Fatalf("calls=%v want=%v", calls, want)
	}
}
