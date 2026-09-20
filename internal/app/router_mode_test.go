package app

import (
	"context"
	"strings"
	"testing"
)

func TestAccountsCommandExplainsRouterMigration(t *testing.T) {
	app := New(fakeRunner{}, nil, nil)
	err := app.Run(context.Background(), []string{"accounts"})
	if err == nil || !strings.Contains(err.Error(), "was removed") || !strings.Contains(err.Error(), "9Router") {
		t.Fatalf("error=%v", err)
	}
}
