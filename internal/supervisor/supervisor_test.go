package supervisor

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	proc "github.com/kazimshah39/herdr-tandem/internal/process"
)

type fakeRunner struct{}

func (fakeRunner) LookPath(name string) (string, error) { return "/bin/" + name, nil }
func (fakeRunner) Run(context.Context, ...string) (proc.Result, error) {
	return proc.Result{ExitCode: 0, Stdout: "--yolo --session --continue list"}, nil
}
func (fakeRunner) RunAttached(string, []string, []string) error { return nil }

func TestRegistryResolvesAllSupervisors(t *testing.T) {
	r := DefaultRegistry()
	for _, id := range []string{CodexID, OpenCodeID, AgyID} {
		got, err := r.Resolve(id)
		if err != nil || got.ID() != id {
			t.Fatalf("id=%q adapter=%v err=%v", id, got, err)
		}
	}
	if _, err := r.Resolve("unknown"); err == nil {
		t.Fatal("unknown supervisor accepted")
	}
}

func TestCodexLaunchKeepsRequiredFlagsAndMCP(t *testing.T) {
	spec, err := (Codex{}).BuildLaunch(LaunchContext{ProjectDir: `/tmp/quote"slash`, Executable: `/tmp/herdr tandem`, MCPEnv: map[string]string{"HERDR_TANDEM_PROJECT_DIR": "/tmp/x"}, Instructions: "delegate only"})
	if err != nil {
		t.Fatal(err)
	}
	joined := strings.Join(spec.Args, " ")
	for _, want := range []string{"--yolo", "--dangerously-bypass-hook-trust", "--search", "developer_instructions=", "mcp_servers.herdr_tandem"} {
		if !strings.Contains(joined, want) {
			t.Fatalf("args=%q missing %q", joined, want)
		}
	}
}

func TestOpenCodeLaunchUsesJSONMCPConfig(t *testing.T) {
	spec, err := (OpenCode{}).BuildLaunch(LaunchContext{ProjectDir: `/tmp/project with spaces`, Executable: `/tmp/herdr tandem`, MCPEnv: map[string]string{"HERDR_ENV": "1"}, Instructions: "delegate safely", BaseEnv: []string{"PATH=/bin"}})
	if err != nil {
		t.Fatal(err)
	}
	if strings.Join(spec.Args, " ") != "opencode /tmp/project with spaces --dangerously-skip-permissions" {
		t.Fatalf("args=%#v", spec.Args)
	}
	var config map[string]any
	for _, entry := range spec.Env {
		if strings.HasPrefix(entry, "OPENCODE_CONFIG_CONTENT=") {
			if err := json.Unmarshal([]byte(strings.TrimPrefix(entry, "OPENCODE_CONFIG_CONTENT=")), &config); err != nil {
				t.Fatal(err)
			}
		}
	}
	mcp, ok := config["mcp"].(map[string]any)
	if !ok {
		t.Fatalf("config=%#v", config)
	}
	server := mcp["herdr_tandem"].(map[string]any)
	if server["type"] != "local" {
		t.Fatalf("server=%#v", server)
	}
	commandValues := server["command"].([]any)
	commandText := ""
	for _, value := range commandValues {
		commandText += value.(string) + " "
	}
	if !strings.Contains(commandText, "mcp-server") {
		t.Fatalf("server=%#v", server)
	}
}

type agyFakeRunner struct {
	helpText string
	exitCode int
	runErr   error
}

func (r agyFakeRunner) LookPath(name string) (string, error) { return "/bin/" + name, nil }
func (r agyFakeRunner) Run(context.Context, ...string) (proc.Result, error) {
	return proc.Result{ExitCode: r.exitCode, Stdout: r.helpText}, r.runErr
}
func (agyFakeRunner) RunAttached(string, []string, []string) error { return nil }

