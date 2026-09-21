package app

import (
	"context"
	"fmt"
	"os"
	"regexp"
	"strings"
	"time"

	"github.com/kazimshah39/herdr-tandem/internal/herdr"
	"github.com/kazimshah39/herdr-tandem/internal/transcript"
)

const (
	defaultInitialPromptWait  = 30 * time.Second
	defaultHealthyStallWindow = 150 * time.Second
	defaultHeartbeatInterval  = 5 * time.Minute
	defaultTaskDeadline       = 30 * time.Minute
)

var agyBackgroundTasksPattern = regexp.MustCompile(`\b[1-9][0-9]*\s+(?:tasks?|task\(s\))(?:\s|$)`)

type developerTaskResult struct {
	agent  herdr.AgentInfo
	output string
}

func (a *App) runDeveloperTask(ctx context.Context, target, task string, checkpoint transcript.Checkpoint) (developerTaskResult, error) {
	taskID := debugTaskFingerprint(task)
	deadline := a.taskDeadline
	if deadline <= 0 {
		deadline = defaultTaskDeadline
	}
	initialWait := a.initialPromptWait
	if initialWait <= 0 {
		initialWait = defaultInitialPromptWait
	}
	a.debugf("watchdog task-start task=%q target=%q deadline=%s initial_wait=%s", taskID, target, deadline, initialWait)
	taskCtx, cancel := context.WithTimeout(ctx, deadline)
	defer cancel()

	fmt.Fprintln(a.stderr, "herdr-tandem: submitting one task to the visible agy developer; monitoring will continue for up to 30 minutes")
	settled, waitErr := a.herdr.Prompt(taskCtx, target, task, durationMS(initialWait))
	a.debugf("watchdog prompt-result task=%q status=%q pane=%q error=%q", taskID, settled.AgentStatus, settled.PaneID, waitErr)
	if waitErr != nil && !herdr.IsCode(waitErr, "timeout") && !herdr.IsCode(waitErr, "agent_prompt_stalled") {
		return developerTaskResult{}, waitErr
	}

	current, err := a.transcriptAgent(taskCtx, target, settled)
	if err != nil {
		return developerTaskResult{}, err
	}
	a.debugf("watchdog transcript-agent task=%q status=%q has_session=%t", taskID, current.AgentStatus, current.AgentSession != nil)
	if current.AgentStatus == "blocked" {
		a.warnTrackedPhase(taskPhaseBlocked, current)
		return developerTaskResult{agent: current}, nil
	}
	a.warnTrackedPhase(taskPhaseMonitoring, current)
	remaining := deadline
	if herdr.IsCode(waitErr, "timeout") {
		remaining -= initialWait
		if remaining < 0 {
			remaining = 0
		}
	}
	return a.monitorDeveloperTask(taskCtx, target, task, checkpoint, current, remaining)
}

// monitorDeveloperTask requires both a stable real idle footer and the exact
// transcript response. Herdr idle/done reports alone cannot finish a task.
func (a *App) monitorDeveloperTask(
	ctx context.Context,
	target, task string,
	checkpoint transcript.Checkpoint,
	current herdr.AgentInfo,
	remaining time.Duration,
) (developerTaskResult, error) {
	taskID := debugTaskFingerprint(task)
	poll := a.developerPoll
	if poll <= 0 {
		poll = time.Second
	}
	flushWait := a.transcriptWait
	if flushWait <= 0 {
		flushWait = 3 * time.Second
	}
	missingWait := a.missingTranscriptWait
	if missingWait <= 0 {
		missingWait = 30 * time.Second
	}
	stallWindow := a.healthyStallWindow
	if stallWindow <= 0 {
		stallWindow = defaultHealthyStallWindow
	}
	heartbeat := a.heartbeatInterval
	if heartbeat <= 0 {
		heartbeat = defaultHeartbeatInterval
	}

	idleFor := time.Duration(0)
	idleWithoutResponse := time.Duration(0)
	sinceTranscriptCheck := time.Duration(0)
	sinceHeartbeat := time.Duration(0)
	stallFor := time.Duration(0)
	elapsed := time.Duration(0)
	tracker := NewProgressTracker(nil)
	lastVisibleState := ""
	a.debugf("watchdog monitor-begin task=%q target=%q remaining=%s poll=%s stall=%s", taskID, target, remaining, poll, stallWindow)
	progressCheckpoint := checkpoint
	if progressCheckpoint.Path == "" && current.AgentSession != nil {
		if paths, err := transcript.PathsFor(a.transcriptRoot, transcript.Ref{Source: current.AgentSession.Source, Agent: current.AgentSession.Agent, Kind: current.AgentSession.Kind, Value: current.AgentSession.Value}); err == nil {
			progressCheckpoint.Path = paths.Compact
			progressCheckpoint.FullPath = paths.Full
		}
	}
	for remaining > 0 {
		step := poll
		if step > remaining {
			step = remaining
		}
		blocked, waitErr := a.herdr.WaitAgentBlocked(ctx, target, durationMS(step))
		if waitErr == nil {
			current = blocked
			if current.AgentStatus == "blocked" {
				a.warnTrackedPhase(taskPhaseBlocked, current)
				return developerTaskResult{agent: current}, nil
			}
		} else if !herdr.IsCode(waitErr, "timeout") {
			if ctx.Err() != nil {
				return developerTaskResult{}, ctx.Err()
			}
			return developerTaskResult{}, waitErr
		}

		remaining -= step
		elapsed += step
		sinceHeartbeat += step
		visible, visibleErr := a.herdr.ReadAgentVisible(ctx, target, 80)
		if visibleErr != nil {
			return developerTaskResult{}, fmt.Errorf("read agy visible state: %w", visibleErr)
		}
		snapshot := progressSnapshotFor(progressCheckpoint, visible, current.AgentStatus)
		if tracker.Observe(snapshot) {
			stallFor = 0
		} else {
			stallFor += step
		}

		visibleState := agyVisibleState(visible)
		if visibleState != lastVisibleState {
			a.debugf("watchdog visible-state task=%q state=%q agent_status=%q elapsed=%s", taskID, visibleState, current.AgentStatus, elapsed)
			lastVisibleState = visibleState
		}
		switch visibleState {
		case "working":
			idleFor, idleWithoutResponse, sinceTranscriptCheck = 0, 0, 0
		case "idle":
			idleFor += step
			sinceTranscriptCheck += step
			if idleFor >= flushWait && sinceTranscriptCheck >= flushWait {
				checkElapsed := sinceTranscriptCheck
				sinceTranscriptCheck = 0
				state, transcriptErr := a.completedTranscriptState(checkpoint, current, task)
				if transcriptErr != nil {
					return developerTaskResult{}, transcriptErr
				}
				if state.Found {
					a.debugf("watchdog transcript-complete task=%q response_bytes=%d elapsed=%s", taskID, len(state.Response), elapsed)
					return developerTaskResult{agent: current, output: state.Response}, nil
				}
				if state.BackgroundPending {
					idleWithoutResponse = 0
				} else {
					idleWithoutResponse += checkElapsed
					if idleWithoutResponse >= missingWait {
						return developerTaskResult{}, fmt.Errorf("agy finished without a complete transcript response; check the right pane")
					}
				}
			}
		default:
			idleFor, idleWithoutResponse, sinceTranscriptCheck = 0, 0, 0
		}

		if sinceHeartbeat >= heartbeat {
			a.debugf("watchdog heartbeat task=%q elapsed=%s status=%q stall=%s", taskID, elapsed, current.AgentStatus, stallFor)
			a.reportTaskProgress(current, elapsed)
			sinceHeartbeat = 0
		}
		if stallFor >= stallWindow {
			a.debugf("watchdog stall-detected task=%q stall=%s elapsed=%s", taskID, stallFor, elapsed)
			return a.cancelHealthyStall(ctx, target, current, checkpoint, task)
		}
	}
	a.debugf("watchdog deadline task=%q elapsed=%s", taskID, elapsed)
	return developerTaskResult{}, fmt.Errorf("agy stayed working for 30 minutes; use recover_task after checking the right pane")
}

