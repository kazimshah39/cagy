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
	Supervisor           string `json:"supervisor"`
	DeveloperKind        string `json:"developer_kind"`
	Developer            string `json:"developer"`
	PaneID               string `json:"pane_id"`
	Status               string `json:"status"`
	Project              string `json:"project"`
	SessionReady         bool   `json:"session_ready"`
	ProviderServiceReady bool   `json:"provider_service_ready"`
	TaskSummary          string `json:"task_summary,omitempty"`
}

func boolPtr(b bool) *bool {
	return &b
}

func (a *App) newMCPServer() *mcp.Server {
	server := mcp.NewServer(&mcp.Implementation{
		Name:    "herdr-tandem",
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
	a.debugf("mcp server begin transport=%q", "stdio")
	server := a.newMCPServer()
	err := server.Run(ctx, &mcp.StdioTransport{})
	a.debugf("mcp server end ok=%t error=%q", err == nil, err)
	return err
}

func (a *App) handleMCPDelegateTask(ctx context.Context, req *mcp.CallToolRequest, in DelegateTaskInput) (*mcp.CallToolResult, DelegateTaskOutput, error) {
	taskID := debugTaskFingerprint(in.Task)
	a.debugf("mcp tool begin name=%q task=%q bytes=%d", "delegate_task", taskID, len(in.Task))
	if err := validateTaskString(in.Task); err != nil {
		a.debugf("mcp tool end name=%q task=%q ok=false stage=%q error=%q", "delegate_task", taskID, "validate", err)
		return nil, DelegateTaskOutput{}, err
	}
	delivery, err := a.delegateTask(ctx, in.Task)
	if err != nil {
		a.debugf("mcp tool end name=%q task=%q ok=false stage=%q error=%q", "delegate_task", taskID, "delegate", err)
		return nil, DelegateTaskOutput{}, err
	}
	a.debugf("mcp tool end name=%q task=%q ok=true status=%q answer_bytes=%d receipt_present=%t", "delegate_task", taskID, "completed_unacknowledged", len(delivery.output), delivery.receipt != "")
	return nil, DelegateTaskOutput{
		Status:                  "completed_unacknowledged",
		Answer:                  delivery.output,
		Receipt:                 delivery.receipt,
		AcknowledgementRequired: true,
	}, nil
}

func (a *App) handleMCPTaskStatus(ctx context.Context, req *mcp.CallToolRequest, in TaskStatusInput) (*mcp.CallToolResult, TaskStatusOutput, error) {
	a.debugf("mcp tool begin name=%q", "task_status")
	status, err := a.getTaskStatus(ctx)
	if err != nil {
		a.debugf("mcp tool end name=%q ok=false error=%q", "task_status", err)
		return nil, TaskStatusOutput{}, err
	}
	a.debugf("mcp tool end name=%q ok=true status=%q recoverable=%t acknowledgement_required=%t", "task_status", status.Status, status.Recoverable, status.AcknowledgementRequired)
	return nil, *status, nil
}

func (a *App) handleMCPRecoverTask(ctx context.Context, req *mcp.CallToolRequest, in RecoverTaskInput) (*mcp.CallToolResult, RecoverTaskOutput, error) {
	a.debugf("mcp tool begin name=%q", "recover_task")
	recovered, err := a.recoverTask(ctx)
	if err != nil {
		a.debugf("mcp tool end name=%q ok=false error=%q", "recover_task", err)
		return nil, RecoverTaskOutput{}, err
	}
	a.debugf("mcp tool end name=%q ok=true status=%q answer_bytes=%d receipt_present=%t", "recover_task", recovered.Status, len(recovered.Answer), recovered.Receipt != "")
	return nil, *recovered, nil
}

func (a *App) handleMCPAcknowledgeTask(ctx context.Context, req *mcp.CallToolRequest, in AcknowledgeTaskInput) (*mcp.CallToolResult, AcknowledgeTaskOutput, error) {
	a.debugf("mcp tool begin name=%q receipt_shape_valid=%t", "acknowledge_task", isValidDeliveryReceipt(in.Receipt))
	if err := a.acknowledgeTask(ctx, in.Receipt); err != nil {
		a.debugf("mcp tool end name=%q ok=false error=%q", "acknowledge_task", err)
		return nil, AcknowledgeTaskOutput{}, err
	}
	a.debugf("mcp tool end name=%q ok=true status=%q", "acknowledge_task", "acknowledged")
	return nil, AcknowledgeTaskOutput{
		Status:  "acknowledged",
		Message: "completed task state cleared",
	}, nil
}

func (a *App) handleMCPForgetTask(ctx context.Context, req *mcp.CallToolRequest, in ForgetTaskInput) (*mcp.CallToolResult, ForgetTaskOutput, error) {
	a.debugf("mcp tool begin name=%q confirm=%t", "forget_task", in.Confirm)
	out, err := a.forgetTask(ctx, in.Confirm)
	if err != nil {
		a.debugf("mcp tool end name=%q ok=false error=%q", "forget_task", err)
		return nil, ForgetTaskOutput{}, err
	}
	a.debugf("mcp tool end name=%q ok=true status=%q", "forget_task", out.Status)
	return nil, *out, nil
}

func (a *App) handleMCPDeveloperStatus(ctx context.Context, req *mcp.CallToolRequest, in DeveloperStatusInput) (*mcp.CallToolResult, DeveloperStatusOutput, error) {
	a.debugf("mcp tool begin name=%q", "developer_status")
	status, err := a.getDeveloperStatus(ctx)
	if err != nil {
		a.debugf("mcp tool end name=%q ok=false error=%q", "developer_status", err)
		return nil, DeveloperStatusOutput{}, err
	}
	a.debugf("mcp tool end name=%q ok=true developer=%q pane=%q status=%q session_ready=%t", "developer_status", status.Developer, status.PaneID, status.Status, status.SessionReady)
	return nil, *status, nil
}
