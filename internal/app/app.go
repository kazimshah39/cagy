package app

import (
	"context"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"time"

	buildmeta "github.com/kazimshah39/herdr-tandem/internal/buildinfo"
	"github.com/kazimshah39/herdr-tandem/internal/developer"
	"github.com/kazimshah39/herdr-tandem/internal/herdr"
	"github.com/kazimshah39/herdr-tandem/internal/platform"
	proc "github.com/kazimshah39/herdr-tandem/internal/process"
	"github.com/kazimshah39/herdr-tandem/internal/supervisor"
)

const (
	developerPromptTimeoutMS = 30 * 60 * 1000
	commandTimeoutMS         = 5 * 60 * 1000
	developerReadyTimeoutMS  = 60 * 1000

	paneOwnershipSource              = "herdr-tandem:pane-owner"
	supervisorDisplaySource          = "herdr-tandem:supervisor-display"
	developerDisplaySource           = "herdr-tandem:developer-display"
	compactDisplayName               = "hdt"
	expandedSupervisorDisplayName    = "hdt Supervisor"
	expandedAgySupervisorDisplayName = "agy Supervisor"
	developerDisplayName             = "agy Developer"
	runtimeIDEnv                     = "HERDR_TANDEM_RUNTIME_ID"
	supervisorKindEnv                = "HERDR_TANDEM_SUPERVISOR_KIND"
	developerKindEnv                 = "HERDR_TANDEM_DEVELOPER_KIND"
	runtimeIDToken                   = "herdr_tandem_runtime_id"
	buildRevisionToken               = "herdr_tandem_build_revision"
)

const supervisorPrompt = `You are the supervisor. The visible agy agent in the right Herdr pane is the developer.
Delegate implementation work using the native herdr-tandem MCP tools. Follow this exact workflow:
1. Inspect task state with task_status before starting new work.
2. Delegate the task with delegate_task(task="..."). Task text is sent literally without shell interpolation.
3. Review the developer's changes, test execution, correctness, and security.
4. Call acknowledge_task(receipt="...") only after the result is received and in context.
5. The configured provider service owns provider accounts and quota fallback. Do not ask the user to switch accounts or restart agy after a quota event; report a visible provider failure only after the service has exhausted its configured fallback.
6. If a session or tool call is interrupted, use recover_task to retrieve the completed answer without resubmitting.
Shell CLI commands (such as herdr-tandem ask --stdin) are for emergency manual use only; always prefer the native MCP tools. Do not edit the same files while agy is working. Use current official web documentation for dependencies and external APIs. Give the final result to the user in clear, simple words.`

func supervisorInstructions(now time.Time) string {
	return fmt.Sprintf("Current local date: %s.\n%s", now.Format("Monday, January 2, 2006"), supervisorPrompt)
}

// App owns command parsing and the fixed herdr-tandem workflow.
type App struct {
	supervisor              supervisor.Adapter
	developerAdapter        developer.Adapter
	runner                  proc.Runner
	herdr                   *herdr.Client
	stdin                   io.Reader
	stdout                  io.Writer
	stderr                  io.Writer
	getenv                  func(string) string
	environ                 func() []string
	stateDir                string
	activeTask              *taskJournal
	token                   func() (string, error)
	now                     func() time.Time
	transcriptRoot          string
	transcriptWait          time.Duration
	missingTranscriptWait   time.Duration
	developerPoll           time.Duration
	initialPromptWait       time.Duration
	healthyStallWindow      time.Duration
	heartbeatInterval       time.Duration
	taskDeadline            time.Duration
	cancellationIdleWait    time.Duration
	agentStopTimeout        time.Duration
	agentStopEscalation     time.Duration
	developerReadyTimeout   time.Duration
	setSidebarView          func(context.Context) error
	clearSidebarView        func(context.Context) error
	reportSidebarVisibility func(context.Context, string, herdr.PaneVisibility) error
	resolveExecutable       func() (string, error)
	checkPlatform           func() error
	runningBuild            func() buildmeta.Identity
	installedBuild          func(string) (buildmeta.Identity, error)
	providerServiceCheck    func(context.Context) error
	diagnosticSink          func(string)
	supervisorModel         string
	developerModel          string
	configRoot              string
	availableAgyModels      map[string]struct{}
	availableAgyModelsErr   error
	agyModelsChecked        bool
}

