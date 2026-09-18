package app

import (
	"context"
	"crypto/sha256"
	"errors"
	"fmt"
	"path/filepath"
	"strings"

	"github.com/kazimshah39/cagy/internal/herdr"
)

func (a *App) start(ctx context.Context, path string, showAgents bool) error {
	project, err := resolveProject(path)
	if err != nil {
		return err
	}
	if err := a.requireHerdr(); err != nil {
		return err
	}
	if err := a.requireExecutables("herdr", "codex", "agy", "agm"); err != nil {
		return err
	}
	if err := a.checkAgyIntegration(ctx); err != nil {
		return err
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
		return fmt.Errorf("cagy supervisor tab is missing")
	}
	developerName := developerName(workspaceID, current.PaneID)
	startInfo := runtimeContext{
		workspaceID:   workspaceID,
		supervisor:    current.PaneID,
		developer:     developerName,
		developerPane: "",
		project:       project,
	}
	if err := a.markPaneRole(ctx, current.PaneID, developerName, "supervisor"); err != nil {
		return fmt.Errorf("mark cagy supervisor: %w", err)
	}
	supervisorDisplayName := compactSupervisorDisplayName
	if showAgents {
		supervisorDisplayName = expandedSupervisorDisplayName
	}
	if err := a.herdr.ReportAgentDisplay(ctx, current.PaneID, supervisorDisplaySource, "codex", supervisorDisplayName); err != nil {
		return fmt.Errorf("label cagy supervisor: %w", err)
	}
	if err := a.configureSidebar(ctx, showAgents); err != nil {
		return fmt.Errorf("configure cagy sidebar: %w", err)
	}

	developer, getErr := a.herdr.GetAgent(ctx, developerName)
	switch {
	case getErr == nil:
		if err := a.validateExistingDeveloper(ctx, developer, startInfo, current); err != nil {
			return err
		}
		if developer.AgentStatus != "idle" && developer.AgentStatus != "done" {
			return fmt.Errorf("developer is %s; check the right pane", developer.AgentStatus)
		}
		if err := a.markDeveloperPane(ctx, startInfo, developer.PaneID); err != nil {
			return err
		}
		if err := a.persistDeveloperSessionIfReported(ctx, developer); err != nil {
			return fmt.Errorf("save existing agy conversation identity: %w", err)
		}
	case herdr.IsCode(getErr, "agent_not_found"):
		pane, err := a.herdr.SplitRight(ctx, project)
		if err != nil {
			return fmt.Errorf("create developer pane: %w", err)
		}
		if err := validatePaneScope(pane, startInfo, current); err != nil {
			_ = a.herdr.ClosePane(ctx, pane.PaneID)
			return fmt.Errorf("new developer pane is unsafe: %w", err)
		}
		_ = a.herdr.RenamePane(ctx, current.PaneID, "Codex Supervisor")
		_ = a.herdr.RenamePane(ctx, pane.PaneID, developerPaneLabel)
		if err := a.markPaneRole(ctx, pane.PaneID, developerName, "developer"); err != nil {
			_ = a.herdr.ClosePane(ctx, pane.PaneID)
			return err
		}
		developer, err = a.herdr.StartAgy(ctx, developerName, pane.PaneID)
		switch {
		case err == nil:
			if err := validateDeveloperSession(developer, startInfo, current); err != nil {
				return a.rollbackStartedAgent(ctx, startInfo, pane.PaneID, err)
			}
			if err := a.markDeveloperPane(ctx, startInfo, pane.PaneID); err != nil {
				return a.rollbackStartedAgent(ctx, startInfo, pane.PaneID, err)
			}
			if err := a.recordFreshDeveloperSessionState(ctx, developer); err != nil {
				return a.rollbackStartedAgent(ctx, startInfo, pane.PaneID, err)
			}
			if err := a.ensureAgyReady(ctx, pane.PaneID); err != nil {
				return a.rollbackStartedAgent(ctx, startInfo, pane.PaneID, fmt.Errorf("prepare agy developer: %w", err))
			}
		case herdr.IsCode(err, "agent_name_taken"):
			existing, getErr := a.herdr.GetAgent(ctx, developerName)
			if getErr == nil && a.validateExistingDeveloper(ctx, existing, startInfo, current) == nil {
				if closeErr := a.confirmClosePane(ctx, pane.PaneID); closeErr != nil {
					return fmt.Errorf("clean up redundant developer pane %s: %w", pane.PaneID, closeErr)
				}
				if existing.AgentStatus != "idle" && existing.AgentStatus != "done" {
					return fmt.Errorf("developer is %s; check the right pane", existing.AgentStatus)
				}
				developer = existing
				break
			}
			_ = a.herdr.ClosePane(ctx, pane.PaneID)
			return fmt.Errorf("start agy developer: %w", err)
		default:
			_ = a.herdr.ClosePane(ctx, pane.PaneID)
			return fmt.Errorf("start agy developer: %w", err)
		}
	case getErr != nil:
		return fmt.Errorf("check existing developer: %w", getErr)
	}

	env := mergeEnv(a.environ(), map[string]string{
		"CAGY_PROJECT_DIR":        project,
		"CAGY_DEVELOPER":          developerName,
		"CAGY_DEVELOPER_PANE_ID":  developer.PaneID,
		"CAGY_SUPERVISOR_PANE_ID": current.PaneID,
	})
	return a.runner.RunAttached(codexArgs(project), env)
}

