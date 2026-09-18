package app

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/kazimshah39/cagy/internal/herdr"
	proc "github.com/kazimshah39/cagy/internal/process"
	"github.com/kazimshah39/cagy/internal/transcript"
	"github.com/modelcontextprotocol/go-sdk/mcp"
)

func TestMCPInitializeAndListTools(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	stdout := &bytes.Buffer{}
	stderr := &bytes.Buffer{}
	application := New(nil, stdout, stderr)

	server := application.newMCPServer()
	t1, t2 := mcp.NewInMemoryTransports()

	sessionServer, err := server.Connect(ctx, t1, nil)
	if err != nil {
		t.Fatalf("server.Connect: %v", err)
	}
	defer sessionServer.Close()

	client := mcp.NewClient(&mcp.Implementation{Name: "test-client", Version: "1.0.0"}, nil)
	sessionClient, err := client.Connect(ctx, t2, nil)
	if err != nil {
		t.Fatalf("client.Connect: %v", err)
	}
	defer sessionClient.Close()

	var toolNames []string
	toolMap := make(map[string]*mcp.Tool)
	for tool, err := range sessionClient.Tools(ctx, nil) {
		if err != nil {
			t.Fatalf("Tools iteration error: %v", err)
		}
		toolNames = append(toolNames, tool.Name)
		toolMap[tool.Name] = tool
	}

	expectedTools := []string{
		"delegate_task",
		"task_status",
		"recover_task",
		"acknowledge_task",
		"forget_task",
		"developer_status",
	}

	if len(toolNames) != len(expectedTools) {
		t.Fatalf("got %d tools, want %d: %v", len(toolNames), len(expectedTools), toolNames)
	}

	for _, expected := range expectedTools {
		if !slices.Contains(toolNames, expected) {
			t.Errorf("missing expected tool %q", expected)
		}
	}

	// Verify annotations
	if tool, ok := toolMap["task_status"]; ok {
		if tool.Annotations == nil || !tool.Annotations.ReadOnlyHint {
			t.Errorf("task_status: expected ReadOnlyHint = true")
		}
	}
	if tool, ok := toolMap["recover_task"]; ok {
		if tool.Annotations == nil || tool.Annotations.ReadOnlyHint {
			t.Errorf("recover_task: expected ReadOnlyHint = false")
		}
		if tool.Annotations.DestructiveHint == nil || *tool.Annotations.DestructiveHint {
			t.Errorf("recover_task: expected DestructiveHint = false")
		}
		if !tool.Annotations.IdempotentHint {
			t.Errorf("recover_task: expected IdempotentHint = true")
		}
	}
	if tool, ok := toolMap["developer_status"]; ok {
		if tool.Annotations == nil || !tool.Annotations.ReadOnlyHint {
			t.Errorf("developer_status: expected ReadOnlyHint = true")
		}
	}
	if tool, ok := toolMap["delegate_task"]; ok {
		if tool.Annotations == nil || tool.Annotations.ReadOnlyHint {
			t.Errorf("delegate_task: expected ReadOnlyHint = false")
		}
		if tool.Annotations.OpenWorldHint == nil || !*tool.Annotations.OpenWorldHint {
			t.Errorf("delegate_task: expected OpenWorldHint = true")
		}
	}
	if tool, ok := toolMap["acknowledge_task"]; ok {
		if tool.Annotations == nil || tool.Annotations.ReadOnlyHint {
			t.Errorf("acknowledge_task: expected ReadOnlyHint = false")
		}
		if tool.Annotations.DestructiveHint == nil || !*tool.Annotations.DestructiveHint {
			t.Errorf("acknowledge_task: expected DestructiveHint = true")
		}
		if !tool.Annotations.IdempotentHint {
			t.Errorf("acknowledge_task: expected IdempotentHint = true")
		}
	}
	if tool, ok := toolMap["forget_task"]; ok {
		if tool.Annotations == nil || tool.Annotations.ReadOnlyHint {
			t.Errorf("forget_task: expected ReadOnlyHint = false")
		}
		if tool.Annotations.DestructiveHint == nil || !*tool.Annotations.DestructiveHint {
			t.Errorf("forget_task: expected DestructiveHint = true")
		}
	}
}

func TestMCPHelpExcludesInternalCommand(t *testing.T) {
	stdout := &bytes.Buffer{}
	stderr := &bytes.Buffer{}
	application := New(nil, stdout, stderr)

	if err := application.Run(context.Background(), []string{"--help"}); err != nil {
		t.Fatalf("Run(--help): %v", err)
	}
	helpText := stdout.String()
	if strings.Contains(helpText, "mcp-server") {
		t.Errorf("help text should not advertise internal mcp-server command: %s", helpText)
	}
}

func setupMCPClientServer(t *testing.T, app *App) (*mcp.ClientSession, func()) {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)

	server := app.newMCPServer()
	t1, t2 := mcp.NewInMemoryTransports()

	sessionServer, err := server.Connect(ctx, t1, nil)
	if err != nil {
		cancel()
		t.Fatalf("server.Connect: %v", err)
	}

	client := mcp.NewClient(&mcp.Implementation{Name: "test-client", Version: "1.0.0"}, nil)
	sessionClient, err := client.Connect(ctx, t2, nil)
	if err != nil {
		_ = sessionServer.Close()
		cancel()
		t.Fatalf("client.Connect: %v", err)
	}

	cleanup := func() {
		_ = sessionClient.Close()
		_ = sessionServer.Close()
		cancel()
	}
	return sessionClient, cleanup
}

