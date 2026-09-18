package app

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
)

const (
	mcpStartupTimeoutSec = 15
	mcpToolTimeoutSec    = 3600
)

var defaultEnabledTools = []string{
	"delegate_task",
	"task_status",
	"recover_task",
	"acknowledge_task",
	"forget_task",
	"developer_status",
}

// resolveExecutable resolves the path returned by resolver, evaluates symlinks,
// and ensures it is an absolute path to a regular executable file.
func resolveExecutable(resolver func() (string, error)) (string, error) {
	if resolver == nil {
		resolver = os.Executable
	}
	execPath, err := resolver()
	if err != nil {
		return "", fmt.Errorf("lookup executable path: %w", err)
	}
	if strings.TrimSpace(execPath) == "" {
		return "", fmt.Errorf("lookup executable path: path is empty")
	}
	resolved, err := filepath.EvalSymlinks(execPath)
	if err != nil {
		return "", fmt.Errorf("eval executable symlinks: %w", err)
	}
	absPath, err := filepath.Abs(resolved)
	if err != nil {
		return "", fmt.Errorf("resolve absolute executable path: %w", err)
	}
	info, err := os.Stat(absPath)
	if err != nil {
		return "", fmt.Errorf("stat executable: %w", err)
	}
	if !info.Mode().IsRegular() {
		return "", fmt.Errorf("executable is not a regular file: %s", absPath)
	}
	if info.Mode().Perm()&0111 == 0 {
		return "", fmt.Errorf("file is not executable: %s", absPath)
	}
	return absPath, nil
}

// codexMCPServerOverride formats the per-invocation Codex configuration override
// for the local stdio MCP bridge. JSON string encoding is valid TOML basic-string
// syntax and safely preserves spaces, quotes, backslashes, Unicode, and dots.
func codexMCPServerOverride(binPath string, env map[string]string) (string, error) {
	encodedBin, err := json.Marshal(binPath)
	if err != nil {
		return "", fmt.Errorf("encode binary path: %w", err)
	}
	encodedArgs, err := json.Marshal([]string{"mcp-server"})
	if err != nil {
		return "", fmt.Errorf("encode mcp args: %w", err)
	}
	encodedTools, err := json.Marshal(defaultEnabledTools)
	if err != nil {
		return "", fmt.Errorf("encode enabled tools: %w", err)
	}

	var envPairs []string
	if len(env) > 0 {
		keys := make([]string, 0, len(env))
		for k := range env {
			keys = append(keys, k)
		}
		sort.Strings(keys)
		for _, k := range keys {
			encKey, err := json.Marshal(k)
			if err != nil {
				return "", fmt.Errorf("encode env key %q: %w", k, err)
			}
			encVal, err := json.Marshal(env[k])
			if err != nil {
				return "", fmt.Errorf("encode env val for %q: %w", k, err)
			}
			envPairs = append(envPairs, fmt.Sprintf("%s=%s", encKey, encVal))
		}
	}
	envInline := "{" + strings.Join(envPairs, ",") + "}"

	override := fmt.Sprintf(
		`mcp_servers.cagy={command=%s,args=%s,required=true,startup_timeout_sec=%d,tool_timeout_sec=%d,enabled_tools=%s,supports_parallel_tool_calls=false,default_tools_approval_mode="approve",env=%s}`,
		string(encodedBin),
		string(encodedArgs),
		mcpStartupTimeoutSec,
		mcpToolTimeoutSec,
		string(encodedTools),
		envInline,
	)
	return override, nil
}