func (a *App) doctor(ctx context.Context) error {
	failed := false
	check := func(name string, err error) {
		if err != nil {
			failed = true
			fmt.Fprintf(a.stdout, "✗ %s: %v\n", name, err)
			return
		}
		fmt.Fprintf(a.stdout, "✓ %s\n", name)
	}

	check("inside Herdr", a.requireHerdr())
	for _, executable := range []string{"herdr", "codex", "agy", "agm"} {
		_, err := a.runner.LookPath(executable)
		check(executable+" on PATH", err)
	}
	check("Codex YOLO flag", a.commandSucceeds(ctx, "codex", "--yolo", "--help"))
	check("agy YOLO, exact resume, and quota probe flags", a.helpContains(ctx, []string{"agy", "--help"}, "--dangerously-skip-permissions", "accept-edits", "--conversation", "--print", "--output-format", "--print-timeout"))
	check("Herdr agent automation", a.helpContains(ctx, []string{"herdr", "agent"}, "agent start", "agent prompt", "agent wait", "agy"))
	check("Herdr pane automation", a.helpContains(ctx, []string{"herdr", "pane"}, "pane split", "pane run", "pane close", "pane report-metadata"))
	check("Herdr sidebar labels", a.helpContains(ctx, []string{"herdr", "pane", "report-metadata", "--help"}, "--source", "--agent", "--display-agent", "--token"))
	check("Herdr Agent view API", a.helpContains(ctx, []string{"herdr", "api", "schema", "--json"}, "agent.view.set", "agent.view.clear"))
	check("AGM recovery commands", a.helpContains(ctx, []string{"agm", "help"}, "refresh-all", "auto-switch"))
	check("AGM auto-switch minimum flag", a.helpContains(ctx, []string{"agm", "auto-switch", "--help"}, "--min"))
	check("Herdr agy transcript integration", a.checkAgyIntegration(ctx))
	check("current Herdr pane", a.checkCurrentPane(ctx))
	if a.getenv("CAGY_SUPERVISOR_PANE_ID") != "" && a.getenv("CAGY_PROJECT_DIR") != "" {
		check("cagy session", a.checkSessionHealth(ctx))
		if taskErr := a.reportTaskJournal(ctx); taskErr != nil {
			failed = true
			if !errors.Is(taskErr, errTaskAttention) {
				fmt.Fprintf(a.stdout, "✗ interrupted task state: %v\n", taskErr)
			}
		}
	}

	if failed {
		return fmt.Errorf("doctor found problems")
	}
	fmt.Fprintln(a.stdout, "cagy is ready")
	return nil
}

