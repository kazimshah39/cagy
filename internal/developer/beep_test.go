package developer

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/kazimshah39/herdr-tandem/internal/supervisor"
)

func TestDeveloperOnlyBehavior(t *testing.T) {
	devAdapter := Agy{}
	spec, err := devAdapter.StartSpec(StartOptions{
		Name:   "developer-agent",
		PaneID: "pane-right",
		Model:  "gemini-3.8-flash-high",
	})
	if err != nil {
		t.Fatalf("devAdapter.StartSpec failed: %v", err)
	}

	// Verify that developer args are scoped to agy developer.
	joinedDevArgs := strings.Join(spec.Args, " ")
	for _, required := range []string{"--dangerously-skip-permissions", "--mode", "accept-edits", "--model", "gemini-3.8-flash-high"} {
		if !strings.Contains(joinedDevArgs, required) {
			t.Errorf("developer args %q missing required developer flag %q", joinedDevArgs, required)
		}
	}

	// Verify that supervisor adapters build supervisor-specific launches and do not
	// inherit developer-specific specs or pane configurations.
	codexAdapter := supervisor.Codex{}
	codexSpec, err := codexAdapter.BuildLaunch(supervisor.LaunchContext{
		ProjectDir:   "/tmp/test-proj",
		Executable:   "/usr/local/bin/herdr-tandem",
		Instructions: "supervisor instructions",
		MCPEnv:       map[string]string{"TEST": "1"},
	})
	if err != nil {
		t.Fatalf("codex.BuildLaunch failed: %v", err)
	}
	codexJoined := strings.Join(codexSpec.Args, " ")
	if !strings.Contains(codexJoined, "--yolo") || !strings.Contains(codexJoined, "--dangerously-bypass-hook-trust") {
		t.Errorf("codex supervisor launch missing expected flags: %q", codexJoined)
	}
	// Codex should not receive developer-specific pane or adapter arguments.
	if strings.Contains(codexJoined, "accept-edits") {
		t.Errorf("codex supervisor launch unexpectedly contains developer accept-edits: %q", codexJoined)
	}

	opencodeAdapter := supervisor.OpenCode{}
	opencodeSpec, err := opencodeAdapter.BuildLaunch(supervisor.LaunchContext{
		ProjectDir:   "/tmp/test-proj",
		Executable:   "/usr/local/bin/herdr-tandem",
		Instructions: "supervisor instructions",
		MCPEnv:       map[string]string{"TEST": "1"},
	})
	if err != nil {
		t.Fatalf("opencode.BuildLaunch failed: %v", err)
	}
	opencodeJoined := strings.Join(opencodeSpec.Args, " ")
	if !strings.Contains(opencodeJoined, "--dangerously-skip-permissions") {
		t.Errorf("opencode supervisor launch missing expected flags: %q", opencodeJoined)
	}
	if strings.Contains(opencodeJoined, "accept-edits") {
		t.Errorf("opencode supervisor launch unexpectedly contains developer accept-edits: %q", opencodeJoined)
	}

	agySupAdapter := supervisor.Agy{}
	agySupSpec, err := agySupAdapter.BuildLaunch(supervisor.LaunchContext{
		ProjectDir:   "/tmp/test-proj",
		Executable:   "/usr/local/bin/herdr-tandem",
		Instructions: "supervisor instructions",
		RuntimeID:    "rt123",
		Model:        "claude-opus-4-6-thinking",
		MCPEnv:       map[string]string{"TEST": "1"},
	})
	if err != nil {
		t.Fatalf("agy supervisor BuildLaunch failed: %v", err)
	}
	agySupJoined := strings.Join(agySupSpec.Args, " ")
	if !strings.Contains(agySupJoined, "--agent herdr-tandem-rt123") {
		t.Errorf("agy supervisor launch missing custom agent: %q", agySupJoined)
	}
}

