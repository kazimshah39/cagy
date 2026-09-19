package accounts

import (
	"bytes"
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"strings"
	"time"
)

type AccountRepository interface {
	LoadCatalog() (Catalog, error)
	SaveCatalog(Catalog) error
	LoadTransaction() (Transaction, bool, error)
	SaveTransaction(Transaction) error
	RemoveTransaction() error
}

type CredentialVault interface {
	Load(context.Context, string) ([]byte, error)
	Save(context.Context, string, []byte) error
	Delete(context.Context, string) error
}

type CredentialVaultInspector interface {
	Exists(context.Context, string) (bool, error)
}

type CanonicalCredentialStore interface {
	Read(context.Context) ([]byte, error)
	Replace(context.Context, []byte) error
}

type ValidationResult struct {
	Identity   ProviderIdentity
	Quota      QuotaSnapshot
	Credential []byte
}

type AccountValidator interface {
	Validate(context.Context, []byte) (ValidationResult, error)
}

type OperationLock interface {
	Release()
}

type AccountService struct {
	Repository AccountRepository
	Vault      CredentialVault
	Canonical  CanonicalCredentialStore
	Identity   IdentityResolver
	Validator  AccountValidator
	Acquire    func() (OperationLock, error)
	Now        func() time.Time
	Operation  func() string
	Log        func(string, ...any)
}

func (s *AccountService) logf(format string, args ...any) {
	if s != nil && s.Log != nil {
		s.Log("accounts "+format, args...)
	}
}

func accountIDPrefix(id string) string {
	if len(id) <= 8 {
		return id
	}
	return id[:8]
}

func (s *AccountService) now() time.Time {
	if s.Now != nil {
		return s.Now().UTC()
	}
	return time.Now().UTC()
}

func (s *AccountService) operationID() string {
	if s.Operation != nil {
		return s.Operation()
	}
	var raw [12]byte
	if _, err := rand.Read(raw[:]); err != nil {
		return "op-fallback"
	}
	return "op-" + hex.EncodeToString(raw[:])
}

func (s *AccountService) lock() (OperationLock, error) {
	if s.Acquire == nil {
		return nil, errors.New("account operation lock is unavailable")
	}
	lock, err := s.Acquire()
	if err != nil {
		return nil, fmt.Errorf("acquire account operation lock: %w", err)
	}
	return lock, nil
}

func (s *AccountService) ensureCleanTransaction() error {
	_, exists, err := s.Repository.LoadTransaction()
	if err != nil {
		return err
	}
	if exists {
		return errors.New("account operation is incomplete; run cagy accounts doctor")
	}
	return nil
}

func (s *AccountService) replaceCanonical(ctx context.Context, credential []byte) error {
	return s.Canonical.Replace(ctx, credential)
}

func (s *AccountService) readCanonical(ctx context.Context, _ Catalog) ([]byte, error) {
	return s.Canonical.Read(ctx)
}

func (s *AccountService) ImportActive(ctx context.Context) (Account, error) {
	s.logf("import-active begin")
	lock, err := s.lock()
	if err != nil {
		return Account{}, err
	}
	defer lock.Release()
	if err := s.ensureCleanTransaction(); err != nil {
		return Account{}, err
	}
	catalog, catalogErr := s.Repository.LoadCatalog()
	if catalogErr != nil {
		return Account{}, catalogErr
	}
	credential, err := s.readCanonical(ctx, catalog)
	if err != nil {
		return Account{}, fmt.Errorf("read active agy credential: %w", err)
	}
	defer Zero(credential)
	result, err := s.validate(ctx, credential)
	if err != nil {
		return Account{}, fmt.Errorf("validate active agy account: %w", err)
	}
	defer Zero(result.Credential)
	account, err := s.upsertValidated(ctx, result, "agy account")
	if err != nil {
		s.logf("import-active failed error=%q", err)
		return Account{}, err
	}
	s.logf("import-active success account=%q", accountIDPrefix(account.ID))
	return account, nil
}

func (s *AccountService) validate(ctx context.Context, credential []byte) (ValidationResult, error) {
	if s.Validator != nil {
		result, err := s.Validator.Validate(ctx, credential)
		if err != nil {
			return ValidationResult{}, err
		}
		if len(result.Credential) == 0 {
			result.Credential = append([]byte(nil), credential...)
		}
		return result, nil
	}
	if s.Identity == nil {
		return ValidationResult{}, errors.New("account identity validator is unavailable")
	}
	identity, err := s.Identity.Resolve(ctx, credential)
	if err != nil {
		return ValidationResult{}, err
	}
	return ValidationResult{Identity: identity, Credential: append([]byte(nil), credential...)}, nil
}

