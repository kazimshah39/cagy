package app

import (
	"context"
	"encoding/json"
	"fmt"
	"regexp"
	"strings"
	"time"

	"github.com/kazimshah39/cagy/internal/herdr"
	"github.com/kazimshah39/cagy/internal/quota"
	"github.com/kazimshah39/cagy/internal/transcript"
)

const (
	developerWaitSegmentMS     = 5 * 60 * 1000
	developerMaxWaits          = developerPromptTimeoutMS / developerWaitSegmentMS
	agyWeeklySwitchThreshold   = 0.03
	agyFiveHourSwitchThreshold = 0.02
)

var agyBackgroundTasksPattern = regexp.MustCompile(`\b[1-9][0-9]*\s+(?:tasks?|task\(s\))(?:\s|$)`)

type developerTaskResult struct {
	agent          herdr.AgentInfo
	output         string
	quotaExhausted bool
}

type agyCommandEnvelope struct {
	Status  string `json:"status"`
	Command struct {
		Name string          `json:"name"`
		Data json.RawMessage `json:"data"`
	} `json:"command"`
}

type agyModel struct {
	ID    string `json:"id"`
	Label string `json:"label"`
}

type agyQuota struct {
	Groups []agyQuotaGroup `json:"groups"`
}

type agyQuotaGroup struct {
	Name    string           `json:"name"`
	Buckets []agyQuotaBucket `json:"buckets"`
}

type agyQuotaBucket struct {
	ID                string   `json:"id"`
	Name              string   `json:"name"`
	RemainingFraction *float64 `json:"remaining_fraction"`
}

func (a *App) runDeveloperTask(ctx context.Context, target, task, before string, checkpoint transcript.Checkpoint) (developerTaskResult, error) {
	taskCtx, cancel := context.WithTimeout(ctx, time.Duration(developerPromptTimeoutMS)*time.Millisecond)
	defer cancel()

	fmt.Fprintln(a.stderr, "cagy: submitting one task to the visible agy developer; monitoring will continue for up to 30 minutes")
	settled, waitErr := a.herdr.Prompt(taskCtx, target, task, developerWaitSegmentMS)
	after, readErr := a.herdr.ReadAgent(taskCtx, target, 400)
	if readErr != nil {
		return developerTaskResult{}, fmt.Errorf("read agy response: %w", readErr)
	}
	newOutput := quota.NewOutput(before, after)
	if quota.DetectedResponse(newOutput, task) {
		return developerTaskResult{agent: settled, output: newOutput, quotaExhausted: true}, nil
	}
	if waitErr != nil && !herdr.IsCode(waitErr, "timeout") && !herdr.IsCode(waitErr, "agent_prompt_stalled") {
		return developerTaskResult{}, waitErr
	}

	current, err := a.transcriptAgent(taskCtx, target, settled)
	if err != nil {
		return developerTaskResult{}, err
	}
	if current.AgentStatus == "blocked" {
		a.warnTrackedPhase(taskPhaseBlocked, current)
		return developerTaskResult{agent: current}, nil
	}
	a.warnTrackedPhase(taskPhaseMonitoring, current)

	remaining := time.Duration(developerPromptTimeoutMS) * time.Millisecond
	var lastProbeErr error
	if herdr.IsCode(waitErr, "timeout") {
		// A timeout consumed the first five-minute segment. The prompt was
		// still submitted exactly once.
		remaining -= time.Duration(developerWaitSegmentMS) * time.Millisecond
		a.reportTaskProgress(current, time.Duration(developerPromptTimeoutMS)*time.Millisecond-remaining)
		exhausted, probeErr := a.agyQuotaExhausted(taskCtx)
		if probeErr != nil {
			lastProbeErr = probeErr
		} else {
			lastProbeErr = nil
			if exhausted {
				return developerTaskResult{agent: current, output: newOutput, quotaExhausted: true}, nil
			}
		}
	}

	return a.monitorDeveloperTask(taskCtx, target, task, before, checkpoint, current, remaining, lastProbeErr)
}

