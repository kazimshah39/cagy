package app

import (
	"context"
	"errors"
	"fmt"
	"io"
	"reflect"
	"strings"
	"testing"

	buildmeta "github.com/kazimshah39/herdr-tandem/internal/buildinfo"
	"github.com/kazimshah39/herdr-tandem/internal/herdr"
	proc "github.com/kazimshah39/herdr-tandem/internal/process"
	"github.com/kazimshah39/herdr-tandem/internal/supervisor"
)

type sidebarTestSupervisor struct{}

func (sidebarTestSupervisor) ID() string                                  { return supervisor.CodexID }
func (sidebarTestSupervisor) DisplayName() string                         { return "Codex" }
func (sidebarTestSupervisor) Executable() string                          { return "codex" }
func (sidebarTestSupervisor) Validate(context.Context, proc.Runner) error { return nil }
func (sidebarTestSupervisor) BuildLaunch(input supervisor.LaunchContext) (supervisor.LaunchSpec, error) {
	return supervisor.LaunchSpec{Args: []string{"codex-test"}, Env: input.BaseEnv}, nil
}

type startupSidebarRunner struct {
	project        string
	supervisorPane string
	developerPane  string
	developerName  string
	calls          [][]string
	attachedEnv    []string
}

func (r *startupSidebarRunner) LookPath(name string) (string, error) { return "/bin/" + name, nil }
func (r *startupSidebarRunner) Run(_ context.Context, args ...string) (proc.Result, error) {
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
	case joined == "herdr pane list --workspace w1":
		return proc.Result{ExitCode: 0, Stdout: `{"id":"x","result":{"type":"pane_list","panes":[]}}`}, nil
	case strings.HasPrefix(joined, "herdr agent get "):
		return proc.Result{ExitCode: 1, Stderr: `{"id":"x","error":{"code":"agent_not_found","message":"missing"}}`}, nil
	case strings.HasPrefix(joined, "herdr pane split "):
		return proc.Result{ExitCode: 0, Stdout: fmt.Sprintf(`{"id":"x","result":{"type":"pane_info","pane":{"pane_id":%q,"workspace_id":"w1","tab_id":"w1:t1","cwd":%q,"tokens":{}}}}`, r.developerPane, r.project)}, nil
	case strings.HasPrefix(joined, "herdr agent start "):
		return proc.Result{ExitCode: 0, Stdout: fmt.Sprintf(`{"id":"x","result":{"type":"agent_started","agent":{"name":%q,"pane_id":%q,"workspace_id":"w1","tab_id":"w1:t1","cwd":%q,"agent":"agy","agent_status":"idle","agent_session":{"source":"herdr:antigravity_cli","agent":"agy","kind":"id","value":"11111111-1111-1111-1111-111111111111"}}}}`, r.developerName, r.developerPane, r.project)}, nil
	case strings.HasPrefix(joined, "herdr pane read "):
		return proc.Result{ExitCode: 0, Stdout: "? for shortcuts"}, nil
	case strings.HasPrefix(joined, "herdr pane wait-output "), strings.HasPrefix(joined, "herdr pane rename "), strings.HasPrefix(joined, "herdr pane report-metadata "):
		return ok, nil
	default:
		return proc.Result{}, errors.New("unexpected startup call: " + joined)
	}
}
func (r *startupSidebarRunner) RunAttached(args []string, env []string) error {
	if !reflect.DeepEqual(args, []string{"codex-test"}) {
		return fmt.Errorf("unexpected attached args: %v", args)
	}
	r.attachedEnv = append([]string(nil), env...)
	return nil
}

