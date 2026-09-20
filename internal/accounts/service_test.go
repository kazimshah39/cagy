package accounts

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"testing"
	"time"
)

type serviceLock struct{}

func (serviceLock) Release() {}

type failingRepository struct {
	AccountRepository
	failCatalogSaves      int
	failCommittedTxnSaves int
}

func (r *failingRepository) SaveCatalog(catalog Catalog) error {
	if r.failCatalogSaves > 0 {
		r.failCatalogSaves--
		return errors.New("injected catalog save failure")
	}
	return r.AccountRepository.SaveCatalog(catalog)
}

func (r *failingRepository) SaveTransaction(transaction Transaction) error {
	if transaction.Phase == PhaseCommitted && r.failCommittedTxnSaves > 0 {
		r.failCommittedTxnSaves--
		return errors.New("injected committed transaction save failure")
	}
	return r.AccountRepository.SaveTransaction(transaction)
}

type serviceVault struct{ items map[string][]byte }

func (v *serviceVault) Load(_ context.Context, id string) ([]byte, error) {
	value, ok := v.items[id]
	if !ok {
		return nil, errors.New("missing credential")
	}
	return append([]byte(nil), value...), nil
}
func (v *serviceVault) Save(_ context.Context, id string, value []byte) error {
	v.items[id] = append([]byte(nil), value...)
	return nil
}
func (v *serviceVault) Delete(_ context.Context, id string) error { delete(v.items, id); return nil }
func (v *serviceVault) Exists(_ context.Context, id string) (bool, error) {
	_, ok := v.items[id]
	return ok, nil
}

type serviceCanonical struct {
	value    []byte
	isolated map[string][]byte
}

func (c *serviceCanonical) Read(context.Context) ([]byte, error) {
	if len(c.value) == 0 {
		return nil, errors.New("canonical missing")
	}
	return append([]byte(nil), c.value...), nil
}
func (c *serviceCanonical) Replace(_ context.Context, value []byte) error {
	c.value = append([]byte(nil), value...)
	return nil
}
func (c *serviceCanonical) Isolate(_ context.Context, id string) error {
	if len(c.value) == 0 {
		return errors.New("canonical missing")
	}
	if c.isolated == nil {
		c.isolated = map[string][]byte{}
	}
	c.isolated[id] = c.value
	c.value = nil
	return nil
}
func (c *serviceCanonical) RestoreIsolated(_ context.Context, id string) error {
	value, ok := c.isolated[id]
	if !ok {
		return errors.New("missing isolated")
	}
	c.value = append([]byte(nil), value...)
	delete(c.isolated, id)
	return nil
}
func (c *serviceCanonical) DeleteIsolated(_ context.Context, id string) error {
	delete(c.isolated, id)
	return nil
}

type serviceIdentity struct{ byCredential map[string]ProviderIdentity }

func (i serviceIdentity) Resolve(_ context.Context, credential []byte) (ProviderIdentity, error) {
	value, ok := i.byCredential[string(credential)]
	if !ok {
		return ProviderIdentity{}, errors.New("identity mismatch")
	}
	return value, nil
}

type serviceValidator struct {
	byCredential map[string]ValidationResult
	errFor       string
}

func (v serviceValidator) Validate(_ context.Context, credential []byte) (ValidationResult, error) {
	if string(credential) == v.errFor {
		return ValidationResult{}, errors.New("validation failed")
	}
	value, ok := v.byCredential[string(credential)]
	if !ok {
		return ValidationResult{}, errors.New("unknown credential")
	}
	value.Credential = append([]byte(nil), credential...)
	return value, nil
}

func serviceFixture(t *testing.T) (*AccountService, *serviceVault, *serviceCanonical, Catalog, Account, Account) {
	t.Helper()
	dir := t.TempDir()
	repository := NewRepository(dir)
	now := time.Date(2026, time.September, 18, 12, 0, 0, 0, time.UTC)
	oldCredential := []byte(`{"token":{"access_token":"old"}}`)
	targetCredential := []byte(`{"token":{"access_token":"target"}}`)
	old := testAccount(t, "old", "Old", "old@example.com")
	target := testAccount(t, "target", "Target", "target@example.com")
	old.CredentialFingerprint, _ = CredentialFingerprint(oldCredential)
	target.CredentialFingerprint, _ = CredentialFingerprint(targetCredential)
	old.LastVerifiedAt = now
	target.LastVerifiedAt = now
	weekly, five := .8, .8
	old.Quota = &QuotaSnapshot{Class: QuotaAvailable, WeeklyRemaining: &weekly, FiveHourRemaining: &five, ObservedAt: now}
	target.Quota = &QuotaSnapshot{Class: QuotaAvailable, WeeklyRemaining: &weekly, FiveHourRemaining: &five, ObservedAt: now}
	catalog := Catalog{Version: CatalogVersion, Revision: 1, DefaultAccountID: old.ID, Accounts: []Account{old, target}}
	if err := repository.SaveCatalog(catalog); err != nil {
		t.Fatal(err)
	}
	vault := &serviceVault{items: map[string][]byte{old.ID: oldCredential, target.ID: targetCredential}}
	canonical := &serviceCanonical{value: oldCredential}
	identity := serviceIdentity{byCredential: map[string]ProviderIdentity{string(oldCredential): {Subject: "old", Email: "old@example.com"}, string(targetCredential): {Subject: "target", Email: "target@example.com"}}}
	validator := serviceValidator{byCredential: map[string]ValidationResult{string(oldCredential): {Identity: ProviderIdentity{Subject: "old", Email: "old@example.com"}, Quota: *old.Quota}, string(targetCredential): {Identity: ProviderIdentity{Subject: "target", Email: "target@example.com"}, Quota: *target.Quota}}}
	service := &AccountService{Repository: repository, Vault: vault, Canonical: canonical, Identity: identity, Validator: validator, Acquire: func() (OperationLock, error) { return serviceLock{}, nil }, Now: func() time.Time { return now }, Operation: func() string { return "operation-123" }}
	return service, vault, canonical, catalog, old, target
}