func progressSnapshotFor(checkpoint transcript.Checkpoint, visible, status string) ProgressSnapshot {
	snapshot := ProgressSnapshot{Visible: visible, CompactOffset: fileSize(checkpoint.Path), FullOffset: fileSize(checkpoint.FullPath), AgentStatus: status}
	if match := agyBackgroundTasksPattern.FindString(visible); match != "" {
		_, _ = fmt.Sscanf(match, "%d", &snapshot.BackgroundTasks)
	}
	return snapshot
}

func fileSize(path string) int64 {
	if strings.TrimSpace(path) == "" {
		return 0
	}
	info, err := os.Stat(path)
	if err != nil || !info.Mode().IsRegular() {
		return 0
	}
	return info.Size()
}

func (a *App) reportTaskProgress(agent herdr.AgentInfo, elapsed time.Duration) {
	if elapsed < 0 {
		elapsed = 0
	}
	a.warnTrackedPhase(taskPhaseMonitoring, agent)
	fmt.Fprintf(a.stderr, "herdr-tandem: task is still running after %s; progress remains visible in the right pane\n", elapsed.Round(time.Second))
}

func (a *App) transcriptAgent(ctx context.Context, target string, agent herdr.AgentInfo) (herdr.AgentInfo, error) {
	if agent.AgentSession != nil {
		return agent, nil
	}
	current, err := a.herdr.GetAgent(ctx, target)
	if err != nil {
		return herdr.AgentInfo{}, fmt.Errorf("refresh agy developer: %w", err)
	}
	if current.AgentSession == nil {
		return herdr.AgentInfo{}, fmt.Errorf("agy transcript was not reported; run: herdr integration install antigravity-cli")
	}
	return current, nil
}

func durationMS(duration time.Duration) int {
	milliseconds := duration.Milliseconds()
	if milliseconds < 1 {
		return 1
	}
	maxInt := int64(^uint(0) >> 1)
	if milliseconds > maxInt {
		return int(maxInt)
	}
	return int(milliseconds)
}

func agyVisibleState(screen string) string {
	lines := strings.Split(screen, "\n")
	footerStart := -1
	for index := len(lines) - 1; index >= 0; index-- {
		if agyDividerLine(lines[index]) {
			footerStart = index + 1
			break
		}
	}
	if footerStart < 0 {
		for index := len(lines) - 1; index >= 0; index-- {
			if strings.TrimSpace(lines[index]) != "" {
				footerStart = index
				break
			}
		}
	}
	if footerStart < 0 {
		return "unknown"
	}
	footer := strings.ToLower(strings.Join(lines[footerStart:], "\n"))
	if strings.Contains(footer, "esc to cancel") || agyBackgroundTasksPattern.MatchString(footer) {
		return "working"
	}
	if strings.Contains(footer, "? for shortcuts") {
		return "idle"
	}
	return "unknown"
}

func agyDividerLine(line string) bool {
	line = strings.TrimSpace(line)
	if len([]rune(line)) < 5 {
		return false
	}
	for _, char := range line {
		if char != '─' && char != '-' {
			return false
		}
	}
	return true
}
