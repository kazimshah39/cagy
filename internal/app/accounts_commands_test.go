package app

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/kazimshah39/cagy/internal/accounts"
	proc "github.com/kazimshah39/cagy/internal/process"
)

func TestAccountsAddUsesLocalOAuthWithoutCanonicalKeychain(t *testing.T) {
	service, vault, canonical, _, _, _ := accountCommandService(t)
	credential := []byte(`{"token":{"access_token":"new","refresh_token":"refresh","token_type":"Bearer","expiry":"2026-09-19T04:00:00.000000Z"},"auth_method":"consumer"}`)
	identity := service.Identity.(commandIdentity)
	identity.byCredential[string(credential)] = accounts.ProviderIdentity{Subject: "new", Email: "new@example.com", Name: "New Owner"}
	service.Identity = identity
	beforeCanonical := append([]byte(nil), canonical.value...)
	var output strings.Builder
	application := New(&scriptedRunner{t: t}, &output, &strings.Builder{})
	application.checkPlatform = func() error { return nil }
	application.accountsFactory = func() (*accounts.AccountService, error) { return service, nil }
	opened := false
	application.openURL = func(_ context.Context, target string) error {
		opened = strings.HasPrefix(target, "https://accounts.google.com/")
		return nil
	}
	application.accountLogin = func(_ context.Context, open func(string) error) ([]byte, error) {
		if err := open("https://accounts.google.com/o/oauth2/v2/auth?redacted-test"); err != nil {
			return nil, err
		}
		return append([]byte(nil), credential...), nil
	}
	if err := application.accountsCommand(context.Background(), []string{"add"}); err != nil {
		t.Fatal(err)
	}
	if !opened {
		t.Fatal("explicit add did not open Google OAuth")
	}
	newID, _ := accounts.StableID("new")
	if string(vault.items[newID]) != string(credential) {
		t.Fatal("new account was not stored locally")
	}
	if string(canonical.value) != string(beforeCanonical) {
		t.Fatal("accounts add changed the canonical Keychain credential")
	}
	if !strings.Contains(output.String(), "agy's current login was not changed") || !strings.Contains(output.String(), "new@example.com") {
		t.Fatalf("output=%q", output.String())
	}
}

func TestAccountsCommandListsFullAccountIdentityAndHelp(t *testing.T) {
	service, _, _, _, _, _ := accountCommandService(t)
	var output strings.Builder
	application := New(&scriptedRunner{t: t}, &output, &strings.Builder{})
	application.checkPlatform = func() error { return nil }
	application.accountsFactory = func() (*accounts.AccountService, error) { return service, nil }
	if err := application.accountsCommand(context.Background(), []string{"list"}); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(output.String(), "Old") || !strings.Contains(output.String(), "old@example.com") {
		t.Fatalf("missing full account identity in list output=%q", output.String())
	}
	output.Reset()
	if err := application.accountsCommand(context.Background(), []string{"--help"}); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(output.String(), "accounts add") || !strings.Contains(output.String(), "accounts import") {
		t.Fatalf("help=%q", output.String())
	}
}

func TestAccountsCommandShowsEmailUsedAsLabel(t *testing.T) {
	service, _, _, _, account, _ := accountCommandService(t)
	catalog, err := service.Repository.LoadCatalog()
	if err != nil {
		t.Fatal(err)
	}
	catalog.Accounts[0].Label = account.Email
	if err := service.Repository.SaveCatalog(catalog); err != nil {
		t.Fatal(err)
	}
	var output strings.Builder
	application := New(&scriptedRunner{t: t}, &output, &strings.Builder{})
	application.checkPlatform = func() error { return nil }
	application.accountsFactory = func() (*accounts.AccountService, error) { return service, nil }
	if err := application.accountsCommand(context.Background(), []string{"list"}); err != nil {
		t.Fatal(err)
	}
	if strings.Count(output.String(), account.Email) < 2 {
		t.Fatalf("full email label and email were not shown: output=%q", output.String())
	}
}

func TestAccountsCommandRejectsUnsafeAndAmbiguousArguments(t *testing.T) {
	service, _, _, _, _, _ := accountCommandService(t)
	application := New(&scriptedRunner{t: t}, &strings.Builder{}, &strings.Builder{})
	application.checkPlatform = func() error { return nil }
	application.accountsFactory = func() (*accounts.AccountService, error) { return service, nil }
	if err := application.accountsCommand(context.Background(), []string{"remove", "id"}); err == nil || !strings.Contains(err.Error(), "--yes") {
		t.Fatalf("remove error=%v", err)
	}
	if err := application.accountsCommand(context.Background(), []string{"status", "does-not-exist"}); err == nil {
		t.Fatal("expected missing account error")
	}
}

