package app

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/kazimshah39/cagy/internal/herdr"
	"github.com/kazimshah39/cagy/internal/transcript"
)

func TestNormalizeVisibleIgnoresVolatileChrome(t *testing.T) {
	got := NormalizeVisible("⠋ Working\nElapsed: 12 seconds\n────────\nesc to cancel\n? for shortcuts")
	if got != "Working" {
		t.Fatalf("got %q", got)
	}
}

func TestProgressTrackerIgnoresRepeatAndDetectsOffsets(t *testing.T) {
	now := time.Unix(100, 0)
	tr := NewProgressTracker(func() time.Time { return now })
	s := ProgressSnapshot{Visible: "⠋ Working", CompactOffset: 10}
	if !tr.Observe(s) {
		t.Fatal("first snapshot should initialize progress")
	}
	now = now.Add(time.Minute)
	if tr.Observe(s) {
		t.Fatal("same normalized snapshot is not progress")
	}
	s.CompactOffset = 11
	if !tr.Observe(s) {
		t.Fatal("transcript offset change should be progress")
	}
	if tr.StalledFor(now) != 0 {
		t.Fatal("progress should reset stall clock")
	}
}

func TestProgressTrackerStalledFor(t *testing.T) {
	now := time.Unix(100, 0)
	tr := NewProgressTracker(func() time.Time { return now })
	tr.Observe(ProgressSnapshot{Visible: "working"})
	now = now.Add(defaultHealthyStallWindowForTracker)
	if tr.StalledFor(now) != defaultHealthyStallWindowForTracker {
		t.Fatalf("stall = %s", tr.StalledFor(now))
	}
}

func TestHealthyStallCancelReturnsFinalWithoutContinuation(t *testing.T) {
	brainRoot := t.TempDir()
	task := "finish work"
	writeAgyTranscript(t, brainRoot, testConversationID, task, "Finished during cancellation.")
	runner := &scriptedRunner{t: t, steps: []runStep{
		{want: []string{"herdr", "pane", "send-keys", "w1:p2", "escape"}, result: jsonResult(`{"type":"pane_info"}`)},
		{want: []string{"herdr", "agent", "read", "developer", "--source", "visible", "--lines", "80"}, result: textResult(">\n? for shortcuts\n")},
	}}
	app := New(runner, &strings.Builder{}, &strings.Builder{})
	app.agyBrainRoot = brainRoot
	app.cancellationIdleWait = time.Millisecond
	app.developerPoll = time.Millisecond
	agent := herdr.AgentInfo{Agent: "agy", AgentStatus: "idle", PaneID: "w1:p2", AgentSession: &herdr.AgentSessionInfo{Source: "herdr:antigravity_cli", Agent: "agy", Kind: "id", Value: testConversationID}}
	result, err := app.cancelHealthyStall(context.Background(), "developer", agent, transcript.Checkpoint{}, task)
	if err != nil {
		t.Fatal(err)
	}
	if result.output != "Finished during cancellation." {
		t.Fatalf("output=%q", result.output)
	}
	runner.assertDone()
	if countCommand(runner.calls, "herdr", "agent", "prompt") != 0 {
		t.Fatal("continuation was sent despite a final answer")
	}
}

func TestHealthyStallContinuationReturnsExactContinuationAnswerOnce(t *testing.T) {
	brainRoot := t.TempDir()
	original := "finish work"
	continuation := continuationPrompt(original)
	writeAgyTaskOnly(t, brainRoot, testConversationID, original)
	runner := &scriptedRunner{t: t, steps: []runStep{
		{want: []string{"herdr", "pane", "send-keys", "w1:p2", "escape"}, result: jsonResult(`{"type":"pane_info"}`)},
		{want: []string{"herdr", "agent", "read", "developer", "--source", "visible", "--lines", "80"}, result: textResult(">\n? for shortcuts\n")},
		{want: []string{"herdr", "agent", "prompt", "developer", continuation, "--wait", "--timeout", "1"}, before: func() {
			appendAgyTranscript(t, brainRoot, testConversationID, continuation, "Finished after safe continuation.")
		}, result: agentJSONWithSession("w1:p2", "w1", "/tmp/project", "done", testConversationID)},
	}}
	app := New(runner, &strings.Builder{}, &strings.Builder{})
	app.agyBrainRoot = brainRoot
	app.cancellationIdleWait = time.Millisecond
	app.developerPoll = time.Millisecond
	agent := herdr.AgentInfo{Agent: "agy", AgentStatus: "idle", PaneID: "w1:p2", WorkspaceID: "w1", ForegroundCWD: "/tmp/project", AgentSession: &herdr.AgentSessionInfo{Source: "herdr:antigravity_cli", Agent: "agy", Kind: "id", Value: testConversationID}}
	result, err := app.cancelHealthyStall(context.Background(), "developer", agent, transcript.Checkpoint{}, original)
	if err != nil {
		t.Fatal(err)
	}
	if result.output != "Finished after safe continuation." {
		t.Fatalf("output=%q", result.output)
	}
	runner.assertDone()
	if countCommand(runner.calls, "herdr", "agent", "prompt") != 1 {
		t.Fatal("continuation was not sent exactly once")
	}
}
