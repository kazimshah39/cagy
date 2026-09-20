package accounts

import (
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
// automatic recovery cycle in persistent circular order. Fresh low/exhausted
// observations remain deferred until stale; stale and unknown accounts are
// returned for live probing.
func SelectCandidates(catalog Catalog, options SelectionOptions) []Candidate {
	if options.Now.IsZero() {
		options.Now = time.Now()
	}
	if options.MaxVerificationAge <= 0 {
		options.MaxVerificationAge = 10 * time.Minute
	}
	byID := make(map[string]Account, len(catalog.Accounts))
	for _, account := range catalog.Accounts {
		byID[account.ID] = account
	}
	order := catalog.RotationOrder()
	if len(order) == 0 {
		for _, account := range catalog.Accounts {
			order = append(order, account.ID)
		}
	}
	cursor := ""
	if catalog.Rotation != nil {
		cursor = catalog.Rotation.CursorAccountID
	}
	start := 0
	if cursor != "" {
		for index, id := range order {
			if id == cursor {
				start = (index + 1) % len(order)
				break
			}
		}
	}
	candidates := make([]Candidate, 0, len(order))
	for offset := 0; offset < len(order); offset++ {
		account, found := byID[order[(start+offset)%len(order)]]
		if !found {
			continue
		}
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
			}
		}
		candidates = append(candidates, candidate)
	}
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
