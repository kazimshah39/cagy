package supervisor

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"

	proc "github.com/kazimshah39/herdr-tandem/internal/process"
)

type OpenCode struct{}

func (OpenCode) ID() string          { return OpenCodeID }
func (OpenCode) DisplayName() string { return "OpenCode" }
func (OpenCode) Executable() string  { return "opencode" }

func (OpenCode) Validate(ctx context.Context, runner proc.Runner) error {
	if _, err := runner.LookPath("opencode"); err != nil {
		return fmt.Errorf("opencode is not on PATH: %w", err)
	}
	result, err := runner.Run(ctx, "opencode", "--dangerously-skip-permissions", "--help")
	text := result.Stdout + result.Stderr
	if err != nil || result.ExitCode != 0 {
		return fmt.Errorf("installed opencode cannot show its command help")
	}
	for _, required := range []string{"--session", "--continue"} {
		if !strings.Contains(text, required) {
			return fmt.Errorf("installed opencode does not support required option %s", required)
		}
	}
	result, err = runner.Run(ctx, "opencode", "mcp", "--help")
	if err != nil || result.ExitCode != 0 {
		return fmt.Errorf("installed opencode does not support MCP servers")
	}
	return nil
}

func (OpenCode) BuildLaunch(input LaunchContext) (LaunchSpec, error) {
	if strings.TrimSpace(input.Executable) == "" {
		return LaunchSpec{}, fmt.Errorf("Herdr Tandem executable path is empty")
	}
	config := struct {
		DefaultAgent string                         `json:"default_agent"`
		Agent        map[string]openCodeAgentConfig `json:"agent"`
		MCP          map[string]openCodeMCPConfig   `json:"mcp"`
	}{
		DefaultAgent: "build",
		Agent:        map[string]openCodeAgentConfig{"build": {Mode: "primary", Prompt: input.Instructions, Permission: map[string]string{"*": "allow"}}},
		MCP: map[string]openCodeMCPConfig{
			"herdr_tandem": {
				Type:        "local",
				Command:     []string{input.Executable, "mcp-server"},
				Environment: input.MCPEnv,
				Enabled:     true,
				Timeout:     mcpToolTimeoutSec * 1000,
			},
		},
	}
	encoded, err := json.Marshal(config)
	if err != nil {
		return LaunchSpec{}, fmt.Errorf("encode OpenCode configuration: %w", err)
	}
	env := mergeEnv(input.BaseEnv, map[string]string{"OPENCODE_CONFIG_CONTENT": string(encoded)})
	args := []string{"opencode", input.ProjectDir, "--dangerously-skip-permissions"}
	return LaunchSpec{Args: args, Env: env}, nil
}

type openCodeAgentConfig struct {
	Mode       string            `json:"mode"`
	Prompt     string            `json:"prompt"`
	Permission map[string]string `json:"permission"`
}

type openCodeMCPConfig struct {
	Type        string            `json:"type"`
	Command     []string          `json:"command"`
	Environment map[string]string `json:"environment,omitempty"`
	Enabled     bool              `json:"enabled"`
	Timeout     int               `json:"timeout"`
}
