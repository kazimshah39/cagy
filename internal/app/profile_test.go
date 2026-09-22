package app

import (
	"context"
	"io"
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

func TestSupervisorModelRequiresAgy(t *testing.T) {
	app := New(fakeRunner{}, nil, nil)

	// Default supervisor (Codex)
	err := app.Run(context.Background(), []string{"--supervisor-model", "gemini-2.5-pro"})
	if err == nil || !strings.Contains(err.Error(), "--supervisor-model is only supported with agy supervisor") {
		t.Fatalf("expected supervisor-model error with default supervisor, got: %v", err)
	}

	// Explicit Codex
	err = app.Run(context.Background(), []string{"--supervisor=codex", "--supervisor-model=gemini-2.5-pro"})
	if err == nil || !strings.Contains(err.Error(), "--supervisor-model is only supported with agy supervisor") {
		t.Fatalf("expected supervisor-model error with codex, got: %v", err)
	}

	// OpenCode
	err = app.Run(context.Background(), []string{"--supervisor", "opencode", "--supervisor-model", "gemini-2.5-pro"})
	if err == nil || !strings.Contains(err.Error(), "--supervisor-model is only supported with agy supervisor") {
		t.Fatalf("expected supervisor-model error with opencode, got: %v", err)
	}
}

func TestDoctorAcceptsAgySupervisorOption(t *testing.T) {
	app := New(fakeRunner{}, io.Discard, io.Discard)
	// We only verify the supervisor selection parsing here; doctor execution itself will fail on fakeRunner lookPath
	err := app.Run(context.Background(), []string{"doctor", "--supervisor=agy"})
	if err != nil && strings.Contains(err.Error(), "usage: herdr-tandem doctor") {
		t.Fatalf("unexpected usage error for valid doctor supervisor flag: %v", err)
	}
	if app.supervisor.ID() != "agy" {
		t.Fatalf("expected supervisor ID agy, got: %s", app.supervisor.ID())
	}

	err = app.Run(context.Background(), []string{"doctor", "--supervisor", "agy"})
	if err != nil && strings.Contains(err.Error(), "usage: herdr-tandem doctor") {
		t.Fatalf("unexpected usage error for valid doctor supervisor flag: %v", err)
	}
	if app.supervisor.ID() != "agy" {
		t.Fatalf("expected supervisor ID agy, got: %s", app.supervisor.ID())
	}

	err = app.Run(context.Background(), []string{"doctor", "--supervisor=unknown"})
	if err == nil || !strings.Contains(err.Error(), "unsupported supervisor") {
		t.Fatalf("expected unsupported supervisor error, got: %v", err)
	}

	err = app.Run(context.Background(), []string{"doctor", "--supervisor-model=gemini-2.5-pro"})
	if err == nil || !strings.Contains(err.Error(), "usage: herdr-tandem doctor") {
		t.Fatalf("expected doctor usage error for supervisor-model flag, got: %v", err)
	}
}
