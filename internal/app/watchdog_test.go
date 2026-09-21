package app

import (
	"strings"
	"testing"
	"time"

	"github.com/kazimshah39/herdr-tandem/internal/transcript"
)

func TestAgyVisibleStateReadsOnlyTheFooter(t *testing.T) {
	working := "old answer says ? for shortcuts\n─────\nEsc to cancel\n"
	if got := agyVisibleState(working); got != "working" {
		t.Fatalf("working state=%q", got)
	}
	idle := "old answer says Esc to cancel\n─────\n? for shortcuts\n"
	if got := agyVisibleState(idle); got != "idle" {
		t.Fatalf("idle state=%q", got)
	}
}

func TestNormalizeVisibleIgnoresVolatileChrome(t *testing.T) {
	got := NormalizeVisible("⠋ Working\nElapsed: 12 seconds\n────────\nesc to cancel\n? for shortcuts")
	if got != "Working" {
		t.Fatalf("got %q", got)
	}
}

func TestProgressTrackerIgnoresRepeatAndDetectsTranscriptOffsets(t *testing.T) {
	now := time.Unix(100, 0)
	tracker := NewProgressTracker(func() time.Time { return now })
	snapshot := ProgressSnapshot{Visible: "⠋ Working", CompactOffset: 10}
	if !tracker.Observe(snapshot) {
		t.Fatal("first snapshot should initialize progress")
	}
	now = now.Add(time.Minute)
	if tracker.Observe(snapshot) {
		t.Fatal("same normalized snapshot must not count as progress")
	}
	snapshot.CompactOffset = 11
	if !tracker.Observe(snapshot) {
		t.Fatal("a transcript offset change must count as progress")
	}
	if got := tracker.StalledFor(now); got != 0 {
		t.Fatalf("stall=%s after progress", got)
	}
}

func TestProgressTrackerReportsElapsedStall(t *testing.T) {
	now := time.Unix(100, 0)
	tracker := NewProgressTracker(func() time.Time { return now })
	tracker.Observe(ProgressSnapshot{Visible: "working"})
	now = now.Add(defaultHealthyStallWindowForTracker)
	if got := tracker.StalledFor(now); got != defaultHealthyStallWindowForTracker {
		t.Fatalf("stall=%s", got)
	}
}

func TestContinuationPromptNeverRepeatsOriginalTask(t *testing.T) {
	original := "do not repeat this secret task text"
	prompt := continuationPrompt(original)
	if prompt == "" || prompt == original || strings.Contains(prompt, original) {
		t.Fatalf("continuation=%q", prompt)
	}
	if got := len(prompt); got > 512 {
		t.Fatalf("continuation is unexpectedly large: %d bytes", got)
	}
}

func TestProgressSnapshotUsesOnlyBoundedMetadata(t *testing.T) {
	snapshot := progressSnapshotFor(transcript.Checkpoint{}, "changed output\n2 tasks", "working")
	if snapshot.BackgroundTasks != 2 || snapshot.AgentStatus != "working" {
		t.Fatalf("snapshot=%+v", snapshot)
	}
}

func TestNewUsesProviderIndependentWatchdogDefaults(t *testing.T) {
	app := New(&fakeRunner{}, nil, nil)
	if app.initialPromptWait != 30*time.Second || app.healthyStallWindow != 150*time.Second || app.heartbeatInterval != 5*time.Minute || app.taskDeadline != 30*time.Minute || app.developerPoll != time.Second {
		t.Fatalf("unexpected defaults")
	}
}
