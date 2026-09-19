package app

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	proc "github.com/kazimshah39/cagy/internal/process"
)

type quotaProbeRunner struct{ results []proc.Result }

func (r *quotaProbeRunner) LookPath(string) (string, error) { return "/bin/agy", nil }
func (r *quotaProbeRunner) Run(_ context.Context, _ ...string) (proc.Result, error) {
	if len(r.results) == 0 {
		return proc.Result{}, errors.New("unexpected probe")
	}
	result := r.results[0]
	r.results = r.results[1:]
	return result, nil
}
func (r *quotaProbeRunner) RunAttached([]string, []string) error { return errors.New("unexpected") }

func TestQuotaProbeClassifiesAvailableLowAndExhausted(t *testing.T) {
	cases := []struct {
		weekly float64
		five   float64
		want   quotaClassification
	}{
		{weekly: .8, five: .9, want: quotaAvailable},
		{weekly: .03, five: .9, want: quotaLow},
		{weekly: 0, five: .9, want: quotaExhausted},
	}
	for _, tc := range cases {
		runner := &quotaProbeRunner{results: []proc.Result{
			agyModelResult("gemini-3", "Gemini"),
			agyQuotaResult("Gemini Models", tc.weekly, tc.five),
		}}
		app := New(runner, &strings.Builder{}, &strings.Builder{})
		app.now = func() time.Time { return time.Unix(100, 0) }
		got := app.probeAgyQuota(context.Background())
		if got.Class != tc.want {
			t.Fatalf("weekly=%v five=%v got=%s want=%s reason=%s", tc.weekly, tc.five, got.Class, tc.want, got.Reason)
		}
	}
}

func TestQuotaProbeStrongFailingCommandIsExhausted(t *testing.T) {
	runner := &quotaProbeRunner{results: []proc.Result{{ExitCode: 1, Stderr: "Error: RESOURCE_EXHAUSTED: quota exceeded"}}}
	app := New(runner, &strings.Builder{}, &strings.Builder{})
	got := app.probeAgyQuota(context.Background())
	if got.Class != quotaExhausted {
		t.Fatalf("got=%s reason=%s", got.Class, got.Reason)
	}
}

func TestQuotaProbeGenericFailureAndMalformedJSONAreUnknown(t *testing.T) {
	for _, result := range []proc.Result{{ExitCode: 1, Stderr: "network unavailable"}, {ExitCode: 0, Stdout: "not json"}} {
		app := New(&quotaProbeRunner{results: []proc.Result{result}}, &strings.Builder{}, &strings.Builder{})
		if got := app.probeAgyQuota(context.Background()); got.Class != quotaUnknown {
			t.Fatalf("result=%#v got=%s", result, got.Class)
		}
	}
}

func TestQuotaRecoveryContinuationDoesNotDuplicateOriginalTask(t *testing.T) {
	original := "unique original task text"
	continuation := continuationPrompt(original)
	if strings.Contains(continuation, original) {
		t.Fatalf("continuation duplicated original task: %q", continuation)
	}
}