type credentialSnapshot struct {
	value  []byte
	exists bool
}

func (s *AccountService) snapshotCredential(ctx context.Context, accountID string) (credentialSnapshot, error) {
	value, err := s.Vault.Load(ctx, accountID)
	if err == nil {
		return credentialSnapshot{value: value, exists: true}, nil
	}
	inspector, ok := s.Vault.(CredentialVaultInspector)
	if !ok {
		return credentialSnapshot{}, fmt.Errorf("inspect account credential: %w", err)
	}
	exists, inspectErr := inspector.Exists(ctx, accountID)
	if inspectErr != nil {
		return credentialSnapshot{}, fmt.Errorf("inspect account credential: %w", inspectErr)
	}
	if exists {
		return credentialSnapshot{}, errors.New("account credential exists but could not be read safely")
	}
	return credentialSnapshot{}, nil
}

func (s *AccountService) restoreCredential(ctx context.Context, accountID string, snapshot credentialSnapshot) error {
	if snapshot.exists {
		return s.Vault.Save(ctx, accountID, snapshot.value)
	}
	return s.Vault.Delete(ctx, accountID)
}

func cloneCatalog(catalog Catalog) Catalog {
	cloned := catalog
	cloned.Accounts = append([]Account(nil), catalog.Accounts...)
	return cloned
}

func mutationRollbackError(cause error, rollbackErrors ...error) error {
	nonNil := make([]error, 0, len(rollbackErrors))
	for _, rollbackErr := range rollbackErrors {
		if rollbackErr != nil {
			nonNil = append(nonNil, rollbackErr)
		}
	}
	if len(nonNil) == 0 {
		return cause
	}
	return fmt.Errorf("%w; rollback failed: %v; run cagy accounts doctor", cause, errors.Join(nonNil...))
}

func (s *AccountService) upsertValidated(ctx context.Context, result ValidationResult, defaultLabel string) (Account, error) {
	if result.Identity.Subject == "" || result.Identity.Email == "" {
		return Account{}, errors.New("validated account identity is incomplete")
	}
	id, err := StableID(result.Identity.Subject)
	if err != nil {
		return Account{}, err
	}
	fingerprint, err := CredentialFingerprint(result.Credential)
	if err != nil {
		return Account{}, err
	}
	catalog, err := s.Repository.LoadCatalog()
	if err != nil {
		return Account{}, err
	}
	previousCatalog := cloneCatalog(catalog)
	previousCredential, err := s.snapshotCredential(ctx, id)
	if err != nil {
		return Account{}, err
	}
	defer Zero(previousCredential.value)
	account, found := catalog.Find(id)
	if !found {
		label, labelErr := NormalizeLabel(defaultLabel)
		if labelErr != nil {
			label = "agy account " + id[:8]
		}
		label = uniqueAccountLabel(catalog, label, id)
		account = Account{ID: id, Provider: ProviderGoogle, Label: label, Email: result.Identity.Email, State: StateUnknown}
	}
	account.Email = result.Identity.Email
	account.CredentialFingerprint = fingerprint
	if result.Quota.ObservedAt.IsZero() {
		account.LastVerifiedAt = s.now()
		account.State = StateUnknown
		account.Quota = nil
		account.ConsecutiveFailures = 0
		account.CooldownUntil = time.Time{}
	} else {
		account = RecordSuccess(account, s.now(), result.Quota)
	}
	if err := s.Vault.Save(ctx, id, result.Credential); err != nil {
		return Account{}, fmt.Errorf("save account credential: %w", err)
	}
	updated := false
	for index := range catalog.Accounts {
		if catalog.Accounts[index].ID == id {
			catalog.Accounts[index] = account
			updated = true
			break
		}
	}
	if !updated {
		catalog.Accounts = append(catalog.Accounts, account)
		if catalog.DefaultAccountID == "" {
			catalog.DefaultAccountID = id
		}
	}
	catalog.Revision++
	if err := s.Repository.SaveCatalog(catalog); err != nil {
		cause := fmt.Errorf("save account catalog: %w", err)
		vaultErr := s.restoreCredential(ctx, id, previousCredential)
		catalogErr := s.Repository.SaveCatalog(previousCatalog)
		return Account{}, mutationRollbackError(cause, vaultErr, catalogErr)
	}
	return account, nil
}

