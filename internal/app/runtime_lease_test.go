package app

import (
	"context"
	"path/filepath"
	"testing"

	"github.com/kazimshah39/cagy/internal/securestate"
)

func runtimeTestRecord(t *testing.T, id, supervisor string) runtimeRecord {
	t.Helper()
	return runtimeRecord{RuntimeID: id, WorkspaceID: "w1", SupervisorPaneID: supervisor, Developer: developerName("w1", supervisor), Project: filepath.Clean(t.TempDir())}
}

func TestRuntimeRecordsAllowIndependentSupervisors(t *testing.T) {
	manager := runtimeRecordManager{stateDir: t.TempDir(), token: func() (string, error) { return "generated", nil }, liveness: func(context.Context, runtimeRecord) (runtimeLiveness, error) {
		return runtimeLiveness{PaneLive: true}, nil
	}}
	first, err := manager.Prepare(context.Background(), runtimeTestRecord(t, "one", "w1:p1"))
	if err != nil {
		t.Fatal(err)
	}
	second, err := manager.Prepare(context.Background(), runtimeTestRecord(t, "two", "w1:p2"))
	if err != nil {
		t.Fatal(err)
	}
	if first.RuntimeID == second.RuntimeID {
		t.Fatal("distinct supervisors shared a runtime ID")
	}
	info := runtimeContext{workspaceID: "w1", supervisor: "w1:p2", developer: developerName("w1", "w1:p2"), project: second.Project}
	found, exists, err := manager.FindForScope(info)
	if err != nil || !exists || found.RuntimeID != "two" {
		t.Fatalf("found=%+v exists=%t err=%v", found, exists, err)
	}
}

func TestRuntimeRecordsRejectDuplicateSupervisor(t *testing.T) {
	manager := runtimeRecordManager{stateDir: t.TempDir(), token: func() (string, error) { return "generated", nil }, liveness: func(context.Context, runtimeRecord) (runtimeLiveness, error) {
		return runtimeLiveness{PaneLive: true}, nil
	}}
	record := runtimeTestRecord(t, "one", "w1:p1")
	if _, err := manager.Prepare(context.Background(), record); err != nil {
		t.Fatal(err)
	}
	duplicate := runtimeTestRecord(t, "two", "w1:p1")
	duplicate.Project = record.Project
	if _, err := manager.Prepare(context.Background(), duplicate); err == nil {
		t.Fatal("duplicate supervisor was allowed")
	}
}

func TestRuntimeRecordsReclaimStaleDuplicateSupervisor(t *testing.T) {
	manager := runtimeRecordManager{stateDir: t.TempDir(), token: func() (string, error) { return "replacement", nil }, liveness: func(context.Context, runtimeRecord) (runtimeLiveness, error) {
		return runtimeLiveness{}, nil
	}}
	firstRecord := runtimeTestRecord(t, "old", "w1:p1")
	first, err := manager.Prepare(context.Background(), firstRecord)
	if err != nil {
		t.Fatal(err)
	}
	replacement := runtimeTestRecord(t, "replacement", "w1:p1")
	replacement.Project = first.Project
	prepared, err := manager.Prepare(context.Background(), replacement)
	if err != nil {
		t.Fatal(err)
	}
	if prepared.RuntimeID != "replacement" {
		t.Fatalf("runtime=%q", prepared.RuntimeID)
	}
	if _, exists, err := manager.loadByIDUnlocked(first.RuntimeID); err != nil || exists {
		t.Fatalf("stale record exists=%t err=%v", exists, err)
	}
}

func TestRuntimeRecordsRejectRuntimeIDCollision(t *testing.T) {
	manager := runtimeRecordManager{stateDir: t.TempDir(), liveness: func(context.Context, runtimeRecord) (runtimeLiveness, error) {
		return runtimeLiveness{}, nil
	}}
	if _, err := manager.Prepare(context.Background(), runtimeTestRecord(t, "same", "w1:p1")); err != nil {
		t.Fatal(err)
	}
	if _, err := manager.Prepare(context.Background(), runtimeTestRecord(t, "same", "w1:p2")); err == nil {
		t.Fatal("runtime ID collision was accepted")
	}
}

func TestRuntimeRecordsRejectMalformedRecordRatherThanIgnoringIt(t *testing.T) {
	stateDir := t.TempDir()
	manager := runtimeRecordManager{stateDir: stateDir, token: func() (string, error) { return "next", nil }, liveness: func(context.Context, runtimeRecord) (runtimeLiveness, error) {
		return runtimeLiveness{}, nil
	}}
	if err := securestate.EnsureDir(manager.recordsDir()); err != nil {
		t.Fatal(err)
	}
	if err := securestate.WriteFile(manager.recordsDir(), "corrupt.json", []byte(`{"version":2}`)); err != nil {
		t.Fatal(err)
	}
	if _, err := manager.Prepare(context.Background(), runtimeTestRecord(t, "next", "w1:p2")); err == nil {
		t.Fatal("malformed runtime record was ignored")
	}
}
