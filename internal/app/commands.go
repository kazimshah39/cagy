package app

import (
	"context"
	"crypto/sha256"
	"errors"
	"fmt"
	"path/filepath"
	"strings"

	"github.com/kazimshah39/herdr-tandem/internal/herdr"
	"github.com/kazimshah39/herdr-tandem/internal/supervisor"
)

func (a *App) start(ctx context.Context, path string, mode sidebarMode) (runErr error) {
	a.debugf("start begin path=%q mode=%q", path, mode)
	if err := a.checkPlatform(); err != nil {
		return err
	}
	project, err := resolveProject(path)
	if err != nil {
		return err
	}
	if err := a.requireHerdr(); err != nil {
		return err
	}
	if err := a.requireExecutables("herdr", a.supervisor.Executable(), a.developerAdapter.Executable()); err != nil {
		return err
	}
	if err := a.supervisor.Validate(ctx, a.runner); err != nil {
		return err
	}
	if err := a.checkAgyIntegration(ctx); err != nil {
		return err
	}
	if a.developerAdapter.RequiresProviderService() {
		if err := a.checkProviderService(ctx); err != nil {
			return err
		}
	}
	if strings.TrimSpace(a.getenv("HERDR_SOCKET_PATH")) == "" {
		return fmt.Errorf("herdr socket path is missing")
	}
	binPath, err := a.resolveExecutable()
	if err != nil {
		return fmt.Errorf("resolve herdr-tandem executable: %w", err)
	}
	current, err := a.herdr.CurrentPane(ctx)
	if err != nil {
		return fmt.Errorf("read current Herdr pane: %w", err)
	}
	if current.PaneID != a.getenv("HERDR_PANE_ID") {
		return fmt.Errorf("herdr current pane does not match this shell")
	}
	workspaceID := a.getenv("HERDR_WORKSPACE_ID")
	if current.WorkspaceID != workspaceID {
		return fmt.Errorf("herdr current pane is in another workspace")
	}
	if strings.TrimSpace(current.TabID) == "" {
		return fmt.Errorf("herdr-tandem supervisor tab is missing")
	}
	developerName := developerName(workspaceID, current.PaneID)
	startInfo := runtimeContext{supervisorKind: a.supervisor.ID(), developerKind: a.developerAdapter.ID(), workspaceID: workspaceID, supervisor: current.PaneID, developer: developerName, project: project}
	buildRevision := "unknown"
	if a.runningBuild != nil {
		buildRevision = a.runningBuild().Fingerprint()
	}
	prepared, err := a.runtimeManager().Prepare(ctx, runtimeRecord{BuildRevision: buildRevision, SidebarMode: string(mode), SupervisorKind: a.supervisor.ID(), DeveloperKind: a.developerAdapter.ID(), WorkspaceID: workspaceID, SupervisorPaneID: current.PaneID, Developer: developerName, Project: project})
	if err != nil {
		return err
	}
	keepRuntime := false
	defer func() {
		if runErr != nil && !keepRuntime {
			a.cleanupPreparedRuntime(ctx, prepared.RuntimeID)
		}
	}()

	if err := a.markPaneRole(ctx, current.PaneID, developerName, "supervisor"); err != nil {
		return fmt.Errorf("mark herdr-tandem supervisor: %w", err)
	}
	if err := a.herdr.ReportPaneRuntime(ctx, current.PaneID, paneOwnershipSource, prepared.RuntimeID, buildRevision); err != nil {
		return fmt.Errorf("mark herdr-tandem supervisor runtime: %w", err)
	}
	if err := a.setPaneSidebarVisibility(ctx, current.PaneID, herdr.PaneVisible); err != nil {
		return fmt.Errorf("show herdr-tandem supervisor: %w", err)
	}
	if err := a.herdr.ReportAgentDisplay(ctx, current.PaneID, supervisorDisplaySource, a.supervisor.ID(), mode.supervisorDisplayName()); err != nil {
		return fmt.Errorf("label herdr-tandem supervisor: %w", err)
	}
	if err := a.setSidebarView(ctx); err != nil {
		return fmt.Errorf("configure herdr-tandem sidebar: %w", err)
	}

	developer, getErr := a.herdr.GetAgent(ctx, developerName)
	switch {
	case getErr == nil:
		if _, removeErr := a.runtimeManager().RemoveAndClearViewIfLast(ctx, prepared.RuntimeID, a.clearSidebarView); removeErr != nil {
			return fmt.Errorf("existing agy developer requires restart, and prepared runtime cleanup failed: %w", removeErr)
		}
		return fmt.Errorf("an existing agy developer is not owned by this herdr-tandem runtime; run herdr-tandem stop, then restart herdr-tandem")
	case herdr.IsCode(getErr, "agent_not_found"):
		pane, splitErr := a.herdr.SplitRight(ctx, project)
		if splitErr != nil {
			return fmt.Errorf("create developer pane: %w", splitErr)
		}
		if err := validatePaneScope(pane, startInfo, current); err != nil {
			_ = a.herdr.ClosePane(ctx, pane.PaneID)
			return fmt.Errorf("new developer pane is unsafe: %w", err)
		}
		_ = a.herdr.RenamePane(ctx, current.PaneID, a.supervisor.DisplayName()+" Supervisor")
		_ = a.herdr.RenamePane(ctx, pane.PaneID, developerPaneLabel)
		if err := a.markPaneRole(ctx, pane.PaneID, developerName, "developer"); err != nil {
			_ = a.confirmClosePane(ctx, pane.PaneID)
			return err
		}
		initialVisibility := herdr.PaneHidden
		if mode == sidebarModeExpanded {
			initialVisibility = herdr.PaneVisible
		}
		if err := a.setPaneSidebarVisibility(ctx, pane.PaneID, initialVisibility); err != nil {
			_ = a.confirmClosePane(ctx, pane.PaneID)
			return fmt.Errorf("set initial developer sidebar visibility: %w", err)
		}
		if err := a.herdr.ReportPaneRuntime(ctx, pane.PaneID, paneOwnershipSource, prepared.RuntimeID, buildRevision); err != nil {
			_ = a.confirmClosePane(ctx, pane.PaneID)
			return fmt.Errorf("mark herdr-tandem developer runtime: %w", err)
		}
		prepared, err = a.runtimeManager().Update(prepared.RuntimeID, func(record *runtimeRecord) error { record.DeveloperPaneID = pane.PaneID; return nil })
		if err != nil {
			_ = a.confirmClosePane(ctx, pane.PaneID)
			return err
		}
		startSpec, specErr := a.developerAdapter.StartSpec(developerName, pane.PaneID, "")
		if specErr != nil {
			err = specErr
		} else {
			developer, err = a.herdr.StartAgent(ctx, startSpec)
		}
		if err == nil {
			err = a.ensureDeveloperReady(ctx, pane.PaneID)
		}
		if err != nil && developer.PaneID != "" {
			keepRuntime = true
			return a.rollbackStartedAgent(ctx, startInfo, pane.PaneID, fmt.Errorf("prepare started agy developer: %w", err))
		}
		if err != nil {
			_ = a.confirmClosePane(ctx, pane.PaneID)
			return fmt.Errorf("start agy developer: %w", err)
		}
		if err := a.validateDeveloperSession(developer, startInfo, current); err != nil {
			keepRuntime = true
			return a.rollbackStartedAgent(ctx, startInfo, pane.PaneID, err)
		}
		if err := a.markDeveloperPane(ctx, startInfo, pane.PaneID, mode); err != nil {
			keepRuntime = true
			return a.rollbackStartedAgent(ctx, startInfo, pane.PaneID, err)
		}
		if err := a.herdr.ReportPaneRuntime(ctx, pane.PaneID, paneOwnershipSource, prepared.RuntimeID, buildRevision); err != nil {
			keepRuntime = true
			return a.rollbackStartedAgent(ctx, startInfo, pane.PaneID, err)
		}
		if err := a.recordFreshDeveloperSessionState(ctx, developer); err != nil {
			keepRuntime = true
			return a.rollbackStartedAgent(ctx, startInfo, pane.PaneID, err)
		}
	default:
		return fmt.Errorf("check existing developer: %w", getErr)
	}
	keepRuntime = true
	mcpEnv, err := a.buildMCPEnvForRuntime(current, developerName, developer.PaneID, project, prepared.RuntimeID, mode)
	if err != nil {
		return err
	}
	env := mergeEnv(a.environ(), map[string]string{"HERDR_TANDEM_PROJECT_DIR": project, "HERDR_TANDEM_DEVELOPER": developerName, "HERDR_TANDEM_DEVELOPER_PANE_ID": developer.PaneID, "HERDR_TANDEM_SUPERVISOR_PANE_ID": current.PaneID, runtimeIDEnv: prepared.RuntimeID, sidebarModeEnv: string(mode), supervisorKindEnv: a.supervisor.ID(), developerKindEnv: a.developerAdapter.ID()})
	launch, err := a.supervisor.BuildLaunch(supervisor.LaunchContext{ProjectDir: project, Executable: binPath, MCPEnv: mcpEnv, BaseEnv: env, Instructions: supervisorInstructions(a.now())})
	if err != nil {
		return fmt.Errorf("configure %s supervisor: %w", a.supervisor.DisplayName(), err)
	}
	return a.runner.RunAttached(launch.Args, launch.Env)
}