func uniqueAccountLabel(catalog Catalog, label, accountID string) string {
	available := func(candidate string) bool {
		for _, account := range catalog.Accounts {
			if account.ID != accountID && strings.EqualFold(account.Label, candidate) {
				return false
			}
		}
		return true
	}
	if available(label) {
		return label
	}
	for length := 8; length <= len(accountID); length += 4 {
		candidate := labelWithSuffix(label, accountID[:length])
		if available(candidate) {
			return candidate
		}
	}
	for counter := 2; ; counter++ {
		candidate := labelWithSuffix(label, fmt.Sprintf("%s-%d", accountID, counter))
		if available(candidate) {
			return candidate
		}
	}
}

func labelWithSuffix(label, suffix string) string {
	const maxLabelRunes = 80
	suffixRunes := []rune(" " + suffix)
	labelRunes := []rune(label)
	limit := maxLabelRunes - len(suffixRunes)
	if limit < 1 {
		// Account IDs are bounded, so this is only a defensive fallback.
		suffixOnly := []rune(suffix)
		return string(suffixOnly[len(suffixOnly)-maxLabelRunes:])
	}
	if len(labelRunes) > limit {
		labelRunes = labelRunes[:limit]
		for len(labelRunes) > 0 && labelRunes[len(labelRunes)-1] == ' ' {
			labelRunes = labelRunes[:len(labelRunes)-1]
		}
	}
	return string(labelRunes) + string(suffixRunes)
}

// EnrollCredential verifies one OAuth credential directly with Google and stores
// it in cagy's private local vault. It never reads or changes agy's canonical
// Keychain item.
func (s *AccountService) EnrollCredential(ctx context.Context, credential []byte) (Account, error) {
	s.logf("enroll begin")
	lock, err := s.lock()
	if err != nil {
		return Account{}, err
	}
	defer lock.Release()
	if err := s.ensureCleanTransaction(); err != nil {
		return Account{}, err
	}
	if len(credential) == 0 {
		return Account{}, errors.New("account credential is empty")
	}
	if s.Identity == nil {
		return Account{}, errors.New("account identity validator is unavailable")
	}
	identity, err := s.Identity.Resolve(ctx, credential)
	if err != nil {
		return Account{}, fmt.Errorf("verify new Google account: %w", err)
	}
	label := identity.Name
	if label == "" {
		label = "agy account"
	}
	account, err := s.upsertValidated(ctx, ValidationResult{
		Identity:   identity,
		Credential: append([]byte(nil), credential...),
	}, label)
	if err != nil {
		s.logf("enroll failed error=%q", err)
		return Account{}, err
	}
	s.logf("enroll success account=%q", accountIDPrefix(account.ID))
	return account, nil
}