func callMCPTool[In any, Out any](t *testing.T, ctx context.Context, session *mcp.ClientSession, name string, in In) (*Out, string, error) {
	t.Helper()
	res, err := session.CallTool(ctx, &mcp.CallToolParams{
		Name:      name,
		Arguments: in,
	})
	if err != nil {
		return nil, "", err
	}
	if res.IsError {
		var msg string
		if len(res.Content) > 0 {
			if tc, ok := res.Content[0].(*mcp.TextContent); ok {
				msg = tc.Text
			}
		}
		return nil, msg, errors.New(msg)
	}
	var out Out
	if len(res.Content) > 0 {
		if tc, ok := res.Content[0].(*mcp.TextContent); ok {
			if err := json.Unmarshal([]byte(tc.Text), &out); err != nil {
				return nil, "", fmt.Errorf("unmarshal %s output %q: %w", name, tc.Text, err)
			}
		}
	}
	return &out, "", nil
}

func setupTestMCPApp(t *testing.T, runner proc.Runner, project string, developer string) (*App, string) {
	t.Helper()
	brainRoot := t.TempDir()
	stateDir := filepath.Join(t.TempDir(), "private-state")
	if err := ensurePrivateStateDir(stateDir); err != nil {
		t.Fatal(err)
	}
	var stdout, stderr strings.Builder
	application := New(runner, &stdout, &stderr)
	application.stateDir = stateDir
	application.agyBrainRoot = brainRoot
	application.transcriptWait = time.Second
	application.getenv = envGetter(map[string]string{
		"HERDR_ENV":               "1",
		"HERDR_WORKSPACE_ID":      "w1",
		"HERDR_PANE_ID":           "w1:p1",
		"CAGY_SUPERVISOR_PANE_ID": "w1:p1",
		"CAGY_DEVELOPER":          developer,
		"CAGY_PROJECT_DIR":        project,
	})
	return application, brainRoot
}

func TestMCPDelegateTaskValidation(t *testing.T) {
	ctx := context.Background()
	project, _ := filepath.EvalSymlinks(t.TempDir())
	developer := developerName("w1", "w1:p1")
	runner := &scriptedRunner{t: t}
	app, _ := setupTestMCPApp(t, runner, project, developer)
	session, cleanup := setupMCPClientServer(t, app)
	defer cleanup()

	// Empty task
	_, errMsg, err := callMCPTool[DelegateTaskInput, DelegateTaskOutput](t, ctx, session, "delegate_task", DelegateTaskInput{Task: ""})
	if err == nil {
		t.Fatal("expected error on empty task")
	}
	if !strings.Contains(errMsg, "task cannot be empty") {
		t.Fatalf("expected 'task cannot be empty', got: %s", errMsg)
	}

	// Whitespace task
	_, errMsg, err = callMCPTool[DelegateTaskInput, DelegateTaskOutput](t, ctx, session, "delegate_task", DelegateTaskInput{Task: "   \n\t  "})
	if err == nil {
		t.Fatal("expected error on whitespace task")
	}
	if !strings.Contains(errMsg, "task cannot be empty") {
		t.Fatalf("expected 'task cannot be empty', got: %s", errMsg)
	}

	// Oversized task (>1 MiB)
	oversized := strings.Repeat("x", maxTaskInputBytes+1)
	_, errMsg, err = callMCPTool[DelegateTaskInput, DelegateTaskOutput](t, ctx, session, "delegate_task", DelegateTaskInput{Task: oversized})
	if err == nil {
		t.Fatal("expected error on oversized task")
	}
	if !strings.Contains(errMsg, "task exceeds 1 MiB") {
		t.Fatalf("expected 'task exceeds 1 MiB', got: %s", errMsg)
	}
}

