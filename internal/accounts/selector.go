package accounts

import (
	"math"
	"sort"
	"time"
)

const (
	WeeklySwitchThreshold   = 0.03
	FiveHourSwitchThreshold = 0.02
)

type CandidateTier string

const (
	CandidateKnownAvailable CandidateTier = "known_available"
	CandidateNeedsProbe     CandidateTier = "needs_probe"
)

type Candidate struct {
	Account Account
	Tier    CandidateTier
	Score   float64
}

type SelectionOptions struct {
	CurrentID          string
	AttemptedIDs       map[string]struct{}
	MissingVaultIDs    map[string]struct{}
	Now                time.Time
	MaxVerificationAge time.Duration
}

// SelectCandidates returns every account that is safe to try during one
// automatic recovery cycle. Fresh known-good accounts come first. Accounts
// with stale or unknown quota follow and must be live-probed after agy starts.
// Fresh low/exhausted accounts are deferred until their observation becomes
// stale so one recovery cycle cannot immediately churn through known-bad
// credentials.
func SelectCandidates(catalog Catalog, options SelectionOptions) []Candidate {
	if options.Now.IsZero() {
		options.Now = time.Now()
	}
	if options.MaxVerificationAge <= 0 {
		options.MaxVerificationAge = 10 * time.Minute
	}
	candidates := make([]Candidate, 0, len(catalog.Accounts))
	for _, account := range catalog.Accounts {
		if account.ID == options.CurrentID || account.State == StateDisabled || account.State == StateNeedsLogin {
			continue
		}
		if _, attempted := options.AttemptedIDs[account.ID]; attempted {
			continue
		}
		if _, missing := options.MissingVaultIDs[account.ID]; missing {
			continue
		}
		if !account.CooldownUntil.IsZero() && options.Now.Before(account.CooldownUntil) {
			continue
		}

		freshQuota := account.Quota != nil && timestampFresh(account.Quota.ObservedAt, options.Now, options.MaxVerificationAge)
		candidate := Candidate{Account: account, Tier: CandidateNeedsProbe}
		if freshQuota {
			switch account.Quota.Class {
			case QuotaLow, QuotaExhausted:
				continue
			case QuotaAvailable:
				if account.Quota.WeeklyRemaining == nil || account.Quota.FiveHourRemaining == nil {
					continue
				}
				weekly := *account.Quota.WeeklyRemaining
				fiveHour := *account.Quota.FiveHourRemaining
				if weekly <= WeeklySwitchThreshold || fiveHour <= FiveHourSwitchThreshold {
					continue
				}
				candidate.Tier = CandidateKnownAvailable
				candidate.Score = math.Min(weekly, fiveHour)
			}
		}
		candidates = append(candidates, candidate)
	}

	sort.SliceStable(candidates, func(left, right int) bool {
		if candidates[left].Tier != candidates[right].Tier {
			return candidates[left].Tier == CandidateKnownAvailable
		}
		if candidates[left].Score != candidates[right].Score {
			return candidates[left].Score > candidates[right].Score
		}
		if !candidates[left].Account.LastVerifiedAt.Equal(candidates[right].Account.LastVerifiedAt) {
			return candidates[left].Account.LastVerifiedAt.After(candidates[right].Account.LastVerifiedAt)
		}
		if candidates[left].Account.ConsecutiveFailures != candidates[right].Account.ConsecutiveFailures {
			return candidates[left].Account.ConsecutiveFailures < candidates[right].Account.ConsecutiveFailures
		}
		if !candidates[left].Account.LastUsedAt.Equal(candidates[right].Account.LastUsedAt) {
			if candidates[left].Account.LastUsedAt.IsZero() {
				return true
			}
			if candidates[right].Account.LastUsedAt.IsZero() {
				return false
			}
			return candidates[left].Account.LastUsedAt.Before(candidates[right].Account.LastUsedAt)
		}
		return candidates[left].Account.ID < candidates[right].Account.ID
	})
	return candidates
}

func timestampFresh(observedAt, now time.Time, maxAge time.Duration) bool {
	if observedAt.IsZero() || observedAt.After(now.Add(time.Minute)) {
		return false
	}
	return now.Sub(observedAt) <= maxAge
}

func RecordFailure(account Account, now time.Time, permanent bool) Account {
	if now.IsZero() {
		now = time.Now()
	}
	account.LastFailureAt = now.UTC()
	account.ConsecutiveFailures++
	if permanent {
		account.State = StateNeedsLogin
		account.CooldownUntil = time.Time{}
		return account
	}
	exponent := account.ConsecutiveFailures - 1
	if exponent > 6 {
		exponent = 6
	}
	cooldown := time.Minute * time.Duration(1<<exponent)
	if cooldown > time.Hour {
		cooldown = time.Hour
	}
	account.CooldownUntil = now.Add(cooldown).UTC()
	return account
}

func RecordSuccess(account Account, now time.Time, quota QuotaSnapshot) Account {
	if now.IsZero() {
		now = time.Now()
	}
	account.LastVerifiedAt = now.UTC()
	account.ConsecutiveFailures = 0
	account.LastFailureAt = time.Time{}
	account.CooldownUntil = time.Time{}
	account.Quota = &quota
	if quota.Class == QuotaAvailable {
		account.State = StateHealthy
	} else if quota.Class == QuotaLow || quota.Class == QuotaExhausted {
		account.State = StateLowQuota
	} else {
		account.State = StateUnknown
	}
	return account
}
