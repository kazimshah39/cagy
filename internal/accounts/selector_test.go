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

func rotationCatalog(accounts ...Account) Catalog {
	catalog := Catalog{Version: CatalogVersion, Accounts: accounts}
	for _, account := range accounts {
		catalog.Rotation = &RotationState{Order: append(catalog.RotationOrder(), account.ID)}
	}
	return catalog
}

func TestSelectCandidatesUsesCircularOrderAfterCursor(t *testing.T) {
	now := time.Unix(1000, 0).UTC()
	a := selectorAccount("a", StateHealthy, now, QuotaAvailable, .8, .8)
	b := selectorAccount("b", StateHealthy, now, QuotaAvailable, .2, .2)
	c := selectorAccount("c", StateHealthy, now, QuotaAvailable, .7, .7)
	d := selectorAccount("d", StateHealthy, now, QuotaAvailable, .9, .9)
	catalog := rotationCatalog(a, b, c, d)
	catalog.Rotation.CursorAccountID = b.ID
	catalog.Rotation.UpdatedAt = now
	got := SelectCandidates(catalog, SelectionOptions{Now: now})
	want := []string{"c", "d", "a", "b"}
	for i, id := range want {
		if got[i].Account.ID != id {
			t.Fatalf("index=%d got=%s want=%s", i, got[i].Account.ID, id)
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
	catalog := rotationCatalog(base, current, attempted, missing, disabled, needsLogin, cooldown, freshLow, freshExhausted, boundary)
	got := SelectCandidates(catalog, SelectionOptions{CurrentID: current.ID, AttemptedIDs: map[string]struct{}{attempted.ID: {}}, MissingVaultIDs: map[string]struct{}{missing.ID: {}}, Now: now})
	if len(got) != 1 || got[0].Account.ID != base.ID {
		t.Fatalf("got=%+v", got)
	}
}

func TestSelectCandidatesStaleLowBecomesProbeCandidate(t *testing.T) {
	now := time.Unix(1000, 0).UTC()
	account := selectorAccount("stale", StateLowQuota, now.Add(-11*time.Minute), QuotaExhausted, 0, 0)
	got := SelectCandidates(rotationCatalog(account), SelectionOptions{Now: now, MaxVerificationAge: 10 * time.Minute})
	if len(got) != 1 || got[0].Tier != CandidateNeedsProbe {
		t.Fatalf("got=%+v", got)
	}
}

func TestSelectCandidatesDoesNotReorderByQuotaScore(t *testing.T) {
	now := time.Unix(1000, 0).UTC()
	low := selectorAccount("low", StateHealthy, now, QuotaAvailable, .2, .2)
	high := selectorAccount("high", StateHealthy, now, QuotaAvailable, .9, .9)
	got := SelectCandidates(rotationCatalog(low, high), SelectionOptions{Now: now})
	if got[0].Account.ID != low.ID || got[1].Account.ID != high.ID {
		t.Fatalf("got=%+v", got)
	}
}
