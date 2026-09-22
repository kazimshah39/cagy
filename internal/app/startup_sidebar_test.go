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
	attachedDir    string
	attachedArgs   []string
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
	case joined == "codex --yolo --help":
		return proc.Result{ExitCode: 0, Stdout: "--yolo"}, nil
	case joined == "codex mcp --help":
		return proc.Result{ExitCode: 0, Stdout: "list"}, nil
	case joined == "agy --help":
		return proc.Result{ExitCode: 0, Stdout: "--agent --model --mode --dangerously-skip-permissions accept-edits --conversation"}, nil
	default:
		return proc.Result{}, errors.New("unexpected startup call: " + joined)
	}
}
func (r *startupSidebarRunner) RunAttached(dir string, args []string, env []string) error {
	r.attachedDir = dir
	r.attachedArgs = append([]string(nil), args...)
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
				if err := app.start(context.Background(), project, mode); err != nil {
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
				wantSupervisorLabel := mode.supervisorDisplayName(app.supervisor.ID())
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
	if err := app.start(context.Background(), project, sidebarModeExpanded); err == nil || !strings.Contains(err.Error(), "configure herdr-tandem sidebar") {
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

func TestSidebarStartupDefaultsToCompactMode(t *testing.T) {
	for _, testCase := range []struct {
		name string
		args []string
	}{
		{name: "no arguments", args: nil},
		{name: "empty slice", args: []string{}},
		{name: "directory only", args: []string{"PROJECT_DIR"}},
		{name: "supervisor only", args: []string{"--supervisor", "codex"}},
		{name: "supervisor and directory", args: []string{"--supervisor", "codex", "PROJECT_DIR"}},
	} {
		t.Run(testCase.name, func(t *testing.T) {
			hasProjectDir := false
			for _, arg := range testCase.args {
				if arg == "PROJECT_DIR" {
					hasProjectDir = true
					break
				}
			}
			var project string
			var err error
			if hasProjectDir {
				project, err = resolveProject(t.TempDir())
			} else {
				project, err = resolveProject(".")
			}
			if err != nil {
				t.Fatal(err)
			}
			args := make([]string, len(testCase.args))
			for i, arg := range testCase.args {
				if arg == "PROJECT_DIR" {
					args[i] = project
				} else {
					args[i] = arg
				}
			}
			supervisorPane := "w1:p1"
			developerPane := "w1:p2"
			runner := &startupSidebarRunner{project: project, supervisorPane: supervisorPane, developerPane: developerPane, developerName: developerName("w1", supervisorPane)}
			app := New(runner, io.Discard, io.Discard)
			app.supervisor = sidebarTestSupervisor{}
			app.stateDir = t.TempDir()
			app.token = func() (string, error) { return "runtime-default-compact", nil }
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
			visibility := map[string]herdr.PaneVisibility{}
			app.reportSidebarVisibility = func(_ context.Context, pane string, value herdr.PaneVisibility) error {
				visibility[pane] = value
				return nil
			}
			app.setSidebarView = func(context.Context) error { return nil }

			if err := app.Run(context.Background(), args); err != nil {
				t.Fatalf("Run(%v) error: %v", args, err)
			}

			// In default compact mode, supervisor is visible, developer is hidden initially
			if visibility[supervisorPane] != herdr.PaneVisible {
				t.Fatalf("supervisor visibility = %v, want %v", visibility[supervisorPane], herdr.PaneVisible)
			}
			if visibility[developerPane] != herdr.PaneHidden {
				t.Fatalf("developer visibility = %v, want %v", visibility[developerPane], herdr.PaneHidden)
			}

			// Check display labels: "hdt" and "hdt"
			if !runner.hasDisplayLabel(supervisorPane, "hdt") {
				t.Fatalf("supervisor missing compact label 'hdt' in calls: %v", runner.calls)
			}
			if !runner.hasDisplayLabel(developerPane, "hdt") {
				t.Fatalf("developer missing compact label 'hdt' in calls: %v", runner.calls)
			}

			// Check runtime record stores "compact"
			record, exists, err := app.runtimeManager().FindForScope(runtimeContext{supervisorKind: supervisor.CodexID, developerKind: "agy", workspaceID: "w1", supervisor: supervisorPane, developer: runner.developerName, project: project})
			if err != nil || !exists || record.SidebarMode != string(sidebarModeCompact) {
				t.Fatalf("record=%+v exists=%t err=%v", record, exists, err)
			}

			// Check attached env passes compact mode
			if !containsEnvValue(runner.attachedEnv, sidebarModeEnv, string(sidebarModeCompact)) {
				t.Fatalf("attached env missing compact mode: %v", runner.attachedEnv)
			}
		})
	}
}

func TestSidebarStartupExplicitExpandedOptIn(t *testing.T) {
	for _, testCase := range []struct {
		name string
		args []string
	}{
		{name: "expanded flag alone", args: []string{"--expanded"}},
		{name: "expanded flag with directory", args: []string{"--expanded", "PROJECT_DIR"}},
		{name: "directory then expanded", args: []string{"PROJECT_DIR", "--expanded"}},
		{name: "supervisor and expanded", args: []string{"--supervisor", "codex", "--expanded", "PROJECT_DIR"}},
		{name: "expanded then supervisor", args: []string{"--expanded", "--supervisor", "codex", "PROJECT_DIR"}},
	} {
		t.Run(testCase.name, func(t *testing.T) {
			hasProjectDir := false
			for _, arg := range testCase.args {
				if arg == "PROJECT_DIR" {
					hasProjectDir = true
					break
				}
			}
			var project string
			var err error
			if hasProjectDir {
				project, err = resolveProject(t.TempDir())
			} else {
				project, err = resolveProject(".")
			}
			if err != nil {
				t.Fatal(err)
			}
			args := make([]string, len(testCase.args))
			for i, arg := range testCase.args {
				if arg == "PROJECT_DIR" {
					args[i] = project
				} else {
					args[i] = arg
				}
			}
			supervisorPane := "w1:p1"
			developerPane := "w1:p2"
			runner := &startupSidebarRunner{project: project, supervisorPane: supervisorPane, developerPane: developerPane, developerName: developerName("w1", supervisorPane)}
			app := New(runner, io.Discard, io.Discard)
			app.supervisor = sidebarTestSupervisor{}
			app.stateDir = t.TempDir()
			app.token = func() (string, error) { return "runtime-expanded-optin", nil }
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
			visibility := map[string]herdr.PaneVisibility{}
			app.reportSidebarVisibility = func(_ context.Context, pane string, value herdr.PaneVisibility) error {
				visibility[pane] = value
				return nil
			}
			app.setSidebarView = func(context.Context) error { return nil }

			if err := app.Run(context.Background(), args); err != nil {
				t.Fatalf("Run(%v) error: %v", args, err)
			}

			// In expanded mode, both rows must be visible immediately
			if visibility[supervisorPane] != herdr.PaneVisible {
				t.Fatalf("supervisor visibility = %v, want %v", visibility[supervisorPane], herdr.PaneVisible)
			}
			if visibility[developerPane] != herdr.PaneVisible {
				t.Fatalf("developer visibility = %v, want %v", visibility[developerPane], herdr.PaneVisible)
			}

			// Check display labels: "hdt Supervisor" and "agy Developer"
			if !runner.hasDisplayLabel(supervisorPane, "hdt Supervisor") {
				t.Fatalf("supervisor missing expanded label 'hdt Supervisor' in calls: %v", runner.calls)
			}
			if !runner.hasDisplayLabel(developerPane, "agy Developer") {
				t.Fatalf("developer missing expanded label 'agy Developer' in calls: %v", runner.calls)
			}

			// Check runtime record stores "expanded"
			record, exists, err := app.runtimeManager().FindForScope(runtimeContext{supervisorKind: supervisor.CodexID, developerKind: "agy", workspaceID: "w1", supervisor: supervisorPane, developer: runner.developerName, project: project})
			if err != nil || !exists || record.SidebarMode != string(sidebarModeExpanded) {
				t.Fatalf("record=%+v exists=%t err=%v", record, exists, err)
			}

			// Check attached env passes expanded mode
			if !containsEnvValue(runner.attachedEnv, sidebarModeEnv, string(sidebarModeExpanded)) {
				t.Fatalf("attached env missing expanded mode: %v", runner.attachedEnv)
			}
		})
	}
}

func TestManualDeveloperWorkCannotSwitchCompactRepresentativeAutomatically(t *testing.T) {
	app := New(fakeRunner{}, io.Discard, io.Discard)
	var visibilityLog []string
	app.reportSidebarVisibility = func(_ context.Context, pane string, visibility herdr.PaneVisibility) error {
		visibilityLog = append(visibilityLog, fmt.Sprintf("%s=%s", pane, visibility))
		return nil
	}

	supervisorPane := "w1:p1"
	developerPane := "w1:p2"

	// 1. Documented limitation: manual work typed directly in agy (managedTask = false)
	// cannot switch the compact representative to developer, even if agy is working or blocked.
	for _, status := range []string{"working", "blocked"} {
		rep := compactRepresentativeForStatus(status, false)
		if rep != sidebarRepresentativeSupervisor {
			t.Fatalf("status %q without managed task got representative %q, want supervisor", status, rep)
		}
	}

	// In compact mode, when supervisor is the representative, supervisor is visible and developer is hidden
	visibilityLog = nil
	if err := app.setSidebarRepresentative(context.Background(), sidebarModeCompact, supervisorPane, developerPane, sidebarRepresentativeSupervisor); err != nil {
		t.Fatal(err)
	}
	compactWant := []string{"w1:p1=visible", "w1:p2=hidden"}
	if !reflect.DeepEqual(visibilityLog, compactWant) {
		t.Fatalf("compact visibilityLog = %v, want %v", visibilityLog, compactWant)
	}

	// Only managed tasks switch the representative to the developer in compact mode
	visibilityLog = nil
	managedRep := compactRepresentativeForStatus("working", true)
	if managedRep != sidebarRepresentativeDeveloper {
		t.Fatalf("managed working task got representative %q, want developer", managedRep)
	}
	if err := app.setSidebarRepresentative(context.Background(), sidebarModeCompact, supervisorPane, developerPane, sidebarRepresentativeDeveloper); err != nil {
		t.Fatal(err)
	}
	compactManagedWant := []string{"w1:p2=visible", "w1:p1=hidden"}
	if !reflect.DeepEqual(visibilityLog, compactManagedWant) {
		t.Fatalf("compact managed visibilityLog = %v, want %v", visibilityLog, compactManagedWant)
	}

	// 2. In contrast, in expanded mode, both rows always remain visible, so manual work in agy is always visible.
	for _, rep := range []sidebarRepresentative{sidebarRepresentativeSupervisor, sidebarRepresentativeDeveloper} {
		visibilityLog = nil
		if err := app.setSidebarRepresentative(context.Background(), sidebarModeExpanded, supervisorPane, developerPane, rep); err != nil {
			t.Fatal(err)
		}
		expandedWant := []string{"w1:p1=visible", "w1:p2=visible"}
		if !reflect.DeepEqual(visibilityLog, expandedWant) {
			t.Fatalf("expanded visibilityLog = %v, want %v", visibilityLog, expandedWant)
		}
	}
}

func TestSidebarExpandedModeWithAgySupervisorLabelsAgySupervisor(t *testing.T) {
	project, err := resolveProject(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	supervisorPane := "w1:p1"
	developerPane := "w1:p2"
	runner := &startupSidebarRunner{
		project:        project,
		supervisorPane: supervisorPane,
		developerPane:  developerPane,
		developerName:  developerName("w1", supervisorPane),
	}
	app := New(runner, io.Discard, io.Discard)
	app.supervisor = supervisor.Agy{}
	app.stateDir = t.TempDir()
	app.configRoot = t.TempDir()
	app.token = func() (string, error) { return "test-token-expanded-agy", nil }
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
	visibility := map[string]herdr.PaneVisibility{}
	app.reportSidebarVisibility = func(_ context.Context, pane string, value herdr.PaneVisibility) error {
		visibility[pane] = value
		return nil
	}
	app.setSidebarView = func(context.Context) error { return nil }

	args := []string{"--expanded", "--supervisor", "agy", project}
	if err := app.Run(context.Background(), args); err != nil {
		t.Fatalf("Run(%v) error: %v", args, err)
	}

	// In expanded mode with agy supervisor, labels must distinguish agy Supervisor and agy Developer
	if !runner.hasDisplayLabel(supervisorPane, "agy Supervisor") {
		t.Fatalf("supervisor missing expanded label 'agy Supervisor' in calls: %v", runner.calls)
	}
	if !runner.hasDisplayLabel(developerPane, "agy Developer") {
		t.Fatalf("developer missing expanded label 'agy Developer' in calls: %v", runner.calls)
	}

	// Check runtime record stores "expanded" and "agy"
	record, exists, err := app.runtimeManager().FindForScope(runtimeContext{supervisorKind: supervisor.AgyID, developerKind: "agy", workspaceID: "w1", supervisor: supervisorPane, developer: runner.developerName, project: project})
	if err != nil || !exists || record.SidebarMode != string(sidebarModeExpanded) || record.SupervisorKind != supervisor.AgyID {
		t.Fatalf("record=%+v exists=%t err=%v", record, exists, err)
	}
}
