package app

import (
	"context"
	"encoding/json"
	"errors"
	"path/filepath"
	"testing"
	"time"

	"github.com/kazimshah39/herdr-tandem/internal/securestate"
)

func runtimeTestRecord(t *testing.T, id, supervisor string) runtimeRecord {
	t.Helper()
	return runtimeRecord{SidebarMode: string(sidebarModeCompact), RuntimeID: id, SupervisorKind: "codex", DeveloperKind: "agy", WorkspaceID: "w1", SupervisorPaneID: supervisor, Developer: developerName("w1", supervisor), Project: filepath.Clean(t.TempDir())}
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
	info := runtimeContext{supervisorKind: "codex", developerKind: "agy", workspaceID: "w1", supervisor: "w1:p2", developer: developerName("w1", "w1:p2"), project: second.Project}
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

func TestRuntimeScopeIncludesWorkflowProfile(t *testing.T) {
	manager := runtimeRecordManager{stateDir: t.TempDir(), liveness: func(context.Context, runtimeRecord) (runtimeLiveness, error) { return runtimeLiveness{}, nil }}
	record := runtimeTestRecord(t, "one", "w1:p1")
	prepared, err := manager.Prepare(context.Background(), record)
	if err != nil {
		t.Fatal(err)
	}
	info := runtimeContext{supervisorKind: "opencode", developerKind: "agy", workspaceID: "w1", supervisor: "w1:p1", developer: prepared.Developer, project: prepared.Project}
	if _, exists, err := manager.FindForScope(info); err != nil || exists {
		t.Fatalf("exists=%t err=%v", exists, err)
	}
}

func TestRuntimeRecordRejectsUnknownProfile(t *testing.T) {
	record := runtimeTestRecord(t, "one", "w1:p1")
	record.SupervisorKind = "unknown"
	if err := validateRuntimeRecord(record); err == nil {
		t.Fatal("unknown supervisor profile accepted")
	}
}

func TestRuntimeRecordVersion4RequiresKnownSidebarMode(t *testing.T) {
	record := runtimeTestRecord(t, "one", "w1:p1")
	record.Version = runtimeRecordVersion
	record.CreatedAt = testRuntimeTime()
	record.UpdatedAt = record.CreatedAt
	for _, mode := range []string{"", "legacy", "COMPACT"} {
		record.SidebarMode = mode
		if err := validateRuntimeRecord(record); err == nil {
			t.Fatalf("sidebar mode %q was accepted", mode)
		}
	}
	for _, mode := range []string{string(sidebarModeCompact), string(sidebarModeExpanded)} {
		record.SidebarMode = mode
		if err := validateRuntimeRecord(record); err != nil {
			t.Fatalf("sidebar mode %q: %v", mode, err)
		}
	}
}

func TestRuntimeRecordRejectsOldVersionWithoutMigration(t *testing.T) {
	record := runtimeTestRecord(t, "one", "w1:p1")
	record.Version = 3
	record.CreatedAt = testRuntimeTime()
	record.UpdatedAt = record.CreatedAt
	if err := validateRuntimeRecord(record); err == nil {
		t.Fatal("version 3 runtime record was accepted")
	}
}

func TestRuntimeSidebarModeRejectsEnvironmentRecordMismatch(t *testing.T) {
	app := New(fakeRunner{}, nil, nil)
	app.stateDir = t.TempDir()
	record := runtimeTestRecord(t, "one", "w1:p1")
	prepared, err := app.runtimeManager().Prepare(context.Background(), record)
	if err != nil {
		t.Fatal(err)
	}
	info := runtimeContext{supervisorKind: prepared.SupervisorKind, developerKind: prepared.DeveloperKind, workspaceID: prepared.WorkspaceID, supervisor: prepared.SupervisorPaneID, developer: prepared.Developer, project: prepared.Project}
	app.getenv = func(key string) string {
		if key == sidebarModeEnv {
			return string(sidebarModeExpanded)
		}
		return ""
	}
	if _, err := app.runtimeSidebarMode(info); err == nil {
		t.Fatal("environment/record sidebar mismatch was accepted")
	}
	app.getenv = func(key string) string {
		if key == sidebarModeEnv {
			return string(sidebarModeCompact)
		}
		return ""
	}
	if mode, err := app.runtimeSidebarMode(info); err != nil || mode != sidebarModeCompact {
		t.Fatalf("mode=%q err=%v", mode, err)
	}
}

func testRuntimeTime() time.Time {
	return time.Date(2026, 9, 21, 0, 0, 0, 0, time.UTC)
}

func TestRuntimeCleanupClearsViewOnlyAfterLastRecord(t *testing.T) {
	manager := runtimeRecordManager{stateDir: t.TempDir()}
	first, err := manager.Prepare(context.Background(), runtimeTestRecord(t, "one", "w1:p1"))
	if err != nil {
		t.Fatal(err)
	}
	second, err := manager.Prepare(context.Background(), runtimeTestRecord(t, "two", "w1:p2"))
	if err != nil {
		t.Fatal(err)
	}
	clearCalls := 0
	clear := func(context.Context) error { clearCalls++; return nil }
	attempted, err := manager.RemoveAndClearViewIfLast(context.Background(), first.RuntimeID, clear)
	if err != nil || attempted || clearCalls != 0 {
		t.Fatalf("first removal attempted=%t calls=%d err=%v", attempted, clearCalls, err)
	}
	attempted, err = manager.RemoveAndClearViewIfLast(context.Background(), second.RuntimeID, clear)
	if err != nil || !attempted || clearCalls != 1 {
		t.Fatalf("last removal attempted=%t calls=%d err=%v", attempted, clearCalls, err)
	}
}

func TestRuntimeCleanupReportsClearFailureAfterRecordRemoval(t *testing.T) {
	manager := runtimeRecordManager{stateDir: t.TempDir()}
	record, err := manager.Prepare(context.Background(), runtimeTestRecord(t, "one", "w1:p1"))
	if err != nil {
		t.Fatal(err)
	}
	attempted, err := manager.RemoveAndClearViewIfLast(context.Background(), record.RuntimeID, func(context.Context) error {
		return errors.New("guarded clear failed")
	})
	if !attempted || err == nil {
		t.Fatalf("attempted=%t err=%v", attempted, err)
	}
	if _, exists, loadErr := manager.loadByIDUnlocked(record.RuntimeID); loadErr != nil || exists {
		t.Fatalf("record still exists=%t err=%v", exists, loadErr)
	}
}

func TestRuntimeConcurrentLastStopSerializesWithNewStart(t *testing.T) {
	manager := runtimeRecordManager{stateDir: t.TempDir()}
	oldRecord, err := manager.Prepare(context.Background(), runtimeTestRecord(t, "old", "w1:p1"))
	if err != nil {
		t.Fatal(err)
	}
	clearEntered := make(chan struct{})
	releaseClear := make(chan struct{})
	removeDone := make(chan error, 1)
	go func() {
		_, removeErr := manager.RemoveAndClearViewIfLast(context.Background(), oldRecord.RuntimeID, func(context.Context) error {
			close(clearEntered)
			<-releaseClear
			return nil
		})
		removeDone <- removeErr
	}()
	<-clearEntered
	prepareDone := make(chan error, 1)
	go func() {
		_, prepareErr := manager.Prepare(context.Background(), runtimeTestRecord(t, "new", "w1:p2"))
		prepareDone <- prepareErr
	}()
	var concurrentErr error
	select {
	case concurrentErr = <-prepareDone:
		if concurrentErr == nil {
			t.Fatal("new start escaped runtime lock while clear was active")
		}
	case <-time.After(250 * time.Millisecond):
		t.Fatal("concurrent start did not return a bounded lock result")
	}
	close(releaseClear)
	if err := <-removeDone; err != nil {
		t.Fatal(err)
	}
	if _, err := manager.Prepare(context.Background(), runtimeTestRecord(t, "new", "w1:p2")); err != nil {
		t.Fatalf("new start did not succeed after cleanup lock released (first error %v): %v", concurrentErr, err)
	}
	if _, exists, err := manager.loadByIDUnlocked("new"); err != nil || !exists {
		t.Fatalf("new runtime exists=%t err=%v", exists, err)
	}
}

func TestRuntimeRecordSupportsAgySupervisorAndModelFields(t *testing.T) {
	record := runtimeTestRecord(t, "agy-sup-1", "w1:p1")
	record.SupervisorKind = "agy"
	record.SupervisorModel = "gemini-2.5-pro"
	record.DeveloperModel = "gemini-2.5-flash"
	record.SupervisorAgentName = "herdr-tandem-agy-sup-1"
	record.Version = runtimeRecordVersion
	record.CreatedAt = testRuntimeTime()
	record.UpdatedAt = record.CreatedAt

	if err := validateRuntimeRecord(record); err != nil {
		t.Fatalf("valid agy supervisor record rejected: %v", err)
	}

	data, err := json.Marshal(record)
	if err != nil {
		t.Fatal(err)
	}
	decoded, err := decodeRuntimeRecord(data)
	if err != nil {
		t.Fatalf("decodeRuntimeRecord failed: %v", err)
	}
	if decoded.SupervisorKind != "agy" || decoded.SupervisorModel != "gemini-2.5-pro" || decoded.DeveloperModel != "gemini-2.5-flash" || decoded.SupervisorAgentName != "herdr-tandem-agy-sup-1" {
		t.Fatalf("decoded mismatch: %+v", decoded)
	}
}

func TestRuntimeRecordDecodeLegacyRecord(t *testing.T) {
	// A version 4 record without model or agent name fields must decode cleanly
	legacyJSON := `{"version":4,"supervisor_kind":"codex","developer_kind":"agy","runtime_id":"legacy-1","sidebar_mode":"compact","workspace_id":"w1","supervisor_pane_id":"p1","developer":"dev-1","project":"/tmp/test","created_at":"2026-09-21T00:00:00Z","updated_at":"2026-09-21T00:00:00Z"}`
	decoded, err := decodeRuntimeRecord([]byte(legacyJSON))
	if err != nil {
		t.Fatalf("legacy record failed to decode: %v", err)
	}
	if decoded.SupervisorModel != "" || decoded.DeveloperModel != "" || decoded.SupervisorAgentName != "" {
		t.Fatalf("unexpected fields populated on legacy record: %+v", decoded)
	}
}

func TestRuntimeRecordValidationFailsOnInvalidModelAndAgentName(t *testing.T) {
	record := runtimeTestRecord(t, "bad-fields-1", "w1:p1")
	record.SupervisorKind = "agy"
	record.Version = runtimeRecordVersion
	record.CreatedAt = testRuntimeTime()
	record.UpdatedAt = record.CreatedAt

	// Leading dash in model
	record.SupervisorModel = "-bad-model"
	if err := validateRuntimeRecord(record); err == nil {
		t.Fatal("leading dash in model accepted")
	}
	record.SupervisorModel = "valid-model"

	// Whitespace in model
	record.DeveloperModel = "model with spaces"
	if err := validateRuntimeRecord(record); err == nil {
		t.Fatal("model with spaces accepted")
	}
	record.DeveloperModel = "valid-dev-model"

	// Non-matching agent name
	record.SupervisorAgentName = "herdr-tandem-different-id"
	if err := validateRuntimeRecord(record); err == nil {
		t.Fatal("non-matching agent name accepted")
	}
	record.SupervisorAgentName = "herdr-tandem-bad-fields-1"

	// SupervisorModel on codex
	codexRecord := record
	codexRecord.SupervisorKind = "codex"
	if err := validateRuntimeRecord(codexRecord); err == nil {
		t.Fatal("supervisor model on codex accepted")
	}
}

func TestRuntimeManagerFindForSupervisorPane(t *testing.T) {
	manager := runtimeRecordManager{stateDir: t.TempDir()}
	r1 := runtimeTestRecord(t, "rt-1", "w1:p1")
	r1.SupervisorKind = "agy"
	if _, err := manager.Prepare(context.Background(), r1); err != nil {
		t.Fatal(err)
	}
	r2 := runtimeTestRecord(t, "rt-2", "w1:p2")
	r2.SupervisorKind = "codex"
	if _, err := manager.Prepare(context.Background(), r2); err != nil {
		t.Fatal(err)
	}

	found, exists, err := manager.FindForSupervisorPane("w1", "w1:p1", r1.Project)
	if err != nil || !exists || found.RuntimeID != "rt-1" {
		t.Fatalf("FindForSupervisorPane failed: found=%+v exists=%t err=%v", found, exists, err)
	}

	found2, exists2, err := manager.FindForSupervisorPane("w1", "w1:p2", r2.Project)
	if err != nil || !exists2 || found2.RuntimeID != "rt-2" {
		t.Fatalf("FindForSupervisorPane failed: found=%+v exists=%t err=%v", found2, exists2, err)
	}

	_, exists3, err := manager.FindForSupervisorPane("w1", "w1:nonexistent", r1.Project)
	if err != nil || exists3 {
		t.Fatalf("expected not found: exists=%t err=%v", exists3, err)
	}
}
