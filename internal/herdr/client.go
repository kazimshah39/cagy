package herdr

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"

	proc "github.com/kazimshah39/cagy/internal/process"
)

// APIError is a structured error returned by Herdr.
type APIError struct {
	Code    string `json:"code"`
	Message string `json:"message"`
}

func (e *APIError) Error() string {
	if e.Code == "" {
		return e.Message
	}
	return fmt.Sprintf("%s: %s", e.Code, e.Message)
}

// AgentSessionInfo identifies the active agent conversation reported by a Herdr integration.
type AgentSessionInfo struct {
	Source string `json:"source"`
	Agent  string `json:"agent"`
	Kind   string `json:"kind"`
	Value  string `json:"value"`
}

// PaneInfo contains only the Herdr pane fields cagy needs.
type PaneInfo struct {
	PaneID        string            `json:"pane_id"`
	WorkspaceID   string            `json:"workspace_id"`
	TabID         string            `json:"tab_id"`
	CWD           string            `json:"cwd"`
	ForegroundCWD string            `json:"foreground_cwd"`
	Label         string            `json:"label"`
	Agent         string            `json:"agent"`
	AgentStatus   string            `json:"agent_status"`
	Tokens        map[string]string `json:"tokens"`
	AgentSession  *AgentSessionInfo `json:"agent_session"`
}

// AgentInfo contains only the Herdr agent fields cagy needs.
type PaneProcess struct {
	Name string   `json:"name"`
	Argv []string `json:"argv"`
}

type PaneProcessInfo struct {
	PaneID              string        `json:"pane_id"`
	ForegroundProcesses []PaneProcess `json:"foreground_processes"`
}

type AgentInfo struct {
	Name          string            `json:"name"`
	PaneID        string            `json:"pane_id"`
	WorkspaceID   string            `json:"workspace_id"`
	TabID         string            `json:"tab_id"`
	CWD           string            `json:"cwd"`
	ForegroundCWD string            `json:"foreground_cwd"`
	Agent         string            `json:"agent"`
	AgentStatus   string            `json:"agent_status"`
	AgentSession  *AgentSessionInfo `json:"agent_session"`
}

type envelope struct {
	Result json.RawMessage `json:"result"`
	Error  *APIError       `json:"error"`
}

type paneResult struct {
	Pane PaneInfo `json:"pane"`
}

type paneListResult struct {
	Panes []PaneInfo `json:"panes"`
}

type paneProcessResult struct {
	ProcessInfo PaneProcessInfo `json:"process_info"`
}

type agentResult struct {
	Agent AgentInfo `json:"agent"`
}

// Client is a small, argv-safe wrapper around the installed Herdr CLI.
type Client struct {
	runner  proc.Runner
	apiCall func(context.Context, string, any) error
}

func New(runner proc.Runner) *Client {
	return &Client{runner: runner}
}

func (c *Client) CurrentPane(ctx context.Context) (PaneInfo, error) {
	var result paneResult
	if err := c.json(ctx, &result, "pane", "current", "--current"); err != nil {
		return PaneInfo{}, err
	}
	return result.Pane, nil
}

func (c *Client) GetPane(ctx context.Context, paneID string) (PaneInfo, error) {
	var result paneResult
	if err := c.json(ctx, &result, "pane", "get", paneID); err != nil {
		return PaneInfo{}, err
	}
	return result.Pane, nil
}

func (c *Client) ListPanes(ctx context.Context, workspaceID string) ([]PaneInfo, error) {
	var result paneListResult
	args := []string{"pane", "list"}
	if workspaceID != "" {
		args = append(args, "--workspace", workspaceID)
	}
	if err := c.json(ctx, &result, args...); err != nil {
		return nil, err
	}
	return result.Panes, nil
}

func (c *Client) PaneProcessInfo(ctx context.Context, paneID string) (PaneProcessInfo, error) {
	var result paneProcessResult
	if err := c.json(ctx, &result, "pane", "process-info", "--pane", paneID); err != nil {
		return PaneProcessInfo{}, err
	}
	return result.ProcessInfo, nil
}

func (c *Client) SplitRight(ctx context.Context, cwd string) (PaneInfo, error) {
	var result paneResult
	if err := c.json(ctx, &result, "pane", "split", "--current", "--direction", "right", "--cwd", cwd, "--no-focus"); err != nil {
		return PaneInfo{}, err
	}
	return result.Pane, nil
}

func (c *Client) RenamePane(ctx context.Context, paneID, label string) error {
	return c.json(ctx, nil, "pane", "rename", paneID, label)
}

