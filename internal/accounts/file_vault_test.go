package accounts

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"testing"
)

func TestFileCredentialVaultRoundTripAndPermissions(t *testing.T) {
	dir := t.TempDir()
	vault := FileCredentialVault{StateDir: dir}
	id := "0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef"
	secret := []byte("credential-bytes")
	ctx := context.Background()
	if exists, err := vault.Exists(ctx, id); err != nil || exists {
		t.Fatalf("initial exists=%v err=%v", exists, err)
	}
	if _, err := vault.Load(ctx, id); !errors.Is(err, ErrCredentialUnavailable) {
		t.Fatalf("missing load error=%v", err)
	}
	if err := vault.Save(ctx, id, secret); err != nil {
		t.Fatal(err)
	}
	got, err := vault.Load(ctx, id)
	if err != nil || string(got) != string(secret) {
		t.Fatalf("load=%q err=%v", got, err)
	}
	path := filepath.Join(dir, fileCredentialPrefix+id+fileCredentialSuffix)
	info, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	if info.Mode().Perm() != 0o600 {
		t.Fatalf("credential mode=%o", info.Mode().Perm())
	}
	if err := vault.Delete(ctx, id); err != nil {
		t.Fatal(err)
	}
	if exists, err := vault.Exists(ctx, id); err != nil || exists {
		t.Fatalf("after delete exists=%v err=%v", exists, err)
	}
}
