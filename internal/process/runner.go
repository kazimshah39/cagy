package process

import (
	"bytes"
	"context"
	"errors"
	"os"
	"os/exec"
)

// Result is the captured result of one non-interactive process.
type Result struct {
	Args     []string
	ExitCode int
	Stdout   string
	Stderr   string
}

// Runner is the process boundary used by herdr-tandem. Tests replace it with a fake.
type Runner interface {
	LookPath(name string) (string, error)
	Run(ctx context.Context, args ...string) (Result, error)
	RunAttached(dir string, args []string, env []string) error
}

// OSRunner executes real local processes.
type OSRunner struct{}

func (OSRunner) LookPath(name string) (string, error) {
	return exec.LookPath(name)
}

func (OSRunner) Run(ctx context.Context, args ...string) (Result, error) {
	result := Result{Args: append([]string(nil), args...), ExitCode: -1}
	if len(args) == 0 {
		return result, errors.New("empty command")
	}

	// #nosec G204 -- callers choose a fixed executable; user text is passed only as argv.
	cmd := exec.CommandContext(ctx, args[0], args[1:]...)
	var stdout, stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr
	err := cmd.Run()
	result.Stdout = stdout.String()
	result.Stderr = stderr.String()

	if cmd.ProcessState != nil {
		result.ExitCode = cmd.ProcessState.ExitCode()
	}
	if err != nil {
		var exitErr *exec.ExitError
		if errors.As(err, &exitErr) {
			return result, nil
		}
		return result, err
	}
	return result, nil
}

func (OSRunner) RunAttached(dir string, args []string, env []string) error {
	if len(args) == 0 {
		return errors.New("empty command")
	}
	// #nosec G204 -- callers choose a fixed supervisor executable and pass no shell string.
	cmd := exec.Command(args[0], args[1:]...)
	if dir != "" {
		cmd.Dir = dir
	}
	cmd.Stdin = os.Stdin
	cmd.Stdout = os.Stdout
	cmd.Stderr = os.Stderr
	cmd.Env = env
	return cmd.Run()
}