func (a *App) buildMCPEnvForRuntime(current herdr.PaneInfo, developerName, developerPaneID, project, runtimeID string, mode sidebarMode) (map[string]string, error) {
	socketPath := strings.TrimSpace(a.getenv("HERDR_SOCKET_PATH"))
	if socketPath == "" {
		return nil, fmt.Errorf("herdr socket path is missing")
	}
	if _, err := parseSidebarMode(string(mode)); err != nil {
		return nil, err
	}
	env := map[string]string{"HERDR_ENV": "1", "HERDR_WORKSPACE_ID": current.WorkspaceID, "HERDR_PANE_ID": current.PaneID, "HERDR_SOCKET_PATH": socketPath, "HERDR_TANDEM_SUPERVISOR_PANE_ID": current.PaneID, "HERDR_TANDEM_DEVELOPER": developerName, "HERDR_TANDEM_DEVELOPER_PANE_ID": developerPaneID, "HERDR_TANDEM_PROJECT_DIR": project, supervisorKindEnv: a.supervisor.ID(), developerKindEnv: a.developerAdapter.ID(), sidebarModeEnv: string(mode)}
	if v := strings.TrimSpace(current.TabID); v != "" {
		env["HERDR_TAB_ID"] = v
	}
	if runtimeID = strings.TrimSpace(runtimeID); runtimeID != "" {
		env[runtimeIDEnv] = runtimeID
	}
	if serviceURL := strings.TrimSpace(a.getenv("HERDR_TANDEM_PROVIDER_SERVICE_URL")); serviceURL != "" {
		env["HERDR_TANDEM_PROVIDER_SERVICE_URL"] = serviceURL
	}
	return env, nil
}