// ImportBackup merges an authenticated backup into the local vault without activating any account.
func (s *AccountService) ImportBackup(ctx context.Context, backupCatalog Catalog, credentials map[string][]byte) error {
	lock, err := s.lock()
	if err != nil {
		return err
	}
	defer lock.Release()
	if err := s.ensureCleanTransaction(); err != nil {
		return err
	}
	if err := validateBackupRecords(backupCatalog, credentials); err != nil {
		return err
	}
	validatedImports := cloneCatalog(backupCatalog)
	for index := range validatedImports.Accounts {
		imported := validatedImports.Accounts[index]
		imported.Quota = nil
		imported.CooldownUntil = time.Time{}
		imported.ConsecutiveFailures = 0
		if s.Identity == nil {
			imported.State = StateNeedsLogin
			imported.LastVerifiedAt = time.Time{}
			validatedImports.Accounts[index] = imported
			continue
		}
		identity, identityErr := s.Identity.Resolve(ctx, credentials[imported.ID])
		if identityErr != nil {
			// An authenticated archive may contain an expired or revoked access
			// token. Keep it for explicit recovery, but never select it
			// automatically until the user logs in again.
			imported.State = StateNeedsLogin
			imported.LastVerifiedAt = time.Time{}
			validatedImports.Accounts[index] = imported
			continue
		}
		verifiedID, idErr := StableID(identity.Subject)
		if idErr != nil || verifiedID != imported.ID || !strings.EqualFold(identity.Email, imported.Email) {
			return errors.New("backup account identity does not match its credential")
		}
		imported.Email = identity.Email
		imported.State = StateUnknown
		imported.LastVerifiedAt = s.now()
		validatedImports.Accounts[index] = imported
	}
	catalog, err := s.Repository.LoadCatalog()
	if err != nil {
		return err
	}
	previousCatalog := cloneCatalog(catalog)
	// Validate every catalog conflict before the first Keychain mutation.
	for _, imported := range validatedImports.Accounts {
		if current, found := catalog.Find(imported.ID); found && current.Email != imported.Email {
			return errors.New("backup account identity conflicts with existing catalog")
		}
	}
	type vaultRollback struct {
		id       string
		previous []byte
		existed  bool
	}
	rollbacks := make([]vaultRollback, 0, len(validatedImports.Accounts))
	rollbackVault := func() error {
		var rollbackErrors []error
		for index := len(rollbacks) - 1; index >= 0; index-- {
			entry := rollbacks[index]
			if entry.existed {
				if err := s.Vault.Save(ctx, entry.id, entry.previous); err != nil {
					rollbackErrors = append(rollbackErrors, err)
				}
			} else {
				if err := s.Vault.Delete(ctx, entry.id); err != nil {
					rollbackErrors = append(rollbackErrors, err)
				}
			}
			Zero(entry.previous)
		}
		return errors.Join(rollbackErrors...)
	}
	for _, imported := range validatedImports.Accounts {
		previous, loadErr := s.Vault.Load(ctx, imported.ID)
		existed := loadErr == nil
		if loadErr != nil {
			inspector, ok := s.Vault.(CredentialVaultInspector)
			if !ok {
				cause := fmt.Errorf("inspect existing account credential before import: %w", loadErr)
				return mutationRollbackError(cause, rollbackVault())
			}
			exists, inspectErr := inspector.Exists(ctx, imported.ID)
			if inspectErr != nil || exists {
				rollbackErr := rollbackVault()
				if inspectErr != nil {
					return mutationRollbackError(fmt.Errorf("inspect existing account credential before import: %w", inspectErr), rollbackErr)
				}
				return mutationRollbackError(errors.New("existing account credential could not be read safely"), rollbackErr)
			}
		}
		entry := vaultRollback{id: imported.ID, previous: previous, existed: existed}
		rollbacks = append(rollbacks, entry)
		if err := s.Vault.Save(ctx, imported.ID, credentials[imported.ID]); err != nil {
			cause := fmt.Errorf("save imported account credential: %w", err)
			return mutationRollbackError(cause, rollbackVault())
		}
		if _, found := catalog.Find(imported.ID); found {
			for index := range catalog.Accounts {
				if catalog.Accounts[index].ID == imported.ID {
					catalog.Accounts[index] = imported
				}
			}
		} else {
			catalog.Accounts = append(catalog.Accounts, imported)
		}
	}
	// Do not import the source default automatically.
	catalog.Revision++
	if err := s.Repository.SaveCatalog(catalog); err != nil {
		vaultErr := rollbackVault()
		catalogErr := s.Repository.SaveCatalog(previousCatalog)
		return mutationRollbackError(err, vaultErr, catalogErr)
	}
	for index := range rollbacks {
		Zero(rollbacks[index].previous)
	}
	return nil
}

// Remove deletes only a cagy vault entry and metadata; it never revokes the provider account.
func (s *AccountService) Remove(ctx context.Context, accountID string, confirmed bool, boundIDs map[string]struct{}) error {
	if !confirmed {
		return errors.New("account removal requires explicit confirmation")
	}
	lock, err := s.lock()
	if err != nil {
		return err
	}
	defer lock.Release()
	if err := s.ensureCleanTransaction(); err != nil {
		return err
	}
	catalog, err := s.Repository.LoadCatalog()
	if err != nil {
		return err
	}
	if catalog.DefaultAccountID == accountID {
		return errors.New("cannot remove the default account; switch to another account first")
	}
	if _, bound := boundIDs[accountID]; bound {
		return errors.New("cannot remove an account bound to a live developer")
	}
	target, found := catalog.Find(accountID)
	if !found {
		return errors.New("account was not found")
	}
	if target.State == StateHealthy {
		healthyAlternative := false
		for _, account := range catalog.Accounts {
			if account.ID != accountID && account.State == StateHealthy {
				healthyAlternative = true
				break
			}
		}
		if !healthyAlternative {
			return errors.New("cannot remove the last healthy account")
		}
	}
	credential, err := s.Vault.Load(ctx, accountID)
	if err != nil {
		return fmt.Errorf("load account credential before removal: %w", err)
	}
	defer Zero(credential)
	previousCatalog := cloneCatalog(catalog)
	if err := s.Vault.Delete(ctx, accountID); err != nil {
		return fmt.Errorf("delete account credential: %w", err)
	}
	for index := range catalog.Accounts {
		if catalog.Accounts[index].ID == accountID {
			catalog.Accounts = append(catalog.Accounts[:index], catalog.Accounts[index+1:]...)
			break
		}
	}
	catalog.Revision++
	if err := s.Repository.SaveCatalog(catalog); err != nil {
		cause := fmt.Errorf("save account catalog after removal: %w", err)
		vaultErr := s.Vault.Save(ctx, accountID, credential)
		catalogErr := s.Repository.SaveCatalog(previousCatalog)
		return mutationRollbackError(cause, vaultErr, catalogErr)
	}
	return nil
}

