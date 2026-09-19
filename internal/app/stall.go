package app

import (
	"context"
	"fmt"
	"regexp"

	"github.com/kazimshah39/cagy/internal/herdr"
	"github.com/kazimshah39/cagy/internal/transcript"
	"strings"
	"time"
)

// ProgressSnapshot contains only bounded, non-sensitive state observed while
// an agy task runs. It deliberately never stores transcript or prompt text.
type ProgressSnapshot struct {
	Visible         string
	CompactOffset   int64
	FullOffset      int64
	BackgroundTasks int
	AgentStatus     string
}

// ProgressTracker detects real work without treating spinner redraws or prompt
// chrome as progress. The clock is injected so watchdog behaviour is
// deterministic in tests.
type ProgressTracker struct {
	now         func() time.Time
	last        time.Time
	lastKey     string
	initialized bool
}

func NewProgressTracker(now func() time.Time) *ProgressTracker {
	if now == nil {
		now = time.Now
	}
	return &ProgressTracker{now: now}
}

// Observe returns true only when the snapshot contains meaningful new state.
// Visible content is normalized to remove volatile UI decorations first.
func (t *ProgressTracker) Observe(s ProgressSnapshot) bool {
	key := progressKey(s)
	if !t.initialized {
		t.initialized, t.lastKey, t.last = true, key, t.now()
		return true
	}
	if key == t.lastKey {
		return false
	}
	t.lastKey, t.last = key, t.now()
	return true
}

func (t *ProgressTracker) LastProgress() time.Time { return t.last }
func (t *ProgressTracker) StalledFor(at time.Time) time.Duration {
	if !t.initialized {
		return 0
	}
	d := at.Sub(t.last)
	if d < 0 {
		return 0
	}
	return d
}

var (
	spinnerPattern   = regexp.MustCompile(`[⠋⠙⠹⠸⠼⠴⠦⠧⠇⠏◐◓◑◒]`)
	elapsedPattern   = regexp.MustCompile(`(?i)\b(?:elapsed|time|duration)\s*:?\s*\d+(?:\.\d+)?\s*(?:s|sec|secs|seconds|m|min|minutes)?\b`)
	taskCountPattern = regexp.MustCompile(`(?i)\b\d+\s+(?:tasks?|task\(s\))\b`)
)

// NormalizeVisible strips volatile spinner frames, counters, separators, and
// agy prompt chrome. It preserves actual visible work text and background task
// identifiers/counts.
func NormalizeVisible(screen string) string {
	lines := strings.Split(strings.ReplaceAll(screen, "\r", ""), "\n")
	out := make([]string, 0, len(lines))
	for _, line := range lines {
		line = strings.TrimSpace(spinnerPattern.ReplaceAllString(line, ""))
		line = elapsedPattern.ReplaceAllString(line, "")
		if line == "" || agyDividerLine(line) || strings.EqualFold(line, "esc to cancel") || strings.Contains(strings.ToLower(line), "? for shortcuts") {
			continue
		}
		// Footer redraws commonly contain only the current task count; retain
		// it because a count change is a lifecycle event, not mere chrome.
		out = append(out, strings.Join(strings.Fields(line), " "))
	}
	return strings.Join(out, "\n")
}

func progressKey(s ProgressSnapshot) string {
	visible := NormalizeVisible(s.Visible)
	// Offsets are monotonic transcript state and avoid retaining transcript
	// content in memory or on disk.
	return strings.Join([]string{visible, formatInt(s.CompactOffset), formatInt(s.FullOffset), formatInt(int64(s.BackgroundTasks)), strings.ToLower(strings.TrimSpace(s.AgentStatus))}, "\x00")
}

func formatInt(v int64) string { return strconvFormat(v) }

// Kept local to avoid fmt allocations in the watchdog hot path.
func strconvFormat(v int64) string {
	if v == 0 {
		return "0"
	}
	neg := v < 0
	if neg {
		v = -v
	}
	b := [20]byte{}
	i := len(b)
	for v > 0 {
		i--
		b[i] = byte('0' + v%10)
		v /= 10
	}
	if neg {
		i--
		b[i] = '-'
	}
	return string(b[i:])
}

