package developer

import "testing"

func TestResolveAgyAndRejectUnknownDeveloper(t *testing.T) {
	adapter, err := Resolve("")
	if err != nil || adapter.ID() != AgyID || adapter.Executable() != "agy" {
		t.Fatalf("adapter=%v err=%v", adapter, err)
	}
	if _, err := Resolve("other"); err == nil {
		t.Fatal("unknown developer accepted")
	}
}

func TestAgyStartSpecUsesRequiredFlagsAndExactSession(t *testing.T) {
	adapter := Agy{}
	fresh, err := adapter.StartSpec(StartOptions{Name: "dev", PaneID: "w1:p2"})
	if err != nil {
		t.Fatal(err)
	}
	if fresh.ExpectedSession != nil {
		t.Fatal("fresh start unexpectedly has a session")
	}
	wantFlags := []string{"--dangerously-skip-permissions", "--mode", "accept-edits"}
	for _, want := range wantFlags {
		found := false
		for _, arg := range fresh.Args {
			if arg == want {
				found = true
			}
		}
		if !found {
			t.Fatalf("fresh args=%#v missing %q", fresh.Args, want)
		}
	}
	for _, arg := range fresh.Args {
		if arg == "--model" {
			t.Fatalf("fresh args unexpectedly contained --model: %#v", fresh.Args)
		}
	}

	freshWithModel, err := adapter.StartSpec(StartOptions{Name: "dev", PaneID: "w1:p2", Model: "gemini-2.5-flash"})
	if err != nil {
		t.Fatal(err)
	}
	hasModel := false
	for i, arg := range freshWithModel.Args {
		if arg == "--model" && i+1 < len(freshWithModel.Args) && freshWithModel.Args[i+1] == "gemini-2.5-flash" {
			hasModel = true
			break
		}
	}
	if !hasModel {
		t.Fatalf("fresh with model missing --model flag: %#v", freshWithModel.Args)
	}

	resumed, err := adapter.StartSpec(StartOptions{Name: "dev", PaneID: "w1:p2", SessionID: "session-123"})
	if err != nil {
		t.Fatal(err)
	}
	if resumed.ExpectedSession == nil || resumed.ExpectedSession.Value != "session-123" {
		t.Fatalf("resumed=%+v", resumed)
	}
	if len(resumed.Args) < 2 || resumed.Args[0] != "--conversation" || resumed.Args[1] != "session-123" {
		t.Fatalf("args=%#v", resumed.Args)
	}

	resumedWithModel, err := adapter.StartSpec(StartOptions{Name: "dev", PaneID: "w1:p2", SessionID: "session-123", Model: "gemini-2.5-pro"})
	if err != nil {
		t.Fatal(err)
	}
	if len(resumedWithModel.Args) < 2 || resumedWithModel.Args[0] != "--conversation" || resumedWithModel.Args[1] != "session-123" {
		t.Fatalf("resumed with model missing conversation-first: %#v", resumedWithModel.Args)
	}
	hasResumedModel := false
	for i, arg := range resumedWithModel.Args {
		if arg == "--model" && i+1 < len(resumedWithModel.Args) && resumedWithModel.Args[i+1] == "gemini-2.5-pro" {
			hasResumedModel = true
			break
		}
	}
	if !hasResumedModel {
		t.Fatalf("resumed with model missing --model flag: %#v", resumedWithModel.Args)
	}
}