// ReportAgentDisplay sets a presentation-only sidebar label for one detected
// agent. The agent guard prevents the label from leaking to a different agent
// that later runs in the same pane.
func (c *Client) ReportAgentDisplay(ctx context.Context, paneID, source, agent, display string) error {
	return c.json(ctx, nil,
		"pane", "report-metadata", paneID,
		"--source", source,
		"--agent", agent,
		"--display-agent", display,
	)
}

// ReportPaneOwnership records stable cagy ownership independently of the agent
// process. These tokens remain available while agy is stopped, which lets cagy
// repair a missing managed agent without claiming unrelated panes.
func (c *Client) ReportPaneOwnership(ctx context.Context, paneID, source, owner, role string) error {
	return c.json(ctx, nil,
		"pane", "report-metadata", paneID,
		"--source", source,
		"--token", "cagy_owner="+owner,
		"--token", "cagy_role="+role,
	)
}

// ReportPaneSession persists the exact agy conversation identity independently
// of the running process. It lets a later repair resume this session instead
// of guessing which conversation is most recent.
func (c *Client) ReportPaneSession(ctx context.Context, paneID, source, sessionID string) error {
	return c.json(ctx, nil,
		"pane", "report-metadata", paneID,
		"--source", source,
		"--token", "cagy_session="+sessionID,
		"--token", "cagy_session_state=ready",
	)
}

// ReportPaneSessionPending marks a cagy-started fresh agy process whose first
// prompt has not run yet, so Herdr cannot have reported a conversation ID.
func (c *Client) ReportPaneSessionPending(ctx context.Context, paneID, source string) error {
	return c.json(ctx, nil,
		"pane", "report-metadata", paneID,
		"--source", source,
		"--token", "cagy_session_state=pending",
	)
}

func (c *Client) ClosePane(ctx context.Context, paneID string) error {
	return c.json(ctx, nil, "pane", "close", paneID)
}

func (c *Client) StartAgy(ctx context.Context, name, paneID string) (AgentInfo, error) {
	args := []string{"agent", "start", name, "--kind", "agy", "--pane", paneID, "--timeout", "60000", "--"}
	return c.startAgyWithArgs(ctx, args)
}

// StartAgyWithSession resumes one exact agy conversation. Fresh sessions must
// use StartAgy so a missing identity can never silently become a new session.
func (c *Client) StartAgyWithSession(ctx context.Context, name, paneID, sessionID string) (AgentInfo, error) {
	sessionID = strings.TrimSpace(sessionID)
	if sessionID == "" {
		return AgentInfo{}, fmt.Errorf("agy conversation ID is required")
	}
	args := []string{"agent", "start", name, "--kind", "agy", "--pane", paneID, "--timeout", "60000", "--", "--conversation", sessionID}
	return c.startAgyWithArgs(ctx, args)
}

func (c *Client) startAgyWithArgs(ctx context.Context, args []string) (AgentInfo, error) {
	args = append(args, "--dangerously-skip-permissions", "--mode", "accept-edits")
	var result agentResult
	if err := c.json(ctx, &result, args...); err != nil {
		return AgentInfo{}, err
	}
	return result.Agent, nil
}

func (c *Client) GetAgent(ctx context.Context, target string) (AgentInfo, error) {
	var result agentResult
	if err := c.json(ctx, &result, "agent", "get", target); err != nil {
		return AgentInfo{}, err
	}
	return result.Agent, nil
}

func (c *Client) Prompt(ctx context.Context, target, text string, timeoutMS int) (AgentInfo, error) {
	var result agentResult
	if err := c.json(ctx, &result, "agent", "prompt", target, text, "--wait", "--timeout", fmt.Sprint(timeoutMS)); err != nil {
		return AgentInfo{}, err
	}
	return result.Agent, nil
}

func (c *Client) WaitAgent(ctx context.Context, target string, timeoutMS int) (AgentInfo, error) {
	var result agentResult
	if err := c.json(ctx, &result, "agent", "wait", target, "--timeout", fmt.Sprint(timeoutMS)); err != nil {
		return AgentInfo{}, err
	}
	return result.Agent, nil
}

// WaitAgentBlocked waits only for a real blocked state. Idle and done are not
// accepted because agy's screen detector can report them transiently mid-turn.
func (c *Client) WaitAgentBlocked(ctx context.Context, target string, timeoutMS int) (AgentInfo, error) {
	var result agentResult
	if err := c.json(ctx, &result, "agent", "wait", target, "--until", "blocked", "--timeout", fmt.Sprint(timeoutMS)); err != nil {
		return AgentInfo{}, err
	}
	return result.Agent, nil
}

