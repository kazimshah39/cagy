package app

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/kazimshah39/cagy/internal/accounts"
	"github.com/kazimshah39/cagy/internal/herdr"
	"github.com/kazimshah39/cagy/internal/transcript"
)

const recoveryQuotaCacheAge = 10 * time.Minute

type recoverySummary struct {
	attempted int
	low       int
	unknown   int
	failed    int
}

// recover rotates through stored agy accounts until one has live available
// quota, then continues the delegated work in the exact same conversation. It
// never opens OAuth and never resends an original task that already started.
func (a *App) recover(ctx context.Context, info runtimeContext, developer herdr.AgentInfo, originalTask string, taskStarted bool, trigger quotaProbeResult) (string, herdr.AgentInfo, error) {
	taskID := debugTaskFingerprint(originalTask)
	a.debugf("recovery begin task=%q task_started=%t developer=%q pane=%q trigger=%q", taskID, taskStarted, info.developer, developer.PaneID, trigger.Class)
	if a.activeTask != nil {
		budget := a.taskDeadline
		if budget <= 0 {
			budget = defaultTaskDeadline
		}
		deadline := a.activeTask.StartedAt.Add(budget)
		if !deadline.IsZero() {
			var cancel context.CancelFunc
			ctx, cancel = context.WithDeadline(ctx, deadline)
			defer cancel()
		}
	}
	service, err := a.accountService()
	if err != nil {
		return "", developer, fmt.Errorf("prepare automatic account recovery: %w", err)
	}
	catalog, err := service.Repository.LoadCatalog()
	if err != nil {
		return "", developer, fmt.Errorf("load stored agy accounts: %w", err)
	}
	currentID, err := a.currentAccountForRecovery(ctx, info, developer, catalog)
	if err != nil {
		return "", developer, err
	}
	if currentID != "" && currentID != catalog.DefaultAccountID {
		// The pane binding is the stronger statement about the running agy
		// process. Reconcile only non-secret catalog metadata while the original
		// developer is still visible. Do not replace the canonical credential:
		// that would make rollback ambiguous when the old catalog default was
		// stale.
		if err := service.SetDefaultAccount(currentID); err != nil {
			return "", developer, fmt.Errorf("reconcile active agy account before recovery: %w", err)
		}
		catalog, err = service.Repository.LoadCatalog()
		if err != nil {
			return "", developer, fmt.Errorf("reload reconciled account catalog: %w", err)
		}
	}
	originalID := currentID

	sessionID, sessionErr := exactAgySessionID(developer)
	if sessionErr != nil && taskStarted {
		return "", developer, fmt.Errorf("automatic account recovery needs the exact agy conversation: %w", sessionErr)
	}
	if sessionErr != nil {
		// Before the first task, agy may still be in the verified pending state
		// and have no conversation ID. It is safe to start a fresh conversation
		// after switching because no task or prior conversation work is being
		// resumed. Once work starts, exact identity is mandatory.
		sessionID = ""
		a.debugf("recovery fresh-session task=%q reason=%q", taskID, "conversation-not-created-before-first-task")
	}
	paneID := developer.PaneID
	currentPrompt := originalTask
	attempted := map[string]struct{}{}
	if currentID != "" {
		attempted[currentID] = struct{}{}
	}

	// A final response can race the quota detector. Deliver it instead of
	// stopping or continuing a task that has already finished.
	if taskStarted {
		if output, found := a.completedTrackedPrompt(developer, currentPrompt); found {
			a.debugf("recovery skipped task=%q reason=%q response_bytes=%d", taskID, "completed-before-switch", len(output))
			return output, developer, nil
		}
	}

	if currentID != "" {
		if err := service.RecordRotationObservation(currentID, trigger.snapshot()); err != nil {
			return "", developer, fmt.Errorf("record exhausted agy account: %w", err)
		}
	}
	catalog, err = service.Repository.LoadCatalog()
	if err != nil {
		return "", developer, err
	}
	missing, err := missingAccountCredentials(ctx, service, catalog)
	if err != nil {
		return "", developer, err
	}
	candidates := accounts.SelectCandidates(catalog, accounts.SelectionOptions{CurrentID: currentID, AttemptedIDs: attempted, MissingVaultIDs: missing, Now: a.now().UTC(), MaxVerificationAge: recoveryQuotaCacheAge})
	a.debugf("recovery candidates task=%q current=%q candidates=%d missing=%d", taskID, debugAccountID(currentID), len(candidates), len(missing))
	if len(candidates) == 0 {
		a.warnTrackedFailure(taskFailureQuotaAccountsUnavailable, developer)
		return "", developer, noRecoveryAccountError(catalog, missing, recoverySummary{})
	}

	fmt.Fprintln(a.stderr, "cagy: quota is low; switching to another stored account automatically")
	if err := a.stopForRecovery(ctx, info.developer); err != nil {
		failureCode := taskFailureQuotaRecoveryStopFailed
		if errors.Is(err, errAgentReleaseTimeout) {
			failureCode = taskFailureQuotaRecoveryStopTimeout
		}
		a.warnTrackedFailure(failureCode, developer)
		return "", developer, fmt.Errorf("stop quota-limited agy before account switch: %w", err)
	}
	developerRunning := false
	var finalOutput string
	var terminalErr error
	terminalAgent := developer
	rotation, rotateErr := a.rotateAccounts(ctx, service, currentID, trigger, attempted, rotationHooks{
		AfterSwitch: func(ctx context.Context, activated accounts.Account) error {
			restarted, err := a.startForRecovery(ctx, info, paneID, sessionID)
			if err != nil {
				return fmt.Errorf("restart agy after automatic account switch: %w", err)
			}
			developer = restarted
			terminalAgent = restarted
			developerRunning = true
			currentID = activated.ID
			return nil
		},
		Probe: func(ctx context.Context) quotaProbeResult {
			probe := a.probeForRecovery(ctx, info.developer)
			a.debugf("recovery candidate-quota task=%q current=%q class=%q reason=%q", taskID, debugAccountID(currentID), probe.Class, probe.Reason)
			return probe
		},
		Reject: func(ctx context.Context, _ accounts.Account, probe quotaProbeResult) error {
			if err := a.stopForRecovery(ctx, info.developer); err != nil {
				return fmt.Errorf("stop rejected account (%s): %w", probe.Class, err)
			}
			developerRunning = false
			return nil
		},
		Accept: func(ctx context.Context, activated accounts.Account, _ quotaProbeResult) error {
			if err := a.bindForRecovery(ctx, info.developer, paneID, activated.ID); err != nil {
				return err
			}
			fmt.Fprintln(a.stderr, "cagy: healthy stored account selected; continuing the task")
			return nil
		},
		RunAccepted: func(ctx context.Context, activated accounts.Account, _ quotaProbeResult) (bool, error) {
			prompt := originalTask
			phase := taskPhaseSubmitting
			if taskStarted {
				prompt = continuationPrompt(originalTask)
				phase = taskPhaseMonitoring
			}
			checkpoint, err := a.checkpointForRecovery(developer)
			if err != nil {
				return false, fmt.Errorf("prepare recovered agy transcript: %w", err)
			}
			if err := a.replaceTrackedPrompt(developer, prompt, checkpoint, phase); err != nil {
				return false, fmt.Errorf("update recovered task state: %w", err)
			}
			result, taskErr := a.runTaskForRecovery(ctx, info.developer, prompt, checkpoint)
			if result.agent.PaneID != "" {
				developer = result.agent
				terminalAgent = result.agent
			}
			if result.needsQuotaRecovery() {
				if output, found := a.completedTrackedPrompt(developer, prompt); found {
					finalOutput = output
					return false, nil
				}
				if err := service.RecordRotationObservation(activated.ID, result.quota.snapshot()); err != nil {
					return false, fmt.Errorf("record newly exhausted agy: %w", err)
				}
				currentPrompt = prompt
				taskStarted = true
				if err := a.stopForRecovery(ctx, info.developer); err != nil {
					return false, fmt.Errorf("stop newly exhausted agy: %w", err)
				}
				developerRunning = false
				return true, nil
			}
			if taskErr != nil {
				terminalErr = taskErr
				return false, nil
			}
			if result.agent.AgentStatus == "blocked" {
				a.warnTrackedPhase(taskPhaseBlocked, result.agent)
				terminalErr = errors.New("developer is blocked; check the right pane")
				return false, nil
			}
			if strings.TrimSpace(result.output) == "" {
				terminalErr = errors.New("agy finished without readable output")
				return false, nil
			}
			finalOutput = strings.TrimSpace(result.output)
			return false, nil
		},
	})
	if terminalErr != nil {
		return "", terminalAgent, terminalErr
	}
	if finalOutput != "" {
		return finalOutput, developer, nil
	}
	if rotateErr == nil {
		return "", developer, errors.New("agy recovery finished without task output")
	}
	if developerRunning {
		_ = a.stopForRecovery(ctx, info.developer)
	}
	refreshUnavailable := accounts.CredentialRefreshUnavailable(rotateErr)
	var restored herdr.AgentInfo
	var restoreErr error
	if refreshUnavailable {
		restored, restoreErr = a.restartUnchangedRecoveryDeveloper(ctx, info, service, paneID, sessionID, originalID)
	} else {
		restored, restoreErr = a.restoreRecoveryDeveloper(ctx, info, service, paneID, sessionID, originalID)
	}
	if restoreErr == nil {
		developer = restored
		failureCode := taskFailureQuotaAccountsUnavailable
		if refreshUnavailable {
			failureCode = taskFailureQuotaRefreshUnavailable
		}
		a.warnTrackedFailure(failureCode, developer)
		if !trigger.ObservedAt.IsZero() {
			_ = service.RecordQuota(originalID, trigger.snapshot())
		}
	} else {
		a.warnTrackedFailure(taskFailureQuotaOriginalRestoreFailed, developer)
	}
	a.debugf("recovery exhausted task=%q attempted=%d low=%d unknown=%d failed=%d restored=%t restore_error=%q", taskID, rotation.Summary.Attempted, rotation.Summary.Low, rotation.Summary.Unknown, rotation.Summary.Failed, restoreErr == nil, restoreErr)
	if restoreErr != nil {
		return "", developer, fmt.Errorf("%w; original developer could not be restored: %v", rotateErr, restoreErr)
	}
	return "", developer, fmt.Errorf("%w; original developer was restored", rotateErr)

}

