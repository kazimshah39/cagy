package app

import (
	"context"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"
)

func TestAGMRefreshDueUsesOneHourWindow(t *testing.T) {
	now := time.Date(2026, time.September, 17, 12, 0, 0, 0, time.UTC)
	tests := []struct {
		name    string
		state   string
		missing bool
		want    bool
	}{
		{name: "missing", missing: true, want: true},
		{name: "recent", state: now.Add(-59 * time.Minute).Format(time.RFC3339Nano), want: false},
		{name: "exactly one hour", state: now.Add(-time.Hour).Format(time.RFC3339Nano), want: true},
		{name: "older", state: now.Add(-2 * time.Hour).Format(time.RFC3339Nano), want: true},
		{name: "future", state: now.Add(time.Minute).Format(time.RFC3339Nano), want: true},
		{name: "corrupt", state: "not-a-time", want: true},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			application := New(&fakeRunner{}, &strings.Builder{}, &strings.Builder{})
			application.tempDir = t.TempDir()
			application.now = func() time.Time { return now }
			if !test.missing {
				path := filepath.Join(application.tempDir, agmRefreshStateName)
				if err := os.WriteFile(path, []byte(test.state+"\n"), 0o600); err != nil {
					t.Fatal(err)
				}
			}
			if got := application.agmRefreshDue(); got != test.want {
				t.Fatalf("agmRefreshDue()=%v want=%v", got, test.want)
			}
		})
	}
}

func TestMaybeRefreshAllRecordsConfirmedPartialSuccess(t *testing.T) {
	now := time.Date(2026, time.September, 17, 12, 0, 0, 0, time.UTC)
	marker := "__CAGY_REFRESH_token__"
	runner := &scriptedRunner{t: t, steps: []runStep{
		{want: []string{"herdr", "pane", "run", "w1:p2", markedCommand("agm refresh-all", marker)}, result: jsonResult(`{"type":"pane_info"}`)},
		{want: []string{"herdr", "pane", "wait-output", "w1:p2", "--regex", completionPattern(marker), "--source", "recent-unwrapped", "--lines", "400", "--timeout", "1800000"}, result: jsonResult(`{"type":"output_matched"}`)},
		{want: []string{"herdr", "pane", "read", "w1:p2", "--source", "recent-unwrapped", "--lines", "400"}, result: textResult("Completed: 2 successful, 1 failed\n" + marker + ":0\n")},
	}}
	application := New(runner, &strings.Builder{}, &strings.Builder{})
	application.tempDir = t.TempDir()
	application.now = func() time.Time { return now }
	application.token = func() (string, error) { return "token", nil }

	attempted, output, err := application.maybeRefreshAll(context.Background(), "w1:p2")
	if err != nil {
		t.Fatal(err)
	}
	if !attempted || !strings.Contains(output, "2 successful, 1 failed") {
		t.Fatalf("attempted=%v output=%q", attempted, output)
	}
	if application.agmRefreshDue() {
		t.Fatal("successful refresh was not recorded")
	}
	runner.assertDone()
}

func TestMaybeRefreshAllRejectsUnconfirmedRefresh(t *testing.T) {
	tests := []struct {
		name   string
		output string
		status int
	}{
		{name: "missing summary", output: "refresh finished", status: 0},
		{name: "zero successes", output: "Completed: 0 successful, 3 failed", status: 0},
		{name: "nonzero status", output: "Completed: 2 successful", status: 1},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			marker := "__CAGY_REFRESH_token__"
			runner := &scriptedRunner{t: t, steps: []runStep{
				{want: []string{"herdr", "pane", "run", "w1:p2", markedCommand("agm refresh-all", marker)}, result: jsonResult(`{"type":"pane_info"}`)},
				{want: []string{"herdr", "pane", "wait-output", "w1:p2", "--regex", completionPattern(marker), "--source", "recent-unwrapped", "--lines", "400", "--timeout", "1800000"}, result: jsonResult(`{"type":"output_matched"}`)},
				{want: []string{"herdr", "pane", "read", "w1:p2", "--source", "recent-unwrapped", "--lines", "400"}, result: textResult(test.output + "\n" + marker + ":" + strconv.Itoa(test.status) + "\n")},
			}}
			application := New(runner, &strings.Builder{}, &strings.Builder{})
			application.tempDir = t.TempDir()
			application.token = func() (string, error) { return "token", nil }

			attempted, _, err := application.maybeRefreshAll(context.Background(), "w1:p2")
			if !attempted || err == nil {
				t.Fatalf("attempted=%v err=%v", attempted, err)
			}
			if _, statErr := os.Stat(filepath.Join(application.tempDir, agmRefreshStateName)); !os.IsNotExist(statErr) {
				t.Fatalf("failed refresh recorded state: %v", statErr)
			}
			runner.assertDone()
		})
	}
}

func TestMaybeRefreshAllSkipsRecentRefresh(t *testing.T) {
	now := time.Date(2026, time.September, 17, 12, 0, 0, 0, time.UTC)
	runner := &scriptedRunner{t: t}
	application := New(runner, &strings.Builder{}, &strings.Builder{})
	application.tempDir = t.TempDir()
	application.now = func() time.Time { return now }
	if err := application.recordAGMRefresh(now.Add(-30 * time.Minute)); err != nil {
		t.Fatal(err)
	}

	attempted, output, err := application.maybeRefreshAll(context.Background(), "w1:p2")
	if err != nil || attempted || output != "" {
		t.Fatalf("attempted=%v output=%q err=%v", attempted, output, err)
	}
	runner.assertDone()
}

func TestAGMRefreshSummary(t *testing.T) {
	successful, failed, ok := agmRefreshSummary("Completed: 12 successful, 3 failed")
	if !ok || successful != 12 || failed != 3 {
		t.Fatalf("successful=%d failed=%d ok=%v", successful, failed, ok)
	}
}