func TestAccountsMetadataExportCommandWritesPrivateFile(t *testing.T) {
	service, _, _, _, _, _ := accountCommandService(t)
	output := filepath.Join(t.TempDir(), "metadata.json")
	application := New(&scriptedRunner{t: t}, &strings.Builder{}, &strings.Builder{})
	application.checkPlatform = func() error { return nil }
	application.accountsFactory = func() (*accounts.AccountService, error) { return service, nil }
	if err := application.accountsCommand(context.Background(), []string{"export", "--metadata-only", "--output", output}); err != nil {
		t.Fatal(err)
	}
	data, err := os.ReadFile(output)
	if err != nil {
		t.Fatal(err)
	}
	text := string(data)
	if !strings.Contains(text, `"email": "old@example.com"`) || strings.Contains(text, "masked_email") {
		t.Fatalf("metadata identity fields are incorrect: %s", data)
	}
	for _, secret := range []string{"access_token", `"credentials"`, `"token"`} {
		if strings.Contains(text, secret) {
			t.Fatalf("metadata contains credential material %q: %s", secret, data)
		}
	}
}

func accountCommandService(t *testing.T) (*accounts.AccountService, *commandVault, *commandCanonical, accounts.Catalog, accounts.Account, accounts.Account) {
	t.Helper()
	dir := t.TempDir()
	repository := accounts.NewRepository(dir)
	now := time.Date(2026, time.September, 18, 12, 0, 0, 0, time.UTC)
	id, _ := accounts.StableID("old")
	credential := []byte(`{"token":{"access_token":"old"}}`)
	fingerprint, _ := accounts.CredentialFingerprint(credential)
	weekly, five := .8, .8
	account := accounts.Account{ID: id, Provider: accounts.ProviderGoogle, Label: "Old", Email: "old@example.com", CredentialFingerprint: fingerprint, State: accounts.StateHealthy, LastVerifiedAt: now, Quota: &accounts.QuotaSnapshot{Class: accounts.QuotaAvailable, WeeklyRemaining: &weekly, FiveHourRemaining: &five, ObservedAt: now}}
	catalog := accounts.Catalog{Version: accounts.CatalogVersion, Revision: 1, DefaultAccountID: id, Accounts: []accounts.Account{account}}
	if err := repository.SaveCatalog(catalog); err != nil {
		t.Fatal(err)
	}
	vault := &commandVault{items: map[string][]byte{id: credential}}
	canonical := &commandCanonical{value: credential, isolated: map[string][]byte{}}
	identity := commandIdentity{byCredential: map[string]accounts.ProviderIdentity{string(credential): {Subject: "old", Email: "old@example.com"}}}
	service := &accounts.AccountService{Repository: repository, Vault: vault, Canonical: canonical, Identity: identity, Acquire: func() (accounts.OperationLock, error) { return commandLock{}, nil }, Now: func() time.Time { return now }}
	return service, vault, canonical, catalog, account, account
}

type commandLock struct{}

func (commandLock) Release() {}

type commandIdentity struct {
	byCredential map[string]accounts.ProviderIdentity
}

func (i commandIdentity) Resolve(_ context.Context, credential []byte) (accounts.ProviderIdentity, error) {
	identity, ok := i.byCredential[string(credential)]
	if !ok {
		return accounts.ProviderIdentity{}, errors.New("identity mismatch")
	}
	return identity, nil
}

type commandVault struct{ items map[string][]byte }

func (v *commandVault) Load(_ context.Context, id string) ([]byte, error) {
	value, ok := v.items[id]
	if !ok {
		return nil, os.ErrNotExist
	}
	return append([]byte(nil), value...), nil
}
func (v *commandVault) Save(_ context.Context, id string, value []byte) error {
	v.items[id] = append([]byte(nil), value...)
	return nil
}
func (v *commandVault) Delete(_ context.Context, id string) error { delete(v.items, id); return nil }
func (v *commandVault) Exists(_ context.Context, id string) (bool, error) {
	_, ok := v.items[id]
	return ok, nil
}

type commandCanonical struct {
	value    []byte
	isolated map[string][]byte
}

func (c *commandCanonical) Read(context.Context) ([]byte, error) {
	if len(c.value) == 0 {
		return nil, os.ErrNotExist
	}
	return append([]byte(nil), c.value...), nil
}
func (c *commandCanonical) Replace(_ context.Context, value []byte) error {
	c.value = append([]byte(nil), value...)
	return nil
}
func (c *commandCanonical) Isolate(_ context.Context, id string) error {
	c.isolated[id] = append([]byte(nil), c.value...)
	c.value = nil
	return nil
}
func (c *commandCanonical) RestoreIsolated(_ context.Context, id string) error {
	c.value = append([]byte(nil), c.isolated[id]...)
	delete(c.isolated, id)
	return nil
}
func (c *commandCanonical) DeleteIsolated(_ context.Context, id string) error {
	delete(c.isolated, id)
	return nil
}

func TestResolveAccountRequiresFullIDAndSupportsExactLabelOrEmail(t *testing.T) {
	service, _, _, catalog, account, _ := accountCommandService(t)
	_ = service
	for _, reference := range []string{account.ID, account.Label, account.Email} {
		got, err := resolveAccount(catalog, reference)
		if err != nil || got.ID != account.ID {
			t.Fatalf("reference=%q got=%+v err=%v", reference, got, err)
		}
	}
	if _, err := resolveAccount(catalog, account.ID[:8]); err == nil {
		t.Fatal("short opaque ID must not resolve")
	}
}