const (
	defaultHealthyStallWindowForTracker = 150 * time.Second
	defaultCancellationIdleWait         = 30 * time.Second
)

// cancelHealthyStall safely interrupts a developer that has healthy quota but
// made no meaningful progress. It never resubmits the original task. If an
// exact final response is already present it is returned; otherwise one short,
// same-conversation continuation is allowed after cancellation is proven.
func (a *App) cancelHealthyStall(ctx context.Context, target string, agent herdr.AgentInfo, checkpoint transcript.Checkpoint, task string) (developerTaskResult, error) {
	if agent.PaneID == "" {
		return developerTaskResult{}, fmt.Errorf("cannot cancel stalled agy task: developer pane is unknown")
	}
	if err := a.herdr.SendPaneKeys(ctx, agent.PaneID, "escape"); err != nil {
		return developerTaskResult{}, fmt.Errorf("cancel stalled agy task: %w", err)
	}
	idleWait := a.cancellationIdleWait
	if idleWait <= 0 {
		idleWait = defaultCancellationIdleWait
	}
	poll := a.developerPoll
	if poll <= 0 || poll > idleWait {
		poll = time.Second
	}
	stable := time.Duration(0)
	for stable < idleWait {
		step := poll
		if left := idleWait - stable; step > left {
			step = left
		}
		visible, err := a.herdr.ReadAgentVisible(ctx, target, 80)
		if err != nil {
			return developerTaskResult{}, fmt.Errorf("verify stalled-task cancellation: %w", err)
		}
		if agyVisibleState(visible) == "idle" {
			stable += step
		} else {
			stable = 0
		}
		if state, err := a.completedTranscriptState(checkpoint, agent, task); err == nil && state.Found {
			return developerTaskResult{agent: agent, output: state.Response}, nil
		}
		select {
		case <-ctx.Done():
			return developerTaskResult{}, ctx.Err()
		case <-time.After(step):
		}
	}
	state, err := a.completedTranscriptState(checkpoint, agent, task)
	if err != nil {
		return developerTaskResult{}, err
	}
	if state.Found {
		return developerTaskResult{agent: agent, output: state.Response}, nil
	}
	if state.BackgroundPending || state.AwaitingResponse {
		return developerTaskResult{}, fmt.Errorf("stalled agy task cancellation is uncertain; background work remains")
	}
	// This continuation intentionally contains no original task text. Capture a
	// new checkpoint and track the continuation hash so both immediate delivery
	// and recover_task inspect the exact turn that can now produce the answer.
	continuation := continuationPrompt(task)
	continuationCheckpoint, checkpointErr := a.transcriptCheckpoint(agent)
	if checkpointErr != nil {
		return developerTaskResult{}, fmt.Errorf("capture stalled-task continuation checkpoint: %w", checkpointErr)
	}
	if err := a.replaceTrackedPrompt(agent, continuation, continuationCheckpoint, taskPhaseMonitoring); err != nil {
		return developerTaskResult{}, fmt.Errorf("save stalled-task continuation state: %w", err)
	}
	cont, promptErr := a.herdr.Prompt(ctx, target, continuation, durationMS(idleWait))
	if promptErr != nil && !herdr.IsCode(promptErr, "timeout") && !herdr.IsCode(promptErr, "agent_prompt_stalled") {
		return developerTaskResult{agent: cont}, fmt.Errorf("stalled agy continuation failed: %w", promptErr)
	}
	state, stateErr := a.completedTranscriptState(continuationCheckpoint, cont, continuation)
	if stateErr == nil && state.Found {
		return developerTaskResult{agent: cont, output: state.Response}, nil
	}
	return developerTaskResult{agent: cont}, fmt.Errorf("stalled agy continuation made no confirmed progress; use recover_task")
}
