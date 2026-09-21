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
	fresh, err := adapter.StartSpec("dev", "w1:p2", "")
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
	resumed, err := adapter.StartSpec("dev", "w1:p2", "session-123")
	if err != nil {
		t.Fatal(err)
	}
	if resumed.ExpectedSession == nil || resumed.ExpectedSession.Value != "session-123" {
		t.Fatalf("resumed=%+v", resumed)
	}
	if len(resumed.Args) < 2 || resumed.Args[0] != "--conversation" || resumed.Args[1] != "session-123" {
		t.Fatalf("args=%#v", resumed.Args)
	}
}