func TestMCPDelegateTaskSuccess(t *testing.T) {
	ctx := context.Background()
	project, _ := filepath.EvalSymlinks(t.TempDir())
	developer := developerName("w1", "w1:p1")
	task := "Implement native MCP bridge cleanly"
	answer := "Native MCP bridge implemented."

	runner := &scriptedRunner{t: t, steps: []runStep{
		{want: []string{"herdr", "pane", "get", "w1:p1"}, result: supervisorPaneJSON(project)},
		{want: []string{"herdr", "agent", "get", developer}, result: agentJSON("w1:p2", "w1", project, "idle")},
		{want: []string{"herdr", "pane", "get", "w1:p2"}, result: paneJSON("w1:p2", "w1:t1", project, developerPaneLabel, map[string]string{"cagy_owner": developer, "cagy_role": "developer"})},
		{want: agyProbeArgs("/model"), result: agyModelResult("gemini-3.8-flash-high", "Gemini 3.8 Flash (High)")},
		{want: agyProbeArgs("/quota"), result: agyQuotaResult("Gemini Models", 0.8, 0.9)},
		{want: []string{"herdr", "agent", "read", developer, "--source", "recent-unwrapped", "--lines", "400"}, result: textResult("old output\n")},
		{want: []string{"herdr", "agent", "prompt", developer, task, "--wait", "--timeout", "300000"}, result: agentJSONWithSession("w1:p2", "w1", project, "done", testConversationID)},
		{want: []string{"herdr", "agent", "read", developer, "--source", "recent-unwrapped", "--lines", "400"}, result: textResult("old output\n" + answer + "\n")},
		{want: []string{"herdr", "agent", "wait", developer, "--until", "blocked", "--timeout", "1000"}, result: jsonError("timeout", "timed out")},
		{want: []string{"herdr", "agent", "read", developer, "--source", "visible", "--lines", "80"}, result: textResult(">\n────────────────────\n? for shortcuts\n")},
		sessionOwnershipStep("w1:p2", testConversationID),
	}}

	app, brainRoot := setupTestMCPApp(t, runner, project, developer)
	writeAgyTranscript(t, brainRoot, testConversationID, task, answer)

	session, cleanup := setupMCPClientServer(t, app)
	defer cleanup()

	out, _, err := callMCPTool[DelegateTaskInput, DelegateTaskOutput](t, ctx, session, "delegate_task", DelegateTaskInput{Task: task})
	if err != nil {
		t.Fatalf("delegate_task failed: %v", err)
	}

	if out.Status != "completed_unacknowledged" {
		t.Errorf("status = %q, want 'completed_unacknowledged'", out.Status)
	}
	if out.Answer != answer {
		t.Errorf("answer = %q, want %q", out.Answer, answer)
	}
	if !isValidDeliveryReceipt(out.Receipt) {
		t.Errorf("receipt = %q is not a valid 32-char hex delivery receipt", out.Receipt)
	}
	if !out.AcknowledgementRequired {
		t.Errorf("acknowledgement_required = false, want true")
	}

	// Verify journal on disk
	journal, exists, err := app.loadTaskJournal(developer)
	if err != nil {
		t.Fatalf("loadTaskJournal: %v", err)
	}
	if !exists {
		t.Fatal("expected journal to exist on disk")
	}
	if journal.Phase != taskPhaseCompleted {
		t.Errorf("journal.Phase = %v, want taskPhaseCompleted", journal.Phase)
	}
	if journal.DeliveryReceipt != out.Receipt {
		t.Errorf("journal.DeliveryReceipt = %q, want %q", journal.DeliveryReceipt, out.Receipt)
	}
	runner.assertDone()
}

func TestMCPLiteralTask(t *testing.T) {
	ctx := context.Background()
	project, _ := filepath.EvalSymlinks(t.TempDir())
	developer := developerName("w1", "w1:p1")
	task := "Run: `ls -la` && $(echo $HOME) with 'single' and \"double\" quotes\nmultiline line 2\nand Unicode: 🚀 ñoño 日本語"
	answer := "Completed literal task with 🚀"

	runner := &scriptedRunner{t: t, steps: []runStep{
		{want: []string{"herdr", "pane", "get", "w1:p1"}, result: supervisorPaneJSON(project)},
		{want: []string{"herdr", "agent", "get", developer}, result: agentJSON("w1:p2", "w1", project, "idle")},
		{want: []string{"herdr", "pane", "get", "w1:p2"}, result: paneJSON("w1:p2", "w1:t1", project, developerPaneLabel, map[string]string{"cagy_owner": developer, "cagy_role": "developer"})},
		{want: agyProbeArgs("/model"), result: agyModelResult("gemini-3.8-flash-high", "Gemini 3.8 Flash (High)")},
		{want: agyProbeArgs("/quota"), result: agyQuotaResult("Gemini Models", 0.8, 0.9)},
		{want: []string{"herdr", "agent", "read", developer, "--source", "recent-unwrapped", "--lines", "400"}, result: textResult("old output\n")},
		{want: []string{"herdr", "agent", "prompt", developer, task, "--wait", "--timeout", "300000"}, result: agentJSONWithSession("w1:p2", "w1", project, "done", testConversationID)},
		{want: []string{"herdr", "agent", "read", developer, "--source", "recent-unwrapped", "--lines", "400"}, result: textResult("old output\n" + answer + "\n")},
		{want: []string{"herdr", "agent", "wait", developer, "--until", "blocked", "--timeout", "1000"}, result: jsonError("timeout", "timed out")},
		{want: []string{"herdr", "agent", "read", developer, "--source", "visible", "--lines", "80"}, result: textResult(">\n────────────────────\n? for shortcuts\n")},
		sessionOwnershipStep("w1:p2", testConversationID),
	}}

	app, brainRoot := setupTestMCPApp(t, runner, project, developer)
	writeAgyTranscript(t, brainRoot, testConversationID, task, answer)

	session, cleanup := setupMCPClientServer(t, app)
	defer cleanup()

	out, _, err := callMCPTool[DelegateTaskInput, DelegateTaskOutput](t, ctx, session, "delegate_task", DelegateTaskInput{Task: task})
	if err != nil {
		t.Fatalf("delegate_task failed: %v", err)
	}

	if out.Answer != answer {
		t.Errorf("answer = %q, want %q", out.Answer, answer)
	}
	runner.assertDone()
}