func New(runner proc.Runner, stdout, stderr io.Writer) *App {
	herdrClient := herdr.New(runner)
	application := &App{
		supervisor:            supervisor.Codex{},
		developerAdapter:      developer.Agy{},
		runner:                runner,
		herdr:                 herdrClient,
		stdin:                 os.Stdin,
		stdout:                stdout,
		stderr:                stderr,
		getenv:                os.Getenv,
		environ:               os.Environ,
		stateDir:              defaultStateDir(),
		token:                 randomToken,
		now:                   time.Now,
		transcriptRoot:        developer.Agy{}.TranscriptRoot(),
		transcriptWait:        3 * time.Second,
		missingTranscriptWait: 30 * time.Second,
		developerPoll:         time.Second,
		initialPromptWait:     30 * time.Second,
		healthyStallWindow:    150 * time.Second,
		heartbeatInterval:     5 * time.Minute,
		taskDeadline:          30 * time.Minute,
		cancellationIdleWait:  30 * time.Second,
		agentStopTimeout:      15 * time.Second,
		agentStopEscalation:   1500 * time.Millisecond,
		developerReadyTimeout: time.Duration(developerReadyTimeoutMS) * time.Millisecond,
		checkPlatform:         platform.Current,
		runningBuild:          buildmeta.Running,
		installedBuild:        buildmeta.ReadFile,
	}
	herdrClient.SetDiagnostic(application.debugf)
	application.resolveExecutable = func() (string, error) {
		return resolveExecutable(os.Executable)
	}
	application.setSidebarView = herdrClient.SetTandemSidebarView
	application.clearSidebarView = herdrClient.ClearTandemSidebarView
	application.reportSidebarVisibility = herdrClient.ReportPaneSidebarVisibility
	return application
}

func (a *App) Run(ctx context.Context, args []string) (runErr error) {
	if id := strings.TrimSpace(a.getenv(supervisorKindEnv)); id != "" {
		adapter, err := supervisor.DefaultRegistry().Resolve(id)
		if err != nil {
			return err
		}
		a.supervisor = adapter
	}
	if id := strings.TrimSpace(a.getenv(developerKindEnv)); id != "" {
		adapter, err := developer.Resolve(id)
		if err != nil {
			return err
		}
		a.developerAdapter = adapter
	}
	started := time.Now()
	command := "start"
	if len(args) > 0 {
		command = args[0]
		if !strings.HasPrefix(command, "-") && command != "ask" && command != "doctor" && command != "stop" && command != "mcp-server" && command != "help" {
			command = "start-path"
		}
	}
	a.debugf("run begin command=%q argc=%d", command, len(args))
	defer func() {
		if runErr != nil {
			a.debugf("run end command=%q ok=false elapsed=%s error=%q", command, time.Since(started).Round(time.Millisecond), runErr)
		} else {
			a.debugf("run end command=%q ok=true elapsed=%s", command, time.Since(started).Round(time.Millisecond))
		}
	}()

	if len(args) == 0 {
		path, mode, supervisorID, supervisorModel, developerModel, err := parseStartArgs(nil)
		if err != nil {
			return err
		}
		if supervisorID != "" {
			a.supervisor, err = supervisor.DefaultRegistry().Resolve(supervisorID)
			if err != nil {
				return err
			}
		}
		a.supervisorModel = supervisorModel
		a.developerModel = developerModel
		return a.start(ctx, path, mode)
	}

	switch args[0] {
	case "help", "-h", "--help":
		a.printHelp()
		return nil
	case "doctor":
		if len(args) > 1 {
			id, err := supervisorIDFromArgs(args[1:])
			if err != nil {
				return err
			}
			a.supervisor, err = supervisor.DefaultRegistry().Resolve(id)
			if err != nil {
				return err
			}
		}
		return a.doctor(ctx)
	case "ask":
		if len(args) == 2 {
			switch args[1] {
			case "-h", "--help":
				a.printAskHelp()
				return nil
			case "--stdin":
				task, err := readTaskInput(a.stdin)
				if err != nil {
					return err
				}
				return a.ask(ctx, task)
			case "--recover":
				return a.recoverInterruptedTask(ctx)
			case "--forget":
				return a.forgetInterruptedTask(ctx)
			}
		}
		if len(args) < 2 || strings.TrimSpace(strings.Join(args[1:], " ")) == "" {
			return fmt.Errorf("usage: herdr-tandem ask --stdin | herdr-tandem ask \"<development task>\" | herdr-tandem ask --recover | herdr-tandem ask --forget")
		}
		if strings.HasPrefix(args[1], "--") {
			return fmt.Errorf("unknown herdr-tandem ask option: %s", args[1])
		}
		return a.ask(ctx, strings.Join(args[1:], " "))
	case "mcp-server":
		if len(args) != 1 {
			return fmt.Errorf("usage: herdr-tandem mcp-server")
		}
		return a.serveMCP(ctx)
	case "stop":
		if len(args) != 1 {
			return fmt.Errorf("usage: herdr-tandem stop")
		}
		return a.stop(ctx)
	default:
		path, mode, supervisorID, supervisorModel, developerModel, err := parseStartArgs(args)
		if err != nil {
			return err
		}
		if supervisorID != "" {
			a.supervisor, err = supervisor.DefaultRegistry().Resolve(supervisorID)
			if err != nil {
				return err
			}
		}
		a.supervisorModel = supervisorModel
		a.developerModel = developerModel
		return a.start(ctx, path, mode)
	}
}