func (a *App) doctor(ctx context.Context) error {
	a.debugf("doctor begin")
	failed := false
	check := func(name string, err error) {
		a.debugf("doctor check name=%q ok=%t error=%q", name, err == nil, err)
		if err != nil {
			failed = true
			fmt.Fprintf(a.stdout, "✗ %s: %v\n", name, err)
			return
		}
		fmt.Fprintf(a.stdout, "✓ %s\n", name)
	}
	check("supported platform", a.checkPlatform())
	check("inside Herdr", a.requireHerdr())
	for _, executable := range []string{"herdr", a.supervisor.Executable(), a.developerAdapter.Executable()} {
		_, err := a.runner.LookPath(executable)
		check(executable+" on PATH", err)
	}
	if a.developerAdapter.RequiresProviderService() {
		check("provider service health", a.checkProviderService(ctx))
	}
	check(a.supervisor.DisplayName()+" capabilities", a.supervisor.Validate(ctx, a.runner))
	check(a.developerAdapter.DisplayName()+" YOLO and exact resume flags", a.helpContains(ctx, []string{a.developerAdapter.Executable(), "--help"}, "--dangerously-skip-permissions", "accept-edits", "--conversation"))
	check("Herdr agent automation", a.helpContains(ctx, []string{"herdr", "agent"}, "agent start", "agent prompt", "agent wait", "agy"))
	check("Herdr pane automation", a.helpContains(ctx, []string{"herdr", "pane"}, "pane split", "pane run", "pane close", "pane report-metadata"))
	check("Herdr sidebar labels", a.helpContains(ctx, []string{"herdr", "pane", "report-metadata", "--help"}, "--source", "--agent", "--display-agent", "--token"))
	check("Herdr Agent view API", a.helpContains(ctx, []string{"herdr", "api", "schema", "--json"}, "agent.view.set", "agent.view.clear"))
	check("Herdr "+a.developerAdapter.DisplayName()+" transcript integration", a.checkAgyIntegration(ctx))
	check("current Herdr pane", a.checkCurrentPane(ctx))
	if a.getenv("HERDR_TANDEM_SUPERVISOR_PANE_ID") != "" && a.getenv("HERDR_TANDEM_PROJECT_DIR") != "" {
		check("herdr-tandem session", a.checkSessionHealth(ctx))
		if taskErr := a.reportTaskJournal(ctx); taskErr != nil {
			failed = true
			if !errors.Is(taskErr, errTaskAttention) {
				fmt.Fprintf(a.stdout, "✗ interrupted task state: %v\n", taskErr)
			}
		}
	}
	if failed {
		a.debugf("doctor end ok=false")
		return fmt.Errorf("doctor found problems")
	}
	a.debugf("doctor end ok=true")
	fmt.Fprintln(a.stdout, "herdr-tandem is ready")
	return nil
}

