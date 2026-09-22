package app

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	"github.com/kazimshah39/herdr-tandem/internal/supervisor"
)

func TestConcurrentProjectsWithAgySupervisor(t *testing.T) {
	stateDir := t.TempDir()
	configRoot := t.TempDir()

	// Project 1
	project1, err := resolveProject(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	runner1 := &lifecycleTestRunner{
		project:        project1,
		supervisorPane: "w1:p1",
		developerPane:  "w1:p2",
		developerName:  developerName("w1", "w1:p1"),
	}
	app1 := New(runner1, nil, nil)
	app1.supervisor = supervisor.Agy{}
	app1.stateDir = stateDir
	app1.configRoot = configRoot
	app1.token = func() (string, error) { return "runtime-project-1", nil }
	app1.checkPlatform = func() error { return nil }
	app1.resolveExecutable = func() (string, error) { return "/tmp/herdr-tandem", nil }
	app1.providerServiceCheck = func(context.Context) error { return nil }
	app1.supervisorModel = "model-sup-1"
	app1.developerModel = "model-dev-1"
	app1.getenv = func(key string) string {
		switch key {
		case "HERDR_ENV":
			return "1"
		case "HERDR_PANE_ID":
			return "w1:p1"
		case "HERDR_WORKSPACE_ID":
			return "w1"
		case "HERDR_TAB_ID":
			return "w1:t1"
		case "HERDR_SOCKET_PATH":
			return "/tmp/herdr.sock"
		}
		return ""
	}

	// Project 2
	project2, err := resolveProject(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	runner2 := &lifecycleTestRunner{
		workspaceID:    "w2",
		project:        project2,
		supervisorPane: "w2:p1",
		developerPane:  "w2:p2",
		developerName:  developerName("w2", "w2:p1"),
	}
	app2 := New(runner2, nil, nil)
	app2.supervisor = supervisor.Agy{}
	app2.stateDir = stateDir
	app2.configRoot = configRoot
	app2.token = func() (string, error) { return "runtime-project-2", nil }
	app2.checkPlatform = func() error { return nil }
	app2.resolveExecutable = func() (string, error) { return "/tmp/herdr-tandem", nil }
	app2.providerServiceCheck = func(context.Context) error { return nil }
	app2.supervisorModel = "model-sup-2"
	app2.developerModel = "model-dev-2"
	app2.getenv = func(key string) string {
		switch key {
		case "HERDR_ENV":
			return "1"
		case "HERDR_PANE_ID":
			return "w2:p1"
		case "HERDR_WORKSPACE_ID":
			return "w2"
		case "HERDR_TAB_ID":
			return "w2:t1"
		case "HERDR_SOCKET_PATH":
			return "/tmp/herdr.sock"
		}
		return ""
	}

	// In project 1 RunAttached, start project 2 and check concurrent coexistence
	p1Started := false
	p2Started := false
	runner1.runAttachedFn = func(dir1 string, args1, env1 []string) error {
		p1Started = true

		// While project 1 is running, start project 2
		runner2.runAttachedFn = func(dir2 string, args2, env2 []string) error {
			p2Started = true

			// Verify both custom agent directories exist simultaneously without collision
			agent1Path := filepath.Join(configRoot, "agents", "herdr-tandem-runtime-project-1", "agent.md")
			agent2Path := filepath.Join(configRoot, "agents", "herdr-tandem-runtime-project-2", "agent.md")

			if _, err := os.Stat(agent1Path); err != nil {
				t.Fatalf("agent 1 missing: %v", err)
			}
			if _, err := os.Stat(agent2Path); err != nil {
				t.Fatalf("agent 2 missing: %v", err)
			}

			// Verify runtime records are distinct
			rec1, found1, err1 := app1.runtimeManager().FindForSupervisorPane("w1", "w1:p1", project1)
			if err1 != nil || !found1 {
				t.Fatalf("rec1 missing: found=%t err=%v", found1, err1)
			}
			if rec1.SupervisorAgentName != "herdr-tandem-runtime-project-1" || rec1.SupervisorModel != "model-sup-1" || rec1.DeveloperModel != "model-dev-1" {
				t.Fatalf("rec1 mismatch: %+v", rec1)
			}

			rec2, found2, err2 := app2.runtimeManager().FindForSupervisorPane("w2", "w2:p1", project2)
			if err2 != nil || !found2 {
				t.Fatalf("rec2 missing: found=%t err=%v", found2, err2)
			}
			if rec2.SupervisorAgentName != "herdr-tandem-runtime-project-2" || rec2.SupervisorModel != "model-sup-2" || rec2.DeveloperModel != "model-dev-2" {
				t.Fatalf("rec2 mismatch: %+v", rec2)
			}

			return nil
		}

		if err := app2.start(context.Background(), project2, sidebarModeCompact); err != nil {
			t.Fatalf("app2 start failed: %v", err)
		}

		// After project 2 exited, its agent directory is gone while project 1 is still present
		agent2Path := filepath.Join(configRoot, "agents", "herdr-tandem-runtime-project-2")
		if _, err := os.Stat(agent2Path); !os.IsNotExist(err) {
			t.Fatalf("agent 2 was not cleaned up after exit: %v", err)
		}
		agent1Path := filepath.Join(configRoot, "agents", "herdr-tandem-runtime-project-1", "agent.md")
		if _, err := os.Stat(agent1Path); err != nil {
			t.Fatalf("agent 1 should still exist: %v", err)
		}

		return nil
	}

	if err := app1.start(context.Background(), project1, sidebarModeExpanded); err != nil {
		t.Fatalf("app1 start failed: %v", err)
	}

	if !p1Started || !p2Started {
		t.Fatalf("p1Started=%t p2Started=%t, expected both true", p1Started, p2Started)
	}

	// Both agent directories must now be cleaned up
	agent1Path := filepath.Join(configRoot, "agents", "herdr-tandem-runtime-project-1")
	if _, err := os.Stat(agent1Path); !os.IsNotExist(err) {
		t.Fatalf("agent 1 was not cleaned up after final exit: %v", err)
	}
}