func TestAccountsDoctorReportsInterruptedTransactionWithoutTouchingKeychain(t *testing.T) {
	service, vault, canonical, catalog, account, _ := accountCommandService(t)
	now := service.Now()
	operationID := "operation-doctor-report"
	canonical.isolated[operationID] = append([]byte(nil), vault.items[account.ID]...)
	canonical.value = []byte(`{"token":{"access_token":"partial-login"}}`)
	before := append([]byte(nil), canonical.value...)
	transaction := accounts.Transaction{
		Version:             accounts.TransactionVersion,
		OperationID:         operationID,
		Kind:                accounts.TransactionAdd,
		Phase:               accounts.PhaseLoginRunning,
		StartedAt:           now,
		UpdatedAt:           now,
		PreviousDefaultID:   catalog.DefaultAccountID,
		PreviousCanonicalID: account.ID,
	}
	if err := service.Repository.SaveTransaction(transaction); err != nil {
		t.Fatal(err)
	}
	var output strings.Builder
	application := New(&scriptedRunner{t: t}, &output, &strings.Builder{})
	application.checkPlatform = func() error { return nil }
	application.accountsFactory = func() (*accounts.AccountService, error) { return service, nil }
	err := application.accountsCommand(context.Background(), []string{"doctor"})
	if err == nil || !strings.Contains(err.Error(), "doctor found problems") {
		t.Fatalf("error=%v", err)
	}
	if !strings.Contains(output.String(), "account transaction is incomplete") {
		t.Fatalf("output=%q", output.String())
	}
	if string(canonical.value) != string(before) {
		t.Fatalf("doctor changed canonical=%q", canonical.value)
	}
	if _, exists, _ := service.Repository.LoadTransaction(); !exists {
		t.Fatal("doctor unexpectedly removed the transaction")
	}
}

func TestAccountsSwitchAutoHealthyNoOp(t *testing.T) {
	service, _, canonical, _, account, _ := accountCommandService(t)
	var output, stderr strings.Builder
	application := New(&quotaProbeRunner{results: []proc.Result{
		{Stdout: agyModelResult("gemini-3", "Gemini").Stdout},
		{Stdout: agyQuotaResult("Gemini Models", .8, .9).Stdout},
	}}, &output, &stderr)
	application.checkPlatform = func() error { return nil }
	application.accountsFactory = func() (*accounts.AccountService, error) { return service, nil }
	before := append([]byte(nil), canonical.value...)
	if err := application.accountsCommand(context.Background(), []string{"switch", "--auto"}); err != nil {
		t.Fatal(err)
	}
	if string(canonical.value) != string(before) || !strings.Contains(output.String(), "no switch needed") {
		t.Fatalf("output=%q canonical_changed=%t", output.String(), string(canonical.value) != string(before))
	}
	if !strings.Contains(output.String(), account.Email) {
		t.Fatalf("output=%q", output.String())
	}
}

func TestAccountsSwitchAutoUnknownDoesNotMutate(t *testing.T) {
	service, _, canonical, _, _, _ := accountCommandService(t)
	var output, stderr strings.Builder
	application := New(&quotaProbeRunner{results: []proc.Result{{Stdout: "not-json"}}}, &output, &stderr)
	application.checkPlatform = func() error { return nil }
	application.accountsFactory = func() (*accounts.AccountService, error) { return service, nil }
	before := append([]byte(nil), canonical.value...)
	if err := application.accountsCommand(context.Background(), []string{"switch", "--auto"}); err == nil || !strings.Contains(err.Error(), "unknown") {
		t.Fatalf("error=%v", err)
	}
	if string(canonical.value) != string(before) {
		t.Fatal("unknown quota changed canonical credential")
	}
}

func TestAccountsSwitchAutoRotatesInRoundRobinOrder(t *testing.T) {
	fixture := newRecoveryFixture(t, 2)
	var output, stderr strings.Builder
	fixture.app.stdout = &output
	fixture.app.stderr = &stderr
	fixture.app.runner = &quotaProbeRunner{results: []proc.Result{
		agyModelResult("gemini-3", "Gemini"),
		agyQuotaResult("Gemini Models", 0, .9),
		agyModelResult("gemini-3", "Gemini"),
		agyQuotaResult("Gemini Models", .8, .8),
	}}
	fixture.app.checkPlatform = func() error { return nil }
	fixture.app.accountsFactory = func() (*accounts.AccountService, error) { return fixture.service, nil }
	if err := fixture.app.accountsCommand(context.Background(), []string{"switch", "--auto"}); err != nil {
		t.Fatal(err)
	}
	loaded, _ := fixture.service.Repository.LoadCatalog()
	if loaded.DefaultAccountID != fixture.accounts[1].ID || loaded.Rotation.CursorAccountID != fixture.accounts[1].ID {
		t.Fatalf("default=%s cursor=%s want=%s", loaded.DefaultAccountID, loaded.Rotation.CursorAccountID, fixture.accounts[1].ID)
	}
	if !strings.Contains(output.String(), fixture.accounts[1].Email) || !strings.Contains(output.String(), "Restart agy") {
		t.Fatalf("output=%q", output.String())
	}
}