type DoctorReport struct {
	Transaction *Transaction
	Issues      []string
	Repairable  bool
}

func (s *AccountService) Doctor(ctx context.Context) (DoctorReport, error) {
	s.logf("doctor begin")
	lock, err := s.lock()
	if err != nil {
		return DoctorReport{}, err
	}
	defer lock.Release()
	var report DoctorReport
	transaction, exists, err := s.Repository.LoadTransaction()
	if err != nil {
		report.Issues = append(report.Issues, err.Error())
	} else if exists {
		report.Transaction = &transaction
		report.Issues = append(report.Issues, "an account transaction is incomplete")
		report.Repairable = transaction.PreviousCanonicalID != ""
	}
	catalog, err := s.Repository.LoadCatalog()
	if err != nil {
		report.Issues = append(report.Issues, err.Error())
		return report, nil
	}
	for _, account := range catalog.Accounts {
		credential, loadErr := s.Vault.Load(ctx, account.ID)
		if loadErr != nil {
			report.Issues = append(report.Issues, fmt.Sprintf("account %s credential is unavailable", account.ID[:8]))
		}
		Zero(credential)
	}
	// Deliberately do not access agy's canonical Keychain item. This command is
	// read-only and must never trigger a macOS password dialog.
	if len(report.Issues) == 0 {
		report.Repairable = false
	}
	s.logf("doctor end issues=%d transaction=%t repairable=%t", len(report.Issues), report.Transaction != nil, report.Repairable)
	return report, nil
}

func (s *AccountService) RepairTransaction(ctx context.Context) error {
	lock, err := s.lock()
	if err != nil {
		return err
	}
	defer lock.Release()
	transaction, exists, err := s.Repository.LoadTransaction()
	if err != nil {
		return err
	}
	if !exists {
		return nil
	}
	if transaction.PreviousCanonicalID == "" {
		return errors.New("account transaction cannot be repaired deterministically")
	}
	desiredCanonicalID, desiredDefaultID := transaction.PreviousCanonicalID, transaction.PreviousDefaultID
	if transaction.Phase == PhaseCommitted {
		switch transaction.Kind {
		case TransactionSwitch:
			desiredCanonicalID, desiredDefaultID = transaction.CandidateID, transaction.CandidateID
		case TransactionAdd:
			if transaction.ActivateAfterAdd {
				desiredCanonicalID, desiredDefaultID = transaction.CandidateID, transaction.CandidateID
			}
		}
	}
	credential, err := s.Vault.Load(ctx, desiredCanonicalID)
	if err != nil {
		return errors.New("repair account credential is unavailable")
	}
	defer Zero(credential)
	if err := s.replaceCanonical(ctx, credential); err != nil {
		return fmt.Errorf("restore canonical credential: %w", err)
	}
	catalog, err := s.Repository.LoadCatalog()
	if err != nil {
		return err
	}
	if desiredDefaultID != "" && catalog.DefaultAccountID != desiredDefaultID {
		catalog.DefaultAccountID = desiredDefaultID
		catalog.Revision++
		if err := s.Repository.SaveCatalog(catalog); err != nil {
			return err
		}
	}
	return s.Repository.RemoveTransaction()
}

