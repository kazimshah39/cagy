package app

import (
	"context"
	"slices"
	"testing"
	"time"

	"github.com/modelcontextprotocol/go-sdk/mcp"
)

func TestMCPExposesOnlyProviderManagedModeWorkflowTools(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	app := New(fakeRunner{}, nil, nil)
	server := app.newMCPServer()
	serverTransport, clientTransport := mcp.NewInMemoryTransports()
	serverSession, err := server.Connect(ctx, serverTransport, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer serverSession.Close()
	client := mcp.NewClient(&mcp.Implementation{Name: "herdr-tandem-test", Version: "1.0.0"}, nil)
	clientSession, err := client.Connect(ctx, clientTransport, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer clientSession.Close()

	var names []string
	for tool, err := range clientSession.Tools(ctx, nil) {
		if err != nil {
			t.Fatal(err)
		}
		names = append(names, tool.Name)
	}
	want := []string{"delegate_task", "task_status", "recover_task", "acknowledge_task", "forget_task", "developer_status"}
	if len(names) != len(want) {
		t.Fatalf("tools=%v want=%v", names, want)
	}
	for _, name := range want {
		if !slices.Contains(names, name) {
			t.Fatalf("missing MCP tool %q in %v", name, names)
		}
	}
	for _, forbidden := range []string{"accounts", "switch_account", "quota"} {
		if slices.Contains(names, forbidden) {
			t.Fatalf("removed account tool remains exposed: %q", forbidden)
		}
	}
}
