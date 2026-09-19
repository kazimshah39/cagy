package accounts

import (
	"strings"
	"testing"
	"time"
)

func testAccount(t *testing.T, subject, label, email string) Account {
	t.Helper()
	id, err := StableID(subject)
	if err != nil {
		t.Fatal(err)
	}
	fingerprint, err := CredentialFingerprint([]byte("credential-" + subject))
	if err != nil {
		t.Fatal(err)
	}
	now := time.Date(2026, time.September, 18, 12, 0, 0, 0, time.UTC)
	weekly, five := 0.8, 0.7
	return Account{
		ID: id, Provider: ProviderGoogle, Label: label, Email: email,
		CredentialFingerprint: fingerprint, State: StateHealthy, LastVerifiedAt: now,
		Quota: &QuotaSnapshot{Class: QuotaAvailable, WeeklyRemaining: &weekly, FiveHourRemaining: &five, ObservedAt: now},
	}
}

func TestStableIDAndFingerprint(t *testing.T) {
	first, _ := StableID("provider-subject")
	second, _ := StableID("provider-subject")
	if first != second || len(first) != 64 {
		t.Fatalf("stable IDs %q %q", first, second)
	}
	if _, err := StableID(" "); err == nil {
		t.Fatal("expected empty subject rejection")
	}
}

func TestCatalogValidationRejectsDuplicatesAndBadDefaults(t *testing.T) {
	account := testAccount(t, "one", "Primary", "one@example.com")
	catalog := Catalog{Version: CatalogVersion, DefaultAccountID: account.ID, Accounts: []Account{account}}
	if err := catalog.Validate(); err != nil {
		t.Fatal(err)
	}
	catalog.Accounts = append(catalog.Accounts, account)
	if err := catalog.Validate(); err == nil || !strings.Contains(err.Error(), "duplicate") {
		t.Fatalf("duplicate error=%v", err)
	}
	catalog.Accounts = []Account{account}
	catalog.DefaultAccountID = strings.Repeat("f", 64)
	if err := catalog.Validate(); err == nil || !strings.Contains(err.Error(), "default") {
		t.Fatalf("default error=%v", err)
	}
}

func TestAccountAndQuotaValidation(t *testing.T) {
	account := testAccount(t, "one", "Primary", "one@example.com")
	if err := account.Validate(); err != nil {
		t.Fatal(err)
	}
	bad := account
	bad.Label = "  Primary  "
	if err := bad.Validate(); err == nil {
		t.Fatal("expected non-normalized label rejection")
	}
	bad = account
	value := 1.1
	bad.Quota.WeeklyRemaining = &value
	if err := bad.Validate(); err == nil {
		t.Fatal("expected quota bound rejection")
	}
}

func TestTransactionValidation(t *testing.T) {
	now := time.Date(2026, time.September, 18, 12, 0, 0, 0, time.UTC)
	transaction := Transaction{Version: TransactionVersion, OperationID: "operation-123", Kind: TransactionSwitch, Phase: PhasePrepared, StartedAt: now, UpdatedAt: now}
	if err := transaction.Validate(); err != nil {
		t.Fatal(err)
	}
	transaction.Phase = "unsafe"
	if err := transaction.Validate(); err == nil {
		t.Fatal("expected invalid phase rejection")
	}
}