func TestImportActiveDeduplicatesAndPreservesCanonical(t *testing.T) {
	service, vault, canonical, catalog, old, _ := serviceFixture(t)
	account, err := service.ImportActive(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if account.ID != old.ID || string(canonical.value) != string(vault.items[old.ID]) {
		t.Fatalf("account=%+v canonical=%q", account, canonical.value)
	}
	loaded, err := service.Repository.LoadCatalog()
	if err != nil {
		t.Fatal(err)
	}
	if loaded.Revision <= catalog.Revision || loaded.DefaultAccountID != catalog.DefaultAccountID || len(loaded.Accounts) != 2 {
		t.Fatalf("catalog=%+v", loaded)
	}
}

func TestSwitchCommitsTargetAndRollsBackOnValidationFailure(t *testing.T) {
	service, _, canonical, _, old, target := serviceFixture(t)
	got, err := service.Switch(context.Background(), target.ID)
	if err != nil {
		t.Fatal(err)
	}
	if got.ID != target.ID || string(canonical.value) != string([]byte(`{"token":{"access_token":"target"}}`)) {
		t.Fatalf("got=%+v canonical=%q", got, canonical.value)
	}
	loaded, _ := service.Repository.LoadCatalog()
	if loaded.DefaultAccountID != target.ID {
		t.Fatalf("default=%q", loaded.DefaultAccountID)
	}
	// New service with a validator failure must restore the original canonical credential.
	service, _, canonical, _, old, _ = serviceFixture(t)
	service.Validator = serviceValidator{errFor: string([]byte(`{"token":{"access_token":"target"}}`)), byCredential: map[string]ValidationResult{}}
	if _, err := service.Switch(context.Background(), target.ID); err == nil || !strings.Contains(err.Error(), "validate switched") {
		t.Fatalf("err=%v", err)
	}
	if string(canonical.value) != string([]byte(`{"token":{"access_token":"old"}}`)) {
		t.Fatalf("rollback canonical=%q", canonical.value)
	}
	loaded, _ = service.Repository.LoadCatalog()
	if loaded.DefaultAccountID != old.ID {
		t.Fatalf("rollback default=%q", loaded.DefaultAccountID)
	}
	if _, exists, _ := service.Repository.LoadTransaction(); exists {
		t.Fatal("transaction not cleaned after rollback")
	}
}

func TestEnrollCredentialStoresLocalSnapshotWithoutCanonicalMutation(t *testing.T) {
	service, vault, canonical, catalog, old, _ := serviceFixture(t)
	credential := []byte(`{"token":{"access_token":"new","refresh_token":"refresh"},"auth_method":"consumer"}`)
	identity := service.Identity.(serviceIdentity)
	identity.byCredential[string(credential)] = ProviderIdentity{Subject: "new", Email: "new@example.com", Name: "New Owner"}
	service.Identity = identity
	beforeCanonical := append([]byte(nil), canonical.value...)
	account, err := service.EnrollCredential(context.Background(), credential)
	if err != nil {
		t.Fatal(err)
	}
	newID, _ := StableID("new")
	if account.ID != newID || account.Email != "new@example.com" || account.Label != "New Owner" {
		t.Fatalf("account=%+v", account)
	}
	if string(vault.items[newID]) != string(credential) {
		t.Fatal("new credential was not stored in the local vault")
	}
	if string(canonical.value) != string(beforeCanonical) {
		t.Fatal("enrollment changed the canonical Keychain credential")
	}
	loaded, err := service.Repository.LoadCatalog()
	if err != nil {
		t.Fatal(err)
	}
	if loaded.DefaultAccountID != old.ID || loaded.DefaultAccountID != catalog.DefaultAccountID {
		t.Fatalf("enrollment changed default account to %s", loaded.DefaultAccountID)
	}
}

func TestEnrollCredentialRejectsUnverifiedCredentialWithoutMutation(t *testing.T) {
	service, vault, canonical, _, _, _ := serviceFixture(t)
	beforeCanonical := append([]byte(nil), canonical.value...)
	beforeCount := len(vault.items)
	if _, err := service.EnrollCredential(context.Background(), []byte(`{"token":{"access_token":"unknown"}}`)); err == nil || !strings.Contains(err.Error(), "verify new Google account") {
		t.Fatalf("error=%v", err)
	}
	if len(vault.items) != beforeCount || string(canonical.value) != string(beforeCanonical) {
		t.Fatal("failed enrollment mutated local or canonical credentials")
	}
}

func TestRemoveAndDoctor(t *testing.T) {
	service, vault, _, _, _, target := serviceFixture(t)
	if err := service.Remove(context.Background(), target.ID, false, nil); err == nil {
		t.Fatal("expected confirmation requirement")
	}
	if err := service.Remove(context.Background(), target.ID, true, map[string]struct{}{target.ID: {}}); err == nil || !strings.Contains(err.Error(), "bound") {
		t.Fatalf("bound removal error=%v", err)
	}
	if err := service.Remove(context.Background(), target.ID, true, nil); err != nil {
		t.Fatal(err)
	}
	if _, ok := vault.items[target.ID]; ok {
		t.Fatal("credential still exists")
	}
	if _, err := service.Repository.LoadCatalog(); err != nil {
		t.Fatal(err)
	}
	report, err := service.Doctor(context.Background())
	if err != nil || len(report.Issues) != 0 {
		t.Fatalf("doctor report=%+v err=%v", report, err)
	}
}

func TestWithAccountActivatesOnlyForCallbackAndRestoresDefault(t *testing.T) {
	service, _, canonical, _, old, target := serviceFixture(t)
	called := false
	got, err := service.WithAccount(context.Background(), target.ID, func(context.Context) error {
		called = true
		if string(canonical.value) != string([]byte(`{"token":{"access_token":"target"}}`)) {
			t.Fatalf("callback canonical=%q", canonical.value)
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	if !called || got.ID != target.ID {
		t.Fatalf("called=%v got=%+v", called, got)
	}
	if string(canonical.value) != string([]byte(`{"token":{"access_token":"old"}}`)) {
		t.Fatalf("canonical was not restored: %q", canonical.value)
	}
	catalog, _ := service.Repository.LoadCatalog()
	if catalog.DefaultAccountID != old.ID {
		t.Fatalf("default changed to %s", catalog.DefaultAccountID)
	}
}

func TestWithAccountLowQuotaNeverRunsCallback(t *testing.T) {
	service, _, canonical, _, _, target := serviceFixture(t)
	validator := service.Validator.(serviceValidator)
	result := validator.byCredential[string([]byte(`{"token":{"access_token":"target"}}`))]
	zero := 0.0
	result.Quota = QuotaSnapshot{Class: QuotaExhausted, WeeklyRemaining: &zero, FiveHourRemaining: &zero, ObservedAt: service.now()}
	validator.byCredential[string([]byte(`{"token":{"access_token":"target"}}`))] = result
	service.Validator = validator
	called := false
	got, err := service.WithAccount(context.Background(), target.ID, func(context.Context) error { called = true; return nil })
	if err != nil {
		t.Fatal(err)
	}
	if called || got.State != StateLowQuota {
		t.Fatalf("called=%v got=%+v", called, got)
	}
	if string(canonical.value) != string([]byte(`{"token":{"access_token":"old"}}`)) {
		t.Fatalf("canonical was not restored: %q", canonical.value)
	}
}

type validatorFunc func(context.Context, []byte) (ValidationResult, error)

func (fn validatorFunc) Validate(ctx context.Context, credential []byte) (ValidationResult, error) {
	return fn(ctx, credential)
}

func TestWithAccountKeepsRefreshedCredentialWhenTargetWasAlreadyCanonical(t *testing.T) {
	service, vault, canonical, _, old, _ := serviceFixture(t)
	refreshed := []byte(`{"token":{"access_token":"old-refreshed"}}`)
	wantRefreshed := string(refreshed)
	quota := *old.Quota
	service.Validator = validatorFunc(func(context.Context, []byte) (ValidationResult, error) {
		canonical.value = append([]byte(nil), refreshed...)
		return ValidationResult{Identity: ProviderIdentity{Subject: "old", Email: "old@example.com"}, Quota: quota, Credential: refreshed}, nil
	})
	if _, err := service.WithAccount(context.Background(), old.ID, nil); err != nil {
		t.Fatal(err)
	}
	if string(canonical.value) != wantRefreshed || string(vault.items[old.ID]) != wantRefreshed {
		t.Fatalf("canonical=%q vault=%q", canonical.value, vault.items[old.ID])
	}
}

func TestImportBackupValidatesConflictsBeforeVaultMutation(t *testing.T) {
	service, vault, _, _, _, target := serviceFixture(t)
	original := append([]byte(nil), vault.items[target.ID]...)
	conflicting := target
	conflicting.Email = "other@example.com"
	credential := []byte(`{"token":{"access_token":"replacement"}}`)
	conflicting.CredentialFingerprint, _ = CredentialFingerprint(credential)
	backup := Catalog{Version: CatalogVersion, Revision: 1, DefaultAccountID: target.ID, Accounts: []Account{conflicting}}
	if err := service.ImportBackup(context.Background(), backup, map[string][]byte{target.ID: credential}); err == nil || !strings.Contains(err.Error(), "identity conflicts") {
		t.Fatalf("error=%v", err)
	}
	if string(vault.items[target.ID]) != string(original) {
		t.Fatalf("vault mutated before validation: %q", vault.items[target.ID])
	}
}

func TestUpsertValidatedRollsBackVaultAndCatalogOnCatalogFailure(t *testing.T) {
	service, vault, _, catalog, _, target := serviceFixture(t)
	baseRepository := service.Repository
	service.Repository = &failingRepository{AccountRepository: baseRepository, failCatalogSaves: 1}
	credential := []byte(`{"token":{"access_token":"new"}}`)
	newID, _ := StableID("new")
	_, err := service.upsertValidated(context.Background(), ValidationResult{
		Identity:   ProviderIdentity{Subject: "new", Email: "new@example.com"},
		Quota:      *target.Quota,
		Credential: credential,
	}, "New")
	if err == nil || !strings.Contains(err.Error(), "save account catalog") {
		t.Fatalf("error=%v", err)
	}
	if _, exists := vault.items[newID]; exists {
		t.Fatal("new vault credential remained after catalog rollback")
	}
	loaded, loadErr := baseRepository.LoadCatalog()
	if loadErr != nil {
		t.Fatal(loadErr)
	}
	if loaded.Revision != catalog.Revision || len(loaded.Accounts) != len(catalog.Accounts) {
		t.Fatalf("catalog changed after rollback: %+v", loaded)
	}
}

func TestRemoveRollsBackCredentialAndCatalogOnCatalogFailure(t *testing.T) {
	service, vault, _, catalog, _, target := serviceFixture(t)
	baseRepository := service.Repository
	service.Repository = &failingRepository{AccountRepository: baseRepository, failCatalogSaves: 1}
	originalCredential := append([]byte(nil), vault.items[target.ID]...)
	if err := service.Remove(context.Background(), target.ID, true, nil); err == nil || !strings.Contains(err.Error(), "save account catalog after removal") {
		t.Fatalf("error=%v", err)
	}
	if string(vault.items[target.ID]) != string(originalCredential) {
		t.Fatalf("credential was not restored: %q", vault.items[target.ID])
	}
	loaded, err := baseRepository.LoadCatalog()
	if err != nil {
		t.Fatal(err)
	}
	if loaded.Revision != catalog.Revision {
		t.Fatalf("catalog revision=%d want %d", loaded.Revision, catalog.Revision)
	}
	if _, found := loaded.Find(target.ID); !found {
		t.Fatal("removed account metadata was not restored")
	}
}

func TestSwitchRollsBackRefreshedVaultAndCatalogOnCommitFailure(t *testing.T) {
	service, vault, canonical, catalog, _, target := serviceFixture(t)
	baseRepository := service.Repository
	service.Repository = &failingRepository{AccountRepository: baseRepository, failCatalogSaves: 1}
	originalTargetCredential := append([]byte(nil), vault.items[target.ID]...)
	refreshed := []byte(`{"token":{"access_token":"target-refreshed"}}`)
	quota := *target.Quota
	service.Validator = validatorFunc(func(_ context.Context, credential []byte) (ValidationResult, error) {
		if string(credential) != string(originalTargetCredential) {
			return ValidationResult{}, errors.New("unexpected credential")
		}
		return ValidationResult{Identity: ProviderIdentity{Subject: "target", Email: "target@example.com"}, Quota: quota, Credential: refreshed}, nil
	})
	if _, err := service.Switch(context.Background(), target.ID); err == nil || !strings.Contains(err.Error(), "commit switched account") {
		t.Fatalf("error=%v", err)
	}
	if string(vault.items[target.ID]) != string(originalTargetCredential) {
		t.Fatalf("target vault was not restored: %q", vault.items[target.ID])
	}
	if string(canonical.value) != string([]byte(`{"token":{"access_token":"old"}}`)) {
		t.Fatalf("canonical was not restored: %q", canonical.value)
	}
	loaded, err := baseRepository.LoadCatalog()
	if err != nil {
		t.Fatal(err)
	}
	if loaded.DefaultAccountID != catalog.DefaultAccountID || loaded.Revision != catalog.Revision {
		t.Fatalf("catalog was not restored: %+v", loaded)
	}
	if _, exists, _ := baseRepository.LoadTransaction(); exists {
		t.Fatal("transaction remained after successful rollback")
	}
}

func TestWithAccountPersistsCrashRepairTransactionUntilRestored(t *testing.T) {
	service, _, canonical, _, _, target := serviceFixture(t)
	called := false
	if _, err := service.WithAccount(context.Background(), target.ID, func(context.Context) error {
		called = true
		transaction, exists, loadErr := service.Repository.LoadTransaction()
		if loadErr != nil || !exists {
			t.Fatalf("transaction exists=%v err=%v", exists, loadErr)
		}
		if transaction.Kind != TransactionProbe || transaction.Phase != PhaseValidating || transaction.CandidateID != target.ID {
			t.Fatalf("transaction=%+v", transaction)
		}
		if string(canonical.value) != string([]byte(`{"token":{"access_token":"target"}}`)) {
			t.Fatalf("callback canonical=%q", canonical.value)
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	if !called {
		t.Fatal("callback was not called")
	}
	if _, exists, _ := service.Repository.LoadTransaction(); exists {
		t.Fatal("temporary transaction remained after restoration")
	}
}

func TestRepairTransactionHonorsCommittedSwitch(t *testing.T) {
	service, vault, canonical, catalog, _, target := serviceFixture(t)
	catalog.DefaultAccountID = target.ID
	catalog.Revision++
	if err := service.Repository.SaveCatalog(catalog); err != nil {
		t.Fatal(err)
	}
	now := service.now()
	transaction := Transaction{
		Version:             TransactionVersion,
		OperationID:         "operation-committed-switch",
		Kind:                TransactionSwitch,
		Phase:               PhaseCommitted,
		StartedAt:           now,
		UpdatedAt:           now,
		PreviousDefaultID:   catalog.Accounts[0].ID,
		PreviousCanonicalID: catalog.Accounts[0].ID,
		CandidateID:         target.ID,
	}
	if err := service.Repository.SaveTransaction(transaction); err != nil {
		t.Fatal(err)
	}
	canonical.value = []byte(`{"token":{"access_token":"old"}}`)
	if err := service.RepairTransaction(context.Background()); err != nil {
		t.Fatal(err)
	}
	if string(canonical.value) != string(vault.items[target.ID]) {
		t.Fatalf("canonical=%q want target", canonical.value)
	}
	if _, exists, _ := service.Repository.LoadTransaction(); exists {
		t.Fatal("committed transaction was not cleaned")
	}
}

func TestDoctorTreatsCatalogSnapshotAsSourceOfTruth(t *testing.T) {
	service, vault, canonical, _, _, target := serviceFixture(t)
	canonical.value = append([]byte(nil), vault.items[target.ID]...)
	report, err := service.Doctor(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if len(report.Issues) != 0 {
		t.Fatalf("issues=%v", report.Issues)
	}
}

func TestRemoveRefusesLastHealthyNonDefaultAccount(t *testing.T) {
	service, _, _, catalog, old, target := serviceFixture(t)
	for index := range catalog.Accounts {
		if catalog.Accounts[index].ID == old.ID {
			catalog.Accounts[index].State = StateLowQuota
		}
	}
	if err := service.Repository.SaveCatalog(catalog); err != nil {
		t.Fatal(err)
	}
	if err := service.Remove(context.Background(), target.ID, true, nil); err == nil || !strings.Contains(err.Error(), "last healthy") {
		t.Fatalf("error=%v", err)
	}
}

func TestRemoveUsesCatalogDefaultInsteadOfAmbientCanonicalItem(t *testing.T) {
	service, _, canonical, _, _, target := serviceFixture(t)
	canonical.value = []byte(`{"token":{"access_token":"target"}}`)
	if err := service.Remove(context.Background(), target.ID, true, nil); err != nil {
		t.Fatal(err)
	}
	catalog, err := service.Repository.LoadCatalog()
	if err != nil {
		t.Fatal(err)
	}
	if _, found := catalog.Find(target.ID); found {
		t.Fatal("non-default account was not removed")
	}
}

func TestUniqueAccountLabelUsesDeterministicShortIDSuffix(t *testing.T) {
	service, _, _, _, _, target := serviceFixture(t)
	credential := []byte(`{"token":{"access_token":"same-name"}}`)
	account, err := service.upsertValidated(context.Background(), ValidationResult{
		Identity:   ProviderIdentity{Subject: "same-name", Email: "same@example.com"},
		Quota:      *target.Quota,
		Credential: credential,
	}, target.Label)
	if err != nil {
		t.Fatal(err)
	}
	if account.Label != target.Label+" "+account.ID[:8] {
		t.Fatalf("label=%q", account.Label)
	}
}

func TestUniqueAccountLabelExpandsCollidingSuffixAndStaysBounded(t *testing.T) {
	newID, _ := StableID("new-collision")
	first := testAccount(t, "first-label", strings.Repeat("L", 80), "first@example.com")
	second := testAccount(t, "second-label", labelWithSuffix(first.Label, newID[:8]), "second@example.com")
	catalog := Catalog{Version: CatalogVersion, Accounts: []Account{first, second}}
	label := uniqueAccountLabel(catalog, first.Label, newID)
	if len([]rune(label)) > 80 || !strings.HasSuffix(label, newID[:12]) {
		t.Fatalf("label=%q runes=%d", label, len([]rune(label)))
	}
}

func TestImportBackupMarksVerifiedUnknownAndUnverifiedNeedsLogin(t *testing.T) {
	service, vault, _, _, _, _ := serviceFixture(t)
	verifiedCredential := []byte(`{"token":{"access_token":"verified-import"}}`)
	unverifiedCredential := []byte(`{"token":{"access_token":"expired-import"}}`)
	verified := testAccount(t, "verified-import", "Verified Import", "verified@example.com")
	unverified := testAccount(t, "unverified-import", "Unverified Import", "unverified@example.com")
	verified.CredentialFingerprint, _ = CredentialFingerprint(verifiedCredential)
	unverified.CredentialFingerprint, _ = CredentialFingerprint(unverifiedCredential)
	verified.State = StateHealthy
	unverified.State = StateHealthy
	weekly, five := .9, .9
	verified.Quota = &QuotaSnapshot{Class: QuotaAvailable, WeeklyRemaining: &weekly, FiveHourRemaining: &five, ObservedAt: service.now()}
	unverified.Quota = &QuotaSnapshot{Class: QuotaAvailable, WeeklyRemaining: &weekly, FiveHourRemaining: &five, ObservedAt: service.now()}
	backup := Catalog{Version: CatalogVersion, Revision: 1, DefaultAccountID: verified.ID, Accounts: []Account{verified, unverified}}
	identity := service.Identity.(serviceIdentity)
	identity.byCredential[string(verifiedCredential)] = ProviderIdentity{Subject: "verified-import", Email: verified.Email}
	service.Identity = identity
	if err := service.ImportBackup(context.Background(), backup, map[string][]byte{
		verified.ID:   verifiedCredential,
		unverified.ID: unverifiedCredential,
	}); err != nil {
		t.Fatal(err)
	}
	loaded, err := service.Repository.LoadCatalog()
	if err != nil {
		t.Fatal(err)
	}
	gotVerified, found := loaded.Find(verified.ID)
	if !found || gotVerified.State != StateUnknown || gotVerified.Quota != nil || gotVerified.LastVerifiedAt.IsZero() {
		t.Fatalf("verified import=%+v", gotVerified)
	}
	gotUnverified, found := loaded.Find(unverified.ID)
	if !found || gotUnverified.State != StateNeedsLogin || gotUnverified.Quota != nil || !gotUnverified.LastVerifiedAt.IsZero() {
		t.Fatalf("unverified import=%+v", gotUnverified)
	}
	if string(vault.items[verified.ID]) != string(verifiedCredential) || string(vault.items[unverified.ID]) != string(unverifiedCredential) {
		t.Fatal("imported credentials are missing")
	}
}

func TestImportBackupRejectsLiveIdentityMismatchBeforeMutation(t *testing.T) {
	service, vault, _, _, _, _ := serviceFixture(t)
	credential := []byte(`{"token":{"access_token":"mismatch-import"}}`)
	imported := testAccount(t, "expected-subject", "Mismatch", "expected@example.com")
	imported.CredentialFingerprint, _ = CredentialFingerprint(credential)
	backup := Catalog{Version: CatalogVersion, Revision: 1, DefaultAccountID: imported.ID, Accounts: []Account{imported}}
	identity := service.Identity.(serviceIdentity)
	identity.byCredential[string(credential)] = ProviderIdentity{Subject: "different-subject", Email: imported.Email}
	service.Identity = identity
	if err := service.ImportBackup(context.Background(), backup, map[string][]byte{imported.ID: credential}); err == nil || !strings.Contains(err.Error(), "identity does not match") {
		t.Fatalf("error=%v", err)
	}
	if _, exists := vault.items[imported.ID]; exists {
		t.Fatal("vault mutated before identity validation completed")
	}
}

func TestImportBackupRollsBackVaultOnCatalogFailure(t *testing.T) {
	service, vault, _, catalog, _, _ := serviceFixture(t)
	baseRepository := service.Repository
	service.Repository = &failingRepository{AccountRepository: baseRepository, failCatalogSaves: 1}
	credential := []byte(`{"token":{"access_token":"rollback-import"}}`)
	imported := testAccount(t, "rollback-import", "Rollback Import", "rollback@example.com")
	imported.CredentialFingerprint, _ = CredentialFingerprint(credential)
	backup := Catalog{Version: CatalogVersion, Revision: 1, DefaultAccountID: imported.ID, Accounts: []Account{imported}}
	identity := service.Identity.(serviceIdentity)
	identity.byCredential[string(credential)] = ProviderIdentity{Subject: "rollback-import", Email: imported.Email}
	service.Identity = identity
	if err := service.ImportBackup(context.Background(), backup, map[string][]byte{imported.ID: credential}); err == nil {
		t.Fatal("expected catalog save failure")
	}
	if _, exists := vault.items[imported.ID]; exists {
		t.Fatal("new imported credential remained after rollback")
	}
	loaded, err := baseRepository.LoadCatalog()
	if err != nil {
		t.Fatal(err)
	}
	if loaded.Revision != catalog.Revision || len(loaded.Accounts) != len(catalog.Accounts) {
		t.Fatalf("catalog changed after rollback: %+v", loaded)
	}
}

func TestDoctorNeverTouchesCanonicalKeychain(t *testing.T) {
	service, _, canonical, _, _, _ := serviceFixture(t)
	report, err := service.Doctor(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if len(report.Issues) != 0 {
		t.Fatalf("issues=%v", report.Issues)
	}
	if string(canonical.value) != string([]byte(`{"token":{"access_token":"old"}}`)) {
		t.Fatalf("doctor changed canonical credential: %q", canonical.value)
	}
}

func TestSwitchFailsCleanlyWhenCanonicalUpdateIsDenied(t *testing.T) {
	service, _, canonical, _, old, target := serviceFixture(t)
	denied := &denyingCanonical{serviceCanonical: *canonical}
	service.Canonical = denied
	if _, err := service.Switch(context.Background(), target.ID); err == nil || !strings.Contains(err.Error(), "activate account") {
		t.Fatalf("error=%v", err)
	}
	if denied.replaceCalls != 1 {
		t.Fatalf("replace calls=%d want=1", denied.replaceCalls)
	}
	if string(canonical.value) != string([]byte(`{"token":{"access_token":"old"}}`)) {
		t.Fatalf("canonical changed after denied switch: %q", canonical.value)
	}
	loaded, err := service.Repository.LoadCatalog()
	if err != nil {
		t.Fatal(err)
	}
	if loaded.DefaultAccountID != old.ID {
		t.Fatalf("default=%s want %s", loaded.DefaultAccountID, old.ID)
	}
	if _, exists, _ := service.Repository.LoadTransaction(); exists {
		t.Fatal("transaction remained after denied switch")
	}
}

type denyingCanonical struct {
	serviceCanonical
	replaceCalls int
}

func (c *denyingCanonical) Replace(context.Context, []byte) error {
	c.replaceCalls++
	return errors.New("legacy keychain ACL denied update")
}

func TestSwitchRefreshesBeforeCanonicalMutation(t *testing.T) {
	service, vault, canonical, _, _, target := serviceFixture(t)
	originalCanonical := append([]byte(nil), canonical.value...)
	refreshed := []byte(`{"token":{"access_token":"fresh-target","refresh_token":"refresh"}}`)
	quota := *target.Quota
	service.Validator = validatorFunc(func(_ context.Context, credential []byte) (ValidationResult, error) {
		if string(canonical.value) != string(originalCanonical) {
			t.Fatal("canonical credential changed before target validation")
		}
		if string(credential) != string(vault.items[target.ID]) {
			t.Fatal("validator did not receive the target snapshot")
		}
		return ValidationResult{
			Identity: ProviderIdentity{Subject: "target", Email: "target@example.com"},
			Quota:    quota, Credential: append([]byte(nil), refreshed...),
		}, nil
	})
	if _, err := service.Switch(context.Background(), target.ID); err != nil {
		t.Fatal(err)
	}
	if string(canonical.value) != string(refreshed) {
		t.Fatalf("canonical did not receive refreshed credential")
	}
	if string(vault.items[target.ID]) != string(refreshed) {
		t.Fatalf("vault did not receive refreshed credential")
	}
}

func TestRecordQuotaPersistsHealthWithoutCredentialMutation(t *testing.T) {
	service, vault, canonical, catalog, _, target := serviceFixture(t)
	beforeCredential := append([]byte(nil), vault.items[target.ID]...)
	beforeCanonical := append([]byte(nil), canonical.value...)
	weekly, five := .6, .7
	observed := service.Now().Add(time.Minute)
	if err := service.RecordQuota(target.ID, QuotaSnapshot{Class: QuotaAvailable, WeeklyRemaining: &weekly, FiveHourRemaining: &five, ObservedAt: observed}); err != nil {
		t.Fatal(err)
	}
	loaded, err := service.Repository.LoadCatalog()
	if err != nil {
		t.Fatal(err)
	}
	got, found := loaded.Find(target.ID)
	if !found || got.State != StateHealthy || got.Quota == nil || !got.Quota.ObservedAt.Equal(observed) {
		t.Fatalf("account=%+v", got)
	}
	if loaded.Revision <= catalog.Revision || string(vault.items[target.ID]) != string(beforeCredential) || string(canonical.value) != string(beforeCanonical) {
		t.Fatal("quota update changed credential state or did not advance catalog")
	}
}

func TestRecordQuotaUpdatesLowAndUnknownStates(t *testing.T) {
	for _, test := range []struct {
		class QuotaClass
		state State
	}{
		{QuotaLow, StateLowQuota},
		{QuotaExhausted, StateLowQuota},
		{QuotaUnknown, StateUnknown},
	} {
		service, _, _, _, _, target := serviceFixture(t)
		quota := QuotaSnapshot{Class: test.class, ObservedAt: service.Now()}
		if err := service.RecordQuota(target.ID, quota); err != nil {
			t.Fatal(err)
		}
		loaded, _ := service.Repository.LoadCatalog()
		got, _ := loaded.Find(target.ID)
		if got.State != test.state || got.Quota == nil || got.Quota.Class != test.class {
			t.Fatalf("class=%s account=%+v", test.class, got)
		}
	}
}

func TestSwitchRecordsLastUsedAt(t *testing.T) {
	service, _, _, _, _, target := serviceFixture(t)
	got, err := service.Switch(context.Background(), target.ID)
	if err != nil {
		t.Fatal(err)
	}
	if !got.LastUsedAt.Equal(service.Now()) {
		t.Fatalf("last_used_at=%s want=%s", got.LastUsedAt, service.Now())
	}
	loaded, _ := service.Repository.LoadCatalog()
	persisted, _ := loaded.Find(target.ID)
	if !persisted.LastUsedAt.Equal(service.Now()) {
		t.Fatalf("persisted last_used_at=%s", persisted.LastUsedAt)
	}
}

func TestCredentialFailureNeedsLogin(t *testing.T) {
	if !CredentialFailureNeedsLogin(fmt.Errorf("wrapped: %w", ErrCredentialNeedsLogin)) {
		t.Fatal("wrapped permanent credential error was not recognized")
	}
	if CredentialFailureNeedsLogin(errors.New("network unavailable")) {
		t.Fatal("transient error was classified as permanent")
	}
}

func TestSetDefaultAccountChangesOnlyCatalogMetadata(t *testing.T) {
	service, vault, canonical, catalog, _, target := serviceFixture(t)
	beforeCanonical := append([]byte(nil), canonical.value...)
	beforeTarget := append([]byte(nil), vault.items[target.ID]...)
	if err := service.SetDefaultAccount(target.ID); err != nil {
		t.Fatal(err)
	}
	loaded, err := service.Repository.LoadCatalog()
	if err != nil {
		t.Fatal(err)
	}
	if loaded.DefaultAccountID != target.ID || loaded.Revision <= catalog.Revision {
		t.Fatalf("catalog=%+v", loaded)
	}
	if string(canonical.value) != string(beforeCanonical) || string(vault.items[target.ID]) != string(beforeTarget) {
		t.Fatal("default reconciliation touched credential state")
	}
}

func TestRotationObservationAndFailureAdvanceCursor(t *testing.T) {
	service, _, _, _, old, target := serviceFixture(t)
	now := service.Now()
	quota := QuotaSnapshot{Class: QuotaExhausted, ObservedAt: now}
	if err := service.RecordRotationObservation(old.ID, quota); err != nil {
		t.Fatal(err)
	}
	loaded, _ := service.Repository.LoadCatalog()
	if loaded.Rotation == nil || loaded.Rotation.CursorAccountID != old.ID {
		t.Fatalf("rotation=%+v", loaded.Rotation)
	}
	if err := service.RecordRotationFailure(target.ID, false); err != nil {
		t.Fatal(err)
	}
	loaded, _ = service.Repository.LoadCatalog()
	got, _ := loaded.Find(target.ID)
	if loaded.Rotation.CursorAccountID != target.ID || got.ConsecutiveFailures != 1 || got.CooldownUntil.IsZero() {
		t.Fatalf("cursor=%+v account=%+v", loaded.Rotation, got)
	}
}

func TestSwitchAutomaticAdvancesCursorButManualSwitchDoesNot(t *testing.T) {
	service, _, _, _, old, target := serviceFixture(t)
	if _, err := service.Switch(context.Background(), target.ID); err != nil {
		t.Fatal(err)
	}
	loaded, _ := service.Repository.LoadCatalog()
	if loaded.Rotation.CursorAccountID != "" {
		t.Fatalf("manual switch advanced cursor=%s", loaded.Rotation.CursorAccountID)
	}
	if _, err := service.SwitchAutomatic(context.Background(), old.ID); err != nil {
		t.Fatal(err)
	}
	loaded, _ = service.Repository.LoadCatalog()
	if loaded.Rotation.CursorAccountID != old.ID {
		t.Fatalf("automatic switch cursor=%s want=%s", loaded.Rotation.CursorAccountID, old.ID)
	}
}
