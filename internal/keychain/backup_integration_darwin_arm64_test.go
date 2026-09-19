//go:build darwin && arm64 && cgo

package keychain

import (
	"bytes"
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/kazimshah39/cagy/internal/accounts"
)

type disposableVault struct {
	store   Store
	service string
	access  Access
}

func (v disposableVault) Load(ctx context.Context, accountID string) ([]byte, error) {
	return v.store.Read(ctx, Item{Service: v.service, Account: accountID})
}
func (v disposableVault) Save(ctx context.Context, accountID string, credential []byte) error {
	return v.store.Save(ctx, Item{Service: v.service, Account: accountID}, credential, v.access)
}
func (v disposableVault) Delete(ctx context.Context, accountID string) error {
	return v.store.Delete(ctx, Item{Service: v.service, Account: accountID})
}
func (v disposableVault) Exists(ctx context.Context, accountID string) (bool, error) {
	return v.store.Exists(ctx, Item{Service: v.service, Account: accountID})
}

type disposableIdentity struct {
	credential string
	identity   accounts.ProviderIdentity
}

func (i disposableIdentity) Resolve(_ context.Context, credential []byte) (accounts.ProviderIdentity, error) {
	if string(credential) != i.credential {
		return accounts.ProviderIdentity{}, fmt.Errorf("identity mismatch")
	}
	return i.identity, nil
}

type disposableLock struct{}

func (disposableLock) Release() {}

func TestDarwinEncryptedBackupImportIntegration(t *testing.T) {
	if os.Getenv("CAGY_KEYCHAIN_INTEGRATION") != "1" {
		t.Skip("set CAGY_KEYCHAIN_INTEGRATION=1 to run disposable Keychain integration")
	}
	serviceName := fmt.Sprintf("com.kazimshah39.cagy.backup-test.%d.%d", os.Getpid(), time.Now().UnixNano())
	if serviceName == CanonicalService || serviceName == VaultService || strings.Contains(serviceName, "gemini") {
		t.Fatal("test service must be unique and must never match a real cagy or agy service")
	}
	store := New()
	ctx := context.Background()
	executable, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	executable, err = filepath.EvalSymlinks(executable)
	if err != nil {
		t.Fatal(err)
	}
	vault := disposableVault{
		store:   store,
		service: serviceName,
		access:  Access{Label: "cagy disposable backup test", TrustedPaths: []string{executable}},
	}
	credential := []byte(`{"token":{"access_token":"disposable-backup-token"}}`)
	accountID, err := accounts.StableID("disposable-backup-subject")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = vault.Delete(ctx, accountID) })
	fingerprint, err := accounts.CredentialFingerprint(credential)
	if err != nil {
		t.Fatal(err)
	}
	now := time.Date(2026, time.September, 18, 0, 0, 0, 0, time.UTC)
	account := accounts.Account{
		ID:                    accountID,
		Provider:              accounts.ProviderGoogle,
		Label:                 "Disposable Backup",
		Email:                 "backup-test@example.com",
		CredentialFingerprint: fingerprint,
		State:                 accounts.StateHealthy,
		LastVerifiedAt:        now,
	}
	catalog := accounts.Catalog{Version: accounts.CatalogVersion, Revision: 1, DefaultAccountID: accountID, Accounts: []accounts.Account{account}}
	archivePath := filepath.Join(t.TempDir(), "accounts.cagy")
	passphrase := []byte("disposable-integration-passphrase")
	defer accounts.Zero(passphrase)
	if err := accounts.ExportEncrypted(archivePath, catalog, map[string][]byte{accountID: credential}, passphrase, false, now); err != nil {
		t.Fatal(err)
	}
	archive, err := os.ReadFile(archivePath)
	if err != nil {
		t.Fatal(err)
	}
	if bytes.Contains(archive, credential) || bytes.Contains(archive, []byte(account.Email)) {
		t.Fatal("encrypted archive contains plaintext credential or email")
	}
	importedCatalog, importedCredentials, err := accounts.ImportEncrypted(archivePath, passphrase)
	if err != nil {
		t.Fatal(err)
	}
	defer func() {
		for _, imported := range importedCredentials {
			accounts.Zero(imported)
		}
	}()
	stateDir := t.TempDir()
	if err := os.Chmod(stateDir, 0o700); err != nil {
		t.Fatal(err)
	}
	repository := accounts.NewRepository(stateDir)
	identity := disposableIdentity{
		credential: string(credential),
		identity:   accounts.ProviderIdentity{Subject: "disposable-backup-subject", Email: account.Email},
	}
	service := accounts.AccountService{
		Repository: repository,
		Vault:      vault,
		Identity:   identity,
		Acquire:    func() (accounts.OperationLock, error) { return disposableLock{}, nil },
		Now:        func() time.Time { return now },
	}
	if err := service.ImportBackup(ctx, importedCatalog, importedCredentials); err != nil {
		t.Fatal(err)
	}
	stored, err := vault.Load(ctx, accountID)
	if err != nil {
		t.Fatal(err)
	}
	defer accounts.Zero(stored)
	if !bytes.Equal(stored, credential) {
		t.Fatal("imported Keychain credential did not round-trip")
	}
	loaded, err := repository.LoadCatalog()
	if err != nil {
		t.Fatal(err)
	}
	importedAccount, found := loaded.Find(accountID)
	if !found || importedAccount.State != accounts.StateUnknown || importedAccount.Quota != nil {
		t.Fatalf("imported account=%+v", importedAccount)
	}
}