type taskDelivery struct {
	output    string
	developer herdr.AgentInfo
	receipt   string
}

func (a *App) delegateTask(ctx context.Context, task string) (*taskDelivery, error) {
	taskID := debugTaskFingerprint(task)
	a.debugf("task delegate begin task=%q bytes=%d", taskID, len(task))
	info, err := a.context()
	if err != nil {
		return nil, err
	}
	lock, err := acquireLock(a.stateDir, info.developer, a.now())
	if err != nil {
		return nil, err
	}
	defer lock.release()
	if err := a.ensureNoInterruptedTask(ctx, info); err != nil {
		return nil, err
	}
	if a.developerAdapter.RequiresProviderService() {
		if err := a.checkProviderService(ctx); err != nil {
			return nil, err
		}
	}
	developer, err := a.ensureDeveloper(ctx, info)
	if err != nil {
		return nil, err
	}
	mode, err := a.runtimeSidebarMode(info)
	if err != nil {
		return nil, err
	}
	switch developer.AgentStatus {
	case "idle", "done":
	case "working":
		return nil, fmt.Errorf("developer is busy")
	case "blocked":
		return nil, fmt.Errorf("developer is blocked; check the right pane")
	default:
		return nil, fmt.Errorf("developer is not ready (%s); check the right pane", developer.AgentStatus)
	}
	checkpoint, err := a.transcriptCheckpoint(developer)
	if err != nil {
		return nil, fmt.Errorf("prepare agy transcript: %w", err)
	}
	if err := a.beginTaskTracking(info, developer, task, checkpoint, taskPhaseSubmitting); err != nil {
		return nil, fmt.Errorf("prepare durable task state: %w", err)
	}
	completed := false
	developerPane := developer.PaneID
	a.warnSidebarTransition(ctx, mode, info, developerPane, sidebarRepresentativeDeveloper)
	defer func() {
		a.reconcileSidebarAfterManagedTask(ctx, mode, info, developerPane, completed)
	}()
	result, taskErr := a.runDeveloperTask(ctx, info.developer, task, checkpoint)
	if result.agent.PaneID != "" {
		developerPane = result.agent.PaneID
	}
	if taskErr != nil {
		a.warnTrackedPhase(taskPhaseUncertain, result.agent)
		if errors.Is(taskErr, context.Canceled) || errors.Is(taskErr, context.DeadlineExceeded) {
			return nil, fmt.Errorf("agy task monitoring stopped, but the visible developer may still be running; run herdr-tandem doctor: %w", taskErr)
		}
		return nil, fmt.Errorf("agy task failed; run herdr-tandem doctor before sending another task: %w", taskErr)
	}
	a.warnIfDeveloperSessionNotPersisted(ctx, result.agent)
	if result.agent.AgentStatus == "blocked" {
		a.warnTrackedPhase(taskPhaseBlocked, result.agent)
		return nil, fmt.Errorf("developer is blocked; check the right pane")
	}
	if strings.TrimSpace(result.output) == "" {
		a.warnTrackedPhase(taskPhaseUncertain, result.agent)
		return nil, fmt.Errorf("agy finished without readable output; run herdr-tandem doctor")
	}
	if err := a.setTrackedPhase(taskPhaseCompleted, result.agent); err != nil {
		return nil, fmt.Errorf("save completed task state before delivering output: %w", err)
	}
	completed = true
	receipt := ""
	if a.activeTask != nil {
		receipt = a.activeTask.DeliveryReceipt
	}
	return &taskDelivery{output: strings.TrimSpace(result.output), developer: result.agent, receipt: receipt}, nil
}