func missingAccountCredentials(ctx context.Context, service *accounts.AccountService, catalog accounts.Catalog) (map[string]struct{}, error) {
	missing := make(map[string]struct{})
	inspector, ok := service.Vault.(accounts.CredentialVaultInspector)
	if !ok {
		return missing, nil
	}
	for _, account := range catalog.Accounts {
		exists, err := inspector.Exists(ctx, account.ID)
		if err != nil {
			return nil, fmt.Errorf("inspect stored account credential: %w", err)
		}
		if !exists {
			missing[account.ID] = struct{}{}
		}
	}
	return missing, nil
}

func noRecoveryAccountError(catalog accounts.Catalog, missing map[string]struct{}, summary recoverySummary) error {
	needsLogin, disabled := 0, 0
	for _, account := range catalog.Accounts {
		switch account.State {
		case accounts.StateNeedsLogin:
			needsLogin++
		case accounts.StateDisabled:
			disabled++
		}
	}
	return fmt.Errorf("no healthy stored agy account is available (tried %d, low %d, unverifiable %d, failed %d, missing credentials %d, needs login %d, disabled %d); add or refresh an account with cagy accounts add", summary.attempted, summary.low, summary.unknown, summary.failed, len(missing), needsLogin, disabled)
}

func (a *App) completedTrackedPrompt(developer herdr.AgentInfo, prompt string) (string, bool) {
	if a.activeTask == nil || developer.AgentSession == nil {
		return "", false
	}
	sessionID, err := exactAgySessionID(developer)
	if err != nil {
		return "", false
	}
	checkpoint, err := a.journalCheckpoint(*a.activeTask, sessionID)
	if err != nil {
		return "", false
	}
	state, err := a.completedTranscriptState(checkpoint, developer, prompt)
	if err != nil || !state.Found || strings.TrimSpace(state.Response) == "" {
		return "", false
	}
	return strings.TrimSpace(state.Response), true
}

