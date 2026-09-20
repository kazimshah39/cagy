package accounts

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func backupFixture(t *testing.T) (Catalog, map[string][]byte) {
	t.Helper()
	now := time.Date(2026, time.September, 18, 12, 0, 0, 0, time.UTC)
	first := testAccount(t, "one", "Primary", "one@example.com")
	second := testAccount(t, "two", "Secondary", "two@example.com")
	credentials := map[string][]byte{first.ID: []byte(`{"token":{"access_token":"one"}}`), second.ID: []byte(`{"token":{"access_token":"two"}}`)}
	first.CredentialFingerprint, _ = CredentialFingerprint(credentials[first.ID])
	second.CredentialFingerprint, _ = CredentialFingerprint(credentials[second.ID])
	first.LastVerifiedAt, second.LastVerifiedAt = now, now
	catalog := Catalog{Version: CatalogVersion, Revision: 4, DefaultAccountID: first.ID, Accounts: []Account{first, second}}
	return catalog, credentials
}

func TestEncryptedBackupRoundTripAndMetadataIdentityOutput(t *testing.T) {
	catalog, credentials := backupFixture(t)
	dir := t.TempDir()
	path := filepath.Join(dir, "accounts.cagy")
	if err := ExportEncrypted(path, catalog, credentials, []byte("correct horse battery staple"), false, time.Now()); err != nil {
		t.Fatal(err)
	}
	loaded, gotCredentials, err := ImportEncrypted(path, []byte("correct horse battery staple"))
	if err != nil {
		t.Fatal(err)
	}
	if loaded.DefaultAccountID != catalog.DefaultAccountID || len(gotCredentials) != 2 {
		t.Fatalf("loaded=%+v credentials=%d", loaded, len(gotCredentials))
	}
	for id, value := range credentials {
		if string(gotCredentials[id]) != string(value) {
			t.Fatalf("credential %s mismatch", id)
		}
	}
	metadataPath := filepath.Join(dir, "metadata.json")
	if err := ExportMetadata(metadataPath, catalog, false, time.Now()); err != nil {
		t.Fatal(err)
	}
	metadata, err := os.ReadFile(metadataPath)
	if err != nil {
		t.Fatal(err)
	}
	text := string(metadata)
	for _, identity := range []string{`"email": "one@example.com"`, `"email": "two@example.com"`} {
		if !strings.Contains(text, identity) {
			t.Fatalf("metadata is missing full identity %q: %s", identity, metadata)
		}
	}
	for _, forbidden := range []string{"access_token", "refresh_token", "credential-one", `"credentials"`, "credential_fingerprint"} {
		if strings.Contains(text, forbidden) {
			t.Fatalf("metadata contains credential material %q: %s", forbidden, metadata)
		}
	}
	if info, _ := os.Stat(metadataPath); info.Mode().Perm() != 0o600 {
		t.Fatalf("metadata mode=%o", info.Mode().Perm())
	}
}

func TestEncryptedBackupRejectsWrongPassphraseTamperAndUnsafeOutput(t *testing.T) {
	catalog, credentials := backupFixture(t)
	dir := t.TempDir()
	path := filepath.Join(dir, "accounts.cagy")
	if err := ExportEncrypted(path, catalog, credentials, []byte("passphrase"), false, time.Now()); err != nil {
		t.Fatal(err)
	}
	if _, _, err := ImportEncrypted(path, []byte("wrong")); err == nil || !strings.Contains(err.Error(), "authentication") {
		t.Fatalf("wrong passphrase error=%v", err)
	}
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	data[len(data)-1] ^= 1
	if err := os.WriteFile(path, data, 0o600); err != nil {
		t.Fatal(err)
	}
	if _, _, err := ImportEncrypted(path, []byte("passphrase")); err == nil || !strings.Contains(err.Error(), "authentication") {
		t.Fatalf("tamper error=%v", err)
	}
	if err := ExportEncrypted(filepath.Join(dir, "accounts.cagy"), catalog, credentials, []byte("passphrase"), false, time.Now()); err == nil {
		t.Fatal("expected overwrite refusal")
	}
	if err := ExportEncrypted(filepath.Join(dir, "missing", "accounts.cagy"), catalog, credentials, []byte("passphrase"), false, time.Now()); err == nil {
		t.Fatal("expected missing parent rejection")
	}
}

func TestEncryptedBackupRejectsTruncationAndCredentialMismatch(t *testing.T) {
	catalog, credentials := backupFixture(t)
	dir := t.TempDir()
	path := filepath.Join(dir, "accounts.cagy")
	if err := ExportEncrypted(path, catalog, credentials, []byte("passphrase"), false, time.Now()); err != nil {
		t.Fatal(err)
	}
	data, _ := os.ReadFile(path)
	if err := os.WriteFile(path, data[:len(data)/2], 0o600); err != nil {
		t.Fatal(err)
	}
	if _, _, err := ImportEncrypted(path, []byte("passphrase")); err == nil {
		t.Fatal("expected truncation rejection")
	}
	credentials[catalog.Accounts[0].ID] = []byte("changed")
	if err := ExportEncrypted(filepath.Join(dir, "mismatch.cagy"), catalog, credentials, []byte("passphrase"), false, time.Now()); err == nil || !strings.Contains(err.Error(), "fingerprint") {
		t.Fatalf("mismatch error=%v", err)
	}
}

func TestEncryptedBackupPreservesRotationState(t *testing.T) {
	catalog, credentials := backupFixture(t)
	if err := catalog.NormalizeRotation(time.Unix(20, 0)); err != nil {
		t.Fatal(err)
	}
	catalog.Rotation.CursorAccountID = catalog.Rotation.Order[1]
	catalog.Rotation.UpdatedAt = time.Unix(21, 0).UTC()
	path := filepath.Join(t.TempDir(), "accounts.cagy")
	if err := ExportEncrypted(path, catalog, credentials, []byte("passphrase"), false, time.Unix(22, 0)); err != nil {
		t.Fatal(err)
	}
	loaded, _, err := ImportEncrypted(path, []byte("passphrase"))
	if err != nil {
		t.Fatal(err)
	}
	if loaded.Rotation == nil || !equalStrings(loaded.Rotation.Order, catalog.Rotation.Order) || loaded.Rotation.CursorAccountID != catalog.Rotation.CursorAccountID {
		t.Fatalf("rotation=%+v want=%+v", loaded.Rotation, catalog.Rotation)
	}
}

func TestMetadataExportIncludesRotationWithoutCredentials(t *testing.T) {
	catalog, _ := backupFixture(t)
	if err := catalog.NormalizeRotation(time.Unix(20, 0)); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(t.TempDir(), "metadata.json")
	if err := ExportMetadata(path, catalog, false, time.Unix(22, 0)); err != nil {
		t.Fatal(err)
	}
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	text := string(data)
	if !strings.Contains(text, `"rotation"`) || !strings.Contains(text, catalog.Rotation.Order[0]) {
		t.Fatalf("rotation missing: %s", text)
	}
	for _, forbidden := range []string{"access_token", "refresh_token", "credential_fingerprint"} {
		if strings.Contains(text, forbidden) {
			t.Fatalf("metadata leaked %q", forbidden)
		}
	}
}