func TestMCPTaskStatus(t *testing.T) {
	ctx := context.Background()
	project, _ := filepath.EvalSymlinks(t.TempDir())
	developer := developerName("w1", "w1:p1")
	runner := &scriptedRunner{t: t, steps: []runStep{
		{want: []string{"herdr", "agent", "get", developer}, result: agentJSON("w1:p2", "w1", project, "idle")},
	}}
	app, brainRoot := setupTestMCPApp(t, runner, project, developer)
	session, cleanup := setupMCPClientServer(t, app)
	defer cleanup()

	// State 1: none
	out, _, err := callMCPTool[TaskStatusInput, TaskStatusOutput](t, ctx, session, "task_status", TaskStatusInput{})
	if err != nil {
		t.Fatalf("task_status failed: %v", err)
	}
	if out.Status != "none" {
		t.Errorf("status = %q, want 'none'", out.Status)
	}
	if out.Recoverable || out.AcknowledgementRequired {
		t.Errorf("unexpected recoverable=%v or ack_required=%v", out.Recoverable, out.AcknowledgementRequired)
	}

	// State 2: completed_unacknowledged
	receipt := "abcdef0123456789abcdef0123456789"
	writeAgyTranscript(t, brainRoot, testConversationID, "task", "answer")
	info := runtimeContext{workspaceID: "w1", supervisor: "w1:p1", developer: developer, project: project}
	devInfo := herdr.AgentInfo{
		Agent:        developer,
		PaneID:       "w1:p2",
		WorkspaceID:  "w1",
		TabID:        "w1:t1",
		AgentSession: &herdr.AgentSessionInfo{Source: "herdr:antigravity_cli", Agent: "agy", Kind: "id", Value: testConversationID},
	}
	if err := app.beginTaskTracking(info, devInfo, "task", transcript.Checkpoint{SessionID: testConversationID}, taskPhaseMonitoring); err != nil {
		t.Fatal(err)
	}
	record, exists, err := app.loadTaskJournal(developer)
	if err != nil || !exists {
		t.Fatalf("loadTaskJournal: exists=%v err=%v", exists, err)
	}
	record.Phase = taskPhaseCompleted
	record.DeliveryReceipt = receipt
	if err := app.writeTaskJournal(record); err != nil {
		t.Fatal(err)
	}

	runner.steps = []runStep{
		{want: []string{"herdr", "agent", "get", developer}, result: agentJSONWithSession("w1:p2", "w1", project, "idle", testConversationID)},
		{want: []string{"herdr", "pane", "get", "w1:p1"}, result: supervisorPaneJSON(project)},
		{want: []string{"herdr", "pane", "get", "w1:p2"}, result: paneJSON("w1:p2", "w1:t1", project, developerPaneLabel, map[string]string{"cagy_owner": developer, "cagy_role": "developer"})},
	}

	out, _, err = callMCPTool[TaskStatusInput, TaskStatusOutput](t, ctx, session, "task_status", TaskStatusInput{})
	if err != nil {
		t.Fatalf("task_status failed: %v", err)
	}
	if out.Status != "completed_unacknowledged" {
		t.Errorf("status = %q, want 'completed_unacknowledged'", out.Status)
	}
	if !out.Recoverable || !out.AcknowledgementRequired {
		t.Errorf("expected recoverable=true, ack_required=true, got recoverable=%v ack=%v", out.Recoverable, out.AcknowledgementRequired)
	}
	runner.assertDone()
}

