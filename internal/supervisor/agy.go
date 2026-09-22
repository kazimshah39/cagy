package supervisor

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"

	proc "github.com/kazimshah39/herdr-tandem/internal/process"
)

type Agy struct{}

func (Agy) ID() string          { return AgyID }
func (Agy) DisplayName() string { return "agy" }
func (Agy) Executable() string  { return "agy" }

func (Agy) Validate(ctx context.Context, runner proc.Runner) error {
	if _, err := runner.LookPath("agy"); err != nil {
		return fmt.Errorf("agy is not on PATH: %w", err)
	}
	result, err := runner.Run(ctx, "agy", "--help")
	text := result.Stdout + result.Stderr
	if err != nil || result.ExitCode != 0 {
		return fmt.Errorf("installed agy cannot show its command help")
	}
	for _, required := range []string{"--agent", "--model", "--mode", "--dangerously-skip-permissions"} {
		if !strings.Contains(text, required) {
			return fmt.Errorf("installed agy does not support required option %s", required)
		}
	}
	return nil
}

func (a Agy) LaunchArtifact(input LaunchContext) (string, error) {
	runtimeID := strings.TrimSpace(input.RuntimeID)
	if !runtimeIDIsSafe(runtimeID) {
		return "", errors.New("runtime ID is invalid")
	}
	return "herdr-tandem-" + runtimeID, nil
}

func (a Agy) BuildLaunch(input LaunchContext) (LaunchSpec, error) {
	if strings.TrimSpace(input.Executable) == "" {
		return LaunchSpec{}, fmt.Errorf("Herdr Tandem executable path is empty")
	}
	if strings.TrimSpace(input.ProjectDir) == "" {
		return LaunchSpec{}, fmt.Errorf("project directory is empty")
	}
	artifact, err := a.LaunchArtifact(input)
	if err != nil {
		return LaunchSpec{}, err
	}
	args := []string{"agy", "--agent", artifact, "--dangerously-skip-permissions", "--mode", "accept-edits"}
	if model := strings.TrimSpace(input.Model); model != "" {
		args = append(args, "--model", model)
	}
	return LaunchSpec{Dir: input.ProjectDir, Args: args, Env: input.BaseEnv}, nil
}

func (Agy) PrepareLaunch(ctx context.Context, runner proc.Runner, input LaunchContext, artifact string) error {
	dir, file, err := resolveAgentPaths(input, artifact)
	if err != nil {
		return err
	}
	if strings.TrimSpace(input.Executable) == "" {
		return fmt.Errorf("Herdr Tandem executable path is empty")
	}

	if _, err := os.Lstat(dir); err == nil {
		return fmt.Errorf("custom agent directory %q already exists", dir)
	} else if !errors.Is(err, os.ErrNotExist) {
		return err
	}

	parent := filepath.Dir(dir)
	if err := os.MkdirAll(parent, 0700); err != nil {
		return fmt.Errorf("create parent directory: %w", err)
	}

	if err := os.Mkdir(dir, 0700); err != nil {
		return fmt.Errorf("create custom agent directory: %w", err)
	}

	rollback := true
	defer func() {
		if rollback {
			_ = os.Remove(file)
			_ = os.Remove(dir)
		}
	}()

	content := formatAgyAgentMD(artifact, input.Executable, input.MCPEnv, input.Instructions)
	f, err := os.OpenFile(file, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0600)
	if err != nil {
		return fmt.Errorf("create agent.md: %w", err)
	}
	if _, err := f.WriteString(content); err != nil {
		_ = f.Close()
		return fmt.Errorf("write agent.md: %w", err)
	}
	if err := f.Close(); err != nil {
		return fmt.Errorf("close agent.md: %w", err)
	}

	rollback = false
	return nil
}

