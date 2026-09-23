package app

import (
	"bytes"
	"context"
	"errors"
	"io"
	"strings"
	"sync"
	"testing"
	"time"

	proc "github.com/kazimshah39/herdr-tandem/internal/process"
)

type blockingModelsRunner struct {
	mu      sync.Mutex
	calls   int
	started chan struct{}
	once    sync.Once
}

func (r *blockingModelsRunner) LookPath(name string) (string, error) { return "/bin/" + name, nil }
func (r *blockingModelsRunner) Run(ctx context.Context, args ...string) (proc.Result, error) {
	r.mu.Lock()
	r.calls++
	r.mu.Unlock()
	r.once.Do(func() { close(r.started) })
	<-ctx.Done()
	return proc.Result{ExitCode: -1}, ctx.Err()
}
func (r *blockingModelsRunner) RunAttached(string, []string, []string) error { return nil }
func (r *blockingModelsRunner) count() int {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.calls
}

func TestAgyModelsPreflightHasDedicatedTimeoutAndVisibleProgress(t *testing.T) {
	runner := &blockingModelsRunner{started: make(chan struct{})}
	var stderr bytes.Buffer
	var diagnostics strings.Builder
	app := New(runner, io.Discard, &stderr)
	app.agyModelsTimeout = 20 * time.Millisecond
	app.diagnosticSink = func(line string) { diagnostics.WriteString(line + "\n") }

	started := time.Now()
	_, err := app.loadAgyModels(context.Background())
	if err == nil || !strings.Contains(err.Error(), "timed out after") || !strings.Contains(err.Error(), "run 'agy models' directly") {
		t.Fatalf("timeout error=%v", err)
	}
	if elapsed := time.Since(started); elapsed > 500*time.Millisecond {
		t.Fatalf("model preflight was not bounded: %s", elapsed)
	}
	if !strings.Contains(stderr.String(), "checking available agy models") || !strings.Contains(stderr.String(), "timeout 0s") {
		t.Fatalf("missing visible progress: %q", stderr.String())
	}
	logged := diagnostics.String()
	if !strings.Contains(logged, "HTD-MDL-001") || !strings.Contains(logged, "HTD-MDL-003") {
		t.Fatalf("missing model diagnostic codes: %q", logged)
	}

	_, cachedErr := app.loadAgyModels(context.Background())
	if cachedErr == nil || runner.count() != 1 {
		t.Fatalf("cached call err=%v count=%d", cachedErr, runner.count())
	}
}

func TestAgyModelsPreflightPreservesParentCancellation(t *testing.T) {
	runner := &blockingModelsRunner{started: make(chan struct{})}
	app := New(runner, io.Discard, io.Discard)
	app.agyModelsTimeout = time.Second
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() {
		_, err := app.loadAgyModels(ctx)
		done <- err
	}()
	<-runner.started
	cancel()
	select {
	case err := <-done:
		if !errors.Is(err, context.Canceled) {
			t.Fatalf("error=%v, want context.Canceled", err)
		}
	case <-time.After(time.Second):
		t.Fatal("parent cancellation did not stop model preflight")
	}
}

type failingModelsRunner struct {
	result proc.Result
	err    error
}

func (r failingModelsRunner) LookPath(name string) (string, error) { return "/bin/" + name, nil }
func (r failingModelsRunner) Run(context.Context, ...string) (proc.Result, error) {
	return r.result, r.err
}
func (r failingModelsRunner) RunAttached(string, []string, []string) error { return nil }

func TestAgyModelsFailureDoesNotLeakCommandOutput(t *testing.T) {
	const secret = "provider-secret-model-output"
	var diagnostics strings.Builder
	app := New(failingModelsRunner{result: proc.Result{ExitCode: 7, Stderr: secret}}, io.Discard, io.Discard)
	app.diagnosticSink = func(line string) { diagnostics.WriteString(line + "\n") }

	_, err := app.loadAgyModels(context.Background())
	if err == nil || !strings.Contains(err.Error(), "status 7") {
		t.Fatalf("error=%v", err)
	}
	if strings.Contains(err.Error(), secret) || strings.Contains(diagnostics.String(), secret) {
		t.Fatalf("model command output leaked: error=%q logs=%q", err, diagnostics.String())
	}
	if !strings.Contains(diagnostics.String(), "HTD-MDL-004") || !strings.Contains(diagnostics.String(), "stderr_bytes=") {
		t.Fatalf("safe failure diagnostics missing: %q", diagnostics.String())
	}
}