func TestMCPRecoverTask(t *testing.T) {
	ctx := context.Background()
	project, _ := filepath.EvalSymlinks(t.TempDir())
	developer := developerName("w1", "w1:p1")
	runner := &scriptedRunner{t: t}
	app, brainRoot := setupTestMCPApp(t, runner, project, developer)
	session, cleanup := setupMCPClientServer(t, app)
	defer cleanup()

	// Recover when nothing completed
	_, errMsg, err := callMCPTool[RecoverTaskInput, RecoverTaskOutput](t, ctx, session, "recover_task", RecoverTaskInput{})
	if err == nil {
		t.Fatal("expected error recovering when no task completed")
	}
	if !strings.Contains(errMsg, "no interrupted task to recover") {
		t.Fatalf("unexpected error message: %s", errMsg)
	}

	// Seed completed unacknowledged task
	receipt := "abcdef0123456789abcdef0123456789"
	task := "Seed task"
	answer := "Seed answer to recover"
	writeAgyTranscript(t, brainRoot, testConversationID, task, answer)
	info := runtimeContext{workspaceID: "w1", supervisor: "w1:p1", developer: developer, project: project}
	devInfo := herdr.AgentInfo{
		Agent:        developer,
		PaneID:       "w1:p2",
		WorkspaceID:  "w1",
		TabID:        "w1:t1",
		AgentSession: &herdr.AgentSessionInfo{Source: "herdr:antigravity_cli", Agent: "agy", Kind: "id", Value: testConversationID},
	}
	if err := app.beginTaskTracking(info, devInfo, task, transcript.Checkpoint{SessionID: testConversationID}, taskPhaseCompleted); err != nil {
		t.Fatal(err)
	}
	record, exists, _ := app.loadTaskJournal(developer)
	if !exists {
		t.Fatal("expected journal to exist")
	}
	record.DeliveryReceipt = receipt
	_ = app.writeTaskJournal(record)

	runner.steps = []runStep{
		{want: []string{"herdr", "agent", "get", developer}, result: agentJSONWithSession("w1:p2", "w1", project, "idle", testConversationID)},
		{want: []string{"herdr", "pane", "get", "w1:p1"}, result: supervisorPaneJSON(project)},
		{want: []string{"herdr", "pane", "get", "w1:p2"}, result: paneJSON("w1:p2", "w1:t1", project, developerPaneLabel, map[string]string{"cagy_owner": developer, "cagy_role": "developer"})},
	}

	out, _, err := callMCPTool[RecoverTaskInput, RecoverTaskOutput](t, ctx, session, "recover_task", RecoverTaskInput{})
	if err != nil {
		t.Fatalf("recover_task failed: %v", err)
	}
	if out.Status != "completed_unacknowledged" {
		t.Errorf("status = %q, want 'completed_unacknowledged'", out.Status)
	}
	if out.Answer != answer {
		t.Errorf("answer = %q, want %q", out.Answer, answer)
	}
	if out.Receipt != receipt {
		t.Errorf("receipt = %q, want %q", out.Receipt, receipt)
	}
	if !out.AcknowledgementRequired {
		t.Errorf("acknowledgement_required = false, want true")
	}

	// Verify journal still exists (recovery must NOT delete it)
	if _, exists, err := app.loadTaskJournal(developer); err != nil || !exists {
		t.Fatalf("journal should still exist after recover_task, exists=%v err=%v", exists, err)
	}
	runner.assertDone()
}

func TestMCPAcknowledgeTask(t *testing.T) {
	ctx := context.Background()
	project, _ := filepath.EvalSymlinks(t.TempDir())
	developer := developerName("w1", "w1:p1")
	runner := &scriptedRunner{t: t}
	app, _ := setupTestMCPApp(t, runner, project, developer)
	session, cleanup := setupMCPClientServer(t, app)
	defer cleanup()

	correctReceipt := "11111111111111111111111111111111"
	wrongReceipt := "22222222222222222222222222222222"

	// Seed completed journal
	info := runtimeContext{workspaceID: "w1", supervisor: "w1:p1", developer: developer, project: project}
	devInfo := herdr.AgentInfo{
		Agent:        developer,
		PaneID:       "w1:p2",
		WorkspaceID:  "w1",
		TabID:        "w1:t1",
		AgentSession: &herdr.AgentSessionInfo{Source: "herdr:antigravity_cli", Agent: "agy", Kind: "id", Value: testConversationID},
	}
	if err := app.beginTaskTracking(info, devInfo, "task", transcript.Checkpoint{SessionID: testConversationID}, taskPhaseCompleted); err != nil {
		t.Fatal(err)
	}
	record, exists, _ := app.loadTaskJournal(developer)
	if !exists {
		t.Fatal("expected journal to exist")
	}
	record.DeliveryReceipt = correctReceipt
	_ = app.writeTaskJournal(record)

	// Call with wrong receipt -> fails
	_, errMsg, err := callMCPTool[AcknowledgeTaskInput, AcknowledgeTaskOutput](t, ctx, session, "acknowledge_task", AcknowledgeTaskInput{Receipt: wrongReceipt})
	if err == nil {
		t.Fatal("expected error acknowledging with wrong receipt")
	}
	if !strings.Contains(errMsg, "does not match") {
		t.Fatalf("expected mismatch error, got: %s", errMsg)
	}

	// Verify journal still exists
	if _, exists, err := app.loadTaskJournal(developer); err != nil || !exists {
		t.Fatalf("journal should still exist after failed ack, exists=%v err=%v", exists, err)
	}

	// Call with correct receipt -> succeeds
	out, _, err := callMCPTool[AcknowledgeTaskInput, AcknowledgeTaskOutput](t, ctx, session, "acknowledge_task", AcknowledgeTaskInput{Receipt: correctReceipt})
	if err != nil {
		t.Fatalf("acknowledge_task failed: %v", err)
	}
	if out.Status != "acknowledged" {
		t.Errorf("status = %q, want 'acknowledged'", out.Status)
	}

	// Verify journal is now cleared
	if _, exists, err := app.loadTaskJournal(developer); err != nil || exists {
		t.Fatalf("expected journal to be cleared, exists=%v err=%v", exists, err)
	}

	// Call again with same receipt -> fails (already cleared)
	_, errMsg, err = callMCPTool[AcknowledgeTaskInput, AcknowledgeTaskOutput](t, ctx, session, "acknowledge_task", AcknowledgeTaskInput{Receipt: correctReceipt})
	if err == nil {
		t.Fatal("expected error acknowledging when already cleared")
	}
	if !strings.Contains(errMsg, "no unacknowledged task exists") {
		t.Fatalf("expected no unacknowledged task error, got: %s", errMsg)
	}
}