func (Agy) CleanupLaunch(ctx context.Context, runner proc.Runner, input LaunchContext, artifact string) error {
	dir, file, err := resolveAgentPaths(input, artifact)
	if err != nil {
		return err
	}

	dirInfo, err := os.Lstat(dir)
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	if err != nil {
		return err
	}
	if dirInfo.Mode()&os.ModeSymlink != 0 {
		return fmt.Errorf("custom agent directory %q is a symlink", dir)
	}
	if !dirInfo.IsDir() {
		return fmt.Errorf("custom agent path %q is not a directory", dir)
	}
	if dirInfo.Mode().Perm() != 0700 {
		return fmt.Errorf("custom agent directory %q permissions %o mismatch expected 0700", dir, dirInfo.Mode().Perm())
	}

	entries, err := os.ReadDir(dir)
	if err != nil {
		return err
	}
	for _, entry := range entries {
		if entry.Name() != "agent.md" {
			return fmt.Errorf("unexpected file %q in custom agent directory", entry.Name())
		}
	}

	fileInfo, err := os.Lstat(file)
	if err == nil {
		if fileInfo.Mode()&os.ModeSymlink != 0 {
			return fmt.Errorf("custom agent file %q is a symlink", file)
		}
		if !fileInfo.Mode().IsRegular() {
			return fmt.Errorf("custom agent file %q is not a regular file", file)
		}
		if fileInfo.Mode().Perm() != 0600 {
			return fmt.Errorf("custom agent file %q permissions %o mismatch expected 0600", file, fileInfo.Mode().Perm())
		}
		data, err := os.ReadFile(file)
		if err != nil {
			return err
		}
		if err := ValidateAgyArtifactOwnership(data, artifact); err != nil {
			return fmt.Errorf("%w in %q", err, file)
		}
		if err := os.Remove(file); err != nil {
			return fmt.Errorf("remove agent.md: %w", err)
		}
	} else if !errors.Is(err, os.ErrNotExist) {
		return err
	}

	if err := os.Remove(dir); err != nil && !errors.Is(err, os.ErrNotExist) {
		return fmt.Errorf("remove custom agent directory: %w", err)
	}
	return nil
}

func resolveConfigRoot(input LaunchContext) (string, error) {
	if root := strings.TrimSpace(input.ConfigRoot); root != "" {
		return filepath.Clean(root), nil
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return "", fmt.Errorf("read user home directory: %w", err)
	}
	return filepath.Join(home, ".gemini", "config"), nil
}

func resolveAgentPaths(input LaunchContext, artifact string) (string, string, error) {
	runtimeID := strings.TrimSpace(input.RuntimeID)
	if !runtimeIDIsSafe(runtimeID) {
		return "", "", errors.New("runtime ID is invalid")
	}
	expected := "herdr-tandem-" + runtimeID
	if artifact != expected {
		return "", "", fmt.Errorf("artifact %q does not match expected %q", artifact, expected)
	}
	root, err := resolveConfigRoot(input)
	if err != nil {
		return "", "", err
	}
	dir := filepath.Join(root, "agents", artifact)
	file := filepath.Join(dir, "agent.md")
	return dir, file, nil
}

func formatAgyAgentMD(artifact, executable string, mcpEnv map[string]string, instructions string) string {
	var sb strings.Builder
	sb.WriteString("---\n")
	sb.WriteString("name: " + artifact + "\n")
	sb.WriteString("description: Herdr Tandem agy supervisor session\n")
	sb.WriteString("tools:\n")
	sb.WriteString("  - view_file\n")
	sb.WriteString("  - grep_search\n")
	sb.WriteString("  - run_command\n")
	sb.WriteString("  - search_web\n")
	sb.WriteString("mainAgent: true\n")
	sb.WriteString("subagent: false\n")
	sb.WriteString("model: inherit\n")
	sb.WriteString("commandExecutionPolicy: eager\n")
	sb.WriteString("mcpServers:\n")
	sb.WriteString("  - name: herdr_tandem\n")
	sb.WriteString("    command: " + yamlQuote(executable) + "\n")
	sb.WriteString("    args:\n")
	sb.WriteString("      - mcp-server\n")
	if len(mcpEnv) > 0 {
		sb.WriteString("    env:\n")
		keys := make([]string, 0, len(mcpEnv))
		for k := range mcpEnv {
			keys = append(keys, k)
		}
		sort.Strings(keys)
		for _, k := range keys {
			sb.WriteString("      " + k + ": " + yamlQuote(mcpEnv[k]) + "\n")
		}
	}
	sb.WriteString("---\n")
	sb.WriteString("# System Prompt\n\n")
	sb.WriteString(instructions)
	if !strings.HasSuffix(instructions, "\n") {
		sb.WriteString("\n")
	}
	return sb.String()
}