func (a *App) ask(ctx context.Context, task string) error {
	contextInfo, err := a.context()
	if err != nil {
		return err
	}
	lock, err := acquireLock(a.stateDir, contextInfo.developer, a.now())
	if err != nil {
		return err
	}
	defer lock.release()

	if err := a.ensureNoInterruptedTask(ctx, contextInfo); err != nil {
		return err
	}

	developer, err := a.ensureDeveloper(ctx, contextInfo)
	if err != nil {
		return err
	}
	switch developer.AgentStatus {
	case "idle", "done":
	case "working":
		return fmt.Errorf("developer is busy")
	case "blocked":
		return fmt.Errorf("developer is blocked; check the right pane")
	default:
		return fmt.Errorf("developer is not ready (%s); check the right pane", developer.AgentStatus)
	}

	exhausted, _ := a.agyQuotaExhausted(ctx)
	checkpoint, err := a.transcriptCheckpoint(developer)
	if err != nil {
		return fmt.Errorf("prepare agy transcript: %w", err)
	}
	phase := taskPhaseSubmitting
	if exhausted {
		phase = taskPhaseRecovering
	}
	if err := a.beginTaskTracking(contextInfo, developer, task, checkpoint, phase); err != nil {
		return fmt.Errorf("prepare durable task state: %w", err)
	}

	if exhausted {
		fmt.Fprintln(a.stderr, "cagy: agy quota is low; starting visible account recovery before task submission")
		recoveredOutput, recoverErr := a.recover(ctx, contextInfo, developer, task, false)
		if recoverErr != nil {
			a.warnTrackedPhase(taskPhaseUncertain, developer)
			return recoverErr
		}
		return a.deliverTrackedOutput(strings.TrimSpace(recoveredOutput), developer)
	}

	before, _ := a.herdr.ReadAgent(ctx, contextInfo.developer, 400)
	result, taskErr := a.runDeveloperTask(ctx, contextInfo.developer, task, before, checkpoint)
	if result.quotaExhausted {
		recoveryDeveloper := developer
		if _, sessionErr := exactAgySessionID(result.agent); sessionErr == nil {
			recoveryDeveloper = result.agent
		}
		a.warnTrackedPhase(taskPhaseRecovering, recoveryDeveloper)
		fmt.Fprintln(a.stderr, "cagy: agy quota was exhausted; starting visible account recovery")
		recoveredOutput, recoverErr := a.recover(ctx, contextInfo, recoveryDeveloper, task, true)
		if recoverErr != nil {
			a.warnTrackedPhase(taskPhaseUncertain, recoveryDeveloper)
			return recoverErr
		}
		return a.deliverTrackedOutput(strings.TrimSpace(recoveredOutput), result.agent)
	}
	if taskErr != nil {
		a.warnTrackedPhase(taskPhaseUncertain, result.agent)
		if errors.Is(taskErr, context.Canceled) || errors.Is(taskErr, context.DeadlineExceeded) {
			return fmt.Errorf("agy task monitoring stopped, but the visible developer may still be running; run cagy doctor: %w", taskErr)
		}
		return fmt.Errorf("agy task failed; run cagy doctor before sending another task: %w", taskErr)
	}
	a.warnIfDeveloperSessionNotPersisted(ctx, result.agent)
	if result.agent.AgentStatus == "blocked" {
		a.warnTrackedPhase(taskPhaseBlocked, result.agent)
		return fmt.Errorf("developer is blocked; check the right pane")
	}
	if result.output == "" {
		a.warnTrackedPhase(taskPhaseUncertain, result.agent)
		return fmt.Errorf("agy finished without readable output; run cagy doctor")
	}
	return a.deliverTrackedOutput(result.output, result.agent)
}

func (a *App) deliverTrackedOutput(output string, developer herdr.AgentInfo) error {
	if strings.TrimSpace(output) == "" {
		a.warnTrackedPhase(taskPhaseUncertain, developer)
		return fmt.Errorf("agy finished without readable output")
	}
	if err := a.setTrackedPhase(taskPhaseCompleted, developer); err != nil {
		return fmt.Errorf("save completed task state before delivering output: %w", err)
	}
	if _, err := fmt.Fprintln(a.stdout, output); err != nil {
		return fmt.Errorf("write agy response; recover it with cagy ask --recover: %w", err)
	}
	if a.activeTask != nil {
		if err := a.removeTaskJournal(a.activeTask.Developer); err != nil {
			return fmt.Errorf("agy answer was delivered, but durable task state could not be cleared; run cagy doctor before new work: %w", err)
		}
	}
	a.activeTask = nil
	return nil
}

func (a *App) stop(ctx context.Context) error {
	info, err := a.context()
	if err != nil {
		return err
	}
	supervisor, err := a.supervisorPane(ctx, info)
	if err != nil {
		return err
	}
	developer, err := a.herdr.GetAgent(ctx, info.developer)
	if herdr.IsCode(err, "agent_not_found") {
		fmt.Fprintln(a.stdout, "cagy developer is not running")
		return nil
	}
	if err != nil {
		return fmt.Errorf("find agy developer: %w", err)
	}
	if err := a.validateExistingDeveloper(ctx, developer, info, supervisor); err != nil {
		return err
	}
	if err := a.herdr.SendAgentKeys(ctx, info.developer, "ctrl+c"); err != nil {
		return fmt.Errorf("stop agy developer: %w", err)
	}
	if err := a.waitAgentReleased(ctx, info.developer); err != nil {
		return fmt.Errorf("wait for agy developer to stop: %w", err)
	}
	if err := a.herdr.ClosePane(ctx, developer.PaneID); err != nil {
		return fmt.Errorf("close developer pane: %w", err)
	}
	fmt.Fprintln(a.stdout, "stopped the agy developer; exit Codex normally to finish")
	return nil
}