func TestAgyValidateRequiresInstalledOptions(t *testing.T) {
	adapter := Agy{}
	validHelp := "--agent --model --mode --dangerously-skip-permissions"

	// Success case
	runner := agyFakeRunner{helpText: validHelp}
	if err := adapter.Validate(context.Background(), runner); err != nil {
		t.Fatalf("Validate failed for valid runner: %v", err)
	}

	// Missing option
	for _, missing := range []string{"--agent", "--model", "--mode", "--dangerously-skip-permissions"} {
		partialHelp := strings.ReplaceAll(validHelp, missing, "")
		runner := agyFakeRunner{helpText: partialHelp}
		if err := adapter.Validate(context.Background(), runner); err == nil {
			t.Fatalf("Validate succeeded despite missing %s", missing)
		}
	}

	// Command failure
	failRunner := agyFakeRunner{helpText: validHelp, exitCode: 1}
	if err := adapter.Validate(context.Background(), failRunner); err == nil {
		t.Fatal("Validate succeeded with non-zero exit code")
	}
}

func TestAgyBuildLaunchArgvAndModel(t *testing.T) {
	adapter := Agy{}
	ctx := LaunchContext{
		ProjectDir:   "/tmp/my-project",
		Executable:   "/usr/local/bin/herdr-tandem",
		RuntimeID:    "rt-test-123",
		Instructions: "supervisor instructions",
		BaseEnv:      []string{"HOME=/tmp"},
	}

	// Without model
	spec, err := adapter.BuildLaunch(ctx)
	if err != nil {
		t.Fatalf("BuildLaunch failed: %v", err)
	}
	if spec.Dir != ctx.ProjectDir {
		t.Fatalf("spec.Dir = %q, want %q", spec.Dir, ctx.ProjectDir)
	}
	expectedArgs := []string{
		"agy",
		"--agent", "herdr-tandem-rt-test-123",
		"--dangerously-skip-permissions",
		"--mode", "accept-edits",
	}
	if strings.Join(spec.Args, " ") != strings.Join(expectedArgs, " ") {
		t.Fatalf("args mismatch:\ngot:  %#v\nwant: %#v", spec.Args, expectedArgs)
	}
	for _, arg := range spec.Args {
		if arg == ctx.ProjectDir {
			t.Fatalf("project directory %q must not be a positional argument in agy argv", arg)
		}
	}

	// With model
	ctxWithModel := ctx
	ctxWithModel.Model = "gemini-2.5-pro"
	specWithModel, err := adapter.BuildLaunch(ctxWithModel)
	if err != nil {
		t.Fatalf("BuildLaunch with model failed: %v", err)
	}
	if specWithModel.Dir != ctxWithModel.ProjectDir {
		t.Fatalf("specWithModel.Dir = %q, want %q", specWithModel.Dir, ctxWithModel.ProjectDir)
	}
	expectedArgsWithModel := []string{
		"agy",
		"--agent", "herdr-tandem-rt-test-123",
		"--dangerously-skip-permissions",
		"--mode", "accept-edits",
		"--model", "gemini-2.5-pro",
	}
	if strings.Join(specWithModel.Args, " ") != strings.Join(expectedArgsWithModel, " ") {
		t.Fatalf("args with model mismatch:\ngot:  %#v\nwant: %#v", specWithModel.Args, expectedArgsWithModel)
	}
	for _, arg := range specWithModel.Args {
		if arg == ctxWithModel.ProjectDir {
			t.Fatalf("project directory %q must not be a positional argument in agy argv with model", arg)
		}
	}

	// User prompt should not be in args
	for _, arg := range specWithModel.Args {
		if strings.Contains(arg, "instructions") {
			t.Fatalf("task prompt leaked into launch args: %q", arg)
		}
	}
}