func (a *App) ask(ctx context.Context, task string) error {
	delivery, err := a.delegateTask(ctx, task)
	if err != nil {
		return err
	}
	return a.deliverTrackedOutput(delivery.output, delivery.developer, delivery.receipt)
}

func (a *App) deliverTrackedOutput(output string, developer herdr.AgentInfo, receiptOverride ...string) error {
	if strings.TrimSpace(output) == "" {
		a.warnTrackedPhase(taskPhaseUncertain, developer)
		return fmt.Errorf("agy finished without readable output")
	}
	if a.activeTask == nil || a.activeTask.Phase != taskPhaseCompleted {
		if err := a.setTrackedPhase(taskPhaseCompleted, developer); err != nil {
			return fmt.Errorf("save completed task state before delivering output: %w", err)
		}
	}
	receipt := ""
	if len(receiptOverride) > 0 && receiptOverride[0] != "" {
		receipt = receiptOverride[0]
	} else if a.activeTask != nil {
		receipt = a.activeTask.DeliveryReceipt
	}
	if _, err := fmt.Fprintln(a.stdout, output); err != nil {
		return fmt.Errorf("write agy response; recover it with herdr-tandem ask --recover: %w", err)
	}
	if receipt != "" {
		if err := a.acknowledgeTask(context.Background(), receipt); err != nil {
			return fmt.Errorf("agy answer was delivered, but durable task state could not be cleared; run herdr-tandem doctor before new work: %w", err)
		}
	} else if a.activeTask != nil {
		if err := a.removeTaskJournal(a.activeTask.Developer); err != nil {
			return fmt.Errorf("agy answer was delivered, but durable task state could not be cleared; run herdr-tandem doctor before new work: %w", err)
		}
		a.activeTask = nil
	}
	return nil
}

