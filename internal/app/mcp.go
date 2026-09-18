package app

import (
	"context"

	"github.com/modelcontextprotocol/go-sdk/mcp"
)

type DelegateTaskInput struct {
	Task string `json:"task" jsonschema:"the development task to delegate to the agy developer"`
}

type DelegateTaskOutput struct {
	Status                  string `json:"status"`
	Answer                  string `json:"answer"`
	Receipt                 string `json:"receipt"`
	AcknowledgementRequired bool   `json:"acknowledgement_required"`
}

type TaskStatusInput struct{}

type TaskStatusOutput struct {
	Status                  string `json:"status"`
	Message                 string `json:"message"`
	Elapsed                 string `json:"elapsed,omitempty"`
	Recoverable             bool   `json:"recoverable"`
	AcknowledgementRequired bool   `json:"acknowledgement_required"`
}

type RecoverTaskInput struct{}

type RecoverTaskOutput struct {
	Status                  string `json:"status"`
	Answer                  string `json:"answer"`
	Receipt                 string `json:"receipt"`
	AcknowledgementRequired bool   `json:"acknowledgement_required"`
}

type AcknowledgeTaskInput struct {
	Receipt string `json:"receipt" jsonschema:"the delivery receipt from delegate_task or recover_task"`
}

type AcknowledgeTaskOutput struct {
	Status  string `json:"status"`
	Message string `json:"message"`
}

type ForgetTaskInput struct {
	Confirm bool `json:"confirm" jsonschema:"explicit confirmation to discard task state"`
}

type ForgetTaskOutput struct {
	Status  string `json:"status"`
	Message string `json:"message"`
}

type DeveloperStatusInput struct{}

type DeveloperStatusOutput struct {
	Developer    string `json:"developer"`
	PaneID       string `json:"pane_id"`
	Status       string `json:"status"`
	Project      string `json:"project"`
	SessionReady bool   `json:"session_ready"`
	TaskSummary  string `json:"task_summary,omitempty"`
}

func boolPtr(b bool) *bool {
	return &b
}

func (a *App) newMCPServer() *mcp.Server {
	server := mcp.NewServer(&mcp.Implementation{
		Name:    "cagy",
		Version: "0.1.0",
	}, nil)

	mcp.AddTool(server, &mcp.Tool{
		Name:        "delegate_task",
		Description: "Delegate an implementation task to the visible agy developer in Herdr",
		Annotations: &mcp.ToolAnnotations{
			ReadOnlyHint:    false,
			DestructiveHint: boolPtr(false),
			OpenWorldHint:   boolPtr(true),
			Title:           "Delegate Task",
		},
	}, a.handleMCPDelegateTask)

	mcp.AddTool(server, &mcp.Tool{
		Name:        "task_status",
		Description: "Inspect the current status of delegated agy tasks",
		Annotations: &mcp.ToolAnnotations{
			ReadOnlyHint: true,
			Title:        "Task Status",
		},
	}, a.handleMCPTaskStatus)

	mcp.AddTool(server, &mcp.Tool{
		Name:        "recover_task",
		Description: "Recover the exact completed answer from an interrupted task without resubmitting",
		Annotations: &mcp.ToolAnnotations{
			ReadOnlyHint:    false,
			DestructiveHint: boolPtr(false),
			IdempotentHint:  true,
			Title:           "Recover Task",
		},
	}, a.handleMCPRecoverTask)

	mcp.AddTool(server, &mcp.Tool{
		Name:        "acknowledge_task",
		Description: "Acknowledge receipt of a completed task answer and clear durable task state",
		Annotations: &mcp.ToolAnnotations{
			ReadOnlyHint:    false,
			DestructiveHint: boolPtr(true),
			IdempotentHint:  true,
			Title:           "Acknowledge Task",
		},
	}, a.handleMCPAcknowledgeTask)

	mcp.AddTool(server, &mcp.Tool{
		Name:        "forget_task",
		Description: "Explicitly discard interrupted task state when recovery is impossible and the developer is not running",
		Annotations: &mcp.ToolAnnotations{
			ReadOnlyHint:    false,
			DestructiveHint: boolPtr(true),
			Title:           "Forget Task",
		},
	}, a.handleMCPForgetTask)

	mcp.AddTool(server, &mcp.Tool{
		Name:        "developer_status",
		Description: "Inspect the health, pane, and readiness of the agy developer",
		Annotations: &mcp.ToolAnnotations{
			ReadOnlyHint: true,
			Title:        "Developer Status",
		},
	}, a.handleMCPDeveloperStatus)

	return server
}

func (a *App) serveMCP(ctx context.Context) error {
	server := a.newMCPServer()
	return server.Run(ctx, &mcp.StdioTransport{})
}

func (a *App) handleMCPDelegateTask(ctx context.Context, req *mcp.CallToolRequest, in DelegateTaskInput) (*mcp.CallToolResult, DelegateTaskOutput, error) {
	if err := validateTaskString(in.Task); err != nil {
		return nil, DelegateTaskOutput{}, err
	}
	delivery, err := a.delegateTask(ctx, in.Task)
	if err != nil {
		return nil, DelegateTaskOutput{}, err
	}
	return nil, DelegateTaskOutput{
		Status:                  "completed_unacknowledged",
		Answer:                  delivery.output,
		Receipt:                 delivery.receipt,
		AcknowledgementRequired: true,
	}, nil
}

func (a *App) handleMCPTaskStatus(ctx context.Context, req *mcp.CallToolRequest, in TaskStatusInput) (*mcp.CallToolResult, TaskStatusOutput, error) {
	status, err := a.getTaskStatus(ctx)
	if err != nil {
		return nil, TaskStatusOutput{}, err
	}
	return nil, *status, nil
}

func (a *App) handleMCPRecoverTask(ctx context.Context, req *mcp.CallToolRequest, in RecoverTaskInput) (*mcp.CallToolResult, RecoverTaskOutput, error) {
	recovered, err := a.recoverTask(ctx)
	if err != nil {
		return nil, RecoverTaskOutput{}, err
	}
	return nil, *recovered, nil
}

func (a *App) handleMCPAcknowledgeTask(ctx context.Context, req *mcp.CallToolRequest, in AcknowledgeTaskInput) (*mcp.CallToolResult, AcknowledgeTaskOutput, error) {
	if err := a.acknowledgeTask(ctx, in.Receipt); err != nil {
		return nil, AcknowledgeTaskOutput{}, err
	}
	return nil, AcknowledgeTaskOutput{
		Status:  "acknowledged",
		Message: "completed task state cleared",
	}, nil
}

func (a *App) handleMCPForgetTask(ctx context.Context, req *mcp.CallToolRequest, in ForgetTaskInput) (*mcp.CallToolResult, ForgetTaskOutput, error) {
	out, err := a.forgetTask(ctx, in.Confirm)
	if err != nil {
		return nil, ForgetTaskOutput{}, err
	}
	return nil, *out, nil
}

func (a *App) handleMCPDeveloperStatus(ctx context.Context, req *mcp.CallToolRequest, in DeveloperStatusInput) (*mcp.CallToolResult, DeveloperStatusOutput, error) {
	status, err := a.getDeveloperStatus(ctx)
	if err != nil {
		return nil, DeveloperStatusOutput{}, err
	}
	return nil, *status, nil
}
