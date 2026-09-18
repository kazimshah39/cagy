package app

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestResolveExecutableValidAndSymlinks(t *testing.T) {
	tempDir := t.TempDir()
	binPath := filepath.Join(tempDir, "fake-cagy")
	if err := os.WriteFile(binPath, []byte("#!/bin/sh\nexit 0\n"), 0755); err != nil {
		t.Fatal(err)
	}

	symlinkPath := filepath.Join(tempDir, "fake-cagy-link")
	if err := os.Symlink(binPath, symlinkPath); err != nil {
		t.Fatal(err)
	}

	realBin, err := filepath.EvalSymlinks(binPath)
	if err != nil {
		t.Fatal(err)
	}

	resolved, err := resolveExecutable(func() (string, error) {
		return symlinkPath, nil
	})
	if err != nil {
		t.Fatalf("unexpected error resolving executable: %v", err)
	}
	if resolved != realBin {
		t.Fatalf("expected resolved %q, got %q", realBin, resolved)
	}
}

func TestResolveExecutableRejectsInvalid(t *testing.T) {
	tempDir := t.TempDir()

	// 1. Non-executable file
	nonExec := filepath.Join(tempDir, "non-exec")
	if err := os.WriteFile(nonExec, []byte("data"), 0644); err != nil {
		t.Fatal(err)
	}
	if _, err := resolveExecutable(func() (string, error) { return nonExec, nil }); err == nil || !strings.Contains(err.Error(), "not executable") {
		t.Fatalf("expected not executable error, got: %v", err)
	}

	// 2. Directory
	dirPath := filepath.Join(tempDir, "a-dir")
	if err := os.Mkdir(dirPath, 0755); err != nil {
		t.Fatal(err)
	}
	if _, err := resolveExecutable(func() (string, error) { return dirPath, nil }); err == nil || !strings.Contains(err.Error(), "not a regular file") {
		t.Fatalf("expected not a regular file error, got: %v", err)
	}

	// 3. Missing file
	missingPath := filepath.Join(tempDir, "does-not-exist")
	if _, err := resolveExecutable(func() (string, error) { return missingPath, nil }); err == nil {
		t.Fatal("expected error for missing executable, got nil")
	}

	// 4. Empty path
	if _, err := resolveExecutable(func() (string, error) { return "", nil }); err == nil || !strings.Contains(err.Error(), "path is empty") {
		t.Fatalf("expected path is empty error, got: %v", err)
	}

	// 5. Resolver error
	testErr := errors.New("resolver failure")
	if _, err := resolveExecutable(func() (string, error) { return "", testErr }); !errors.Is(err, testErr) {
		t.Fatalf("expected resolver failure error, got: %v", err)
	}
}

func TestCodexMCPServerOverrideFormatting(t *testing.T) {
	binPath := "/usr/local/bin/cagy"
	env := map[string]string{
		"HERDR_ENV":               "1",
		"HERDR_WORKSPACE_ID":      "w1",
		"CAGY_SUPERVISOR_PANE_ID": "w1:p1",
		"CAGY_DEVELOPER":          "w1-p1-dev",
		"CAGY_DEVELOPER_PANE_ID":  "w1:p2",
		"CAGY_PROJECT_DIR":        "/Users/test/project",
	}

	override, err := codexMCPServerOverride(binPath, env)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	mustContain := []string{
		`mcp_servers.cagy=`,
		`command="/usr/local/bin/cagy"`,
		`args=["mcp-server"]`,
		`required=true`,
		`startup_timeout_sec=15`,
		`tool_timeout_sec=3600`,
		`enabled_tools=["delegate_task","task_status","recover_task","acknowledge_task","forget_task","developer_status"]`,
		`supports_parallel_tool_calls=false`,
		`default_tools_approval_mode="approve"`,
		`"CAGY_DEVELOPER"="w1-p1-dev"`,
		`"CAGY_DEVELOPER_PANE_ID"="w1:p2"`,
		`"CAGY_PROJECT_DIR"="/Users/test/project"`,
		`"CAGY_SUPERVISOR_PANE_ID"="w1:p1"`,
		`"HERDR_ENV"="1"`,
		`"HERDR_WORKSPACE_ID"="w1"`,
	}

	for _, s := range mustContain {
		if !strings.Contains(override, s) {
			t.Fatalf("override missing %q in %s", s, override)
		}
	}
}

func TestCodexMCPServerOverrideHostilePaths(t *testing.T) {
	hostileBin := `/usr/local/bin/cagy with spaces "quotes" \backslashes\ and 🚀 dots.v2`
	hostileEnv := map[string]string{
		"KEY with \"quotes\"": `/path with spaces/and "quotes" and \backslashes\ and 🚀 unicode.dir`,
	}

	override, err := codexMCPServerOverride(hostileBin, hostileEnv)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	// Make sure json-escaped strings are present
	if !strings.Contains(override, `\u0026`) && !strings.Contains(override, `&`) {
		// Either raw or escaped & is fine
	}
	if !strings.Contains(override, `\"quotes\"`) {
		t.Fatalf("expected escaped quotes in override: %s", override)
	}
	if !strings.Contains(override, `\\backslashes\\`) {
		t.Fatalf("expected escaped backslashes in override: %s", override)
	}
	if !strings.Contains(override, `🚀`) && !strings.Contains(override, `\ud83d\ude80`) {
		t.Fatalf("expected unicode emoji in override: %s", override)
	}
}

func TestCodexArgsIncludesMCPOverride(t *testing.T) {
	project := "/tmp/test-project"
	mcpOverride := `mcp_servers.cagy={command="/bin/cagy",args=["mcp-server"]}`

	args := codexArgs(project, mcpOverride)
	if !contains(args, mcpOverride) {
		t.Fatalf("expected args to contain mcpOverride: %#v", args)
	}

	// Verify -c flag precedes the mcpOverride
	for i, a := range args {
		if a == mcpOverride {
			if i == 0 || args[i-1] != "-c" {
				t.Fatalf("expected -c before mcpOverride at index %d: %#v", i, args)
			}
		}
	}
}
