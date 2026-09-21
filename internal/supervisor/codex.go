package supervisor

import (
	"context"
	"encoding/json"
	"fmt"
	"sort"
	"strings"

	proc "github.com/kazimshah39/herdr-tandem/internal/process"
)

const (
	mcpStartupTimeoutSec = 15
	mcpToolTimeoutSec    = 3600
)

var enabledTools = []string{"delegate_task", "task_status", "recover_task", "acknowledge_task", "forget_task", "developer_status"}

type Codex struct{}

func (Codex) ID() string          { return CodexID }
func (Codex) DisplayName() string { return "Codex" }
func (Codex) Executable() string  { return "codex" }

func (Codex) Validate(ctx context.Context, runner proc.Runner) error {
	if _, err := runner.LookPath("codex"); err != nil {
		return fmt.Errorf("codex is not on PATH: %w", err)
	}
	result, err := runner.Run(ctx, "codex", "--yolo", "--help")
	if err != nil || result.ExitCode != 0 {
		return fmt.Errorf("installed codex does not support the required --yolo mode")
	}
	result, err = runner.Run(ctx, "codex", "mcp", "--help")
	if err != nil || result.ExitCode != 0 || !strings.Contains(result.Stdout+result.Stderr, "list") {
		return fmt.Errorf("installed codex does not support MCP servers")
	}
	return nil
}

func (Codex) BuildLaunch(input LaunchContext) (LaunchSpec, error) {
	override, err := codexMCPOverride(input.Executable, input.MCPEnv)
	if err != nil {
		return LaunchSpec{}, err
	}
	project, _ := json.Marshal(input.ProjectDir)
	instructions, _ := json.Marshal(input.Instructions)
	args := []string{
		"codex", "--yolo", "--dangerously-bypass-hook-trust", "--search",
		"-c", `projects={` + string(project) + `={trust_level="trusted"}}`,
		"-c", "developer_instructions=" + string(instructions),
		"-c", override,
		"-C", input.ProjectDir,
	}
	return LaunchSpec{Args: args, Env: input.BaseEnv}, nil
}

func codexMCPOverride(binPath string, env map[string]string) (string, error) {
	encodedBin, err := json.Marshal(binPath)
	if err != nil {
		return "", fmt.Errorf("encode binary path: %w", err)
	}
	encodedArgs, _ := json.Marshal([]string{"mcp-server"})
	encodedTools, _ := json.Marshal(enabledTools)
	keys := make([]string, 0, len(env))
	for key := range env {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	pairs := make([]string, 0, len(keys))
	for _, key := range keys {
		encodedKey, _ := json.Marshal(key)
		encodedValue, _ := json.Marshal(env[key])
		pairs = append(pairs, string(encodedKey)+"="+string(encodedValue))
	}
	return fmt.Sprintf(
		`mcp_servers.herdr_tandem={command=%s,args=%s,required=true,startup_timeout_sec=%d,tool_timeout_sec=%d,enabled_tools=%s,supports_parallel_tool_calls=false,default_tools_approval_mode="approve",env={%s}}`,
		encodedBin, encodedArgs, mcpStartupTimeoutSec, mcpToolTimeoutSec, encodedTools, strings.Join(pairs, ","),
	), nil
}