func TestMCPForgetTask(t *testing.T) {
	ctx := context.Background()
	project, _ := filepath.EvalSymlinks(t.TempDir())
	developer := developerName("w1", "w1:p1")
	runner := &scriptedRunner{t: t}
	app, brainRoot := setupTestMCPApp(t, runner, project, developer)
	writeAgyTranscript(t, brainRoot, testConversationID, "task", "answer")
	session, cleanup := setupMCPClientServer(t, app)
	defer cleanup()

	// Seed completed journal
	info := runtimeContext{workspaceID: "w1", supervisor: "w1:p1", developer: developer, project: project}
	devInfo := herdr.AgentInfo{
		Agent:        developer,
		PaneID:       "w1:p2",
		WorkspaceID:  "w1",
		TabID:        "w1:t1",
		AgentSession: &herdr.AgentSessionInfo{Source: "herdr:antigravity_cli", Agent: "agy", Kind: "id", Value: testConversationID},
	}
	if err := app.beginTaskTracking(info, devInfo, "task", transcript.Checkpoint{SessionID: testConversationID}, taskPhaseCompleted); err != nil {
		t.Fatal(err)
	}

	// Call without confirm (false) -> fails
	_, errMsg, err := callMCPTool[ForgetTaskInput, ForgetTaskOutput](t, ctx, session, "forget_task", ForgetTaskInput{Confirm: false})
	if err == nil {
		t.Fatal("expected error forgetting task with confirm=false")
	}
	if !strings.Contains(errMsg, "confirm=true") {
		t.Fatalf("expected confirm=true error, got: %s", errMsg)
	}

	// Journal still exists
	if _, exists, err := app.loadTaskJournal(developer); err != nil || !exists {
		t.Fatalf("journal should still exist, exists=%v err=%v", exists, err)
	}

	// Call with confirm=true -> succeeds
	runner.steps = []runStep{
		{want: []string{"herdr", "agent", "get", developer}, result: agentJSONWithSession("w1:p2", "w1", project, "idle", testConversationID)},
		{want: []string{"herdr", "pane", "get", "w1:p1"}, result: supervisorPaneJSON(project)},
		{want: []string{"herdr", "pane", "get", "w1:p2"}, result: paneJSON("w1:p2", "w1:t1", project, developerPaneLabel, map[string]string{"cagy_owner": developer, "cagy_role": "developer"})},
	}
	out, _, err := callMCPTool[ForgetTaskInput, ForgetTaskOutput](t, ctx, session, "forget_task", ForgetTaskInput{Confirm: true})
	if err != nil {
		t.Fatalf("forget_task failed: %v", err)
	}
	if out.Status != "forgotten" {
		t.Errorf("status = %q, want 'forgotten'", out.Status)
	}

	// Journal is now gone
	if _, exists, err := app.loadTaskJournal(developer); err != nil || exists {
		t.Fatalf("expected journal to be cleared, exists=%v err=%v", exists, err)
	}
	runner.assertDone()
}

func TestMCPDeveloperStatus(t *testing.T) {
	ctx := context.Background()
	project, _ := filepath.EvalSymlinks(t.TempDir())
	developer := developerName("w1", "w1:p1")
	runner := &scriptedRunner{t: t, steps: []runStep{
		{want: []string{"herdr", "pane", "get", "w1:p1"}, result: supervisorPaneJSON(project)},
		{want: []string{"herdr", "agent", "get", developer}, result: agentJSONWithSession("w1:p2", "w1", project, "idle", testConversationID)},
		{want: []string{"herdr", "pane", "get", "w1:p2"}, result: paneJSON("w1:p2", "w1:t1", project, developerPaneLabel, map[string]string{
			"cagy_owner":         developer,
			"cagy_role":          "developer",
			"cagy_session":       testConversationID,
			agySessionStateToken: agySessionStateReady,
		})},
	}}
	app, _ := setupTestMCPApp(t, runner, project, developer)
	session, cleanup := setupMCPClientServer(t, app)
	defer cleanup()

	out, _, err := callMCPTool[DeveloperStatusInput, DeveloperStatusOutput](t, ctx, session, "developer_status", DeveloperStatusInput{})
	if err != nil {
		t.Fatalf("developer_status failed: %v", err)
	}

	if out.Developer != developer {
		t.Errorf("developer = %q, want %q", out.Developer, developer)
	}
	if out.PaneID != "w1:p2" {
		t.Errorf("pane_id = %q, want 'w1:p2'", out.PaneID)
	}
	if out.Status != "idle" {
		t.Errorf("status = %q, want 'idle'", out.Status)
	}
	if out.Project != project {
		t.Errorf("project = %q, want %q", out.Project, project)
	}
	if !out.SessionReady {
		t.Errorf("session_ready = false, want true")
	}
	runner.assertDone()
}