func (s *AccountService) Switch(ctx context.Context, accountID string) (Account, error) {
	s.logf("switch begin account=%q", accountIDPrefix(accountID))
	lock, err := s.lock()
	if err != nil {
		return Account{}, err
	}
	defer lock.Release()
	if err := s.ensureCleanTransaction(); err != nil {
		return Account{}, err
	}
	catalog, err := s.Repository.LoadCatalog()
	if err != nil {
		return Account{}, err
	}
	previousCatalog := cloneCatalog(catalog)
	target, found := catalog.Find(accountID)
	if !found {
		return Account{}, fmt.Errorf("account %s was not found", accountID)
	}
	credential, err := s.Vault.Load(ctx, target.ID)
	if err != nil {
		return Account{}, fmt.Errorf("load target account credential: %w", err)
	}
	defer Zero(credential)
	previousID := catalog.DefaultAccountID
	if previousID == "" {
		return Account{}, errors.New("default agy account is not set")
	}
	previous, err := s.Vault.Load(ctx, previousID)
	if err != nil {
		return Account{}, errors.New("default account credential is unavailable in the vault")
	}
	defer Zero(previous)
	now := s.now()
	transaction := Transaction{Version: TransactionVersion, OperationID: s.operationID(), Kind: TransactionSwitch, Phase: PhasePrepared, StartedAt: now, UpdatedAt: now, PreviousDefaultID: previousID, PreviousCanonicalID: previousID, CandidateID: target.ID}
	if err := s.Repository.SaveTransaction(transaction); err != nil {
		return Account{}, err
	}
	canonicalChanged, targetVaultUpdated, catalogAttempted := false, false, false
	rollback := func(cause error) (Account, error) {
		transaction.Phase = PhaseRestoring
		transaction.UpdatedAt = s.now()
		_ = s.Repository.SaveTransaction(transaction)
		var restoreErr error
		if canonicalChanged {
			restoreErr = s.replaceCanonical(ctx, previous)
		}
		var vaultErr, catalogErr error
		if targetVaultUpdated {
			vaultErr = s.Vault.Save(ctx, target.ID, credential)
		}
		if catalogAttempted {
			catalogErr = s.Repository.SaveCatalog(previousCatalog)
		}
		if restoreErr != nil || vaultErr != nil || catalogErr != nil {
			return Account{}, mutationRollbackError(cause, restoreErr, vaultErr, catalogErr)
		}
		if err := s.Repository.RemoveTransaction(); err != nil {
			return Account{}, fmt.Errorf("%w; rollback succeeded but transaction cleanup failed: %v", cause, err)
		}
		return Account{}, cause
	}

	// Validate and refresh the local snapshot before changing agy's canonical
	// Keychain item. This prevents an expired or invalid token from making agy
	// open browser OAuth during a manual switch.
	transaction.Phase = PhaseValidating
	transaction.UpdatedAt = s.now()
	if err := s.Repository.SaveTransaction(transaction); err != nil {
		return rollback(err)
	}
	s.logf("switch refresh begin account=%q", accountIDPrefix(target.ID))
	result, err := s.validate(ctx, credential)
	if err != nil {
		s.logf("switch refresh failed account=%q error=%q", accountIDPrefix(target.ID), err)
		return rollback(fmt.Errorf("validate switched account: %w", err))
	}
	s.logf("switch refresh success account=%q credential_changed=%t", accountIDPrefix(target.ID), len(result.Credential) > 0 && !bytes.Equal(result.Credential, credential))
	defer Zero(result.Credential)
	verifiedID, err := StableID(result.Identity.Subject)
	if err != nil || verifiedID != target.ID {
		return rollback(errors.New("switched credential identity did not match the requested account"))
	}
	refreshed := result.Credential
	if len(refreshed) == 0 {
		refreshed = credential
	}
	fingerprint, err := CredentialFingerprint(refreshed)
	if err != nil {
		return rollback(err)
	}
	if !bytes.Equal(refreshed, credential) {
		if err := s.Vault.Save(ctx, target.ID, refreshed); err != nil {
			return rollback(fmt.Errorf("save refreshed account credential: %w", err))
		}
		targetVaultUpdated = true
	}
	s.logf("switch canonical replace begin account=%q", accountIDPrefix(target.ID))
	if err := s.Canonical.Replace(ctx, refreshed); err != nil {
		s.logf("switch canonical replace failed account=%q error=%q", accountIDPrefix(target.ID), err)
		return rollback(fmt.Errorf("activate account: %w", err))
	}
	canonicalChanged = true
	s.logf("switch canonical replace success account=%q", accountIDPrefix(target.ID))
	transaction.Phase = PhaseCandidateActive
	transaction.UpdatedAt = s.now()
	if err := s.Repository.SaveTransaction(transaction); err != nil {
		return rollback(err)
	}

	target.CredentialFingerprint = fingerprint
	usedAt := s.now()
	target = RecordSuccess(target, usedAt, result.Quota)
	target.LastUsedAt = usedAt
	for i := range catalog.Accounts {
		if catalog.Accounts[i].ID == target.ID {
			catalog.Accounts[i] = target
		}
	}
	catalog.DefaultAccountID = target.ID
	catalog.Revision++
	catalogAttempted = true
	if err := s.Repository.SaveCatalog(catalog); err != nil {
		return rollback(fmt.Errorf("commit switched account: %w", err))
	}
	transaction.Phase = PhaseCommitted
	transaction.UpdatedAt = s.now()
	if err := s.Repository.SaveTransaction(transaction); err != nil {
		return rollback(fmt.Errorf("save committed account transaction: %w", err))
	}
	if err := s.Repository.RemoveTransaction(); err != nil {
		return Account{}, err
	}
	s.logf("switch success account=%q", accountIDPrefix(target.ID))
	return target, nil
}