const startUsage = "usage: herdr-tandem [--expanded] [--supervisor codex|opencode|agy] [--supervisor-model <model>] [--developer-model <model>] [DIRECTORY]"

func parseStartArgs(args []string) (string, sidebarMode, string, string, string, error) {
	path := "."
	pathSet := false
	mode := sidebarModeCompact
	expandedSet := false
	supervisorSet := false
	supervisorID := ""
	supervisorModelSet := false
	supervisorModel := ""
	developerModelSet := false
	developerModel := ""

	for i := 0; i < len(args); i++ {
		arg := args[i]
		switch {
		case arg == "--expanded":
			if expandedSet {
				return "", "", "", "", "", fmt.Errorf("%s", startUsage)
			}
			expandedSet = true
			mode = sidebarModeExpanded
		case arg == "--supervisor":
			if supervisorSet || i+1 >= len(args) || strings.HasPrefix(args[i+1], "-") {
				return "", "", "", "", "", fmt.Errorf("%s", startUsage)
			}
			supervisorSet = true
			i++
			supervisorID = args[i]
		case strings.HasPrefix(arg, "--supervisor="):
			if supervisorSet {
				return "", "", "", "", "", fmt.Errorf("%s", startUsage)
			}
			supervisorSet = true
			supervisorID = strings.TrimPrefix(arg, "--supervisor=")
			if supervisorID == "" {
				return "", "", "", "", "", fmt.Errorf("%s", startUsage)
			}
		case arg == "--supervisor-model":
			if supervisorModelSet || i+1 >= len(args) || strings.HasPrefix(args[i+1], "-") {
				return "", "", "", "", "", fmt.Errorf("%s", startUsage)
			}
			supervisorModelSet = true
			i++
			supervisorModel = args[i]
		case strings.HasPrefix(arg, "--supervisor-model="):
			if supervisorModelSet {
				return "", "", "", "", "", fmt.Errorf("%s", startUsage)
			}
			supervisorModelSet = true
			supervisorModel = strings.TrimPrefix(arg, "--supervisor-model=")
		case arg == "--developer-model":
			if developerModelSet || i+1 >= len(args) || strings.HasPrefix(args[i+1], "-") {
				return "", "", "", "", "", fmt.Errorf("%s", startUsage)
			}
			developerModelSet = true
			i++
			developerModel = args[i]
		case strings.HasPrefix(arg, "--developer-model="):
			if developerModelSet {
				return "", "", "", "", "", fmt.Errorf("%s", startUsage)
			}
			developerModelSet = true
			developerModel = strings.TrimPrefix(arg, "--developer-model=")
		default:
			if strings.HasPrefix(arg, "-") || pathSet {
				return "", "", "", "", "", fmt.Errorf("%s", startUsage)
			}
			path = arg
			pathSet = true
		}
	}

	if supervisorModelSet {
		if strings.TrimSpace(supervisorModel) == "" {
			return "", "", "", "", "", fmt.Errorf("%s", startUsage)
		}
		if err := validateModelName(supervisorModel); err != nil {
			return "", "", "", "", "", fmt.Errorf("%s: %w", startUsage, err)
		}
		effectiveSupervisor := supervisorID
		if effectiveSupervisor == "" {
			effectiveSupervisor = supervisor.CodexID
		}
		if effectiveSupervisor != supervisor.AgyID {
			return "", "", "", "", "", fmt.Errorf("--supervisor-model is only supported with %s supervisor", supervisor.AgyID)
		}
	} else if supervisorID == supervisor.AgyID {
		supervisorModel = DefaultAgySupervisorModel
	}

	if developerModelSet {
		if strings.TrimSpace(developerModel) == "" {
			return "", "", "", "", "", fmt.Errorf("%s", startUsage)
		}
		if err := validateModelName(developerModel); err != nil {
			return "", "", "", "", "", fmt.Errorf("%s: %w", startUsage, err)
		}
	} else {
		developerModel = DefaultAgyDeveloperModel
	}

	return path, mode, supervisorID, supervisorModel, developerModel, nil
}