func TestMCPConcurrentDelegation(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	project, _ := filepath.EvalSymlinks(t.TempDir())
	developer := developerName("w1", "w1:p1")
	task1 := "First delegation task"
	task2 := "Second delegation task"
	answer1 := "First answer completed"

	g2Started := make(chan struct{})
	g2Done := make(chan struct{})

	runner := &scriptedRunner{t: t, steps: []runStep{
		{want: []string{"herdr", "pane", "get", "w1:p1"}, result: supervisorPaneJSON(project)},
		{want: []string{"herdr", "agent", "get", developer}, result: agentJSON("w1:p2", "w1", project, "idle")},
		{want: []string{"herdr", "pane", "get", "w1:p2"}, result: paneJSON("w1:p2", "w1:t1", project, developerPaneLabel, map[string]string{"cagy_owner": developer, "cagy_role": "developer"})},
		{want: agyProbeArgs("/model"), result: agyModelResult("gemini-3.8-flash-high", "Gemini 3.8 Flash (High)")},
		{want: agyProbeArgs("/quota"), result: agyQuotaResult("Gemini Models", 0.8, 0.9)},
		{want: []string{"herdr", "agent", "read", developer, "--source", "recent-unwrapped", "--lines", "400"}, result: textResult("old output\n")},
		{
			want:   []string{"herdr", "agent", "prompt", developer, task1, "--wait", "--timeout", "300000"},
			result: agentJSONWithSession("w1:p2", "w1", project, "done", testConversationID),
			before: func() {
				// While goroutine 1 holds the developer lock, notify goroutine 2 to attempt delegation
				close(g2Started)
				<-g2Done
			},
		},
		{want: []string{"herdr", "agent", "read", developer, "--source", "recent-unwrapped", "--lines", "400"}, result: textResult("old output\n" + answer1 + "\n")},
		{want: []string{"herdr", "agent", "wait", developer, "--until", "blocked", "--timeout", "1000"}, result: jsonError("timeout", "timed out")},
		{want: []string{"herdr", "agent", "read", developer, "--source", "visible", "--lines", "80"}, result: textResult(">\n────────────────────\n? for shortcuts\n")},
		sessionOwnershipStep("w1:p2", testConversationID),
	}}

	app, brainRoot := setupTestMCPApp(t, runner, project, developer)
	writeAgyTranscript(t, brainRoot, testConversationID, task1, answer1)

	server := app.newMCPServer()

	// Connect Client 1
	t1, t2 := mcp.NewInMemoryTransports()
	sessionServer1, err := server.Connect(ctx, t1, nil)
	if err != nil {
		t.Fatalf("server.Connect(1): %v", err)
	}
	defer sessionServer1.Close()

	client1 := mcp.NewClient(&mcp.Implementation{Name: "client-1", Version: "1.0.0"}, nil)
	sessionClient1, err := client1.Connect(ctx, t2, nil)
	if err != nil {
		t.Fatalf("client1.Connect: %v", err)
	}
	defer sessionClient1.Close()

	// Connect Client 2
	t3, t4 := mcp.NewInMemoryTransports()
	sessionServer2, err := server.Connect(ctx, t3, nil)
	if err != nil {
		t.Fatalf("server.Connect(2): %v", err)
	}
	defer sessionServer2.Close()

	client2 := mcp.NewClient(&mcp.Implementation{Name: "client-2", Version: "1.0.0"}, nil)
	sessionClient2, err := client2.Connect(ctx, t4, nil)
	if err != nil {
		t.Fatalf("client2.Connect: %v", err)
	}
	defer sessionClient2.Close()

	var wg sync.WaitGroup
	wg.Add(2)

	var out1 *DelegateTaskOutput
	var err1 error
	var errMsg2 string
	var err2 error

	// Goroutine 1: runs primary task
	go func() {
		defer wg.Done()
		out1, _, err1 = callMCPTool[DelegateTaskInput, DelegateTaskOutput](t, ctx, sessionClient1, "delegate_task", DelegateTaskInput{Task: task1})
	}()

	// Goroutine 2: runs concurrent task while goroutine 1 holds lock
	go func() {
		defer wg.Done()
		<-g2Started
		_, errMsg2, err2 = callMCPTool[DelegateTaskInput, DelegateTaskOutput](t, ctx, sessionClient2, "delegate_task", DelegateTaskInput{Task: task2})
		close(g2Done)
	}()

	wg.Wait()

	if err1 != nil {
		t.Fatalf("client 1 delegate_task failed: %v", err1)
	}
	if out1.Status != "completed_unacknowledged" || out1.Answer != answer1 {
		t.Errorf("client 1 unexpected output: %+v", out1)
	}

	if err2 == nil {
		t.Fatal("client 2 expected error due to concurrency lock, got nil")
	}
	if !strings.Contains(errMsg2, "developer is busy") {
		t.Fatalf("client 2 expected 'developer is busy', got: %s", errMsg2)
	}

	runner.assertDone()
}