func TestSidebarStartupKeepsCompactAndExpandedProjectsIndependentInBothOrders(t *testing.T) {
	for _, order := range [][]sidebarMode{{sidebarModeExpanded, sidebarModeCompact}, {sidebarModeCompact, sidebarModeExpanded}} {
		t.Run(string(order[0])+"-then-"+string(order[1]), func(t *testing.T) {
			stateDir := t.TempDir()
			visibility := map[string]herdr.PaneVisibility{}
			viewSets := 0
			for index, mode := range order {
				project, err := resolveProject(t.TempDir())
				if err != nil {
					t.Fatal(err)
				}
				supervisorPane := fmt.Sprintf("w1:p%d", index*2+1)
				developerPane := fmt.Sprintf("w1:p%d", index*2+2)
				runner := &startupSidebarRunner{project: project, supervisorPane: supervisorPane, developerPane: developerPane, developerName: developerName("w1", supervisorPane)}
				app := New(runner, io.Discard, io.Discard)
				app.supervisor = sidebarTestSupervisor{}
				app.stateDir = stateDir
				runtimeID := fmt.Sprintf("runtime-%d", index)
				app.token = func() (string, error) { return runtimeID, nil }
				app.checkPlatform = func() error { return nil }
				app.resolveExecutable = func() (string, error) { return "/tmp/herdr-tandem", nil }
				app.providerServiceCheck = func(context.Context) error { return nil }
				app.runningBuild = func() buildmeta.Identity { return buildmeta.Identity{} }
				app.getenv = func(key string) string {
					switch key {
					case "HERDR_ENV":
						return "1"
					case "HERDR_WORKSPACE_ID":
						return "w1"
					case "HERDR_PANE_ID":
						return supervisorPane
					case "HERDR_SOCKET_PATH":
						return "/tmp/herdr.sock"
					}
					return ""
				}
				app.environ = func() []string { return nil }
				app.setSidebarView = func(context.Context) error { viewSets++; return nil }
				app.clearSidebarView = func(context.Context) error { return errors.New("view must not be cleared during startup") }
				app.reportSidebarVisibility = func(_ context.Context, pane string, value herdr.PaneVisibility) error {
					visibility[pane] = value
					return nil
				}
				if err := app.start(context.Background(), project, mode == sidebarModeExpanded); err != nil {
					t.Fatal(err)
				}
				if len(runner.attachedEnv) == 0 || !containsEnvValue(runner.attachedEnv, sidebarModeEnv, string(mode)) {
					t.Fatalf("attached env missing mode %q: %v", mode, runner.attachedEnv)
				}
				record, exists, err := app.runtimeManager().FindForScope(runtimeContext{supervisorKind: supervisor.CodexID, developerKind: "agy", workspaceID: "w1", supervisor: supervisorPane, developer: runner.developerName, project: project})
				if err != nil || !exists || record.SidebarMode != string(mode) {
					t.Fatalf("record=%+v exists=%t err=%v", record, exists, err)
				}
				wantDeveloper := herdr.PaneHidden
				if mode == sidebarModeExpanded {
					wantDeveloper = herdr.PaneVisible
				}
				if visibility[supervisorPane] != herdr.PaneVisible || visibility[developerPane] != wantDeveloper {
					t.Fatalf("mode=%q supervisor=%q developer=%q", mode, visibility[supervisorPane], visibility[developerPane])
				}
				wantSupervisorLabel := mode.supervisorDisplayName()
				wantDeveloperLabel := mode.developerDisplayName()
				if !runner.hasDisplayLabel(supervisorPane, wantSupervisorLabel) || !runner.hasDisplayLabel(developerPane, wantDeveloperLabel) {
					t.Fatalf("mode=%q calls=%v", mode, runner.calls)
				}
			}
			if viewSets != 2 {
				t.Fatalf("stable view sets=%d want=2", viewSets)
			}
		})
	}
}

func containsEnvValue(env []string, key, value string) bool {
	want := key + "=" + value
	for _, item := range env {
		if item == want {
			return true
		}
	}
	return false
}

func (r *startupSidebarRunner) hasDisplayLabel(pane, display string) bool {
	for _, call := range r.calls {
		joined := strings.Join(call, " ")
		if strings.Contains(joined, "pane report-metadata "+pane+" ") && strings.Contains(joined, "--display-agent "+display) {
			return true
		}
	}
	return false
}