func (a *App) stop(ctx context.Context) error {
	a.debugf("stop begin")
	info, err := a.context()
	if err != nil {
		return err
	}
	manager := a.runtimeManager()
	record, mode, err := a.runtimeSidebarRecord(info)
	if err != nil {
		return err
	}
	hasRecord := true
	if strings.TrimSpace(a.getenv(runtimeIDEnv)) != "" && a.getenv(runtimeIDEnv) != record.RuntimeID {
		return errors.New("herdr-tandem runtime ID is stale; restart the supervisor before stopping")
	}
	supervisor, err := a.supervisorPane(ctx, info)
	if err != nil {
		return err
	}
	developer, err := a.herdr.GetAgent(ctx, info.developer)
	if herdr.IsCode(err, "agent_not_found") {
		paneID := info.developerPane
		if hasRecord && record.DeveloperPaneID != "" {
			paneID = record.DeveloperPaneID
		}
		restored := false
		if paneID != "" {
			pane, paneErr := a.herdr.GetPane(ctx, paneID)
			switch {
			case paneErr == nil:
				if err := validatePaneScope(pane, info, supervisor); err != nil {
					return err
				}
				if pane.Tokens["herdr_tandem_owner"] != info.developer || pane.Tokens["herdr_tandem_role"] != "developer" {
					return errors.New("managed developer pane ownership is invalid")
				}
				a.warnSidebarTransition(ctx, mode, info, paneID, sidebarRepresentativeSupervisor)
				restored = true
				if err := a.confirmClosePane(ctx, paneID); err != nil {
					return err
				}
			case herdr.IsCode(paneErr, "pane_not_found"):
			default:
				return fmt.Errorf("verify stopped developer pane: %w", paneErr)
			}
		}
		if !restored {
			if showErr := a.setPaneSidebarVisibility(ctx, info.supervisor, herdr.PaneVisible); showErr != nil {
				fmt.Fprintf(a.stderr, "herdr-tandem warning: supervisor sidebar state could not be restored: %v\n", showErr)
			}
		}
		if hasRecord {
			clearAttempted, removeErr := manager.RemoveAndClearViewIfLast(ctx, record.RuntimeID, a.clearSidebarView)
			if removeErr != nil && !clearAttempted {
				return fmt.Errorf("clear herdr-tandem runtime ownership: %w", removeErr)
			}
			if removeErr != nil {
				fmt.Fprintf(a.stderr, "herdr-tandem warning: stopped runtime but could not clear sidebar view: %v\n", removeErr)
			}
		}
		fmt.Fprintln(a.stdout, "herdr-tandem developer is not running")
		return nil
	}
	if err != nil {
		return fmt.Errorf("find agy developer: %w", err)
	}
	if err := a.validateExistingDeveloper(ctx, developer, info, supervisor); err != nil {
		return err
	}
	if hasRecord && record.DeveloperPaneID != "" && record.DeveloperPaneID != developer.PaneID {
		return errors.New("herdr-tandem runtime pane does not match the visible developer")
	}
	a.warnSidebarTransition(ctx, mode, info, developer.PaneID, sidebarRepresentativeSupervisor)
	if err := a.interruptAndWaitAgent(ctx, info.developer); err != nil {
		if errors.Is(err, errAgentReleaseTimeout) {
			return fmt.Errorf("wait for agy developer to stop: %w", err)
		}
		return fmt.Errorf("stop agy developer: %w", err)
	}
	if err := a.confirmClosePane(ctx, developer.PaneID); err != nil {
		return err
	}
	if hasRecord {
		clearAttempted, removeErr := manager.RemoveAndClearViewIfLast(ctx, record.RuntimeID, a.clearSidebarView)
		if removeErr != nil && !clearAttempted {
			return fmt.Errorf("clear herdr-tandem runtime ownership: %w", removeErr)
		}
		if removeErr != nil {
			fmt.Fprintf(a.stderr, "herdr-tandem warning: stopped runtime but could not clear sidebar view: %v\n", removeErr)
		}
	}
	fmt.Fprintf(a.stdout, "stopped the agy developer; exit %s normally to finish\n", a.supervisor.DisplayName())
	return nil
}

type runtimeContext struct {
	supervisorKind string
	developerKind  string
	workspaceID    string
	supervisor     string
	developer      string
	developerPane  string
	project        string
}

func (a *App) context() (runtimeContext, error) {
	if err := a.requireHerdr(); err != nil {
		return runtimeContext{}, err
	}
	workspaceID := a.getenv("HERDR_WORKSPACE_ID")
	supervisor := a.getenv("HERDR_TANDEM_SUPERVISOR_PANE_ID")
	if supervisor == "" {
		supervisor = a.getenv("HERDR_PANE_ID")
	}
	developer := developerName(workspaceID, supervisor)
	if saved := a.getenv("HERDR_TANDEM_DEVELOPER"); saved != "" && saved != developer {
		return runtimeContext{}, fmt.Errorf("herdr-tandem developer identity is stale; restart the supervisor")
	}
	developerPane, project := a.getenv("HERDR_TANDEM_DEVELOPER_PANE_ID"), a.getenv("HERDR_TANDEM_PROJECT_DIR")
	if project == "" {
		var err error
		project, err = resolveProject(".")
		if err != nil {
			return runtimeContext{}, err
		}
	}
	return runtimeContext{supervisorKind: a.supervisor.ID(), developerKind: a.developerAdapter.ID(), workspaceID: workspaceID, supervisor: supervisor, developer: developer, developerPane: developerPane, project: filepath.Clean(project)}, nil
}