func (a *App) restartUnchangedRecoveryDeveloper(ctx context.Context, info runtimeContext, service *accounts.AccountService, paneID, sessionID, originalAccountID string) (herdr.AgentInfo, error) {
	if originalAccountID == "" {
		return herdr.AgentInfo{}, errors.New("original stored account is unknown")
	}
	catalog, err := service.Repository.LoadCatalog()
	if err != nil {
		return herdr.AgentInfo{}, fmt.Errorf("verify unchanged original account: %w", err)
	}
	if catalog.DefaultAccountID != originalAccountID {
		return herdr.AgentInfo{}, errors.New("original account is no longer the active stored account")
	}
	if _, exists, err := service.Repository.LoadTransaction(); err != nil {
		return herdr.AgentInfo{}, fmt.Errorf("verify account recovery transaction: %w", err)
	} else if exists {
		return herdr.AgentInfo{}, errors.New("account recovery transaction is still incomplete")
	}
	restarted, err := a.startForRecovery(ctx, info, paneID, sessionID)
	if err != nil {
		return herdr.AgentInfo{}, err
	}
	if err := a.bindForRecovery(ctx, info.developer, paneID, originalAccountID); err != nil {
		return herdr.AgentInfo{}, err
	}
	return restarted, nil
}

func (a *App) restoreRecoveryDeveloper(ctx context.Context, info runtimeContext, service *accounts.AccountService, paneID, sessionID, originalAccountID string) (herdr.AgentInfo, error) {
	if originalAccountID == "" {
		return herdr.AgentInfo{}, errors.New("original stored account is unknown")
	}
	if _, err := service.Switch(ctx, originalAccountID); err != nil {
		return herdr.AgentInfo{}, fmt.Errorf("restore original account: %w", err)
	}
	restarted, err := a.startForRecovery(ctx, info, paneID, sessionID)
	if err != nil {
		return herdr.AgentInfo{}, err
	}
	if err := a.bindForRecovery(ctx, info.developer, paneID, originalAccountID); err != nil {
		return herdr.AgentInfo{}, err
	}
	return restarted, nil
}

