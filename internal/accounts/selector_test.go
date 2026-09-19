package accounts

import (
	"testing"
	"time"
)

func selectorAccount(id string, state State, verified time.Time, quotaClass QuotaClass, weekly, five float64) Account {
	account := Account{ID: id, Provider: ProviderGoogle, Label: id, Email: id + "@example.com", State: state, LastVerifiedAt: verified}
	if quotaClass != "" {
		account.Quota = &QuotaSnapshot{Class: quotaClass, ObservedAt: verified, WeeklyRemaining: &weekly, FiveHourRemaining: &five}
	}
	return account
}

func TestSelectCandidatesRanksKnownAvailableBeforeNeedsProbe(t *testing.T) {
	now := time.Unix(1000, 0).UTC()
	known := selectorAccount("known", StateHealthy, now.Add(-time.Minute), QuotaAvailable, .7, .8)
	unknown := selectorAccount("unknown", StateUnknown, now.Add(-time.Minute), QuotaUnknown, 0, 0)
	staleLow := selectorAccount("stale-low", StateLowQuota, now.Add(-time.Hour), QuotaExhausted, 0, 0)

	got := SelectCandidates(Catalog{Accounts: []Account{unknown, staleLow, known}}, SelectionOptions{Now: now})
	if len(got) != 3 {
		t.Fatalf("candidates=%d want=3", len(got))
	}
	if got[0].Account.ID != known.ID || got[0].Tier != CandidateKnownAvailable {
		t.Fatalf("first=%+v", got[0])
	}
	for _, candidate := range got[1:] {
		if candidate.Tier != CandidateNeedsProbe {
			t.Fatalf("candidate=%+v want needs_probe", candidate)
		}
	}
}

func TestSelectCandidatesExcludesUnsafeAccounts(t *testing.T) {
	now := time.Unix(1000, 0).UTC()
	base := selectorAccount("safe", StateHealthy, now, QuotaAvailable, .8, .9)
	current := base
	current.ID, current.Label, current.Email = "current", "current", "current@example.com"
	attempted := base
	attempted.ID, attempted.Label, attempted.Email = "attempted", "attempted", "attempted@example.com"
	missing := base
	missing.ID, missing.Label, missing.Email = "missing", "missing", "missing@example.com"
	disabled := selectorAccount("disabled", StateDisabled, now, QuotaAvailable, .8, .9)
	needsLogin := selectorAccount("needs-login", StateNeedsLogin, now, QuotaAvailable, .8, .9)
	cooldown := base
	cooldown.ID, cooldown.Label, cooldown.Email, cooldown.CooldownUntil = "cooldown", "cooldown", "cooldown@example.com", now.Add(time.Minute)
	freshLow := selectorAccount("fresh-low", StateLowQuota, now, QuotaLow, .01, .9)
	freshExhausted := selectorAccount("fresh-exhausted", StateLowQuota, now, QuotaExhausted, 0, .9)
	boundary := selectorAccount("boundary", StateHealthy, now, QuotaAvailable, WeeklySwitchThreshold, .9)

	got := SelectCandidates(Catalog{Accounts: []Account{base, current, attempted, missing, disabled, needsLogin, cooldown, freshLow, freshExhausted, boundary}}, SelectionOptions{
		CurrentID:       current.ID,
		AttemptedIDs:    map[string]struct{}{attempted.ID: {}},
		MissingVaultIDs: map[string]struct{}{missing.ID: {}},
		Now:             now,
	})
	if len(got) != 1 || got[0].Account.ID != base.ID {
		t.Fatalf("got=%+v", got)
	}
}

func TestSelectCandidatesStaleLowBecomesProbeCandidate(t *testing.T) {
	now := time.Unix(1000, 0).UTC()
	account := selectorAccount("stale", StateLowQuota, now.Add(-11*time.Minute), QuotaExhausted, 0, 0)
	got := SelectCandidates(Catalog{Accounts: []Account{account}}, SelectionOptions{Now: now, MaxVerificationAge: 10 * time.Minute})
	if len(got) != 1 || got[0].Tier != CandidateNeedsProbe {
		t.Fatalf("got=%+v", got)
	}
}

func TestSelectCandidatesTieBreaksDeterministically(t *testing.T) {
	now := time.Unix(1000, 0).UTC()
	olderUsed := selectorAccount("older-used", StateHealthy, now, QuotaAvailable, .8, .8)
	olderUsed.LastUsedAt = now.Add(-2 * time.Hour)
	newerUsed := selectorAccount("newer-used", StateHealthy, now, QuotaAvailable, .8, .8)
	newerUsed.LastUsedAt = now.Add(-time.Hour)
	failed := selectorAccount("failed", StateHealthy, now, QuotaAvailable, .8, .8)
	failed.ConsecutiveFailures = 1
	neverUsed := selectorAccount("never-used", StateHealthy, now, QuotaAvailable, .8, .8)

	got := SelectCandidates(Catalog{Accounts: []Account{newerUsed, failed, olderUsed, neverUsed}}, SelectionOptions{Now: now})
	want := []string{"never-used", "older-used", "newer-used", "failed"}
	for index, id := range want {
		if got[index].Account.ID != id {
			t.Fatalf("index=%d got=%s want=%s all=%+v", index, got[index].Account.ID, id, got)
		}
	}
}