// monitorDeveloperTask requires both a stable real idle footer and the exact
// transcript response. Herdr idle/done reports alone cannot finish a task.
func (a *App) monitorDeveloperTask(
	ctx context.Context,
	target, task, before string,
	checkpoint transcript.Checkpoint,
	current herdr.AgentInfo,
	remaining time.Duration,
	lastProbeErr error,
) (developerTaskResult, error) {
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

	idleFor := time.Duration(0)
	idleWithoutResponse := time.Duration(0)
	sinceTranscriptCheck := time.Duration(0)
	sinceProbe := time.Duration(0)
	latestOutput := ""
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
		sinceProbe += step

		visible, visibleErr := a.herdr.ReadAgentVisible(ctx, target, 80)
		if visibleErr != nil {
			return developerTaskResult{}, fmt.Errorf("read agy visible state: %w", visibleErr)
		}
		switch agyVisibleState(visible) {
		case "working":
			idleFor = 0
			idleWithoutResponse = 0
			sinceTranscriptCheck = 0
		case "idle":
			idleFor += step
			sinceTranscriptCheck += step
			if idleFor >= flushWait && sinceTranscriptCheck >= flushWait {
				checkElapsed := sinceTranscriptCheck
				sinceTranscriptCheck = 0
				// A non-empty planner response can be a progress update while a
				// transcript-visible background task is still running. Require
				// stable idle plus no pending background work before returning it.
				state, transcriptErr := a.completedTranscriptState(checkpoint, current, task)
				if transcriptErr != nil {
					return developerTaskResult{}, transcriptErr
				}
				if state.Found {
					if quota.DetectedResponse(state.Response, task) {
						return developerTaskResult{agent: current, output: state.Response, quotaExhausted: true}, nil
					}
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
			// Unknown screen states are not proof of completion.
			idleFor = 0
			idleWithoutResponse = 0
			sinceTranscriptCheck = 0
		}

		if sinceProbe >= time.Duration(developerWaitSegmentMS)*time.Millisecond {
			a.reportTaskProgress(current, time.Duration(developerPromptTimeoutMS)*time.Millisecond-remaining)
			recent, readErr := a.herdr.ReadAgent(ctx, target, 400)
			if readErr != nil {
				return developerTaskResult{}, fmt.Errorf("read agy response: %w", readErr)
			}
			latestOutput = quota.NewOutput(before, recent)
			if quota.DetectedResponse(latestOutput, task) {
				return developerTaskResult{agent: current, output: latestOutput, quotaExhausted: true}, nil
			}
			exhausted, probeErr := a.agyQuotaExhausted(ctx)
			if probeErr != nil {
				lastProbeErr = probeErr
			} else {
				lastProbeErr = nil
				if exhausted {
					return developerTaskResult{agent: current, output: latestOutput, quotaExhausted: true}, nil
				}
			}
			sinceProbe = 0
		}
	}

	if lastProbeErr != nil {
		return developerTaskResult{}, fmt.Errorf("agy stayed working for 30 minutes; quota check failed: %w", lastProbeErr)
	}
	return developerTaskResult{}, fmt.Errorf("agy stayed working for 30 minutes even though quota is available; check the right pane")
}

func (a *App) reportTaskProgress(agent herdr.AgentInfo, elapsed time.Duration) {
	if elapsed < 0 {
		elapsed = 0
	}
	a.warnTrackedPhase(taskPhaseMonitoring, agent)
	fmt.Fprintf(a.stderr, "cagy: task is still running after %s; progress remains visible in the right pane\n", elapsed.Round(time.Second))
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

func (a *App) agyQuotaExhausted(ctx context.Context) (bool, error) {
	modelPayload, err := a.runAgySlashCommand(ctx, "/model")
	if err != nil {
		return false, fmt.Errorf("read agy model: %w", err)
	}
	var model agyModel
	if err := json.Unmarshal(modelPayload, &model); err != nil {
		return false, fmt.Errorf("decode agy model: %w", err)
	}
	groupName, ok := quotaGroupForModel(model)
	if !ok {
		return false, fmt.Errorf("unknown quota group for model %q", firstNonEmpty(model.ID, model.Label))
	}

	quotaPayload, err := a.runAgySlashCommand(ctx, "/quota")
	if err != nil {
		return false, fmt.Errorf("read agy quota: %w", err)
	}
	var status agyQuota
	if err := json.Unmarshal(quotaPayload, &status); err != nil {
		return false, fmt.Errorf("decode agy quota: %w", err)
	}
	return quotaGroupExhausted(status, groupName)
}

func (a *App) runAgySlashCommand(ctx context.Context, command string) (json.RawMessage, error) {
	result, err := a.runner.Run(ctx, "agy", "-p", command, "--output-format", "json", "--print-timeout", "30s")
	if err != nil {
		return nil, err
	}
	if result.ExitCode != 0 {
		message := strings.TrimSpace(result.Stderr)
		if message == "" {
			message = fmt.Sprintf("exit status %d", result.ExitCode)
		}
		return nil, fmt.Errorf("%s", message)
	}
	var response agyCommandEnvelope
	if err := json.Unmarshal([]byte(strings.TrimSpace(result.Stdout)), &response); err != nil {
		return nil, fmt.Errorf("invalid JSON: %w", err)
	}
	if !strings.EqualFold(response.Status, "SUCCESS") {
		return nil, fmt.Errorf("command status is %q", response.Status)
	}
	if len(response.Command.Data) == 0 || string(response.Command.Data) == "null" {
		return nil, fmt.Errorf("command returned no data")
	}
	return response.Command.Data, nil
}

func quotaGroupForModel(model agyModel) (string, bool) {
	name := strings.ToLower(model.ID + " " + model.Label)
	switch {
	case strings.Contains(name, "gemini"):
		return "Gemini Models", true
	case strings.Contains(name, "claude"), strings.Contains(name, "gpt"), strings.Contains(name, "opus"), strings.Contains(name, "sonnet"):
		return "Claude and GPT models", true
	default:
		return "", false
	}
}

func quotaGroupExhausted(status agyQuota, expectedGroup string) (bool, error) {
	for _, group := range status.Groups {
		if !strings.EqualFold(strings.TrimSpace(group.Name), expectedGroup) {
			continue
		}
		weeklyFound := false
		fiveHourFound := false
		for _, bucket := range group.Buckets {
			if bucket.RemainingFraction == nil {
				continue
			}
			bucketName := strings.ToLower(bucket.ID + " " + bucket.Name)
			switch {
			case strings.Contains(bucketName, "weekly"), strings.Contains(bucketName, "week"):
				weeklyFound = true
				if *bucket.RemainingFraction <= agyWeeklySwitchThreshold {
					return true, nil
				}
			case strings.Contains(bucketName, "5h"), strings.Contains(bucketName, "5-hour"), strings.Contains(bucketName, "5 hour"):
				fiveHourFound = true
				if *bucket.RemainingFraction <= agyFiveHourSwitchThreshold {
					return true, nil
				}
			}
		}
		if !weeklyFound || !fiveHourFound {
			return false, fmt.Errorf("quota group %q does not have readable weekly and 5-hour buckets", expectedGroup)
		}
		return false, nil
	}
	return false, fmt.Errorf("quota group %q was not found", expectedGroup)
}

func firstNonEmpty(values ...string) string {
	for _, value := range values {
		if strings.TrimSpace(value) != "" {
			return value
		}
	}
	return "unknown"
}
