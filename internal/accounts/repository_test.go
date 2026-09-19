package accounts

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestRepositoryCatalogRoundTripAndPrivacy(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "state")
	repository := NewRepository(dir)
	account := testAccount(t, "one", "Primary", "one@example.com")
	catalog := Catalog{Version: CatalogVersion, Revision: 1, DefaultAccountID: account.ID, Accounts: []Account{account}}
	if err := repository.SaveCatalog(catalog); err != nil {
		t.Fatal(err)
	}
	loaded, err := repository.LoadCatalog()
	if err != nil || loaded.DefaultAccountID != account.ID || len(loaded.Accounts) != 1 {
		t.Fatalf("catalog=%+v err=%v", loaded, err)
	}
	data, err := os.ReadFile(filepath.Join(dir, catalogFileName))
	if err != nil {
		t.Fatal(err)
	}
	for _, forbidden := range []string{"refresh_token", "access_token", "password", "credential-one"} {
		if strings.Contains(string(data), forbidden) {
			t.Fatalf("catalog contains secret-shaped data %q: %s", forbidden, data)
		}
	}
	info, _ := os.Stat(filepath.Join(dir, catalogFileName))
	if info.Mode().Perm() != 0o600 {
		t.Fatalf("catalog mode=%o", info.Mode().Perm())
	}
}

func TestRepositoryRejectsUnknownAndUnsafeCatalog(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "state")
	if err := os.MkdirAll(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(dir, catalogFileName)
	if err := os.WriteFile(path, []byte(`{"version":1,"revision":0,"accounts":[],"secret":"bad"}`), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := NewRepository(dir).LoadCatalog(); err == nil || !strings.Contains(err.Error(), "unknown field") {
		t.Fatalf("unknown field error=%v", err)
	}
	if err := os.Remove(path); err != nil {
		t.Fatal(err)
	}
	outside := filepath.Join(t.TempDir(), "outside")
	if err := os.WriteFile(outside, []byte("x"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(outside, path); err != nil {
		t.Fatal(err)
	}
	if _, err := NewRepository(dir).LoadCatalog(); err == nil || !strings.Contains(err.Error(), "regular") {
		t.Fatalf("symlink error=%v", err)
	}
}

func TestRepositoryTransactionRoundTrip(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "state")
	repository := NewRepository(dir)
	now := time.Date(2026, time.September, 18, 12, 0, 0, 0, time.UTC)
	transaction := Transaction{Version: TransactionVersion, OperationID: "operation-123", Kind: TransactionAdd, Phase: PhasePrepared, StartedAt: now, UpdatedAt: now}
	if err := repository.SaveTransaction(transaction); err != nil {
		t.Fatal(err)
	}
	loaded, exists, err := repository.LoadTransaction()
	if err != nil || !exists || loaded.OperationID != transaction.OperationID {
		t.Fatalf("transaction=%+v exists=%v err=%v", loaded, exists, err)
	}
	if err := repository.RemoveTransaction(); err != nil {
		t.Fatal(err)
	}
	_, exists, err = repository.LoadTransaction()
	if err != nil || exists {
		t.Fatalf("removed exists=%v err=%v", exists, err)
	}
}
