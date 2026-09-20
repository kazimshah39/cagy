package app

import (
	"context"
	"fmt"

	"github.com/kazimshah39/cagy/internal/accounts"
)

// rotationHooks are the small workflow-specific lifecycle seams shared by
// standalone account switching and Herdr task recovery.
type rotationHooks struct {
	BeforeCandidate func(context.Context) error
	AfterSwitch     func(context.Context, accounts.Account) error
	Probe           func(context.Context) quotaProbeResult
	Reject          func(context.Context, accounts.Account, quotaProbeResult) error
	Accept          func(context.Context, accounts.Account, quotaProbeResult) error
	RunAccepted     func(context.Context, accounts.Account, quotaProbeResult) (bool, error)
}

type rotationSummary struct {
	Attempted int
	Low       int
	Unknown   int
	Failed    int
}

type rotationResult struct {
	Account  accounts.Account
	Quota    quotaProbeResult
	Summary  rotationSummary
	Original string
}

// rotateAccounts performs exactly one persistent, circular account pass. The
// account service owns durable cursor/health mutations; hooks own process or
// task lifecycle details and never receive credentials.
func (a *App) rotateAccounts(ctx context.Context, service *accounts.AccountService, currentID string, trigger quotaProbeResult, attempted map[string]struct{}, hooks rotationHooks) (rotationResult, error) {
	if attempted == nil {
		attempted = map[string]struct{}{}
	}
	a.debugf("rotation begin current=%q attempted=%d", debugAccountID(currentID), len(attempted))
	if currentID != "" {
		attempted[currentID] = struct{}{}
		if err := service.RecordRotationObservation(currentID, trigger.snapshot()); err != nil {
			return rotationResult{}, fmt.Errorf("record exhausted agy account: %w", err)
		}
		a.debugf("rotation cursor advanced account=%q class=%q", debugAccountID(currentID), trigger.Class)
	}
	originalID := currentID
	summary := rotationSummary{}
	for {
		catalog, err := service.Repository.LoadCatalog()
		if err != nil {
			return rotationResult{Summary: summary, Original: originalID}, fmt.Errorf("reload stored agy accounts: %w", err)
		}
		missing, err := missingAccountCredentials(ctx, service, catalog)
		if err != nil {
			return rotationResult{Summary: summary, Original: originalID}, err
		}
		candidates := accounts.SelectCandidates(catalog, accounts.SelectionOptions{
			CurrentID: currentID, AttemptedIDs: attempted, MissingVaultIDs: missing,
			Now: a.now().UTC(), MaxVerificationAge: recoveryQuotaCacheAge,
		})
		if len(candidates) == 0 {
			return rotationResult{Summary: summary, Original: originalID}, noRecoveryAccountError(catalog, missing, recoverySummary{attempted: summary.Attempted, low: summary.Low, unknown: summary.Unknown, failed: summary.Failed})
		}
		candidate := candidates[0]
		attempted[candidate.Account.ID] = struct{}{}
		summary.Attempted++
		a.debugf("rotation attempt number=%d account=%q tier=%q", summary.Attempted, debugAccountID(candidate.Account.ID), candidate.Tier)
		a.debugf("recovery attempt number=%d account=%q tier=%q", summary.Attempted, debugAccountID(candidate.Account.ID), candidate.Tier)
		if hooks.BeforeCandidate != nil {
			if err := hooks.BeforeCandidate(ctx); err != nil {
				return rotationResult{Summary: summary, Original: originalID}, err
			}
		}
		activated, switchErr := service.SwitchAutomatic(ctx, candidate.Account.ID)
		if switchErr != nil {
			summary.Failed++
			permanent := accounts.CredentialFailureNeedsLogin(switchErr)
			_ = service.RecordRotationFailure(candidate.Account.ID, permanent)
			continue
		}
		if hooks.AfterSwitch != nil {
			if err := hooks.AfterSwitch(ctx, activated); err != nil {
				return rotationResult{Summary: summary, Original: originalID}, err
			}
		}
		probe := quotaProbeResult{Class: quotaUnknown, Reason: "quota probe unavailable", ObservedAt: a.now().UTC()}
		if hooks.Probe != nil {
			probe = hooks.Probe(ctx)
		}
		a.debugf("rotation candidate-quota account=%q class=%q reason=%q", debugAccountID(candidate.Account.ID), probe.Class, probe.Reason)
		if err := service.RecordRotationObservation(candidate.Account.ID, probe.snapshot()); err != nil {
			return rotationResult{Summary: summary, Original: originalID}, fmt.Errorf("record candidate quota: %w", err)
		}
		switch probe.Class {
		case quotaAvailable:
			if hooks.Accept != nil {
				if err := hooks.Accept(ctx, activated, probe); err != nil {
					return rotationResult{Summary: summary, Original: originalID}, err
				}
			}
			if hooks.RunAccepted != nil {
				retry, err := hooks.RunAccepted(ctx, activated, probe)
				if err != nil {
					return rotationResult{Summary: summary, Original: originalID}, err
				}
				if retry {
					currentID = activated.ID
					continue
				}
			}
			return rotationResult{Account: activated, Quota: probe, Summary: summary, Original: originalID}, nil
		case quotaLow, quotaExhausted:
			summary.Low++
			if hooks.Reject != nil {
				if err := hooks.Reject(ctx, activated, probe); err != nil {
					return rotationResult{Summary: summary, Original: originalID}, err
				}
			}
		case quotaUnknown:
			summary.Unknown++
			_ = service.RecordRotationFailure(candidate.Account.ID, false)
			if hooks.Reject != nil {
				if err := hooks.Reject(ctx, activated, probe); err != nil {
					return rotationResult{Summary: summary, Original: originalID}, err
				}
			}
		}
	}
}