func TestSidebarRepairRestoresPersistedModeVisibilityAndLabel(t *testing.T) {
	for _, mode := range []sidebarMode{sidebarModeCompact, sidebarModeExpanded} {
		t.Run(string(mode), func(t *testing.T) {
			project, err := resolveProject(t.TempDir())
			if err != nil {
				t.Fatal(err)
			}
			info := runtimeContext{supervisorKind: supervisor.CodexID, developerKind: "agy", workspaceID: "w1", supervisor: "w1:p1", developer: developerName("w1", "w1:p1"), project: project}
			runner := &startupSidebarRunner{project: project, supervisorPane: info.supervisor, developerPane: "w1:p2", developerName: info.developer}
			app := New(runner, io.Discard, io.Discard)
			app.supervisor = sidebarTestSupervisor{}
			app.stateDir = t.TempDir()
			app.token = func() (string, error) { return "runtime-repair", nil }
			app.getenv = func(key string) string {
				if key == sidebarModeEnv {
					return string(mode)
				}
				return ""
			}
			visibility := map[string]herdr.PaneVisibility{}
			app.reportSidebarVisibility = func(_ context.Context, pane string, value herdr.PaneVisibility) error {
				visibility[pane] = value
				return nil
			}
			prepared, err := app.runtimeManager().Prepare(context.Background(), runtimeRecord{SidebarMode: string(mode), SupervisorKind: supervisor.CodexID, DeveloperKind: "agy", WorkspaceID: "w1", SupervisorPaneID: info.supervisor, Developer: info.developer, Project: project})
			if err != nil {
				t.Fatal(err)
			}
			developer, err := app.repairMissingDeveloper(context.Background(), info)
			if err != nil {
				t.Fatal(err)
			}
			if developer.PaneID != "w1:p2" {
				t.Fatalf("developer=%+v", developer)
			}
			want := herdr.PaneHidden
			if mode == sidebarModeExpanded {
				want = herdr.PaneVisible
			}
			if visibility[developer.PaneID] != want {
				t.Fatalf("visibility=%q want=%q", visibility[developer.PaneID], want)
			}
			if !runner.hasDisplayLabel(developer.PaneID, mode.developerDisplayName()) {
				t.Fatalf("missing developer label %q in %v", mode.developerDisplayName(), runner.calls)
			}
			updated, exists, err := app.runtimeManager().loadByIDUnlocked(prepared.RuntimeID)
			if err != nil || !exists || updated.DeveloperPaneID != developer.PaneID {
				t.Fatalf("updated=%+v exists=%t err=%v", updated, exists, err)
			}
		})
	}
}

func TestSidebarStartupPresentationFailureKeepsSupervisorVisibleAndRollsBackRuntime(t *testing.T) {
	project, err := resolveProject(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	runner := &startupSidebarRunner{project: project, supervisorPane: "w1:p1", developerPane: "w1:p2", developerName: developerName("w1", "w1:p1")}
	app := New(runner, io.Discard, io.Discard)
	app.supervisor = sidebarTestSupervisor{}
	app.stateDir = t.TempDir()
	app.token = func() (string, error) { return "runtime-failed-start", nil }
	app.checkPlatform = func() error { return nil }
	app.resolveExecutable = func() (string, error) { return "/tmp/herdr-tandem", nil }
	app.providerServiceCheck = func(context.Context) error { return nil }
	app.runningBuild = func() buildmeta.Identity { return buildmeta.Identity{} }
	app.getenv = func(key string) string {
		switch key {
		case "HERDR_ENV":
			return "1"
		case "HERDR_WORKSPACE_ID":
			return "w1"
		case "HERDR_PANE_ID":
			return "w1:p1"
		case "HERDR_SOCKET_PATH":
			return "/tmp/herdr.sock"
		}
		return ""
	}
	app.environ = func() []string { return nil }
	var visibility []string
	app.reportSidebarVisibility = func(_ context.Context, pane string, value herdr.PaneVisibility) error {
		visibility = append(visibility, pane+"="+string(value))
		return nil
	}
	app.setSidebarView = func(context.Context) error { return errors.New("projection failed") }
	clears := 0
	app.clearSidebarView = func(context.Context) error { clears++; return nil }
	if err := app.start(context.Background(), project, false); err == nil || !strings.Contains(err.Error(), "configure herdr-tandem sidebar") {
		t.Fatalf("error=%v", err)
	}
	if !reflect.DeepEqual(visibility, []string{"w1:p1=visible"}) {
		t.Fatalf("visibility=%v", visibility)
	}
	if clears != 1 {
		t.Fatalf("clear calls=%d", clears)
	}
	if _, exists, err := app.runtimeManager().loadByIDUnlocked("runtime-failed-start"); err != nil || exists {
		t.Fatalf("failed runtime exists=%t err=%v", exists, err)
	}
}
