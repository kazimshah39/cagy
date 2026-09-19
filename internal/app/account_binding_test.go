package app

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/kazimshah39/cagy/internal/herdr"
)

func TestAccountBindingSaveLoadAndPaneToken(t *testing.T) {
	dir := t.TempDir()
	if err := os.Chmod(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	a := &App{stateDir: dir}
	developer := "dev-w1-p1"
	accountID := strings.Repeat("a", 64)
	if err := a.saveAccountBinding(developer, accountID); err != nil {
		t.Fatal(err)
	}
	got, exists, err := a.loadAccountBinding(developer)
	if err != nil || !exists {
		t.Fatalf("load binding: exists=%v err=%v", exists, err)
	}
	if got.AccountID != accountID || got.Developer != developer || got.Version != accountBindingVersion {
		t.Fatalf("unexpected binding: %#v", got)
	}
	paneID, err := a.boundAccountID(developer, herdr.PaneInfo{Tokens: map[string]string{"cagy_account_id": accountID}})
	if err != nil || paneID != accountID {
		t.Fatalf("pane binding: %q %v", paneID, err)
	}
	fallback, err := a.boundAccountID(developer, herdr.PaneInfo{})
	if err != nil || fallback != accountID {
		t.Fatalf("file binding: %q %v", fallback, err)
	}
	if mode := mustMode(t, filepath.Join(dir, filepath.Base(a.accountBindingPath(developer)))); mode != 0o600 {
		t.Fatalf("binding mode %04o", mode)
	}
}

func TestBoundAccountIDRejectsMalformedPaneToken(t *testing.T) {
	a := &App{stateDir: t.TempDir()}
	_, err := a.boundAccountID("dev", herdr.PaneInfo{Tokens: map[string]string{"cagy_account_id": "not-an-id"}})
	if err == nil {
		t.Fatal("expected malformed binding error")
	}
}

func TestSaveAccountBindingRejectsNonOpaqueIDs(t *testing.T) {
	a := &App{stateDir: t.TempDir()}
	for _, id := range []string{"", "short", strings.Repeat("g", 64), strings.Repeat("a", 63) + "!"} {
		if err := a.saveAccountBinding("dev", id); err == nil {
			t.Fatalf("expected rejection for %q", id)
		}
	}
}

func TestBindDeveloperAccountReportsBeforePersisting(t *testing.T) {
	// This test only verifies invalid IDs fail before any Herdr call or state write.
	a := &App{stateDir: t.TempDir()}
	if err := a.bindDeveloperAccount(context.Background(), "dev", "pane", "bad"); err == nil {
		t.Fatal("expected invalid account ID")
	}
}

func mustMode(t *testing.T, path string) os.FileMode {
	t.Helper()
	info, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	return info.Mode().Perm()
}