func (a *App) currentAccountForRecovery(ctx context.Context, info runtimeContext, developer herdr.AgentInfo, catalog accounts.Catalog) (string, error) {
	if a.recoveryCurrentAccount != nil {
		return a.recoveryCurrentAccount(ctx, info, developer, catalog)
	}
	pane, err := a.herdr.GetPane(ctx, developer.PaneID)
	if err != nil {
		return "", fmt.Errorf("read developer account binding: %w", err)
	}
	boundID, err := a.boundAccountID(info.developer, pane)
	if err != nil {
		return "", fmt.Errorf("resolve developer account binding: %w", err)
	}
	if boundID == "" {
		return strings.TrimSpace(catalog.DefaultAccountID), nil
	}
	if _, found := catalog.Find(boundID); !found {
		return "", errors.New("developer account binding is not present in the stored account catalog")
	}
	return boundID, nil
}

func (a *App) stopForRecovery(ctx context.Context, developer string) error {
	if a.recoveryStop != nil {
		return a.recoveryStop(ctx, developer)
	}
	return a.interruptAndWaitAgent(ctx, developer)
}

func (a *App) startForRecovery(ctx context.Context, info runtimeContext, paneID, sessionID string) (herdr.AgentInfo, error) {
	if a.recoveryStart != nil {
		return a.recoveryStart(ctx, info, paneID, sessionID)
	}
	return a.startDeveloperInPlace(ctx, info, paneID, sessionID)
}

func (a *App) probeForRecovery(ctx context.Context, developer string) quotaProbeResult {
	if a.recoveryProbe != nil {
		return a.recoveryProbe(ctx, developer)
	}
	return a.probeDeveloperQuota(ctx, developer)
}

