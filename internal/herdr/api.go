package herdr

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"net"
	"os"
	"strings"
	"time"
)

const cagySidebarViewSource = "cagy:sidebar"

// SetCagySidebarCompact hides panes marked as cagy developers from Herdr's
// Agent view. Other agents remain visible. Herdr supports one transient Agent
// view per server, so this intentionally becomes the active projection.
func (c *Client) SetCagySidebarCompact(ctx context.Context) error {
	params := map[string]any{
		"source": cagySidebarViewSource,
		"label":  "cagy",
		"filter": map[string]any{
			"op": "not",
			"filter": map[string]any{
				"op":    "eq",
				"field": map[string]string{"token": "cagy_role"},
				"value": "developer",
			},
		},
	}
	return c.callAPI(ctx, "agent.view.set", params)
}

// ClearCagySidebarView removes the compact projection only when cagy still
// owns it. A view installed by another Herdr tool is left unchanged.
func (c *Client) ClearCagySidebarView(ctx context.Context) error {
	return c.callAPI(ctx, "agent.view.clear", map[string]any{
		"source": cagySidebarViewSource,
	})
}

func (c *Client) callAPI(ctx context.Context, method string, params any) error {
	if c.apiCall != nil {
		return c.apiCall(ctx, method, params)
	}
	path, err := c.apiSocketPath(ctx)
	if err != nil {
		return err
	}

	dialer := net.Dialer{Timeout: 5 * time.Second}
	connection, err := dialer.DialContext(ctx, "unix", path)
	if err != nil {
		return fmt.Errorf("connect to Herdr API: %w", err)
	}
	defer connection.Close()

	deadline := time.Now().Add(5 * time.Second)
	if contextDeadline, ok := ctx.Deadline(); ok && contextDeadline.Before(deadline) {
		deadline = contextDeadline
	}
	if err := connection.SetDeadline(deadline); err != nil {
		return fmt.Errorf("set Herdr API deadline: %w", err)
	}

	request := map[string]any{
		"id":     "cagy:sidebar",
		"method": method,
		"params": params,
	}
	payload, err := json.Marshal(request)
	if err != nil {
		return fmt.Errorf("encode Herdr API request: %w", err)
	}
	payload = append(payload, '\n')
	writer := bufio.NewWriter(connection)
	if _, err := writer.Write(payload); err != nil {
		return fmt.Errorf("write Herdr API request: %w", err)
	}
	if err := writer.Flush(); err != nil {
		return fmt.Errorf("flush Herdr API request: %w", err)
	}

	line, err := bufio.NewReader(connection).ReadBytes('\n')
	if err != nil {
		return fmt.Errorf("read Herdr API response: %w", err)
	}
	var response envelope
	if err := json.Unmarshal(line, &response); err != nil {
		return fmt.Errorf("invalid Herdr API response: %w", err)
	}
	if response.Error != nil {
		return response.Error
	}
	if len(response.Result) == 0 {
		return fmt.Errorf("Herdr API response has no result")
	}
	return nil
}

func (c *Client) apiSocketPath(ctx context.Context) (string, error) {
	if path := strings.TrimSpace(os.Getenv("HERDR_SOCKET_PATH")); path != "" {
		return path, nil
	}
	result, err := c.runner.Run(ctx, "herdr", "status", "server")
	if err != nil {
		return "", fmt.Errorf("read Herdr server status: %w", err)
	}
	if result.ExitCode != 0 {
		return "", fmt.Errorf("read Herdr server status: exit status %d", result.ExitCode)
	}
	for _, line := range strings.Split(result.Stdout+"\n"+result.Stderr, "\n") {
		if value, found := strings.CutPrefix(strings.TrimSpace(line), "socket:"); found {
			if path := strings.TrimSpace(value); path != "" {
				return path, nil
			}
		}
	}
	return "", fmt.Errorf("Herdr server status did not report its API socket")
}