func (a *App) requireHerdr() error {
	if a.getenv("HERDR_ENV") != "1" {
		return fmt.Errorf("run herdr-tandem from a shell pane inside Herdr")
	}
	paneID := a.getenv("HERDR_PANE_ID")
	if paneID == "" {
		paneID = a.getenv("HERDR_TANDEM_SUPERVISOR_PANE_ID")
	}
	if a.getenv("HERDR_WORKSPACE_ID") == "" || paneID == "" {
		return fmt.Errorf("herdr pane context is missing")
	}
	return nil
}
func (a *App) requireExecutables(names ...string) error {
	for _, name := range names {
		if _, err := a.runner.LookPath(name); err != nil {
			return fmt.Errorf("required command not found: %s", name)
		}
	}
	return nil
}

func (a *App) checkSessionHealth(ctx context.Context) error {
	if a.developerAdapter.RequiresProviderService() {
		if err := a.checkProviderService(ctx); err != nil {
			return err
		}
	}
	info, err := a.context()
	if err != nil {
		return err
	}
	supervisor, err := a.supervisorPane(ctx, info)
	if err != nil {
		return err
	}
	developer, err := a.herdr.GetAgent(ctx, info.developer)
	if err == nil {
		pane, err := a.validatedDeveloperPane(ctx, developer, info, supervisor)
		if err != nil {
			return err
		}
		if live, sessionErr := a.exactDeveloperSessionID(developer); sessionErr == nil {
			if strings.TrimSpace(pane.Tokens["herdr_tandem_session"]) != live || pane.Tokens[agySessionStateToken] != agySessionStateReady {
				return fmt.Errorf("saved agy conversation does not match the live conversation")
			}
		} else {
			pending := (developer.AgentSession == nil || strings.TrimSpace(developer.AgentSession.Value) == "") && strings.TrimSpace(pane.Tokens["herdr_tandem_session"]) == "" && pane.Tokens[agySessionStateToken] == agySessionStatePending
			if !pending {
				return fmt.Errorf("exact agy conversation is not ready: %w", sessionErr)
			}
		}
		return nil
	}
	if !herdr.IsCode(err, "agent_not_found") {
		return fmt.Errorf("find agy developer: %w", err)
	}
	pane, repairErr := a.findRepairPane(ctx, info, supervisor)
	if repairErr != nil {
		return repairErr
	}
	if pane.PaneID != "" {
		return fmt.Errorf("agy developer is missing; herdr-tandem ask can repair pane %s", pane.PaneID)
	}
	return fmt.Errorf("agy developer is missing; herdr-tandem ask can create a replacement")
}

func (a *App) getDeveloperStatus(ctx context.Context) (*DeveloperStatusOutput, error) {
	info, err := a.context()
	if err != nil {
		return nil, err
	}
	providerServiceReady := !a.developerAdapter.RequiresProviderService() || a.checkProviderService(ctx) == nil
	supervisor, err := a.supervisorPane(ctx, info)
	if err != nil {
		return nil, err
	}
	developer, err := a.herdr.GetAgent(ctx, info.developer)
	if err != nil {
		if herdr.IsCode(err, "agent_not_found") {
			return &DeveloperStatusOutput{Supervisor: a.supervisor.ID(), DeveloperKind: a.developerAdapter.ID(), Developer: info.developer, Status: "not_running", Project: info.project, ProviderServiceReady: providerServiceReady, TaskSummary: "developer is not running"}, nil
		}
		return nil, fmt.Errorf("find agy developer: %w", err)
	}
	pane, err := a.validatedDeveloperPane(ctx, developer, info, supervisor)
	if err != nil {
		return nil, err
	}
	sessionReady := false
	if live, sessionErr := a.exactDeveloperSessionID(developer); sessionErr == nil {
		sessionReady = strings.TrimSpace(pane.Tokens["herdr_tandem_session"]) == live && pane.Tokens[agySessionStateToken] == agySessionStateReady
	} else {
		sessionReady = (developer.AgentSession == nil || strings.TrimSpace(developer.AgentSession.Value) == "") && strings.TrimSpace(pane.Tokens["herdr_tandem_session"]) == "" && pane.Tokens[agySessionStateToken] == agySessionStatePending
	}
	taskSummary := "no active task"
	record, exists, loadErr := a.loadTaskJournal(info.developer)
	if loadErr != nil {
		taskSummary = "interrupted task state is unreadable; run herdr-tandem doctor"
	} else if exists {
		inspection, inspectErr := a.inspectTaskJournal(ctx, info, record)
		if inspectErr != nil {
			taskSummary = "interrupted task state is invalid; run herdr-tandem doctor"
		} else {
			taskSummary = fmt.Sprintf("task is %s: %s", inspection.kind, inspection.message)
		}
	}
	return &DeveloperStatusOutput{Supervisor: a.supervisor.ID(), DeveloperKind: a.developerAdapter.ID(), Developer: info.developer, PaneID: developer.PaneID, Status: developer.AgentStatus, Project: info.project, SessionReady: sessionReady, ProviderServiceReady: providerServiceReady, TaskSummary: taskSummary}, nil
}

