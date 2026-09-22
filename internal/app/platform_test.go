package app

import (
	"context"
	"errors"
	"strings"
	"testing"
)

func TestStartRejectsUnsupportedPlatformBeforeMutation(t *testing.T) {
	runner := &scriptedRunner{t: t}
	application := New(runner, &strings.Builder{}, &strings.Builder{})
	application.checkPlatform = func() error { return errors.New("herdr-tandem supports only Apple Silicon macOS (darwin/arm64)") }
	err := application.start(context.Background(), ".", sidebarModeCompact)
	if err == nil || err.Error() != "herdr-tandem supports only Apple Silicon macOS (darwin/arm64)" {
		t.Fatalf("error=%v", err)
	}
	runner.assertDone()
}