func (c *Client) ReadAgent(ctx context.Context, target string, lines int) (string, error) {
	return c.text(ctx, "agent", "read", target, "--source", "recent-unwrapped", "--lines", fmt.Sprint(lines))
}

// ReadAgentVisible returns only the current rendered screen. It is used for
// lifecycle markers, never as the completed-answer transport.
func (c *Client) ReadAgentVisible(ctx context.Context, target string, lines int) (string, error) {
	return c.text(ctx, "agent", "read", target, "--source", "visible", "--lines", fmt.Sprint(lines))
}

func (c *Client) SendAgentKeys(ctx context.Context, target string, keys ...string) error {
	args := []string{"agent", "send-keys", target}
	args = append(args, keys...)
	return c.json(ctx, nil, args...)
}

func (c *Client) RunInPane(ctx context.Context, paneID, command string) error {
	return c.json(ctx, nil, "pane", "run", paneID, command)
}

func (c *Client) SendPaneText(ctx context.Context, paneID, text string) error {
	return c.json(ctx, nil, "pane", "send-text", paneID, text)
}

func (c *Client) SendPaneKeys(ctx context.Context, paneID string, keys ...string) error {
	args := []string{"pane", "send-keys", paneID}
	args = append(args, keys...)
	return c.json(ctx, nil, args...)
}

func (c *Client) WaitPaneMatch(ctx context.Context, paneID, match string, timeoutMS int) error {
	return c.json(ctx, nil, "pane", "wait-output", paneID, "--match", match, "--source", "recent-unwrapped", "--lines", "400", "--timeout", fmt.Sprint(timeoutMS))
}

func (c *Client) WaitPaneRegex(ctx context.Context, paneID, pattern string, timeoutMS int) error {
	return c.json(ctx, nil, "pane", "wait-output", paneID, "--regex", pattern, "--source", "recent-unwrapped", "--lines", "400", "--timeout", fmt.Sprint(timeoutMS))
}

func (c *Client) ReadPane(ctx context.Context, paneID string, lines int) (string, error) {
	return c.text(ctx, "pane", "read", paneID, "--source", "recent-unwrapped", "--lines", fmt.Sprint(lines))
}

func IsCode(err error, code string) bool {
	var apiErr *APIError
	return errors.As(err, &apiErr) && apiErr.Code == code
}

func (c *Client) json(ctx context.Context, target any, args ...string) error {
	command := append([]string{"herdr"}, args...)
	result, err := c.runner.Run(ctx, command...)
	if err != nil {
		return fmt.Errorf("run herdr: %w", err)
	}

	payload := strings.TrimSpace(result.Stdout)
	if payload == "" {
		payload = strings.TrimSpace(result.Stderr)
	}
	if payload == "" {
		if result.ExitCode != 0 {
			return fmt.Errorf("herdr exited with status %d", result.ExitCode)
		}
		// Some successful mutating commands can lose their JSON acknowledgment
		// when the target immediately redraws or exits. The exit status is enough
		// only when the caller does not need a response body.
		if target == nil {
			return nil
		}
		return errors.New("herdr returned an empty response")
	}

	var response envelope
	if err := json.Unmarshal([]byte(payload), &response); err != nil {
		return fmt.Errorf("invalid herdr response: %w", err)
	}
	if response.Error != nil {
		return response.Error
	}
	if result.ExitCode != 0 {
		return fmt.Errorf("herdr exited with status %d", result.ExitCode)
	}
	if target == nil {
		return nil
	}
	if len(response.Result) == 0 {
		return errors.New("herdr response has no result")
	}
	if err := json.Unmarshal(response.Result, target); err != nil {
		return fmt.Errorf("invalid herdr result: %w", err)
	}
	return nil
}

func (c *Client) text(ctx context.Context, args ...string) (string, error) {
	command := append([]string{"herdr"}, args...)
	result, err := c.runner.Run(ctx, command...)
	if err != nil {
		return "", fmt.Errorf("run herdr: %w", err)
	}
	if result.ExitCode == 0 {
		return result.Stdout, nil
	}

	payload := strings.TrimSpace(result.Stderr)
	var response envelope
	if json.Unmarshal([]byte(payload), &response) == nil && response.Error != nil {
		return "", response.Error
	}
	return "", fmt.Errorf("herdr exited with status %d: %s", result.ExitCode, payload)
}