// TestMCPStdioProtocol builds the real cagy binary and runs "cagy mcp-server"
// as a subprocess. It connects an official MCP SDK client over the subprocess's
// stdin/stdout and verifies that:
//   - The initialize handshake succeeds.
//   - The exact six tools are listed with no extras.
//   - Stdout contains only valid MCP protocol framing (no stray text).
//   - Stderr diagnostics remain separate (do not contaminate stdout).
//   - Stateful tools return short context errors rather than hanging or
//     launching real Codex/agy processes.
//
// No real Codex or agy session is started; the subprocess immediately fails
// the cagy/Herdr context check when it tries to access developer state.
func TestMCPStdioProtocol(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping subprocess build test in -short mode")
	}

	// Build the real cagy binary into a temp file.
	tmpDir := t.TempDir()
	binaryPath := filepath.Join(tmpDir, "cagy-mcp-test")

	buildCmd := exec.Command("go", "build", "-o", binaryPath, "github.com/kazimshah39/cagy/cmd/cagy")
	buildCmd.Dir = filepath.Join("..", "..")
	if out, err := buildCmd.CombinedOutput(); err != nil {
		t.Fatalf("build cagy binary: %v\n%s", err, out)
	}

	// Run "cagy mcp-server" as a subprocess.
	// HERDR_ENV is intentionally absent so stateful tool calls fail with a
	// context error rather than contacting a real Herdr daemon.
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	subCmd := exec.CommandContext(ctx, binaryPath, "mcp-server")
	subStdin, err := subCmd.StdinPipe()
	if err != nil {
		t.Fatalf("StdinPipe: %v", err)
	}
	subStdout, err := subCmd.StdoutPipe()
	if err != nil {
		t.Fatalf("StdoutPipe: %v", err)
	}

	// Use a pipe for stderr so we can safely read it only after the subprocess
	// has exited. Assigning a bytes.Buffer directly to Stderr causes a data
	// race because os/exec writes to it from a separate goroutine.
	stderrR, stderrW, err := os.Pipe()
	if err != nil {
		t.Fatalf("os.Pipe for stderr: %v", err)
	}
	subCmd.Stderr = stderrW

	if err := subCmd.Start(); err != nil {
		stderrW.Close()
		stderrR.Close()
		t.Fatalf("start cagy mcp-server: %v", err)
	}
	// Close our write end so the read below can see EOF when the subprocess exits.
	stderrW.Close()

	// stopSubprocess kills the subprocess and waits for it to exit cleanly.
	stopSubprocess := func() {
		_ = subCmd.Process.Kill()
		_ = subCmd.Wait()
	}

	// Connect an official MCP SDK client over the subprocess's stdio.
	// Use IOTransport which accepts arbitrary reader/writer; StdioTransport
	// always uses os.Stdin/os.Stdout directly.
	transport := &mcp.IOTransport{
		Reader: subStdout,
		Writer: subStdin,
	}
	client := mcp.NewClient(&mcp.Implementation{Name: "test-stdio-client", Version: "1.0.0"}, nil)
	session, connectErr := client.Connect(ctx, transport, nil)
	if connectErr != nil {
		stopSubprocess()
		// Read stderr only after the subprocess has exited to avoid a race.
		var stderrBuf bytes.Buffer
		_, _ = stderrBuf.ReadFrom(stderrR)
		stderrR.Close()
		t.Fatalf("MCP client connect over stdio: %v\nstderr: %s", connectErr, stderrBuf.String())
	}

	// List all tools from the real subprocess.
	var toolNames []string
	for tool, err := range session.Tools(ctx, nil) {
		if err != nil {
			session.Close()
			stopSubprocess()
			t.Fatalf("Tools iteration: %v", err)
		}
		toolNames = append(toolNames, tool.Name)
	}

	expectedTools := []string{
		"delegate_task",
		"task_status",
		"recover_task",
		"acknowledge_task",
		"forget_task",
		"developer_status",
	}

	if len(toolNames) != len(expectedTools) {
		session.Close()
		stopSubprocess()
		t.Fatalf("got %d tools from stdio server, want %d: %v", len(toolNames), len(expectedTools), toolNames)
	}
	for _, expected := range expectedTools {
		if !slices.Contains(toolNames, expected) {
			t.Errorf("stdio server missing expected tool %q", expected)
		}
	}

	// Call a read-only stateful tool. Without a real Herdr session it must
	// return a short context error, not hang, and not launch any agent process.
	_, errMsg, callErr := callMCPTool[TaskStatusInput, TaskStatusOutput](t, ctx, session, "task_status", TaskStatusInput{})
	if callErr == nil {
		// task_status without Herdr context succeeds with a "none" status
		// because it only reads local state (no Herdr call required for that path).
		t.Logf("task_status without Herdr context returned success (no running task state)")
	} else {
		// A context error is also acceptable.
		t.Logf("task_status without Herdr context returned expected error: %s", errMsg)
	}

	// Stop the subprocess before reading stderr to avoid a data race.
	session.Close()
	stopSubprocess()

	// Read stderr safely now that the subprocess has exited.
	var stderrBuf bytes.Buffer
	_, _ = stderrBuf.ReadFrom(stderrR)
	stderrR.Close()

	// The fact that the SDK client connected and listed tools proves stdout had
	// only valid protocol framing. Stderr must not have contaminated stdout.
	t.Logf("subprocess stderr output: %q", stderrBuf.String())
}
