package app

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"fmt"
	"regexp"
	"strconv"
	"strings"

	"github.com/kazimshah39/cagy/internal/herdr"
)

const agmConfirmation = "Switch to this account? [y/N]:"

func (a *App) recover(ctx context.Context, info runtimeContext, developer herdr.AgentInfo, originalTask string, taskStarted bool) (string, error) {
	if err := a.closeDeveloper(ctx, info, developer); err != nil {
		return "", fmt.Errorf("stop quota-limited developer: %w", err)
	}

	var lastErr error
	for attempt := 1; attempt <= maxRecoveryAttempts; attempt++ {
		pane, err := a.herdr.SplitRight(ctx, info.project)
		if err != nil {
			lastErr = fmt.Errorf("create recovery pane: %w", err)
			continue
		}
		if pane.WorkspaceID != info.workspaceID {
			_ = a.herdr.ClosePane(ctx, pane.PaneID)
			return "", fmt.Errorf("recovery pane was created in another workspace")
		}
		_ = a.herdr.RenamePane(ctx, pane.PaneID, "agy Recovery")

		if _, _, err := a.maybeRefreshAll(ctx, pane.PaneID); err != nil {
			lastErr = err
			a.closeFailedRecoveryPane(ctx, pane.PaneID, attempt)
			continue
		}

		switchOutput, switchStatus, err := a.runAutoSwitch(ctx, pane.PaneID)
		if err != nil {
			lastErr = err
			a.closeFailedRecoveryPane(ctx, pane.PaneID, attempt)
			continue
		}
		if !agySwitchSucceeded(switchOutput) {
			lastErr = fmt.Errorf("AGM could not confirm the agy account switch (status %d)", switchStatus)
			a.closeFailedRecoveryPane(ctx, pane.PaneID, attempt)
			continue
		}

		exhausted, probeErr := a.agyQuotaExhausted(ctx)
		if probeErr != nil {
			lastErr = fmt.Errorf("verify switched agy account: %w", probeErr)
			a.closeFailedRecoveryPane(ctx, pane.PaneID, attempt)
			continue
		}
		if exhausted {
			lastErr = fmt.Errorf("AGM selected an agy account with insufficient quota")
			a.closeFailedRecoveryPane(ctx, pane.PaneID, attempt)
			continue
		}

		_ = a.herdr.RenamePane(ctx, pane.PaneID, "agy Developer")
		resumed, err := a.herdr.StartAgy(ctx, info.developer, pane.PaneID, true)
		if err != nil {
			lastErr = fmt.Errorf("restart agy: %w", err)
			a.closeFailedRecoveryPane(ctx, pane.PaneID, attempt)
			continue
		}
		if err := validateDeveloper(resumed, info.workspaceID, info.project); err != nil {
			_ = a.herdr.ClosePane(ctx, pane.PaneID)
			return "", err
		}
		if err := a.herdr.ReportAgentDisplay(ctx, pane.PaneID, developerDisplaySource, "agy", developerDisplayName, "developer"); err != nil {
			_ = a.herdr.ClosePane(ctx, pane.PaneID)
			return "", fmt.Errorf("label resumed cagy developer: %w", err)
		}
		if err := a.ensureAgyReady(ctx, pane.PaneID); err != nil {
			lastErr = fmt.Errorf("prepare resumed agy: %w", err)
			a.closeFailedRecoveryPane(ctx, pane.PaneID, attempt)
			continue
		}

		checkpoint, checkpointErr := a.transcriptCheckpoint(resumed)
		if checkpointErr != nil {
			return "", fmt.Errorf("prepare resumed agy transcript: %w", checkpointErr)
		}
		before, _ := a.herdr.ReadAgent(ctx, info.developer, 400)
		prompt := originalTask
		if taskStarted {
			prompt = continuationPrompt(originalTask)
		}
		result, taskErr := a.runDeveloperTask(ctx, info.developer, prompt, before, checkpoint)
		taskStarted = true
		if result.quotaExhausted {
			lastErr = fmt.Errorf("resumed agy account also reached quota")
			if attempt < maxRecoveryAttempts {
				if err := a.closeDeveloper(ctx, info, resumed); err != nil {
					return "", err
				}
			}
			continue
		}
		if taskErr != nil {
			return "", fmt.Errorf("resumed agy task failed: %w", taskErr)
		}
		if result.agent.AgentStatus == "blocked" {
			return "", fmt.Errorf("resumed developer is blocked; check the right pane")
		}
		if result.output == "" {
			return "", fmt.Errorf("resumed agy finished without readable output")
		}
		return result.output, nil
	}

	if lastErr == nil {
		lastErr = fmt.Errorf("no usable AGM account was found")
	}
	return "", fmt.Errorf("quota recovery failed after %d attempts: %w", maxRecoveryAttempts, lastErr)
}

func (a *App) ensureAgyReady(ctx context.Context, paneID string) error {
	output, err := a.herdr.ReadPane(ctx, paneID, 200)
	if err != nil {
		return fmt.Errorf("read agy startup: %w", err)
	}
	if strings.Contains(output, "Do you trust the contents of this project?") {
		if !strings.Contains(output, "> Yes, I trust this folder") {
			return fmt.Errorf("agy project trust prompt is not on the Yes option")
		}
		if err := a.herdr.SendPaneKeys(ctx, paneID, "enter"); err != nil {
			return fmt.Errorf("accept agy project trust: %w", err)
		}
	}
	if err := a.herdr.WaitPaneMatch(ctx, paneID, "? for shortcuts", agyReadyTimeoutMS); err != nil {
		return fmt.Errorf("wait for agy prompt: %w", err)
	}
	return nil
}

