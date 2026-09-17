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

	supervisorDisplaySource       = "cagy:supervisor-display"
	developerDisplaySource        = "cagy:developer-display"
	compactSupervisorDisplayName  = "cagy"
	expandedSupervisorDisplayName = "cagy Supervisor"
	developerDisplayName          = "cagy Developer"
)

const supervisorPrompt = `You are the Codex supervisor. The visible agy agent in the right Herdr pane is the developer.
Delegate implementation work by running: cagy ask "<clear development task>".
Do not edit the same files while agy is working. After agy finishes, inspect the changes, review correctness and security, and run relevant tests. Send corrections through another cagy ask when needed. Use current official web documentation for dependencies and external APIs. Give the final result to the user in clear, simple words.`

// App owns command parsing and the fixed cagy workflow.
type App struct {
	runner           proc.Runner
	herdr            *herdr.Client
	stdout           io.Writer
	stderr           io.Writer
	getenv           func(string) string
	environ          func() []string
	tempDir          string
	token            func() (string, error)
	now              func() time.Time
	agyBrainRoot     string
	transcriptWait   time.Duration
	developerPoll    time.Duration
	configureSidebar func(context.Context, bool) error
}

func New(runner proc.Runner, stdout, stderr io.Writer) *App {
	herdrClient := herdr.New(runner)
	application := &App{
		runner:         runner,
		herdr:          herdrClient,
		stdout:         stdout,
		stderr:         stderr,
		getenv:         os.Getenv,
		environ:        os.Environ,
		tempDir:        os.TempDir(),
		token:          randomToken,
		now:            time.Now,
		agyBrainRoot:   defaultAgyBrainRoot(),
		transcriptWait: 3 * time.Second,
		developerPoll:  time.Second,
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
		if len(args) == 2 && (args[1] == "-h" || args[1] == "--help") {
			a.printAskHelp()
			return nil
		}
		if len(args) < 2 || strings.TrimSpace(strings.Join(args[1:], " ")) == "" {
			return fmt.Errorf("usage: cagy ask \"<development task>\"")
		}
		return a.ask(ctx, strings.Join(args[1:], " "))
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

Internal supervisor command:
  cagy ask "<development task>"`)
}

func (a *App) printAskHelp() {
	fmt.Fprintln(a.stdout, `cagy ask - send one task to the visible agy developer

Usage:
  cagy ask "<development task>"`)
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

func codexArgs(project string) []string {
	return []string{
		"codex",
		"--yolo",
		"--dangerously-bypass-hook-trust",
		"--search",
		"-c",
		codexProjectTrustOverride(project),
		"-c",
		codexDeveloperInstructionsOverride(),
		"-C",
		project,
	}
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
