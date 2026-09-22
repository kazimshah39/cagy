package app

import (
	"context"
	"errors"
	"strings"
	"testing"

	proc "github.com/kazimshah39/herdr-tandem/internal/process"
)

type fakeRunner struct{}

func (fakeRunner) LookPath(name string) (string, error) { return "/bin/" + name, nil }
func (fakeRunner) Run(_ context.Context, args ...string) (proc.Result, error) {
	return proc.Result{}, errors.New("unexpected process call: " + strings.Join(args, " "))
}
func (fakeRunner) RunAttached(string, []string, []string) error { return nil }

type scriptedRunner struct {
	t     *testing.T
	calls [][]string
}

func (r *scriptedRunner) LookPath(name string) (string, error) { return "/bin/" + name, nil }
func (r *scriptedRunner) Run(_ context.Context, args ...string) (proc.Result, error) {
	r.calls = append(r.calls, append([]string(nil), args...))
	return proc.Result{}, errors.New("unexpected process call: " + strings.Join(args, " "))
}
func (r *scriptedRunner) RunAttached(dir string, args []string, env []string) error {
	r.calls = append(r.calls, append([]string(nil), args...))
	return nil
}
func (r *scriptedRunner) assertDone() {}

func contains(values []string, target string) bool {
	for _, value := range values {
		if value == target {
			return true
		}
	}
	return false
}
