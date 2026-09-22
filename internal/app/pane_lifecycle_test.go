package app

import (
	"context"
	"fmt"
	"io"
	"strconv"
	"strings"
	"testing"
	"time"

	proc "github.com/kazimshah39/herdr-tandem/internal/process"
)

type paneLifecycleRunner struct {
	fn    func(args []string) (proc.Result, error)
	calls [][]string
}

func (r *paneLifecycleRunner) LookPath(name string) (string, error) { return "/bin/" + name, nil }
func (r *paneLifecycleRunner) Run(_ context.Context, args ...string) (proc.Result, error) {
	r.calls = append(r.calls, append([]string(nil), args...))
	if r.fn != nil {
		return r.fn(args)
	}
	return proc.Result{}, fmt.Errorf("unexpected call: %s", strings.Join(args, " "))
}
func (r *paneLifecycleRunner) RunAttached(string, []string, []string) error { return nil }

func TestEnsureDeveloperReadyReturnsImmediatelyWhenAlreadyReady(t *testing.T) {
	runner := &paneLifecycleRunner{
		fn: func(args []string) (proc.Result, error) {
			joined := strings.Join(args, " ")
			if strings.HasPrefix(joined, "herdr pane read ") {
				return proc.Result{ExitCode: 0, Stdout: "welcome to agy\n? for shortcuts\n"}, nil
			}
			return proc.Result{}, fmt.Errorf("unexpected call: %s", joined)
		},
	}
	app := New(runner, io.Discard, io.Discard)
	if err := app.ensureDeveloperReady(context.Background(), "w1:p2"); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(runner.calls) != 1 {
		t.Fatalf("calls=%#v, want exactly 1 read call", runner.calls)
	}
}

func TestEnsureDeveloperReadyAcceptsTrustPromptThenMatchesReady(t *testing.T) {
	runner := &paneLifecycleRunner{
		fn: func(args []string) (proc.Result, error) {
			joined := strings.Join(args, " ")
			switch {
			case strings.HasPrefix(joined, "herdr pane read "):
				return proc.Result{ExitCode: 0, Stdout: "Do you trust the contents of this project? [Y/n]"}, nil
			case strings.HasPrefix(joined, "herdr pane send-keys "):
				return proc.Result{ExitCode: 0, Stdout: `{"id":"x","result":{"type":"ok"}}`}, nil
			case strings.HasPrefix(joined, "herdr pane wait-output "):
				return proc.Result{ExitCode: 0, Stdout: `{"id":"x","result":{"type":"ok"}}`}, nil
			default:
				return proc.Result{}, fmt.Errorf("unexpected call: %s", joined)
			}
		},
	}
	app := New(runner, io.Discard, io.Discard)
	if err := app.ensureDeveloperReady(context.Background(), "w1:p2"); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	sentEnter := false
	for _, call := range runner.calls {
		if len(call) >= 4 && call[0] == "herdr" && call[1] == "pane" && call[2] == "send-keys" && call[len(call)-1] == "enter" {
			sentEnter = true
		}
	}
	if !sentEnter {
		t.Fatalf("expected send-keys enter in calls: %#v", runner.calls)
	}
}

func TestEnsureDeveloperReadyPassesRealTimeoutNotOneMillisecond(t *testing.T) {
	var waitOutputTimeout string
	runner := &paneLifecycleRunner{
		fn: func(args []string) (proc.Result, error) {
			joined := strings.Join(args, " ")
			switch {
			case strings.HasPrefix(joined, "herdr pane read "):
				return proc.Result{ExitCode: 0, Stdout: "starting agy..."}, nil
			case strings.HasPrefix(joined, "herdr pane wait-output "):
				for i, arg := range args {
					if arg == "--timeout" && i+1 < len(args) {
						waitOutputTimeout = args[i+1]
					}
				}
				return proc.Result{ExitCode: 0, Stdout: `{"id":"x","result":{"type":"ok"}}`}, nil
			default:
				return proc.Result{}, fmt.Errorf("unexpected call: %s", joined)
			}
		},
	}
	app := New(runner, io.Discard, io.Discard)
	if err := app.ensureDeveloperReady(context.Background(), "w1:p2"); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if waitOutputTimeout == "" {
		t.Fatal("expected wait-output call with --timeout")
	}
	timeoutVal, err := strconv.Atoi(waitOutputTimeout)
	if err != nil {
		t.Fatalf("invalid timeout value %q: %v", waitOutputTimeout, err)
	}
	if timeoutVal < 1000 {
		t.Fatalf("wait-output timeout was %d ms (expected >= 1000 ms, never 1 ms)", timeoutVal)
	}
}

func TestEnsureDeveloperReadyHandlesDelayedTrustPrompt(t *testing.T) {
	readCount := 0
	runner := &paneLifecycleRunner{
		fn: func(args []string) (proc.Result, error) {
			joined := strings.Join(args, " ")
			switch {
			case strings.HasPrefix(joined, "herdr pane read "):
				readCount++
				if readCount == 1 {
					return proc.Result{ExitCode: 0, Stdout: "initializing..."}, nil
				}
				return proc.Result{ExitCode: 0, Stdout: "Do you trust the contents of this project? [Y/n]"}, nil
			case strings.HasPrefix(joined, "herdr pane wait-output "):
				if readCount == 1 {
					return proc.Result{ExitCode: 1, Stderr: `{"error":{"code":"timeout","message":"timed out"}}`}, nil
				}
				return proc.Result{ExitCode: 0, Stdout: `{"id":"x","result":{"type":"ok"}}`}, nil
			case strings.HasPrefix(joined, "herdr pane send-keys "):
				return proc.Result{ExitCode: 0, Stdout: `{"id":"x","result":{"type":"ok"}}`}, nil
			default:
				return proc.Result{}, fmt.Errorf("unexpected call: %s", joined)
			}
		},
	}
	app := New(runner, io.Discard, io.Discard)
	if err := app.ensureDeveloperReady(context.Background(), "w1:p2"); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if readCount < 2 {
		t.Fatalf("expected at least 2 read calls for delayed trust prompt, got %d", readCount)
	}
}

func TestEnsureDeveloperReadyTimesOutAfterConfiguredTimeout(t *testing.T) {
	runner := &paneLifecycleRunner{
		fn: func(args []string) (proc.Result, error) {
			joined := strings.Join(args, " ")
			switch {
			case strings.HasPrefix(joined, "herdr pane read "):
				return proc.Result{ExitCode: 0, Stdout: "something else"}, nil
			case strings.HasPrefix(joined, "herdr pane wait-output "):
				return proc.Result{ExitCode: 1, Stderr: `{"error":{"code":"timeout","message":"timed out waiting for output match"}}`}, nil
			default:
				return proc.Result{}, fmt.Errorf("unexpected call: %s", joined)
			}
		},
	}
	app := New(runner, io.Discard, io.Discard)
	app.developerReadyTimeout = 10 * time.Millisecond
	err := app.ensureDeveloperReady(context.Background(), "w1:p2")
	if err == nil {
		t.Fatal("expected timeout error")
	}
	if !strings.Contains(err.Error(), "timed out waiting for output match") {
		t.Fatalf("unexpected error: %v", err)
	}
}
