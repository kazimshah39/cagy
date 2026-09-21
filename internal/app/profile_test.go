package app

import (
	"context"
	"strings"
	"testing"
)

func TestUnsupportedSupervisorIsRejectedBeforeStartup(t *testing.T) {
	app := New(fakeRunner{}, nil, nil)
	err := app.Run(context.Background(), []string{"--supervisor=unknown"})
	if err == nil || !strings.Contains(err.Error(), "unsupported supervisor") {
		t.Fatalf("error=%v", err)
	}
}
