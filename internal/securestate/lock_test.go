package securestate

import (
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestLockRejectsLiveOwnerAndReclaimsDeadOwner(t *testing.T) {
	dir := t.TempDir()
	now := time.Now().UTC()
	options := LockOptions{Name: "accounts.lock", Subject: "accounts", OperationID: "one", Now: now}
	first, err := Acquire(dir, options)
	if err != nil {
		t.Fatal(err)
	}
	defer first.Release()
	if _, err := Acquire(dir, options); err == nil || !strings.Contains(err.Error(), "busy") {
		t.Fatalf("live owner error=%v", err)
	}
	first.Release()

	metadata := LockMetadata{Version: 1, PID: 99999999, Subject: "accounts", StartedAt: now.Add(-time.Minute)}
	data, _ := json.Marshal(metadata)
	if err := os.WriteFile(filepath.Join(dir, options.Name), data, 0o600); err != nil {
		t.Fatal(err)
	}
	reclaimed, err := Acquire(dir, options)
	if err != nil {
		t.Fatalf("reclaim dead owner: %v", err)
	}
	reclaimed.Release()
}

func TestLockIncompleteYoungFailsClosedAndOldIsReclaimed(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "accounts.lock")
	if err := os.WriteFile(path, []byte("{"), 0o600); err != nil {
		t.Fatal(err)
	}
	now := time.Now().UTC()
	if _, err := Acquire(dir, LockOptions{Name: "accounts.lock", Subject: "accounts", Now: now}); err == nil || !strings.Contains(err.Error(), "too new") {
		t.Fatalf("young incomplete error=%v", err)
	}
	old := now.Add(-time.Minute)
	if err := os.Chtimes(path, old, old); err != nil {
		t.Fatal(err)
	}
	lock, err := Acquire(dir, LockOptions{Name: "accounts.lock", Subject: "accounts", Now: now})
	if err != nil {
		t.Fatalf("old incomplete reclaim: %v", err)
	}
	lock.Release()
}

func TestLockProcessContention(t *testing.T) {
	if os.Getenv("HERDR_TANDEM_LOCK_HELPER") == "1" {
		dir := os.Getenv("HERDR_TANDEM_LOCK_DIR")
		lock, err := Acquire(dir, LockOptions{Name: "accounts.lock", Subject: "accounts", OperationID: "helper", Now: time.Now()})
		if err != nil {
			os.Exit(2)
		}
		defer lock.Release()
		_, _ = os.Stdout.WriteString("locked\n")
		time.Sleep(2 * time.Second)
		return
	}

	dir := t.TempDir()
	cmd := exec.Command(os.Args[0], "-test.run=^TestLockProcessContention$")
	cmd.Env = append(os.Environ(), "HERDR_TANDEM_LOCK_HELPER=1", "HERDR_TANDEM_LOCK_DIR="+dir)
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		t.Fatal(err)
	}
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	buf := make([]byte, 7)
	if _, err := stdout.Read(buf); err != nil || string(buf) != "locked\n" {
		_ = cmd.Process.Kill()
		t.Fatalf("helper readiness=%q err=%v", buf, err)
	}
	if _, err := Acquire(dir, LockOptions{Name: "accounts.lock", Subject: "accounts", OperationID: "parent", Now: time.Now()}); err == nil || !strings.Contains(err.Error(), "busy") {
		_ = cmd.Process.Kill()
		t.Fatalf("process contention error=%v", err)
	}
	if err := cmd.Wait(); err != nil {
		t.Fatal(err)
	}
	lock, err := Acquire(dir, LockOptions{Name: "accounts.lock", Subject: "accounts", OperationID: "parent", Now: time.Now()})
	if err != nil {
		t.Fatalf("acquire after helper exit: %v", err)
	}
	lock.Release()
}
