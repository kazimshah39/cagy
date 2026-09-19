package app

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestRedactDiagnosticSecrets(t *testing.T) {
	input := `Authorization: Bearer bearer-secret access_token=access-secret refresh_token:"refresh-secret" id_token='id-secret' client_secret:client-secret password=password-secret code=4/0ATsMZq0123456789abcdef`
	got := redactDiagnostic(input)
	for _, secret := range []string{"bearer-secret", "access-secret", "refresh-secret", "id-secret", "client-secret", "password-secret", "4/0ATsMZq0123456789abcdef"} {
		if strings.Contains(got, secret) {
			t.Fatalf("redacted output still contains %q: %s", secret, got)
		}
	}
	if !strings.Contains(got, "[REDACTED]") || !strings.Contains(got, "[REDACTED_OAUTH_CODE]") {
		t.Fatalf("redaction markers missing: %s", got)
	}
}

func TestDebugTaskFingerprintDoesNotContainTask(t *testing.T) {
	task := "secret task text that must not enter diagnostics"
	fingerprint := debugTaskFingerprint(task)
	if strings.Contains(fingerprint, task) || strings.Contains(task, fingerprint) {
		t.Fatalf("fingerprint leaked task text: %q", fingerprint)
	}
	if len(fingerprint) != 12 {
		t.Fatalf("fingerprint length=%d, want 12", len(fingerprint))
	}
}

func TestDiagnosticsCanBeDisabled(t *testing.T) {
	getenv := func(key string) string {
		if key == "CAGY_DIAGNOSTICS" {
			return "0"
		}
		return ""
	}
	if diagnosticsEnabled(getenv, false) {
		t.Fatal("diagnostics should be disabled by CAGY_DIAGNOSTICS=0")
	}
	if diagnosticsEnabled(func(string) string { return "" }, true) {
		t.Fatal("diagnostics should be disabled in test processes")
	}
	if !diagnosticsEnabled(func(string) string { return "" }, false) {
		t.Fatal("diagnostics should be enabled by default")
	}
}

func TestDebugFormattingDoesNotEmitFmtErrorsForNilArguments(t *testing.T) {
	args := []any{nil}
	for index, arg := range args {
		if arg == nil {
			args[index] = ""
		}
	}
	got := fmt.Sprintf("error=%q", args...)
	if got != `error=""` || strings.Contains(got, "%!") {
		t.Fatalf("formatted diagnostic=%q", got)
	}
}

func TestWriteDiagnosticLogUsesPrivateModesAndEscapesLines(t *testing.T) {
	stateDir := t.TempDir()
	when := time.Date(2026, time.September, 19, 12, 0, 0, 123, time.UTC)
	if err := writeDiagnosticLog(stateDir, when, 42, "commands.go:12", "first\nsecond", 1024, 5); err != nil {
		t.Fatal(err)
	}
	logDir := filepath.Join(stateDir, "logs")
	logPath := filepath.Join(logDir, diagnosticLogFileName)
	assertPerm(t, logDir, 0o700)
	assertPerm(t, logPath, 0o600)
	assertPerm(t, logPath+".lock", 0o600)
	data, err := os.ReadFile(logPath)
	if err != nil {
		t.Fatal(err)
	}
	text := string(data)
	if strings.Count(text, "\n") != 1 || !strings.Contains(text, `first\nsecond`) {
		t.Fatalf("diagnostic entry was not kept on one line: %q", text)
	}
	if !strings.Contains(text, "pid=42 caller=commands.go:12") {
		t.Fatalf("diagnostic metadata missing: %q", text)
	}
}

func TestDiagnosticRotationRetainsAtMostFiveBackups(t *testing.T) {
	stateDir := t.TempDir()
	for index := 0; index < 12; index++ {
		message := fmt.Sprintf("entry-%02d-xxxxxxxxxxxxxxxx", index)
		if err := writeDiagnosticLog(stateDir, time.Unix(int64(index), 0), 7, "test.go:1", message, 1, 5); err != nil {
			t.Fatal(err)
		}
	}
	logPath := filepath.Join(stateDir, "logs", diagnosticLogFileName)
	for index := 1; index <= 5; index++ {
		if _, err := os.Stat(fmt.Sprintf("%s.%d", logPath, index)); err != nil {
			t.Fatalf("expected backup %d: %v", index, err)
		}
	}
	if _, err := os.Stat(logPath + ".6"); !os.IsNotExist(err) {
		t.Fatalf("unexpected sixth backup: %v", err)
	}
}

func assertPerm(t *testing.T, path string, want os.FileMode) {
	t.Helper()
	info, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	if got := info.Mode().Perm(); got != want {
		t.Fatalf("%s mode=%#o, want %#o", path, got, want)
	}
}
