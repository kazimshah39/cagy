package supervisor

import (
	"context"
	"encoding/json"
	"strings"
	"testing"

	proc "github.com/kazimshah39/herdr-tandem/internal/process"
)

type fakeRunner struct{}

func (fakeRunner) LookPath(name string) (string, error) { return "/bin/" + name, nil }
func (fakeRunner) Run(context.Context, ...string) (proc.Result, error) {
	return proc.Result{ExitCode: 0, Stdout: "--yolo --session --continue list"}, nil
}
func (fakeRunner) RunAttached([]string, []string) error { return nil }

func TestRegistryResolvesBothSupervisors(t *testing.T) {
	r := DefaultRegistry()
	for _, id := range []string{CodexID, OpenCodeID} {
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