func (a *App) checkpointForRecovery(developer herdr.AgentInfo) (transcript.Checkpoint, error) {
	if a.recoveryCheckpoint != nil {
		return a.recoveryCheckpoint(developer)
	}
	return a.transcriptCheckpoint(developer)
}

func (a *App) bindForRecovery(ctx context.Context, developer, paneID, accountID string) error {
	if a.recoveryBind != nil {
		return a.recoveryBind(ctx, developer, paneID, accountID)
	}
	return a.bindDeveloperAccount(ctx, developer, paneID, accountID)
}

func (a *App) runTaskForRecovery(ctx context.Context, developer, prompt string, checkpoint transcript.Checkpoint) (developerTaskResult, error) {
	if a.recoveryRunTask != nil {
		return a.recoveryRunTask(ctx, developer, prompt, checkpoint)
	}
	before, _ := a.herdr.ReadAgent(ctx, developer, 400)
	return a.runDeveloperTask(ctx, developer, prompt, before, checkpoint)
}

func (a *App) confirmClosePane(ctx context.Context, paneID string) error {
	const maxRetries = 3
	a.debugf("pane-close begin pane=%q max_attempts=%d", paneID, maxRetries)
	var lastErr error
	for attempt := 1; attempt <= maxRetries; attempt++ {
		closeErr := a.herdr.ClosePane(ctx, paneID)
		if closeErr != nil && herdr.IsCode(closeErr, "pane_not_found") {
			a.debugf("pane-close success pane=%q attempt=%d already_missing=true", paneID, attempt)
			return nil
		}
		if closeErr != nil {
			lastErr = closeErr
		}
		_, getErr := a.herdr.GetPane(ctx, paneID)
		if herdr.IsCode(getErr, "pane_not_found") {
			a.debugf("pane-close success pane=%q attempt=%d", paneID, attempt)
			return nil
		}
		if getErr == nil {
			lastErr = fmt.Errorf("pane %s is still present", paneID)
		} else if !herdr.IsCode(getErr, "pane_not_found") {
			lastErr = getErr
		}
		if attempt < maxRetries {
			a.debugf("pane-close retry pane=%q attempt=%d close_error=%q verify_error=%q", paneID, attempt, closeErr, getErr)
			select {
			case <-ctx.Done():
				a.debugf("pane-close cancelled pane=%q attempt=%d error=%q", paneID, attempt, ctx.Err())
				return ctx.Err()
			case <-time.After(10 * time.Millisecond):
			}
		}
	}
	a.debugf("pane-close failed pane=%q attempts=%d error=%q", paneID, maxRetries, lastErr)
	return fmt.Errorf("close pane %s could not be confirmed: %w", paneID, lastErr)
}

func (a *App) ensureAgyReady(ctx context.Context, paneID string) error {
	a.debugf("agy-ready begin pane=%q", paneID)
	output, err := a.herdr.ReadPane(ctx, paneID, 200)
	if err != nil {
		return fmt.Errorf("read agy startup: %w", err)
	}
	if strings.Contains(output, "Do you trust the contents of this project?") {
		a.debugf("agy-ready trust-prompt pane=%q", paneID)
		if !strings.Contains(output, "> Yes, I trust this folder") {
			return fmt.Errorf("agy project trust prompt is not on the Yes option")
		}
		if err := a.herdr.SendPaneKeys(ctx, paneID, "enter"); err != nil {
			return fmt.Errorf("accept agy project trust: %w", err)
		}
	}
	if err := a.herdr.WaitPaneMatch(ctx, paneID, "? for shortcuts", agyReadyTimeoutMS); err != nil {
		a.debugf("agy-ready failed pane=%q error=%q", paneID, err)
		return fmt.Errorf("wait for agy prompt: %w", err)
	}
	a.debugf("agy-ready success pane=%q", paneID)
	return nil
}

func continuationPrompt(_ string) string {
	return "Continue the same interrupted task in this exact conversation. Inspect the current working tree first, do not repeat completed work, and do not wait for the original prompt to be resent."
}