func (a *App) checkCurrentPane(ctx context.Context) error {
	if err := a.requireHerdr(); err != nil {
		return err
	}
	pane, err := a.herdr.CurrentPane(ctx)
	if err != nil {
		return err
	}
	if pane.PaneID != a.getenv("HERDR_PANE_ID") {
		return fmt.Errorf("pane mismatch")
	}
	return nil
}
func (a *App) checkAgyIntegration(ctx context.Context) error {
	result, err := a.runner.Run(ctx, "herdr", "integration", "status")
	if err != nil {
		return err
	}
	if result.ExitCode != 0 {
		return fmt.Errorf("check Herdr integrations: exit status %d", result.ExitCode)
	}
	for _, line := range strings.Split(result.Stdout, "\n") {
		if strings.HasPrefix(strings.TrimSpace(line), "antigravity-cli: current ") {
			return nil
		}
	}
	return fmt.Errorf("agy transcript integration is missing or outdated; run: herdr integration install antigravity-cli")
}
func (a *App) commandSucceeds(ctx context.Context, args ...string) error {
	result, err := a.runner.Run(ctx, args...)
	if err != nil {
		return err
	}
	if result.ExitCode != 0 {
		return fmt.Errorf("exited with status %d", result.ExitCode)
	}
	return nil
}
func (a *App) helpContains(ctx context.Context, args []string, expected ...string) error {
	result, err := a.runner.Run(ctx, args...)
	if err != nil {
		return err
	}
	text := result.Stdout + result.Stderr
	for _, item := range expected {
		if !strings.Contains(text, item) {
			return fmt.Errorf("missing %s support", item)
		}
	}
	return nil
}
func developerName(workspaceID, paneID string) string {
	sum := sha256.Sum256([]byte(workspaceID + "\x00" + paneID))
	return fmt.Sprintf("herdr_tandem_dev_%x", sum[:5])
}
func validateDeveloper(agent herdr.AgentInfo, workspaceID, project string, expected string) error {
	if agent.Agent != expected {
		return fmt.Errorf("herdr-tandem developer target is not %s", expected)
	}
	if agent.WorkspaceID != workspaceID {
		return fmt.Errorf("herdr-tandem developer belongs to another Herdr workspace")
	}
	agentProject, err := agentCWD(agent)
	if err != nil {
		return err
	}
	if agentProject != filepath.Clean(project) {
		return fmt.Errorf("herdr-tandem developer belongs to another project: %s", agentProject)
	}
	return nil
}
func mergeEnv(base []string, values map[string]string) []string {
	merged := make(map[string]string, len(base)+len(values))
	for _, item := range base {
		key, value, found := strings.Cut(item, "=")
		if found {
			merged[key] = value
		}
	}
	for key, value := range values {
		merged[key] = value
	}
	result := make([]string, 0, len(merged))
	for key, value := range merged {
		result = append(result, key+"="+value)
	}
	return result
}
