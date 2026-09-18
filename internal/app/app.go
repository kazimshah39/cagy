package app

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/kazimshah39/cagy/internal/herdr"
	proc "github.com/kazimshah39/cagy/internal/process"
)

const (
	developerPromptTimeoutMS = 30 * 60 * 1000
	commandTimeoutMS         = 5 * 60 * 1000
	agyReadyTimeoutMS        = 60 * 1000
	maxRecoveryAttempts      = 2

	paneOwnershipSource           = "cagy:pane-owner"
	supervisorDisplaySource       = "cagy:supervisor-display"
	developerDisplaySource        = "cagy:developer-display"
	compactSupervisorDisplayName  = "cagy"
	expandedSupervisorDisplayName = "cagy Supervisor"
	developerDisplayName          = "cagy Developer"
)

const supervisorPrompt = `You are the Codex supervisor. The visible agy agent in the right Herdr pane is the developer.
Delegate implementation work using the native cagy MCP tools. Follow this exact workflow:
1. Inspect task state with task_status before starting new work.
2. Delegate the task with delegate_task(task="..."). Task text is sent literally without shell interpolation.
3. Review the developer's changes, test execution, correctness, and security.
4. Call acknowledge_task(receipt="...") only after the result is received and in context.
5. If a session or tool call is interrupted, use recover_task to retrieve the completed answer without resubmitting.
Shell CLI commands (such as cagy ask --stdin) are for emergency and manual compatibility only; always prefer the native MCP tools. Do not edit the same files while agy is working. Use current official web documentation for dependencies and external APIs. Give the final result to the user in clear, simple words.`

// App owns command parsing and the fixed cagy workflow.
type App struct {
	runner                proc.Runner
	herdr                 *herdr.Client
	stdin                 io.Reader
	stdout                io.Writer
	stderr                io.Writer
	getenv                func(string) string
	environ               func() []string
	stateDir              string
	activeTask            *taskJournal
	token                 func() (string, error)
	now                   func() time.Time
	agyBrainRoot          string
	transcriptWait        time.Duration
	missingTranscriptWait time.Duration
	developerPoll         time.Duration
	agentStopTimeout      time.Duration
	configureSidebar      func(context.Context, bool) error
	resolveExecutable     func() (string, error)
}

func New(runner proc.Runner, stdout, stderr io.Writer) *App {
	herdrClient := herdr.New(runner)
	application := &App{
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
		agyBrainRoot:          defaultAgyBrainRoot(),
		transcriptWait:        3 * time.Second,
		missingTranscriptWait: 30 * time.Second,
		developerPoll:         time.Second,
		agentStopTimeout:      10 * time.Second,
	}
	application.resolveExecutable = func() (string, error) {
		return resolveExecutable(os.Executable)
	}
	application.configureSidebar = func(ctx context.Context, showAgents bool) error {
		if showAgents {
			return herdrClient.ClearCagySidebarView(ctx)
		}
		return herdrClient.SetCagySidebarCompact(ctx)
	}
	return application
}

func (a *App) Run(ctx context.Context, args []string) error {
	if len(args) == 0 {
		return a.start(ctx, ".", false)
	}

	switch args[0] {
	case "help", "-h", "--help":
		a.printHelp()
		return nil
	case "doctor":
		if len(args) != 1 {
			return fmt.Errorf("usage: cagy doctor")
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
			return fmt.Errorf("usage: cagy ask --stdin | cagy ask \"<development task>\" | cagy ask --recover | cagy ask --forget")
		}
		if strings.HasPrefix(args[1], "--") {
			return fmt.Errorf("unknown cagy ask option: %s", args[1])
		}
		return a.ask(ctx, strings.Join(args[1:], " "))
	case "mcp-server":
		if len(args) != 1 {
			return fmt.Errorf("usage: cagy mcp-server")
		}
		return a.serveMCP(ctx)
	case "stop":
		if len(args) != 1 {
			return fmt.Errorf("usage: cagy stop")
		}
		return a.stop(ctx)
	default:
		path, showAgents, err := parseStartArgs(args)
		if err != nil {
			return err
		}
		return a.start(ctx, path, showAgents)
	}
}

func parseStartArgs(args []string) (string, bool, error) {
	path := "."
	pathSet := false
	showAgents := false
	for _, arg := range args {
		switch arg {
		case "--show-agents":
			showAgents = true
		default:
			if strings.HasPrefix(arg, "-") || pathSet {
				return "", false, fmt.Errorf("usage: cagy [--show-agents] [DIRECTORY]")
			}
			path = arg
			pathSet = true
		}
	}
	return path, showAgents, nil
}

func (a *App) printHelp() {
	fmt.Fprintln(a.stdout, `cagy - visible Codex supervisor and agy developer in Herdr

Usage:
  cagy [DIRECTORY]
  cagy --show-agents [DIRECTORY]
  cagy doctor
  cagy stop

Compatibility and emergency fallback command:
  cagy ask --stdin

Note: Codex supervisor interacts with agy via native MCP tools (delegate_task,
task_status, recover_task, acknowledge_task). Already-running Codex supervisor
sessions must be restarted (cagy stop; cagy) to receive the per-invocation bridge.`)
}

func (a *App) printAskHelp() {
	fmt.Fprintln(a.stdout, `cagy ask - send one task to the visible agy developer (compatibility and emergency fallback)

Usage:
  cagy ask --stdin
  cagy ask "<development task>"
  cagy ask --recover
  cagy ask --forget`)
}

const maxTaskInputBytes = 1 << 20

func readTaskInput(reader io.Reader) (string, error) {
	if reader == nil {
		return "", fmt.Errorf("cagy ask --stdin has no input stream")
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

func codexArgs(project string, mcpOverride ...string) []string {
	args := []string{
		"codex",
		"--yolo",
		"--dangerously-bypass-hook-trust",
		"--search",
		"-c",
		codexProjectTrustOverride(project),
		"-c",
		codexDeveloperInstructionsOverride(),
	}
	if len(mcpOverride) > 0 && mcpOverride[0] != "" {
		args = append(args, "-c", mcpOverride[0])
	}
	args = append(args, "-C", project)
	return args
}

// codexProjectTrustOverride avoids Codex's first-run directory prompt for this
// invocation only. JSON string syntax is valid TOML basic-string syntax and
// safely preserves spaces, quotes, backslashes, Unicode, and dots in paths.
func codexProjectTrustOverride(project string) string {
	encoded, _ := json.Marshal(project)
	return `projects={` + string(encoded) + `={trust_level="trusted"}}`
}

func codexDeveloperInstructionsOverride() string {
	encoded, _ := json.Marshal(supervisorPrompt)
	return "developer_instructions=" + string(encoded)
}