// WithAccount temporarily makes one stored account canonical while fn runs.
// It validates the account live, captures refreshed credentials, updates only
// that account's health metadata, and always restores the previous canonical
// credential. The catalog default is never changed. A durable transaction lets
// accounts doctor restore the previous account after a crash at any phase.
func (s *AccountService) WithAccount(ctx context.Context, accountID string, fn func(context.Context) error) (Account, error) {
	return s.withAccount(ctx, accountID, TransactionProbe, "", fn)
}

// WithDeveloperAccount is the developer-start variant of WithAccount. It
// records only the non-secret developer identifier in the recovery journal.
func (s *AccountService) WithDeveloperAccount(ctx context.Context, accountID, developer string, fn func(context.Context) error) (Account, error) {
	return s.withAccount(ctx, accountID, TransactionDeveloperStart, developer, fn)
}

func (s *AccountService) withAccount(ctx context.Context, accountID string, kind TransactionKind, targetDeveloper string, fn func(context.Context) error) (Account, error) {
	lock, err := s.lock()
	if err != nil {
		return Account{}, err
	}
	defer lock.Release()
	if err := s.ensureCleanTransaction(); err != nil {
		return Account{}, err
	}
	catalog, err := s.Repository.LoadCatalog()
	if err != nil {
		return Account{}, err
	}
	target, found := catalog.Find(accountID)
	if !found {
		return Account{}, fmt.Errorf("account %s was not found", accountID)
	}
	credential, err := s.Vault.Load(ctx, target.ID)
	if err != nil {
		return Account{}, fmt.Errorf("load bound account credential: %w", err)
	}
	defer Zero(credential)
	previousID := catalog.DefaultAccountID
	if previousID == "" {
		return Account{}, errors.New("default agy account is not set")
	}
	previous, err := s.Vault.Load(ctx, previousID)
	if err != nil {
		return Account{}, errors.New("default account credential is unavailable in the vault")
	}
	defer Zero(previous)
	now := s.now()
	tx := Transaction{Version: TransactionVersion, OperationID: s.operationID(), Kind: kind, Phase: PhasePrepared, StartedAt: now, UpdatedAt: now, PreviousDefaultID: previousID, PreviousCanonicalID: previousID, CandidateID: target.ID, TargetDeveloper: targetDeveloper}
	if err := s.Repository.SaveTransaction(tx); err != nil {
		return Account{}, err
	}
	if err := s.Canonical.Replace(ctx, credential); err != nil {
		_ = s.Repository.RemoveTransaction()
		return Account{}, fmt.Errorf("activate account: %w", err)
	}
	tx.Phase = PhaseCandidateActive
	tx.UpdatedAt = s.now()
	_ = s.Repository.SaveTransaction(tx)
	tx.Phase = PhaseValidating
	tx.UpdatedAt = s.now()
	if err := s.Repository.SaveTransaction(tx); err != nil {
		return Account{}, err
	}
	result, err := s.validate(ctx, credential)
	if err != nil {
		_ = s.replaceCanonical(ctx, previous)
		_ = s.Repository.RemoveTransaction()
		return Account{}, fmt.Errorf("validate bound account: %w", err)
	}
	defer Zero(result.Credential)
	verifiedID, err := StableID(result.Identity.Subject)
	if err != nil || verifiedID != target.ID {
		_ = s.replaceCanonical(ctx, previous)
		_ = s.Repository.RemoveTransaction()
		return Account{}, errors.New("bound credential identity did not match the requested account")
	}
	refreshed := result.Credential
	if len(refreshed) == 0 {
		refreshed = credential
	}
	fingerprint, _ := CredentialFingerprint(refreshed)
	if len(result.Credential) > 0 && !bytes.Equal(result.Credential, credential) {
		_ = s.Vault.Save(ctx, target.ID, result.Credential)
	}
	target.CredentialFingerprint = fingerprint
	target = RecordSuccess(target, s.now(), result.Quota)
	for i := range catalog.Accounts {
		if catalog.Accounts[i].ID == target.ID {
			catalog.Accounts[i] = target
		}
	}
	catalog.Revision++
	_ = s.Repository.SaveCatalog(catalog)
	if target.Quota == nil || (target.Quota.Class != QuotaAvailable) {
		_ = s.restoreWithAccountCanonical(ctx, previousID, target.ID, refreshed, previous)
		_ = s.Repository.RemoveTransaction()
		return target, nil
	}
	if fn != nil {
		if err := fn(ctx); err != nil {
			_ = s.restoreWithAccountCanonical(ctx, previousID, target.ID, refreshed, previous)
			_ = s.Repository.RemoveTransaction()
			return Account{}, err
		}
	}
	if err := s.restoreWithAccountCanonical(ctx, previousID, target.ID, refreshed, previous); err != nil {
		return Account{}, err
	}
	tx.Phase = PhaseCommitted
	tx.UpdatedAt = s.now()
	_ = s.Repository.SaveTransaction(tx)
	if err := s.Repository.RemoveTransaction(); err != nil {
		return Account{}, err
	}
	return target, nil
}