func TestNoGlobalConfigWrite(t *testing.T) {
	home, err := os.UserHomeDir()
	if err != nil {
		t.Skip("skipping global config check: unable to resolve user home dir")
	}

	agySettingsPath := filepath.Join(home, ".gemini", "antigravity-cli", "settings.json")
	herdrConfigPath := filepath.Join(home, ".config", "herdr", "config.toml")

	checkFileInfo := func(path string) (bool, time.Time, int64) {
		info, err := os.Stat(path)
		if err != nil {
			return false, time.Time{}, 0
		}
		return true, info.ModTime(), info.Size()
	}

	agyExistedBefore, agyModBefore, agySizeBefore := checkFileInfo(agySettingsPath)
	herdrExistedBefore, herdrModBefore, herdrSizeBefore := checkFileInfo(herdrConfigPath)

	// Execute developer launch spec construction.
	devAdapter := Agy{}
	_, err = devAdapter.StartSpec(StartOptions{
		Name:      "dev",
		PaneID:    "w1:p2",
		SessionID: "sess-abc",
		Model:     "gemini-3.8-flash-high",
	})
	if err != nil {
		t.Fatalf("StartSpec failed: %v", err)
	}

	// Execute developer resolution.
	_, err = Resolve(AgyID)
	if err != nil {
		t.Fatalf("Resolve failed: %v", err)
	}

	// Execute supervisor build launches.
	supRegistry := supervisor.DefaultRegistry()
	for _, supID := range []string{supervisor.CodexID, supervisor.OpenCodeID, supervisor.AgyID} {
		sup, err := supRegistry.Resolve(supID)
		if err != nil {
			t.Fatalf("resolve supervisor %q failed: %v", supID, err)
		}
		_, err = sup.BuildLaunch(supervisor.LaunchContext{
			ProjectDir:   "/tmp/test-proj",
			Executable:   "/usr/local/bin/herdr-tandem",
			Instructions: "instructions",
			RuntimeID:    "rt-test",
			Model:        "test-model",
			MCPEnv:       map[string]string{"A": "B"},
		})
		if err != nil {
			t.Fatalf("BuildLaunch for %q failed: %v", supID, err)
		}
	}

	// Verify agy settings.json was not modified or created.
	agyExistedAfter, agyModAfter, agySizeAfter := checkFileInfo(agySettingsPath)
	if agyExistedBefore != agyExistedAfter {
		t.Fatalf("agy global settings.json existence changed: before=%v after=%v", agyExistedBefore, agyExistedAfter)
	}
	if agyExistedBefore && (!agyModBefore.Equal(agyModAfter) || agySizeBefore != agySizeAfter) {
		t.Fatalf("agy global settings.json was modified: before(mod=%v, size=%d) after(mod=%v, size=%d)",
			agyModBefore, agySizeBefore, agyModAfter, agySizeAfter)
	}

	// Verify herdr config.toml was not modified or created.
	herdrExistedAfter, herdrModAfter, herdrSizeAfter := checkFileInfo(herdrConfigPath)
	if herdrExistedBefore != herdrExistedAfter {
		t.Fatalf("herdr global config.toml existence changed: before=%v after=%v", herdrExistedBefore, herdrExistedAfter)
	}
	if herdrExistedBefore && (!herdrModBefore.Equal(herdrModAfter) || herdrSizeBefore != herdrSizeAfter) {
		t.Fatalf("herdr global config.toml was modified: before(mod=%v, size=%d) after(mod=%v, size=%d)",
			herdrModBefore, herdrSizeBefore, herdrModAfter, herdrSizeAfter)
	}
}

