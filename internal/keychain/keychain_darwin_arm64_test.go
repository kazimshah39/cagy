//go:build darwin && arm64 && cgo

package keychain

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestDarwinKeychainIntegration(t *testing.T) {
	if os.Getenv("CAGY_KEYCHAIN_INTEGRATION") != "1" {
		t.Skip("set CAGY_KEYCHAIN_INTEGRATION=1 to run disposable Keychain integration")
	}
	service := fmt.Sprintf("com.kazimshah39.cagy.test.%d.%d", os.Getpid(), time.Now().UnixNano())
	if service == CanonicalService || strings.Contains(service, "gemini") {
		t.Fatal("test service must never match the canonical agy service")
	}
	first := Item{Service: service, Account: "first"}
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
	access := Access{Label: "cagy disposable integration test", TrustedPaths: []string{executable}}
	t.Cleanup(func() {
		_ = store.Delete(ctx, first)
	})
	if err := store.Save(ctx, first, []byte("one"), access); err != nil {
		t.Fatal(err)
	}
	if exists, err := store.Exists(ctx, first); err != nil || !exists {
		t.Fatalf("exists=%v err=%v", exists, err)
	}
	got, err := store.Read(ctx, first)
	if err != nil || string(got) != "one" {
		t.Fatalf("read=%q err=%v", got, err)
	}
	Zero(got)
	if err := store.Save(ctx, first, []byte("updated"), access); err != nil {
		t.Fatal(err)
	}
	if err := store.Save(ctx, first, []byte("replaced"), access); err != nil {
		t.Fatal(err)
	}
	got, err = store.Read(ctx, first)
	if err != nil || string(got) != "replaced" {
		t.Fatalf("replaced=%q err=%v", got, err)
	}
	Zero(got)
	if err := store.Delete(ctx, first); err != nil {
		t.Fatal(err)
	}
	if _, err := store.Read(ctx, first); !errors.Is(err, ErrNotFound) {
		t.Fatalf("deleted item still readable: %v", err)
	}
	if exists, err := store.Exists(ctx, first); err != nil || exists {
		t.Fatalf("exists after delete=%v err=%v", exists, err)
	}
}

func TestDarwinReplaceCanonicalUsesPromptFreeAGMCompatibleFlow(t *testing.T) {
	var calls [][]string
	deleteCalls := 0
	store := DarwinStore{runSecurity: func(_ context.Context, args ...string) error {
		calls = append(calls, append([]string(nil), args...))
		if len(args) > 0 && args[0] == "delete-generic-password" {
			deleteCalls++
			if deleteCalls > 1 {
				return errors.New("item not found")
			}
		}
		return nil
	}}
	credential := []byte(`{"token":{"access_token":"access","refresh_token":"refresh"},"auth_method":"consumer"}`)
	if err := store.ReplaceCanonical(context.Background(), credential, Access{}); err != nil {
		t.Fatal(err)
	}
	if len(calls) != 3 || calls[0][0] != "delete-generic-password" || calls[1][0] != "delete-generic-password" || calls[2][0] != "add-generic-password" {
		t.Fatalf("unexpected command sequence")
	}
	add := calls[2]
	contains := func(want string) bool {
		for _, value := range add {
			if value == want {
				return true
			}
		}
		return false
	}
	if !contains("-A") || !contains("/usr/bin/security") || !contains("/usr/bin/codesign") {
		t.Fatalf("allow-all AGM compatibility flags are missing")
	}
	passwordIndex := -1
	for index, value := range add {
		if value == "-w" {
			passwordIndex = index + 1
			break
		}
	}
	if passwordIndex <= 0 || passwordIndex >= len(add) || !strings.HasPrefix(add[passwordIndex], "go-keyring-base64:") {
		t.Fatal("canonical credential was not encoded for agy")
	}
}
