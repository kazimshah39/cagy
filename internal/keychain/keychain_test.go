package keychain

import (
	"context"
	"encoding/base64"
	"strings"
	"testing"
)

type fakeStore struct {
	items map[Item][]byte
}

func (f *fakeStore) Read(_ context.Context, item Item) ([]byte, error) {
	value, ok := f.items[item]
	if !ok {
		return nil, ErrNotFound
	}
	return append([]byte(nil), value...), nil
}
func (f *fakeStore) Save(_ context.Context, item Item, secret []byte, _ Access) error {
	f.items[item] = append([]byte(nil), secret...)
	return nil
}
func (f *fakeStore) Delete(_ context.Context, item Item) error {
	delete(f.items, item)
	return nil
}
func (f *fakeStore) Exists(_ context.Context, item Item) (bool, error) {
	_, ok := f.items[item]
	return ok, nil
}
func TestVaultAndCanonicalContracts(t *testing.T) {
	store := &fakeStore{items: map[Item][]byte{}}
	vault := CredentialVault{Store: store}
	canonical := CanonicalStore{Store: store}
	ctx := context.Background()
	if err := vault.Save(ctx, "account-id", []byte("vault-secret")); err != nil {
		t.Fatal(err)
	}
	got, err := vault.Load(ctx, "account-id")
	if err != nil || string(got) != "vault-secret" {
		t.Fatalf("vault=%q err=%v", got, err)
	}
	if err := canonical.Replace(ctx, []byte("active-secret")); err != nil {
		t.Fatal(err)
	}
	if err := canonical.Replace(ctx, []byte("replaced-secret")); err != nil {
		t.Fatal(err)
	}
	replaced, err := canonical.Read(ctx)
	if err != nil || string(replaced) != "replaced-secret" {
		t.Fatalf("replaced=%q err=%v", replaced, err)
	}
}

func TestErrorAndZeroNeverExposeSecret(t *testing.T) {
	secret := []byte("top-secret-token")
	err := (&Error{Op: "save", Kind: ErrorDenied, Status: -1}).Error()
	if err == "" || err == string(secret) {
		t.Fatalf("unsafe error=%q", err)
	}
	Zero(secret)
	for _, value := range secret {
		if value != 0 {
			t.Fatalf("secret was not cleared: %v", secret)
		}
	}
}

func TestCanonicalKeychainPayloadNormalizesAgyCredential(t *testing.T) {
	raw := []byte(`{"token":{"access_token":"access","refresh_token":"refresh"},"auth_method":"consumer"}`)
	payload, err := canonicalKeychainPayload(raw)
	if err != nil {
		t.Fatal(err)
	}
	const prefix = "go-keyring-base64:"
	if !strings.HasPrefix(string(payload), prefix) {
		t.Fatalf("payload is missing keyring prefix")
	}
	decoded, err := base64.StdEncoding.DecodeString(strings.TrimPrefix(string(payload), prefix))
	if err != nil || string(decoded) != string(raw) {
		t.Fatalf("decoded payload mismatch")
	}
	again, err := canonicalKeychainPayload(payload)
	if err != nil || string(again) != string(payload) {
		t.Fatalf("prefixed payload changed: err=%v", err)
	}
	if _, err := canonicalKeychainPayload([]byte("not-a-credential")); err == nil {
		t.Fatal("expected invalid credential rejection")
	}
}
