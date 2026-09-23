package app

import (
	"context"
	"fmt"
	"path/filepath"
	"strings"
	"time"

	"github.com/kazimshah39/herdr-tandem/internal/herdr"
)

const supervisorTerminalWakePrompt = `The delegated developer task has reached a terminal state. Do not wait for the developer to report back and do not submit the task again. Call task_status now. If it reports completed_unacknowledged, call recover_task, review the result, and then call acknowledge_task with the returned receipt. If it reports blocked or uncertain, inspect and handle that state.`

func isTerminalTaskPhase(phase taskPhase) bool {
	switch phase {
	case taskPhaseCompleted, taskPhaseBlocked, taskPhaseUncertain:
		return true
	default:
		return false
	}
}

func (a *App) schedulePendingSupervisorWake() {
	info, err := a.context()
	if err != nil {
		a.debugf("pending supervisor-wake context-error error=%q", err)
		return
	}
	record, exists, err := a.loadTaskJournal(info.developer)
	if err != nil {
		a.debugf("code=HTD-SUP-003 pending supervisor-wake journal-error developer=%q error=%q", info.developer, err)
		return
	}
	if !exists || !isTerminalTaskPhase(record.Phase) {
		return
	}
	delay := a.supervisorNotifyDelay
	if delay < 0 {
		delay = 0
	}
	a.monitorWG.Add(1)
	go func() {
		defer a.monitorWG.Done()
		if delay > 0 {
			timer := time.NewTimer(delay)
			<-timer.C
		}
		if notifyErr := a.notifySupervisorTaskTerminal(info, record.TaskHash, record.Phase); notifyErr != nil {
			a.debugf("code=HTD-SUP-003 pending supervisor-wake failed task=%q phase=%q error=%q", debugHashPrefix(record.TaskHash), record.Phase, notifyErr)
		}
	}()
}

// notifySupervisorTaskTerminal wakes the existing supervisor only after the
// durable task journal has reached a terminal phase. The prompt is fixed and
// contains no task text, developer output, receipt, or other untrusted data.
func (a *App) notifySupervisorTaskTerminal(info runtimeContext, taskHash string, phase taskPhase) error {
	if !isTerminalTaskPhase(phase) {
		return fmt.Errorf("task phase %q is not terminal", phase)
	}
	if strings.TrimSpace(taskHash) == "" {
		return fmt.Errorf("terminal task identity is missing")
	}

	timeout := a.supervisorNotifyTimeout
	if timeout <= 0 {
		timeout = 30 * time.Minute
	}
	poll := a.supervisorNotifyPoll
	if poll <= 0 {
		poll = time.Second
	}
	promptWait := a.supervisorNotifyWait
	if promptWait <= 0 {
		promptWait = 5 * time.Second
	}

	ctx, cancel := context.WithTimeout(context.Background(), timeout)
	defer cancel()
	a.debugf("code=HTD-SUP-001 supervisor-wake begin task=%q phase=%q timeout=%s", debugHashPrefix(taskHash), phase, timeout)

	for {
		pending, err := a.terminalTaskStillPending(info, taskHash, phase)
		if err != nil {
			a.debugf("code=HTD-SUP-003 supervisor-wake journal-failed task=%q phase=%q error=%q", debugHashPrefix(taskHash), phase, err)
			return err
		}
		if !pending {
			a.debugf("code=HTD-SUP-003 supervisor-wake skipped task=%q phase=%q reason=%q", debugHashPrefix(taskHash), phase, "task-no-longer-pending")
			return nil
		}

		agent, err := a.supervisorNotificationAgent(ctx, info)
		if err != nil {
			a.debugf("code=HTD-SUP-003 supervisor-wake validation-failed phase=%q error=%q", phase, err)
			return err
		}

		switch agent.AgentStatus {
		case "idle", "done":
			pending, pendingErr := a.terminalTaskStillPending(info, taskHash, phase)
			if pendingErr != nil {
				return pendingErr
			}
			if !pending {
				a.debugf("code=HTD-SUP-003 supervisor-wake skipped task=%q phase=%q reason=%q", debugHashPrefix(taskHash), phase, "task-acknowledged-before-wake")
				return nil
			}
			_, promptErr := a.herdr.Prompt(ctx, info.supervisor, supervisorTerminalWakePrompt, durationMS(promptWait))
			if promptErr != nil && !herdr.IsCode(promptErr, "timeout") && !herdr.IsCode(promptErr, "agent_prompt_stalled") {
				a.debugf("code=HTD-SUP-003 supervisor-wake prompt-failed phase=%q status=%q error=%q", phase, agent.AgentStatus, promptErr)
				return fmt.Errorf("wake supervisor for terminal task: %w", promptErr)
			}
			a.debugf("code=HTD-SUP-002 supervisor-wake submitted phase=%q status=%q wait_result=%q", phase, agent.AgentStatus, promptOutcome(promptErr))
			return nil
		}

		timer := time.NewTimer(poll)
		select {
		case <-ctx.Done():
			if !timer.Stop() {
				<-timer.C
			}
			err := fmt.Errorf("supervisor did not become idle within %s: %w", timeout, ctx.Err())
			a.debugf("code=HTD-SUP-003 supervisor-wake timeout phase=%q last_status=%q error=%q", phase, agent.AgentStatus, err)
			return err
		case <-timer.C:
		}
	}
}