func (s *AccountService) restoreWithAccountCanonical(ctx context.Context, previousID, targetID string, refreshed, previous []byte) error {
	if previousID == targetID {
		return s.replaceCanonical(ctx, refreshed)
	}
	return s.replaceCanonical(ctx, previous)
}

// RecordAccountFailure applies a bounded cooldown after a failed automatic
// recovery attempt. Permanent authentication failures can be marked as
// needs_login by callers that have strong identity evidence.
func (s *AccountService) RecordAccountFailure(accountID string, permanent bool) error {
	lock, err := s.lock()
	if err != nil {
		return err
	}
	defer lock.Release()
	if err := s.ensureCleanTransaction(); err != nil {
		return err
	}
	catalog, err := s.Repository.LoadCatalog()
	if err != nil {
		return err
	}
	found := false
	for index := range catalog.Accounts {
		if catalog.Accounts[index].ID == accountID {
			catalog.Accounts[index] = RecordFailure(catalog.Accounts[index], s.now(), permanent)
			found = true
			break
		}
	}
	if !found {
		return errors.New("account was not found")
	}
	catalog.Revision++
	return s.Repository.SaveCatalog(catalog)
}

// RecordQuota persists one live agy quota observation for a stored account. It
// never reads or changes credential material or the canonical Keychain item.
func (s *AccountService) RecordQuota(accountID string, quota QuotaSnapshot) error {
	if err := quota.Validate(); err != nil {
		return fmt.Errorf("validate account quota: %w", err)
	}
	lock, err := s.lock()
	if err != nil {
		return err
	}
	defer lock.Release()
	if err := s.ensureCleanTransaction(); err != nil {
		return err
	}
	catalog, err := s.Repository.LoadCatalog()
	if err != nil {
		return err
	}
	found := false
	for index := range catalog.Accounts {
		if catalog.Accounts[index].ID == accountID {
			catalog.Accounts[index] = RecordSuccess(catalog.Accounts[index], s.now(), quota)
			found = true
			break
		}
	}
	if !found {
		return errors.New("account was not found")
	}
	catalog.Revision++
	return s.Repository.SaveCatalog(catalog)
}

// CredentialFailureNeedsLogin reports only strong local/OAuth evidence that a
// stored credential cannot be reused without an explicit account login.
func CredentialFailureNeedsLogin(err error) bool {
	return errors.Is(err, ErrCredentialNeedsLogin)
}

// SetDefaultAccount reconciles non-secret catalog metadata with a verified
// cagy developer binding. It never reads or changes credential material or the
// canonical Keychain item.
func (s *AccountService) SetDefaultAccount(accountID string) error {
	lock, err := s.lock()
	if err != nil {
		return err
	}
	defer lock.Release()
	if err := s.ensureCleanTransaction(); err != nil {
		return err
	}
	catalog, err := s.Repository.LoadCatalog()
	if err != nil {
		return err
	}
	if _, found := catalog.Find(accountID); !found {
		return errors.New("account was not found")
	}
	if catalog.DefaultAccountID == accountID {
		return nil
	}
	catalog.DefaultAccountID = accountID
	catalog.Revision++
	return s.Repository.SaveCatalog(catalog)
}