func supervisorIDFromArgs(args []string) (string, error) {
	if len(args) == 0 {
		return "", nil
	}
	if len(args) == 1 && strings.HasPrefix(args[0], "--supervisor=") {
		id := strings.TrimPrefix(args[0], "--supervisor=")
		if id == "" {
			return "", fmt.Errorf("usage: herdr-tandem doctor [--supervisor codex|opencode|agy]")
		}
		return id, nil
	}
	if len(args) == 2 && args[0] == "--supervisor" && strings.TrimSpace(args[1]) != "" {
		if strings.HasPrefix(args[1], "-") {
			return "", fmt.Errorf("usage: herdr-tandem doctor [--supervisor codex|opencode|agy]")
		}
		return args[1], nil
	}
	return "", fmt.Errorf("usage: herdr-tandem doctor [--supervisor codex|opencode|agy]")
}

func (a *App) printHelp() {
	fmt.Fprintln(a.stdout, `herdr-tandem - visible supervisor and agy developer in Herdr

Usage:
  herdr-tandem [--expanded] [--supervisor codex|opencode|agy] [--supervisor-model <model>] [--developer-model <model>] [DIRECTORY]
  herdr-tandem doctor [--supervisor codex|opencode|agy]
  herdr-tandem stop

Shorthand alias:
  hdt (optional shorthand for herdr-tandem)

Emergency fallback command:
  herdr-tandem ask --stdin

Note: The selected supervisor interacts with agy via native MCP tools (delegate_task,
task_status, recover_task, acknowledge_task). Already-running supervisor
sessions must be restarted (herdr-tandem stop; herdr-tandem) to receive the per-invocation bridge.`)
}

func (a *App) printAskHelp() {
	fmt.Fprintln(a.stdout, `herdr-tandem ask - emergency task command for the visible agy developer

Usage:
  herdr-tandem ask --stdin
  herdr-tandem ask "<development task>"
  herdr-tandem ask --recover
  herdr-tandem ask --forget`)
}

const maxTaskInputBytes = 1 << 20

func readTaskInput(reader io.Reader) (string, error) {
	if reader == nil {
		return "", fmt.Errorf("herdr-tandem ask --stdin has no input stream")
	}
	data, err := io.ReadAll(io.LimitReader(reader, maxTaskInputBytes+1))
	if err != nil {
		return "", fmt.Errorf("read task from stdin: %w", err)
	}
	if len(data) > maxTaskInputBytes {
		return "", fmt.Errorf("task from stdin exceeds 1 MiB")
	}
	task := strings.TrimSpace(string(data))
	if task == "" {
		return "", fmt.Errorf("task from stdin is empty")
	}
	return task, nil
}

func validateTaskString(task string) error {
	if len(task) > maxTaskInputBytes {
		return fmt.Errorf("task exceeds 1 MiB")
	}
	if strings.TrimSpace(task) == "" {
		return fmt.Errorf("task cannot be empty")
	}
	return nil
}

func resolveProject(path string) (string, error) {
	absolute, err := filepath.Abs(path)
	if err != nil {
		return "", fmt.Errorf("resolve project: %w", err)
	}
	resolved, err := filepath.EvalSymlinks(absolute)
	if err != nil {
		return "", fmt.Errorf("project directory does not exist: %s", absolute)
	}
	info, err := os.Stat(resolved)
	if err != nil {
		return "", fmt.Errorf("read project directory: %w", err)
	}
	if !info.IsDir() {
		return "", fmt.Errorf("project path is not a directory: %s", resolved)
	}
	return filepath.Clean(resolved), nil
}
