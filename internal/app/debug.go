package app

import (
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"runtime"
	"strings"
	"sync"
	"syscall"
	"time"
)

const (
	diagnosticLogFileName = "herdr-tandem.log"
	diagnosticLogMaxBytes = 8 << 20
	diagnosticLogBackups  = 5
)

var (
	diagnosticLogMu  sync.Mutex
	bearerPattern    = regexp.MustCompile(`(?i)(authorization[^\n]*bearer\s+)[^\s"']+`)
	tokenPattern     = regexp.MustCompile(`(?i)(access_token|refresh_token|id_token|client_secret|password)(["'\s:=]+)[^\s,"'}]+`)
	oauthCodePattern = regexp.MustCompile(`\b4/[0-9A-Za-z._-]{12,}`)
)

// debugf writes always-on, bounded diagnostics for real herdr-tandem executions. Go
// test binaries are excluded so automated tests never pollute the user's state.
// Callers must still pass metadata only—never credential or task contents.
func (a *App) debugf(format string, args ...any) {
	if a == nil {
		return
	}
	for index, arg := range args {
		if arg == nil {
			args[index] = ""
		}
	}
	message := fmt.Sprintf(format, args...)
	if a.supervisorModel != "" {
		message = strings.ReplaceAll(message, a.supervisorModel, "[REDACTED_MODEL]")
	}
	if a.developerModel != "" {
		message = strings.ReplaceAll(message, a.developerModel, "[REDACTED_MODEL]")
	}
	message = strings.ReplaceAll(message, DefaultAgySupervisorModel, "[REDACTED_MODEL]")
	message = strings.ReplaceAll(message, DefaultAgyDeveloperModel, "[REDACTED_MODEL]")
	if a.diagnosticSink != nil {
		a.diagnosticSink(redactDiagnostic(message))
	}
	if a.stateDir == "" || !diagnosticsEnabled(a.getenv, isTestProcess()) {
		return
	}
	_ = writeDiagnosticLog(a.stateDir, time.Now().UTC(), os.Getpid(), diagnosticCaller(1), message, diagnosticLogMaxBytes, diagnosticLogBackups)
}

func diagnosticsEnabled(getenv func(string) string, testProcess bool) bool {
	if testProcess {
		return false
	}
	return getenv == nil || getenv("HERDR_TANDEM_DIAGNOSTICS") != "0"
}

func diagnosticCaller(skip int) string {
	_, file, line, ok := runtime.Caller(skip + 1)
	if !ok {
		return "unknown"
	}
	return fmt.Sprintf("%s:%d", filepath.Base(file), line)
}

func writeDiagnosticLog(stateDir string, now time.Time, pid int, caller, message string, maxBytes int64, backups int) error {
	message = redactDiagnostic(message)
	message = strings.ReplaceAll(message, "\r", "\\r")
	message = strings.ReplaceAll(message, "\n", "\\n")
	caller = strings.ReplaceAll(strings.TrimSpace(caller), " ", "_")
	if caller == "" {
		caller = "unknown"
	}
	line := fmt.Sprintf("%s pid=%d caller=%s %s\n", now.UTC().Format(time.RFC3339Nano), pid, caller, message)

	diagnosticLogMu.Lock()
	defer diagnosticLogMu.Unlock()
	logDir := filepath.Join(stateDir, "logs")
	if err := os.MkdirAll(logDir, 0o700); err != nil {
		return err
	}
	if err := os.Chmod(logDir, 0o700); err != nil {
		return err
	}
	path := filepath.Join(logDir, diagnosticLogFileName)
	lock, err := os.OpenFile(path+".lock", os.O_WRONLY|os.O_CREATE, 0o600)
	if err != nil {
		return err
	}
	if err := lock.Chmod(0o600); err != nil {
		_ = lock.Close()
		return err
	}
	if err := syscall.Flock(int(lock.Fd()), syscall.LOCK_EX); err != nil {
		_ = lock.Close()
		return err
	}
	defer func() {
		_ = syscall.Flock(int(lock.Fd()), syscall.LOCK_UN)
		_ = lock.Close()
	}()
	if err := rotateDiagnosticLog(path, maxBytes, backups); err != nil {
		return err
	}
	file, err := os.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_APPEND, 0o600)
	if err != nil {
		return err
	}
	if err := file.Chmod(0o600); err != nil {
		_ = file.Close()
		return err
	}
	if _, err := file.WriteString(line); err != nil {
		_ = file.Close()
		return err
	}
	return file.Close()
}

func rotateDiagnosticLog(path string, maxBytes int64, backups int) error {
	if maxBytes <= 0 || backups <= 0 {
		return nil
	}
	info, err := os.Stat(path)
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	if err != nil {
		return err
	}
	if info.Size() < maxBytes {
		return nil
	}
	if err := os.Remove(fmt.Sprintf("%s.%d", path, backups)); err != nil && !errors.Is(err, os.ErrNotExist) {
		return err
	}
	for index := backups - 1; index >= 1; index-- {
		source := fmt.Sprintf("%s.%d", path, index)
		target := fmt.Sprintf("%s.%d", path, index+1)
		if err := os.Rename(source, target); err != nil && !errors.Is(err, os.ErrNotExist) {
			return err
		}
	}
	return os.Rename(path, path+".1")
}

func redactDiagnostic(message string) string {
	message = bearerPattern.ReplaceAllString(message, `${1}[REDACTED]`)
	message = tokenPattern.ReplaceAllString(message, `${1}${2}[REDACTED]`)
	return oauthCodePattern.ReplaceAllString(message, "[REDACTED_OAUTH_CODE]")
}

func isTestProcess() bool {
	return strings.HasSuffix(filepath.Base(os.Args[0]), ".test")
}

func debugAccountID(id string) string {
	id = strings.TrimSpace(id)
	if len(id) <= 8 {
		return id
	}
	return id[:8]
}

func debugTaskFingerprint(task string) string {
	sum := sha256.Sum256([]byte(task))
	return hex.EncodeToString(sum[:6])
}

func debugHashPrefix(hash string) string {
	hash = strings.TrimSpace(hash)
	if len(hash) <= 12 {
		return hash
	}
	return hash[:12]
}
