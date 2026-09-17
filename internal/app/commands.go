package app

import (
	"context"
	"crypto/sha256"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

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
	supervisorDisplayName := compactSupervisorDisplayName
	if showAgents {
		supervisorDisplayName = expandedSupervisorDisplayName
	}
	if err := a.herdr.ReportAgentDisplay(ctx, current.PaneID, supervisorDisplaySource, "codex", supervisorDisplayName, "supervisor"); err != nil {
		return fmt.Errorf("label cagy supervisor: %w", err)
	}
	if err := a.configureSidebar(ctx, showAgents); err != nil {
		return fmt.Errorf("configure cagy sidebar: %w", err)
	}

	developerName := developerName(workspaceID, current.PaneID)
	developer, getErr := a.herdr.GetAgent(ctx, developerName)
	switch {
	case getErr == nil:
		if err := validateDeveloper(developer, workspaceID, project); err != nil {
			return err
		}
		if developer.AgentStatus != "idle" && developer.AgentStatus != "done" {
			return fmt.Errorf("developer is %s; check the right pane", developer.AgentStatus)
		}
		if err := a.herdr.ReportAgentDisplay(ctx, developer.PaneID, developerDisplaySource, "agy", developerDisplayName, "developer"); err != nil {
			return fmt.Errorf("label cagy developer: %w", err)
		}
	case herdr.IsCode(getErr, "agent_not_found"):
		pane, err := a.herdr.SplitRight(ctx, project)
		if err != nil {
			return fmt.Errorf("create developer pane: %w", err)
		}
		if pane.WorkspaceID != workspaceID {
			_ = a.herdr.ClosePane(ctx, pane.PaneID)
			return fmt.Errorf("new developer pane was created in another workspace")
		}
		_ = a.herdr.RenamePane(ctx, current.PaneID, "Codex Supervisor")
		_ = a.herdr.RenamePane(ctx, pane.PaneID, "agy Developer")
		developer, err = a.herdr.StartAgy(ctx, developerName, pane.PaneID, false)
		if err != nil {
			_ = a.herdr.ClosePane(ctx, pane.PaneID)
			return fmt.Errorf("start agy developer: %w", err)
		}
		if err := validateDeveloper(developer, workspaceID, project); err != nil {
			_ = a.herdr.ClosePane(ctx, pane.PaneID)
			return err
		}
		if err := a.herdr.ReportAgentDisplay(ctx, pane.PaneID, developerDisplaySource, "agy", developerDisplayName, "developer"); err != nil {
			_ = a.herdr.ClosePane(ctx, pane.PaneID)
			return fmt.Errorf("label cagy developer: %w", err)
		}
		if err := a.ensureAgyReady(ctx, pane.PaneID); err != nil {
			_ = a.herdr.ClosePane(ctx, pane.PaneID)
			return fmt.Errorf("prepare agy developer: %w", err)
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
	check("agy YOLO and quota probe flags", a.helpContains(ctx, []string{"agy", "--help"}, "--dangerously-skip-permissions", "accept-edits", "--continue", "--print", "--output-format", "--print-timeout"))
	check("Herdr agent automation", a.helpContains(ctx, []string{"herdr", "agent"}, "agent start", "agent prompt", "agent wait", "agy"))
	check("Herdr pane automation", a.helpContains(ctx, []string{"herdr", "pane"}, "pane split", "pane run", "pane close", "pane report-metadata"))
	check("Herdr sidebar labels", a.helpContains(ctx, []string{"herdr", "pane", "report-metadata", "--help"}, "--source", "--agent", "--display-agent", "--token"))
	check("Herdr Agent view API", a.helpContains(ctx, []string{"herdr", "api", "schema", "--json"}, "agent.view.set", "agent.view.clear"))
	check("AGM recovery commands", a.helpContains(ctx, []string{"agm", "help"}, "refresh-all", "auto-switch"))
	check("AGM auto-switch minimum flag", a.helpContains(ctx, []string{"agm", "auto-switch", "--help"}, "--min"))
	check("Herdr agy transcript integration", a.checkAgyIntegration(ctx))
	check("current Herdr pane", a.checkCurrentPane(ctx))

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
	lock, err := acquireLock(a.tempDir, contextInfo.developer)
	if err != nil {
		return err
	}
	defer lock.release()

	developer, err := a.herdr.GetAgent(ctx, contextInfo.developer)
	if err != nil {
		return fmt.Errorf("find agy developer: %w", err)
	}
	if err := validateDeveloper(developer, contextInfo.workspaceID, contextInfo.project); err != nil {
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

	if exhausted, probeErr := a.agyQuotaExhausted(ctx); probeErr == nil && exhausted {
		recoveredOutput, err := a.recover(ctx, contextInfo, developer, task, false)
		if err != nil {
			return err
		}
		fmt.Fprintln(a.stdout, strings.TrimSpace(recoveredOutput))
		return nil
	}

	checkpoint, err := a.transcriptCheckpoint(developer)
	if err != nil {
		return fmt.Errorf("prepare agy transcript: %w", err)
	}
	before, _ := a.herdr.ReadAgent(ctx, contextInfo.developer, 400)
	result, taskErr := a.runDeveloperTask(ctx, contextInfo.developer, task, before, checkpoint)
	if result.quotaExhausted {
		recoveredOutput, err := a.recover(ctx, contextInfo, developer, task, true)
		if err != nil {
			return err
		}
		fmt.Fprintln(a.stdout, strings.TrimSpace(recoveredOutput))
		return nil
	}
	if taskErr != nil {
		return fmt.Errorf("agy task failed: %w", taskErr)
	}
	if result.agent.AgentStatus == "blocked" {
		if result.output != "" {
			fmt.Fprintln(a.stdout, result.output)
		}
		return fmt.Errorf("developer is blocked; check the right pane")
	}
	if result.output == "" {
		return fmt.Errorf("agy finished without readable output")
	}
	fmt.Fprintln(a.stdout, result.output)
	return nil
}

func (a *App) stop(ctx context.Context) error {
	info, err := a.context()
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
	if err := validateDeveloper(developer, info.workspaceID, info.project); err != nil {
		return err
	}
	_ = a.herdr.SendAgentKeys(ctx, info.developer, "ctrl+c")
	if err := a.herdr.ClosePane(ctx, developer.PaneID); err != nil {
		return fmt.Errorf("close developer pane: %w", err)
	}
	fmt.Fprintln(a.stdout, "stopped the agy developer; exit Codex normally to finish")
	return nil
}

type runtimeContext struct {
	workspaceID string
	supervisor  string
	developer   string
	project     string
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
	developer := a.getenv("CAGY_DEVELOPER")
	if developer == "" {
		developer = developerName(workspaceID, supervisor)
	}
	project := a.getenv("CAGY_PROJECT_DIR")
	if project == "" {
		var err error
		project, err = resolveProject(".")
		if err != nil {
			return runtimeContext{}, err
		}
	}
	return runtimeContext{workspaceID: workspaceID, supervisor: supervisor, developer: developer, project: filepath.Clean(project)}, nil
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
	agentProject := agent.ForegroundCWD
	if agentProject == "" {
		agentProject = agent.CWD
	}
	if filepath.Clean(agentProject) != filepath.Clean(project) {
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

type taskLock struct {
	path string
	file *os.File
}

func acquireLock(tempDir, developer string) (*taskLock, error) {
	sum := sha256.Sum256([]byte(developer))
	path := filepath.Join(tempDir, fmt.Sprintf("cagy-%x.lock", sum[:8]))
	// #nosec G304 -- path is under the configured temp directory with a SHA-256 filename.
	file, err := os.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600)
	if os.IsExist(err) {
		return nil, fmt.Errorf("developer is busy; if no task is running, remove %s", path)
	}
	if err != nil {
		return nil, fmt.Errorf("create task lock: %w", err)
	}
	_, _ = fmt.Fprintf(file, "pid=%d\nstarted=%s\n", os.Getpid(), time.Now().Format(time.RFC3339))
	return &taskLock{path: path, file: file}, nil
}

func (l *taskLock) release() {
	_ = l.file.Close()
	_ = os.Remove(l.path)
}