type runtimeContext struct {
	workspaceID   string
	supervisor    string
	developer     string
	developerPane string
	project       string
}

func (a *App) context() (runtimeContext, error) {
	if err := a.requireHerdr(); err != nil {
		return runtimeContext{}, err
	}
	workspaceID := a.getenv("HERDR_WORKSPACE_ID")
	supervisor := a.getenv("CAGY_SUPERVISOR_PANE_ID")
	if supervisor == "" {
		supervisor = a.getenv("HERDR_PANE_ID")
	}
	developer := developerName(workspaceID, supervisor)
	if saved := a.getenv("CAGY_DEVELOPER"); saved != "" && saved != developer {
		return runtimeContext{}, fmt.Errorf("cagy developer identity is stale; restart the supervisor")
	}
	developerPane := a.getenv("CAGY_DEVELOPER_PANE_ID")
	project := a.getenv("CAGY_PROJECT_DIR")
	if project == "" {
		var err error
		project, err = resolveProject(".")
		if err != nil {
			return runtimeContext{}, err
		}
	}
	return runtimeContext{workspaceID: workspaceID, supervisor: supervisor, developer: developer, developerPane: developerPane, project: filepath.Clean(project)}, nil
}

func (a *App) requireHerdr() error {
	if a.getenv("HERDR_ENV") != "1" {
		return fmt.Errorf("run cagy from a shell pane inside Herdr")
	}
	if a.getenv("HERDR_WORKSPACE_ID") == "" || a.getenv("HERDR_PANE_ID") == "" {
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
		pane, validateErr := a.validatedDeveloperPane(ctx, developer, info, supervisor)
		if validateErr != nil {
			return validateErr
		}
		liveSessionID, sessionErr := exactAgySessionID(developer)
		if sessionErr != nil {
			pendingFresh := (developer.AgentSession == nil || strings.TrimSpace(developer.AgentSession.Value) == "") && strings.TrimSpace(pane.Tokens["cagy_session"]) == "" && pane.Tokens[agySessionStateToken] == agySessionStatePending
			if !pendingFresh {
				return fmt.Errorf("exact agy conversation is not ready: %w", sessionErr)
			}
		} else {
			savedSessionID := strings.TrimSpace(pane.Tokens["cagy_session"])
			if savedSessionID == "" {
				return fmt.Errorf("developer pane has no saved agy conversation identity; run one successful cagy ask")
			}
			if !agyConversationIDPattern.MatchString(savedSessionID) {
				return fmt.Errorf("developer pane has an invalid saved agy conversation identity")
			}
			if savedSessionID != liveSessionID {
				return fmt.Errorf("saved agy conversation does not match the live conversation")
			}
			if pane.Tokens[agySessionStateToken] != agySessionStateReady {
				return fmt.Errorf("developer pane has an invalid saved agy conversation state")
			}
		}
		panes, listErr := a.herdr.ListPanes(ctx, info.workspaceID)
		if listErr != nil {
			return fmt.Errorf("list cagy panes: %w", listErr)
		}
		for _, pane := range panes {
			if pane.PaneID == info.supervisor || pane.PaneID == developer.PaneID || validatePaneScope(pane, info, supervisor) != nil {
				continue
			}
			if pane.Tokens["cagy_owner"] == info.developer && pane.Tokens["cagy_role"] == "recovery" {
				return fmt.Errorf("recovery pane %s remains; inspect or close it after diagnosis", pane.PaneID)
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
		return fmt.Errorf("agy developer is missing; cagy ask can repair pane %s", pane.PaneID)
	}
	return fmt.Errorf("agy developer is missing; cagy ask can create a replacement")
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
	return fmt.Sprintf("cagy_dev_%x", sum[:5])
}

func validateDeveloper(agent herdr.AgentInfo, workspaceID, project string) error {
	if agent.Agent != "agy" {
		return fmt.Errorf("cagy developer target is not agy")
	}
	if agent.WorkspaceID != workspaceID {
		return fmt.Errorf("cagy developer belongs to another Herdr workspace")
	}
	agentProject, err := agentCWD(agent)
	if err != nil {
		return err
	}
	if agentProject != filepath.Clean(project) {
		return fmt.Errorf("cagy developer belongs to another project: %s", agentProject)
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