func (a *App) terminalTaskStillPending(info runtimeContext, taskHash string, phase taskPhase) (bool, error) {
	record, exists, err := a.loadTaskJournal(info.developer)
	if err != nil {
		return false, fmt.Errorf("read terminal task before supervisor wake: %w", err)
	}
	if !exists {
		return false, nil
	}
	if err := validateTaskJournal(record, info); err != nil {
		return false, fmt.Errorf("validate terminal task before supervisor wake: %w", err)
	}
	return record.TaskHash == taskHash && record.Phase == phase && isTerminalTaskPhase(record.Phase), nil
}

func (a *App) supervisorNotificationAgent(ctx context.Context, info runtimeContext) (herdr.AgentInfo, error) {
	pane, err := a.supervisorPane(ctx, info)
	if err != nil {
		return herdr.AgentInfo{}, err
	}
	if err := validatePaneScope(pane, info, pane); err != nil {
		return herdr.AgentInfo{}, fmt.Errorf("supervisor pane scope invalid: %w", err)
	}
	if pane.Tokens["herdr_tandem_owner"] != info.developer || pane.Tokens["herdr_tandem_role"] != "supervisor" {
		return herdr.AgentInfo{}, fmt.Errorf("supervisor pane ownership is invalid")
	}

	agent, err := a.herdr.GetAgent(ctx, info.supervisor)
	if err != nil {
		return herdr.AgentInfo{}, fmt.Errorf("find supervisor agent: %w", err)
	}
	if agent.PaneID != pane.PaneID || agent.WorkspaceID != info.workspaceID || agent.TabID != pane.TabID {
		return herdr.AgentInfo{}, fmt.Errorf("supervisor agent scope does not match its pane")
	}
	cwd := strings.TrimSpace(agent.ForegroundCWD)
	if cwd == "" {
		cwd = strings.TrimSpace(agent.CWD)
	}
	if cwd == "" || filepath.Clean(cwd) != filepath.Clean(info.project) {
		return herdr.AgentInfo{}, fmt.Errorf("supervisor agent belongs to another project")
	}
	if strings.TrimSpace(agent.Agent) != a.supervisor.ID() {
		return herdr.AgentInfo{}, fmt.Errorf("supervisor agent kind does not match the configured supervisor")
	}
	return agent, nil
}

func promptOutcome(err error) string {
	switch {
	case err == nil:
		return "completed"
	case herdr.IsCode(err, "timeout"):
		return "accepted_timeout"
	case herdr.IsCode(err, "agent_prompt_stalled"):
		return "accepted_stalled"
	default:
		return "failed"
	}
}