func TestAgyPrepareAndCleanupLaunch(t *testing.T) {
	adapter := Agy{}
	tempConfigRoot := t.TempDir()
	ctx := LaunchContext{
		ProjectDir:   "/tmp/my-project",
		Executable:   "/usr/local/bin/herdr-tandem",
		RuntimeID:    "rt-test-lifecycle",
		Instructions: "You are the supervisor. Delegate with MCP.",
		ConfigRoot:   tempConfigRoot,
		MCPEnv:       map[string]string{"HERDR_ENV": "1", "HERDR_PANE_ID": "p1"},
	}

	artifact, err := adapter.LaunchArtifact(ctx)
	if err != nil {
		t.Fatalf("LaunchArtifact failed: %v", err)
	}
	if artifact != "herdr-tandem-rt-test-lifecycle" {
		t.Fatalf("unexpected artifact name: %q", artifact)
	}

	runner := fakeRunner{}
	// PrepareLaunch
	if err := adapter.PrepareLaunch(context.Background(), runner, ctx, artifact); err != nil {
		t.Fatalf("PrepareLaunch failed: %v", err)
	}

	agentDir := filepath.Join(tempConfigRoot, "agents", artifact)
	agentFile := filepath.Join(agentDir, "agent.md")

	// Verify directory permissions
	dirInfo, err := os.Stat(agentDir)
	if err != nil {
		t.Fatalf("agent dir stat failed: %v", err)
	}
	if dirInfo.Mode().Perm() != 0700 {
		t.Fatalf("agent dir perm: got %o want 0700", dirInfo.Mode().Perm())
	}

	// Verify file permissions
	fileInfo, err := os.Stat(agentFile)
	if err != nil {
		t.Fatalf("agent file stat failed: %v", err)
	}
	if fileInfo.Mode().Perm() != 0600 {
		t.Fatalf("agent file perm: got %o want 0600", fileInfo.Mode().Perm())
	}

	// Verify content structure
	content, err := os.ReadFile(agentFile)
	if err != nil {
		t.Fatalf("read agent.md failed: %v", err)
	}
	text := string(content)
	for _, expected := range []string{
		"name: " + artifact,
		"description: Herdr Tandem agy supervisor session",
		"mainAgent: true",
		"subagent: false",
		"model: inherit",
		"commandExecutionPolicy: eager",
		"- name: herdr_tandem",
		"command: \"/usr/local/bin/herdr-tandem\"",
		"- mcp-server",
		"HERDR_ENV: \"1\"",
		"# System Prompt",
		"You are the supervisor. Delegate with MCP.",
	} {
		if !strings.Contains(text, expected) {
			t.Fatalf("agent.md missing expected content %q; full content:\n%s", expected, text)
		}
	}

	// Collision refusal: preparing again must fail
	if err := adapter.PrepareLaunch(context.Background(), runner, ctx, artifact); err == nil {
		t.Fatal("PrepareLaunch succeeded on collision")
	}

	// Idempotent CleanupLaunch
	if err := adapter.CleanupLaunch(context.Background(), runner, ctx, artifact); err != nil {
		t.Fatalf("CleanupLaunch failed: %v", err)
	}
	if _, err := os.Stat(agentDir); !os.IsNotExist(err) {
		t.Fatalf("agent dir still exists after cleanup: %v", err)
	}

	// Second cleanup is a no-op
	if err := adapter.CleanupLaunch(context.Background(), runner, ctx, artifact); err != nil {
		t.Fatalf("idempotent CleanupLaunch failed: %v", err)
	}
}