func yamlQuote(s string) string {
	b, _ := json.Marshal(s)
	return string(b)
}

func runtimeIDIsSafe(id string) bool {
	if id == "" || len(id) > 128 {
		return false
	}
	for _, r := range id {
		if !(r >= 'a' && r <= 'z' || r >= 'A' && r <= 'Z' || r >= '0' && r <= '9' || r == '-' || r == '_') {
			return false
		}
	}
	return true
}

// ExtractAgyFrontmatterName parses frontmatter from agent.md content and returns
// the exact value of the single top-level name field, or ("", false) if missing,
// malformed, duplicated, nested, or unterminated.
func ExtractAgyFrontmatterName(content string) (string, bool) {
	content = strings.TrimPrefix(content, "\ufeff")
	content = strings.ReplaceAll(content, "\r\n", "\n")
	lines := strings.Split(content, "\n")
	if len(lines) < 2 || strings.TrimRight(lines[0], " \t") != "---" {
		return "", false
	}

	var extractedName string
	topLevelNameCount := 0
	closed := false

	for i := 1; i < len(lines); i++ {
		line := lines[i]
		if strings.TrimRight(line, " \t") == "---" {
			closed = true
			break
		}
		// Only top-level fields start at column 0 with "name:"
		if strings.HasPrefix(line, "name:") {
			topLevelNameCount++
			if topLevelNameCount > 1 {
				return "", false
			}
			rawVal := strings.TrimPrefix(line, "name:")
			if !strings.HasPrefix(rawVal, " ") && !strings.HasPrefix(rawVal, "\t") && rawVal != "" {
				// YAML requires a space or tab after colon
				return "", false
			}
			val := strings.TrimSpace(rawVal)
			if val == "" {
				return "", false
			}

			if strings.HasPrefix(val, `"`) {
				// Double-quoted string
				end := strings.Index(val[1:], `"`)
				if end == -1 {
					return "", false // Unmatched quote
				}
				closingIdx := 1 + end
				remainder := strings.TrimSpace(val[closingIdx+1:])
				if remainder != "" && !strings.HasPrefix(remainder, "#") {
					return "", false // Stray content after closing quote
				}
				extractedName = val[1:closingIdx]
			} else if strings.HasPrefix(val, `'`) {
				// Single-quoted string
				end := strings.Index(val[1:], `'`)
				if end == -1 {
					return "", false // Unmatched quote
				}
				closingIdx := 1 + end
				remainder := strings.TrimSpace(val[closingIdx+1:])
				if remainder != "" && !strings.HasPrefix(remainder, "#") {
					return "", false // Stray content after closing quote
				}
				extractedName = val[1:closingIdx]
			} else {
				// Unquoted string: must not contain dangling quote characters
				if strings.Contains(val, `"`) || strings.Contains(val, `'`) {
					return "", false
				}
				if idx := strings.Index(val, "#"); idx != -1 {
					val = strings.TrimSpace(val[:idx])
				}
				if val == "" {
					return "", false
				}
				extractedName = val
			}
		}
	}

	if !closed || topLevelNameCount != 1 || extractedName == "" {
		return "", false
	}
	return extractedName, true
}

// ValidateAgyArtifactOwnership checks that the file data contains valid frontmatter
// whose exact name field matches expectedArtifact.
func ValidateAgyArtifactOwnership(data []byte, expectedArtifact string) error {
	expectedArtifact = strings.TrimSpace(expectedArtifact)
	if expectedArtifact == "" {
		return errors.New("expected artifact name is empty")
	}
	name, ok := ExtractAgyFrontmatterName(string(data))
	if !ok || name != expectedArtifact {
		return fmt.Errorf("ownership mismatch: expected name %q in frontmatter", expectedArtifact)
	}
	return nil
}
