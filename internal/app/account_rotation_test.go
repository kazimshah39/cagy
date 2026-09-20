package app

import (
	"context"
	"fmt"
	"strings"
	"testing"

	"github.com/kazimshah39/cagy/internal/accounts"
)

type rotationValidatorFunc func(context.Context, []byte) (accounts.ValidationResult, error)

func (f rotationValidatorFunc) Validate(ctx context.Context, credential []byte) (accounts.ValidationResult, error) {
	return f(ctx, credential)
}

func TestAccountRotationUsesPersistentCursorAndWraps(t *testing.T) {
	fixture := newRecoveryFixture(t, 4)
	catalog, err := fixture.service.Repository.LoadCatalog()
	if err != nil {
		t.Fatal(err)
	}
	catalog.Rotation.CursorAccountID = fixture.accounts[0].ID
	catalog.Rotation.UpdatedAt = fixture.app.now()
	if err := fixture.service.Repository.SaveCatalog(catalog); err != nil {
		t.Fatal(err)
	}
	fixture.probeQueue = []quotaProbeResult{availableRecoveryQuota(fixture.app.now())}
	result, err := fixture.app.rotateAccounts(context.Background(), fixture.service, fixture.accounts[0].ID, exhaustedRecoveryQuota(fixture.app.now()), nil, rotationHooks{Probe: func(context.Context) quotaProbeResult {
		probe := fixture.probeQueue[0]
		fixture.probeQueue = fixture.probeQueue[1:]
		return probe
	}})
	if err != nil || result.Account.ID != fixture.accounts[1].ID {
		t.Fatalf("result=%+v err=%v", result, err)
	}
	loaded, _ := fixture.service.Repository.LoadCatalog()
	if loaded.Rotation.CursorAccountID != fixture.accounts[1].ID {
		t.Fatalf("cursor=%s want=%s", loaded.Rotation.CursorAccountID, fixture.accounts[1].ID)
	}
}

func TestAccountRotationTriesEachAccountOnceAndStops(t *testing.T) {
	fixture := newRecoveryFixture(t, 3)
	fixture.probeQueue = []quotaProbeResult{exhaustedRecoveryQuota(fixture.app.now()), exhaustedRecoveryQuota(fixture.app.now())}
	_, err := fixture.app.rotateAccounts(context.Background(), fixture.service, fixture.accounts[0].ID, exhaustedRecoveryQuota(fixture.app.now()), nil, rotationHooks{Probe: func(context.Context) quotaProbeResult {
		probe := fixture.probeQueue[0]
		fixture.probeQueue = fixture.probeQueue[1:]
		return probe
	}})
	if err == nil || !strings.Contains(err.Error(), "no healthy stored agy account") {
		t.Fatalf("err=%v", err)
	}
	loaded, _ := fixture.service.Repository.LoadCatalog()
	if loaded.Rotation.CursorAccountID != fixture.accounts[2].ID {
		t.Fatalf("cursor=%s want last attempted=%s", loaded.Rotation.CursorAccountID, fixture.accounts[2].ID)
	}
}

func TestAccountRotationUnknownCandidateIsSkippedWithCooldown(t *testing.T) {
	fixture := newRecoveryFixture(t, 2)
	unknown := quotaProbeResult{Class: quotaUnknown, Reason: "malformed", ObservedAt: fixture.app.now()}
	fixture.probeQueue = []quotaProbeResult{unknown}
	_, err := fixture.app.rotateAccounts(context.Background(), fixture.service, fixture.accounts[0].ID, exhaustedRecoveryQuota(fixture.app.now()), nil, rotationHooks{Probe: func(context.Context) quotaProbeResult {
		probe := fixture.probeQueue[0]
		fixture.probeQueue = fixture.probeQueue[1:]
		return probe
	}})
	if err == nil {
		t.Fatal("expected exhausted pool")
	}
	loaded, _ := fixture.service.Repository.LoadCatalog()
	candidate, _ := loaded.Find(fixture.accounts[1].ID)
	if candidate.ConsecutiveFailures != 1 || candidate.CooldownUntil.IsZero() {
		t.Fatalf("candidate failure state=%+v", candidate)
	}
}

func TestAccountRotationStopsOnGlobalCredentialRefreshFailure(t *testing.T) {
	fixture := newRecoveryFixture(t, 3)
	fixture.service.Validator = rotationValidatorFunc(func(context.Context, []byte) (accounts.ValidationResult, error) {
		return accounts.ValidationResult{}, fmt.Errorf("temporary refresh outage: %w", accounts.ErrCredentialRefreshUnavailable)
	})

	result, err := fixture.app.rotateAccounts(context.Background(), fixture.service, fixture.accounts[0].ID, exhaustedRecoveryQuota(fixture.app.now()), nil, rotationHooks{})
	if err == nil || !accounts.CredentialRefreshUnavailable(err) {
		t.Fatalf("result=%+v err=%v", result, err)
	}
	if result.Summary.Attempted != 1 || result.Summary.Failed != 0 {
		t.Fatalf("summary=%+v", result.Summary)
	}
	loaded, loadErr := fixture.service.Repository.LoadCatalog()
	if loadErr != nil {
		t.Fatal(loadErr)
	}
	for _, account := range loaded.Accounts[1:] {
		if account.ConsecutiveFailures != 0 || !account.LastFailureAt.IsZero() || account.State != accounts.StateHealthy {
			t.Fatalf("global failure incorrectly penalized account: %+v", account)
		}
	}
}