func TestAgyCleanupOwnershipAndSafetyChecks(t *testing.T) {
	adapter := Agy{}
	tempConfigRoot := t.TempDir()
	ctx := LaunchContext{
		ProjectDir:   "/tmp/my-project",
		Executable:   "/usr/local/bin/herdr-tandem",
		RuntimeID:    "rt-safety",
		Instructions: "test",
		ConfigRoot:   tempConfigRoot,
	}
	artifact, err := adapter.LaunchArtifact(ctx)
	if err != nil {
		t.Fatal(err)
	}
	runner := fakeRunner{}

	// Test ownership mismatch: file exists but belongs to a different agent
	agentDir := filepath.Join(tempConfigRoot, "agents", artifact)
	if err := os.MkdirAll(agentDir, 0700); err != nil {
		t.Fatal(err)
	}
	agentFile := filepath.Join(agentDir, "agent.md")
	if err := os.WriteFile(agentFile, []byte("---\nname: unrelated-agent\n---\n"), 0600); err != nil {
		t.Fatal(err)
	}
	if err := adapter.CleanupLaunch(context.Background(), runner, ctx, artifact); err == nil {
		t.Fatal("CleanupLaunch succeeded on ownership mismatch")
	}
	_ = os.Remove(agentFile)
	_ = os.Remove(agentDir)

	// Test ownership mismatch: comment inside frontmatter contains artifact name
	if err := os.MkdirAll(agentDir, 0700); err != nil {
		t.Fatal(err)
	}
	commentContent := fmt.Sprintf("---\nname: unrelated-agent\n# name: %s\n---\n", artifact)
	if err := os.WriteFile(agentFile, []byte(commentContent), 0600); err != nil {
		t.Fatal(err)
	}
	if err := adapter.CleanupLaunch(context.Background(), runner, ctx, artifact); err == nil {
		t.Fatal("CleanupLaunch succeeded when expected name only appeared in a frontmatter comment")
	}
	_ = os.Remove(agentFile)
	_ = os.Remove(agentDir)

	// Test ownership mismatch: body text contains artifact name
	if err := os.MkdirAll(agentDir, 0700); err != nil {
		t.Fatal(err)
	}
	bodyContent := fmt.Sprintf("---\nname: unrelated-agent\n---\n# name: %s\n", artifact)
	if err := os.WriteFile(agentFile, []byte(bodyContent), 0600); err != nil {
		t.Fatal(err)
	}
	if err := adapter.CleanupLaunch(context.Background(), runner, ctx, artifact); err == nil {
		t.Fatal("CleanupLaunch succeeded when expected name only appeared in markdown body")
	}
	_ = os.Remove(agentFile)
	_ = os.Remove(agentDir)

	// Test unexpected extra files: directory has extra files
	if err := os.MkdirAll(agentDir, 0700); err != nil {
		t.Fatal(err)
	}
	validContent := fmt.Sprintf("---\nname: %s\n---\n", artifact)
	if err := os.WriteFile(agentFile, []byte(validContent), 0600); err != nil {
		t.Fatal(err)
	}
	extraFile := filepath.Join(agentDir, "extra.txt")
	if err := os.WriteFile(extraFile, []byte("extra"), 0600); err != nil {
		t.Fatal(err)
	}
	if err := adapter.CleanupLaunch(context.Background(), runner, ctx, artifact); err == nil {
		t.Fatal("CleanupLaunch succeeded with unexpected extra file")
	}
	_ = os.Remove(extraFile)
	_ = os.Remove(agentFile)
	_ = os.Remove(agentDir)

	// Test permission mismatch
	if err := os.MkdirAll(agentDir, 0755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(agentFile, []byte(validContent), 0600); err != nil {
		t.Fatal(err)
	}
	if err := adapter.CleanupLaunch(context.Background(), runner, ctx, artifact); err == nil {
		t.Fatal("CleanupLaunch succeeded with directory permission 0755")
	}
	_ = os.Remove(agentFile)
	_ = os.Remove(agentDir)
}

func TestValidateAgyArtifactOwnership(t *testing.T) {
	artifact := "herdr-tandem-rt-test-123"

	// Exact match
	valid := []byte(fmt.Sprintf("---\nname: %s\ndescription: test\n---\n# Body\n", artifact))
	if err := ValidateAgyArtifactOwnership(valid, artifact); err != nil {
		t.Fatalf("expected valid match: %v", err)
	}

	// Quoted name
	quoted := []byte(fmt.Sprintf("---\nname: %q\n---\n", artifact))
	if err := ValidateAgyArtifactOwnership(quoted, artifact); err != nil {
		t.Fatalf("expected quoted match: %v", err)
	}

	// Name with trailing comment
	withComment := []byte(fmt.Sprintf("---\nname: %s # inline comment\n---\n", artifact))
	if err := ValidateAgyArtifactOwnership(withComment, artifact); err != nil {
		t.Fatalf("expected trailing comment match: %v", err)
	}

	// Mismatched name
	mismatch := []byte("---\nname: other-agent\n---\n")
	if err := ValidateAgyArtifactOwnership(mismatch, artifact); err == nil {
		t.Fatal("expected error for mismatched name")
	}

	// Name in frontmatter comment only
	commentOnly := []byte(fmt.Sprintf("---\nname: other-agent\n# name: %s\n---\n", artifact))
	if err := ValidateAgyArtifactOwnership(commentOnly, artifact); err == nil {
		t.Fatal("expected error when name is in frontmatter comment")
	}

	// Name in body only
	bodyOnly := []byte(fmt.Sprintf("---\nname: other-agent\n---\n# name: %s\n", artifact))
	if err := ValidateAgyArtifactOwnership(bodyOnly, artifact); err == nil {
		t.Fatal("expected error when name is in body")
	}

	// No frontmatter delimiter
	noFrontmatter := []byte(fmt.Sprintf("name: %s\n", artifact))
	if err := ValidateAgyArtifactOwnership(noFrontmatter, artifact); err == nil {
		t.Fatal("expected error when no frontmatter exists")
	}

	// Nested / indented name field
	nested := []byte(fmt.Sprintf("---\nparent:\n  name: %s\n---\n", artifact))
	if err := ValidateAgyArtifactOwnership(nested, artifact); err == nil {
		t.Fatal("expected error for nested/indented name")
	}
	tabNested := []byte(fmt.Sprintf("---\n\tname: %s\n---\n", artifact))
	if err := ValidateAgyArtifactOwnership(tabNested, artifact); err == nil {
		t.Fatal("expected error for tab-indented name")
	}

	// Unmatched / malformed quotes
	unmatchedDouble := []byte(fmt.Sprintf("---\nname: \"%s\n---\n", artifact))
	if err := ValidateAgyArtifactOwnership(unmatchedDouble, artifact); err == nil {
		t.Fatal("expected error for unmatched double quote")
	}
	unmatchedSingle := []byte(fmt.Sprintf("---\nname: '%s\n---\n", artifact))
	if err := ValidateAgyArtifactOwnership(unmatchedSingle, artifact); err == nil {
		t.Fatal("expected error for unmatched single quote")
	}
	danglingQuote := []byte(fmt.Sprintf("---\nname: %s\"\n---\n", artifact))
	if err := ValidateAgyArtifactOwnership(danglingQuote, artifact); err == nil {
		t.Fatal("expected error for dangling quote")
	}

	// Duplicate top-level name fields
	duplicate := []byte(fmt.Sprintf("---\nname: %s\nname: other-agent\n---\n", artifact))
	if err := ValidateAgyArtifactOwnership(duplicate, artifact); err == nil {
		t.Fatal("expected error for duplicate top-level name")
	}
	duplicateSame := []byte(fmt.Sprintf("---\nname: %s\nname: %s\n---\n", artifact, artifact))
	if err := ValidateAgyArtifactOwnership(duplicateSame, artifact); err == nil {
		t.Fatal("expected error for duplicate identical top-level name")
	}

	// Unterminated frontmatter (missing closing ---)
	unterminated := []byte(fmt.Sprintf("---\nname: %s\ndescription: unterminated\n", artifact))
	if err := ValidateAgyArtifactOwnership(unterminated, artifact); err == nil {
		t.Fatal("expected error for unterminated frontmatter")
	}

	// Full generated agent.md structure with indented - name under mcpServers passes
	generated := []byte(fmt.Sprintf("---\nname: %s\ndescription: test\ntools:\n  - view_file\nmcpServers:\n  - name: herdr_tandem\n    command: /tmp/bin\n---\n# System Prompt\nHello\n", artifact))
	if err := ValidateAgyArtifactOwnership(generated, artifact); err != nil {
		t.Fatalf("expected real generated content with nested mcpServers name to pass: %v", err)
	}
}