func TestRequiredFlagsModelAndResumeRemainIntact(t *testing.T) {
	adapter := Agy{}

	// Fresh start without session or model
	fresh, err := adapter.StartSpec(StartOptions{Name: "dev", PaneID: "w1:p1"})
	if err != nil {
		t.Fatal(err)
	}
	if fresh.ExpectedSession != nil {
		t.Fatal("fresh start unexpectedly has ExpectedSession")
	}
	joinedFresh := strings.Join(fresh.Args, " ")
	if !strings.Contains(joinedFresh, "--dangerously-skip-permissions") {
		t.Fatalf("missing --dangerously-skip-permissions: %q", joinedFresh)
	}
	if !strings.Contains(joinedFresh, "--mode accept-edits") {
		t.Fatalf("missing --mode accept-edits: %q", joinedFresh)
	}
	for _, forbidden := range []string{"--conversation", "--continue"} {
		if strings.Contains(joinedFresh, forbidden) {
			t.Fatalf("fresh start unexpectedly contained %q: %q", forbidden, joinedFresh)
		}
	}

	// Fresh start with model
	freshWithModel, err := adapter.StartSpec(StartOptions{Name: "dev", PaneID: "w1:p1", Model: "gemini-3.8-flash-high"})
	if err != nil {
		t.Fatal(err)
	}
	joinedModel := strings.Join(freshWithModel.Args, " ")
	if !strings.Contains(joinedModel, "--model gemini-3.8-flash-high") {
		t.Fatalf("missing --model flag: %q", joinedModel)
	}

	// Resumed start with exact session
	resumed, err := adapter.StartSpec(StartOptions{
		Name:      "dev",
		PaneID:    "w1:p1",
		SessionID: "conversation-xyz-789",
		Model:     "gemini-3.8-flash-high",
	})
	if err != nil {
		t.Fatal(err)
	}
	if resumed.ExpectedSession == nil || resumed.ExpectedSession.Value != "conversation-xyz-789" {
		t.Fatalf("ExpectedSession mismatch: %+v", resumed.ExpectedSession)
	}
	if len(resumed.Args) < 2 || resumed.Args[0] != "--conversation" || resumed.Args[1] != "conversation-xyz-789" {
		t.Fatalf("resumed start must have --conversation <id> first, got: %#v", resumed.Args)
	}
	joinedResumed := strings.Join(resumed.Args, " ")
	if strings.Contains(joinedResumed, "--continue") {
		t.Fatalf("resumed start unexpectedly used ambiguous --continue: %q", joinedResumed)
	}
	if !strings.Contains(joinedResumed, "--dangerously-skip-permissions") || !strings.Contains(joinedResumed, "--mode accept-edits") {
		t.Fatalf("resumed start dropped required YOLO flags: %q", joinedResumed)
	}
	if !strings.Contains(joinedResumed, "--model gemini-3.8-flash-high") {
		t.Fatalf("resumed start missing model: %q", joinedResumed)
	}
}

func TestSupervisorBehaviorUnchanged(t *testing.T) {
	// 1. Codex supervisor
	codex := supervisor.Codex{}
	codexSpec, err := codex.BuildLaunch(supervisor.LaunchContext{
		ProjectDir:   "/path/to/project",
		Executable:   "/bin/herdr-tandem",
		Instructions: "supervisor instructions",
		MCPEnv:       map[string]string{"P1": "V1"},
		BaseEnv:      []string{"HOME=/Users/test", "PATH=/usr/bin"},
	})
	if err != nil {
		t.Fatalf("codex.BuildLaunch failed: %v", err)
	}
	codexArgs := strings.Join(codexSpec.Args, " ")
	for _, expected := range []string{"codex", "--yolo", "--dangerously-bypass-hook-trust", "--search", "-C", "/path/to/project"} {
		if !strings.Contains(codexArgs, expected) {
			t.Errorf("codex args %q missing %q", codexArgs, expected)
		}
	}

	// 2. OpenCode supervisor
	opencode := supervisor.OpenCode{}
	opencodeSpec, err := opencode.BuildLaunch(supervisor.LaunchContext{
		ProjectDir:   "/path/to/project",
		Executable:   "/bin/herdr-tandem",
		Instructions: "supervisor instructions",
		MCPEnv:       map[string]string{"P1": "V1"},
		BaseEnv:      []string{"HOME=/Users/test", "PATH=/usr/bin"},
	})
	if err != nil {
		t.Fatalf("opencode.BuildLaunch failed: %v", err)
	}
	opencodeArgs := strings.Join(opencodeSpec.Args, " ")
	if !strings.Contains(opencodeArgs, "opencode /path/to/project --dangerously-skip-permissions") {
		t.Errorf("opencode args mismatch: %q", opencodeArgs)
	}

	// 3. Agy supervisor
	agy := supervisor.Agy{}
	agySpec, err := agy.BuildLaunch(supervisor.LaunchContext{
		ProjectDir:   "/path/to/project",
		Executable:   "/bin/herdr-tandem",
		Instructions: "supervisor instructions",
		RuntimeID:    "rt-live-1",
		Model:        "claude-opus-4-6-thinking",
		MCPEnv:       map[string]string{"P1": "V1"},
		BaseEnv:      []string{"HOME=/Users/test", "PATH=/usr/bin"},
	})
	if err != nil {
		t.Fatalf("agy supervisor BuildLaunch failed: %v", err)
	}
	agyArgs := strings.Join(agySpec.Args, " ")
	for _, expected := range []string{"agy", "--agent herdr-tandem-rt-live-1", "--dangerously-skip-permissions", "--mode accept-edits", "--model claude-opus-4-6-thinking"} {
		if !strings.Contains(agyArgs, expected) {
			t.Errorf("agy supervisor args %q missing %q", agyArgs, expected)
		}
	}
}