func (a *App) closeFailedRecoveryPane(ctx context.Context, paneID string, attempt int) {
	if attempt < maxRecoveryAttempts {
		_ = a.herdr.ClosePane(ctx, paneID)
	}
}

func (a *App) closeDeveloper(ctx context.Context, info runtimeContext, developer herdr.AgentInfo) error {
	if err := validateDeveloper(developer, info.workspaceID, info.project); err != nil {
		return err
	}
	_ = a.herdr.SendAgentKeys(ctx, info.developer, "ctrl+c")
	if err := a.herdr.ClosePane(ctx, developer.PaneID); err != nil {
		return fmt.Errorf("close developer pane: %w", err)
	}
	return nil
}

func (a *App) runAutoSwitch(ctx context.Context, paneID string) (string, int, error) {
	token, err := a.token()
	if err != nil {
		return "", -1, err
	}
	marker := "__CAGY_SWITCH_" + token + "__"
	command := markedCommand("agm auto-switch --min 5", marker)
	if err := a.herdr.RunInPane(ctx, paneID, command); err != nil {
		return "", -1, fmt.Errorf("start AGM auto-switch: %w", err)
	}

	pattern := regexp.QuoteMeta(agmConfirmation) + "|" + completionPattern(marker)
	if err := a.herdr.WaitPaneRegex(ctx, paneID, pattern, commandTimeoutMS); err != nil {
		return "", -1, fmt.Errorf("wait for AGM confirmation: %w", err)
	}
	output, err := a.herdr.ReadPane(ctx, paneID, 400)
	if err != nil {
		return "", -1, fmt.Errorf("read AGM output: %w", err)
	}
	if !strings.Contains(output, agmConfirmation) {
		status, parseErr := markerStatus(output, marker)
		if parseErr != nil {
			return output, -1, fmt.Errorf("AGM ended before confirmation")
		}
		return output, status, fmt.Errorf("AGM did not offer an account to switch")
	}

	if err := a.herdr.SendPaneText(ctx, paneID, "y"); err != nil {
		return output, -1, fmt.Errorf("answer AGM confirmation: %w", err)
	}
	if err := a.herdr.SendPaneKeys(ctx, paneID, "enter"); err != nil {
		return output, -1, fmt.Errorf("submit AGM confirmation: %w", err)
	}
	if err := a.herdr.WaitPaneRegex(ctx, paneID, completionPattern(marker), commandTimeoutMS); err != nil {
		return output, -1, fmt.Errorf("wait for AGM switch: %w", err)
	}
	output, err = a.herdr.ReadPane(ctx, paneID, 400)
	if err != nil {
		return "", -1, fmt.Errorf("read completed AGM output: %w", err)
	}
	status, err := markerStatus(output, marker)
	if err != nil {
		return output, -1, err
	}
	return output, status, nil
}

func (a *App) runMarkedWithTimeout(ctx context.Context, paneID, command, label string, timeoutMS int) (string, int, error) {
	token, err := a.token()
	if err != nil {
		return "", -1, err
	}
	marker := "__CAGY_" + label + "_" + token + "__"
	if err := a.herdr.RunInPane(ctx, paneID, markedCommand(command, marker)); err != nil {
		return "", -1, fmt.Errorf("start %s: %w", command, err)
	}
	if err := a.herdr.WaitPaneRegex(ctx, paneID, completionPattern(marker), timeoutMS); err != nil {
		return "", -1, fmt.Errorf("wait for %s: %w", command, err)
	}
	output, err := a.herdr.ReadPane(ctx, paneID, 400)
	if err != nil {
		return "", -1, fmt.Errorf("read %s output: %w", command, err)
	}
	status, err := markerStatus(output, marker)
	if err != nil {
		return output, -1, err
	}
	return output, status, nil
}

func markedCommand(command, marker string) string {
	return command + `; __cagy_status=$?; printf '\n` + marker + `:%s\n' "$__cagy_status"`
}

func completionPattern(marker string) string {
	// The pane echoes the command before it runs. Requiring a numeric status
	// prevents the echoed printf format (":%s") from looking complete.
	return regexp.QuoteMeta(marker) + `:[0-9]+`
}

func markerStatus(output, marker string) (int, error) {
	pattern := regexp.MustCompile(regexp.QuoteMeta(marker) + `:(\d+)`)
	match := pattern.FindStringSubmatch(output)
	if len(match) != 2 {
		return -1, fmt.Errorf("completion marker was not readable")
	}
	status, err := strconv.Atoi(match[1])
	if err != nil {
		return -1, fmt.Errorf("invalid completion status: %w", err)
	}
	return status, nil
}

func agySwitchSucceeded(output string) bool {
	return strings.Contains(output, "✓ Antigravity CLI (agy)") ||
		strings.Contains(output, "✔ Antigravity CLI (agy)")
}

func continuationPrompt(originalTask string) string {
	return `The previous task was interrupted by an account quota change. Continue the same task. Inspect the current working tree first and do not repeat completed work.

Original task:
` + originalTask
}

func randomToken() (string, error) {
	bytes := make([]byte, 8)
	if _, err := rand.Read(bytes); err != nil {
		return "", fmt.Errorf("create recovery marker: %w", err)
	}
	return hex.EncodeToString(bytes), nil
}
