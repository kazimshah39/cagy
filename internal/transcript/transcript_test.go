package transcript

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

const testSessionID = "512995cf-e151-4934-9dad-c43327869bf1"

func TestPathValidatesHerdrAgySession(t *testing.T) {
	root := t.TempDir()
	path, err := Path(root, Ref{Source: "herdr:antigravity_cli", Agent: "agy", Kind: "id", Value: testSessionID})
	if err != nil {
		t.Fatal(err)
	}
	want := filepath.Join(root, testSessionID, ".system_generated", "logs", "transcript.jsonl")
	if path != want {
		t.Fatalf("path=%q want=%q", path, want)
	}
	for _, ref := range []Ref{
		{Source: "other", Agent: "agy", Kind: "id", Value: testSessionID},
		{Source: "herdr:antigravity_cli", Agent: "agy", Kind: "path", Value: testSessionID},
		{Source: "herdr:antigravity_cli", Agent: "agy", Kind: "id", Value: "../../bad"},
	} {
		if _, err := Path(root, ref); err == nil {
			t.Fatalf("expected rejection for %+v", ref)
		}
	}
}

func TestFinalResponseReturnsCompleteLongAnswerAfterCheckpoint(t *testing.T) {
	path := filepath.Join(t.TempDir(), "transcript.jsonl")
	old := event("USER_EXPLICIT", "USER_INPUT", "DONE", wrappedTask("old task")) +
		event("MODEL", "PLANNER_RESPONSE", "DONE", "old answer")
	if err := os.WriteFile(path, []byte(old), 0o600); err != nil {
		t.Fatal(err)
	}
	offset := int64(len(old))
	longAnswer := strings.Repeat("complete long response line\n", 12000)
	current := event("USER_EXPLICIT", "USER_INPUT", "DONE", wrappedTask("new task")) +
		event("MODEL", "PLANNER_RESPONSE", "DONE", "") +
		event("MODEL", "GENERIC", "DONE", "tool output must not leak") +
		event("MODEL", "PLANNER_RESPONSE", "DONE", longAnswer)
	file, err := os.OpenFile(path, os.O_APPEND|os.O_WRONLY, 0)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := file.WriteString(current); err != nil {
		t.Fatal(err)
	}
	if err := file.Close(); err != nil {
		t.Fatal(err)
	}

	got, found, err := FinalResponse(path, offset, "new task")
	if err != nil {
		t.Fatal(err)
	}
	if !found || got != strings.TrimSpace(longAnswer) {
		t.Fatalf("found=%v length=%d want=%d", found, len(got), len(strings.TrimSpace(longAnswer)))
	}
	if strings.Contains(got, "tool output") || strings.Contains(got, "old answer") {
		t.Fatalf("response leaked unrelated transcript content")
	}
}

func TestFinalResponseRequiresExactTaskAndCompleteFinalEvent(t *testing.T) {
	path := filepath.Join(t.TempDir(), "transcript.jsonl")
	content := "not json\n" +
		event("USER_EXPLICIT", "USER_INPUT", "DONE", wrappedTask("another task")) +
		event("MODEL", "PLANNER_RESPONSE", "DONE", "wrong answer") +
		`{"source":"USER_EXPLICIT"`
	if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
		t.Fatal(err)
	}
	if got, found, err := FinalResponse(path, 0, "wanted task"); err != nil || found || got != "" {
		t.Fatalf("got=%q found=%v err=%v", got, found, err)
	}
}

func TestCaptureUsesCurrentByteOffset(t *testing.T) {
	root := t.TempDir()
	ref := Ref{Source: "herdr:antigravity_cli", Agent: "agy", Kind: "id", Value: testSessionID}
	path, err := Path(root, ref)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte("12345"), 0o600); err != nil {
		t.Fatal(err)
	}
	checkpoint, err := Capture(root, &ref)
	if err != nil {
		t.Fatal(err)
	}
	fullPath := filepath.Join(filepath.Dir(path), "transcript_full.jsonl")
	if checkpoint.Path != path || checkpoint.Offset != 5 || checkpoint.FullPath != fullPath || checkpoint.FullOffset != 0 || checkpoint.SessionID != testSessionID {
		t.Fatalf("checkpoint=%+v", checkpoint)
	}
}

func event(source, eventType, status, content string) string {
	replacer := strings.NewReplacer("\\", "\\\\", "\"", "\\\"", "\n", "\\n", "\r", "\\r", "\t", "\\t")
	return `{"source":"` + source + `","type":"` + eventType + `","status":"` + status + `","content":"` + replacer.Replace(content) + `"}` + "\n"
}

func wrappedTask(task string) string {
	return "<USER_REQUEST>\n" + task + "\n</USER_REQUEST>\n<ADDITIONAL_METADATA>ignored</ADDITIONAL_METADATA>"
}

func TestFinalResponseForPrefersFullTranscript(t *testing.T) {
	root := t.TempDir()
	ref := Ref{Source: "herdr:antigravity_cli", Agent: "agy", Kind: "id", Value: testSessionID}
	paths, err := PathsFor(root, ref)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Dir(paths.Compact), 0o700); err != nil {
		t.Fatal(err)
	}
	compact := event("USER_EXPLICIT", "USER_INPUT", "DONE", wrappedTask("new task")) +
		event("MODEL", "PLANNER_RESPONSE", "DONE", "truncated answer")
	full := event("USER_EXPLICIT", "USER_INPUT", "DONE", wrappedTask("new task")) +
		event("MODEL", "PLANNER_RESPONSE", "DONE", "complete untruncated answer")
	if err := os.WriteFile(paths.Compact, []byte(compact), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(paths.Full, []byte(full), 0o600); err != nil {
		t.Fatal(err)
	}

	got, found, err := FinalResponseFor(root, ref, Checkpoint{}, "new task")
	if err != nil {
		t.Fatal(err)
	}
	if !found || got != "complete untruncated answer" {
		t.Fatalf("got=%q found=%v", got, found)
	}
}

func TestFinalResponseForFallsBackToCompactTranscript(t *testing.T) {
	root := t.TempDir()
	ref := Ref{Source: "herdr:antigravity_cli", Agent: "agy", Kind: "id", Value: testSessionID}
	paths, err := PathsFor(root, ref)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Dir(paths.Compact), 0o700); err != nil {
		t.Fatal(err)
	}
	compact := event("USER_EXPLICIT", "USER_INPUT", "DONE", wrappedTask("new task")) +
		event("MODEL", "PLANNER_RESPONSE", "DONE", "compact answer")
	if err := os.WriteFile(paths.Compact, []byte(compact), 0o600); err != nil {
		t.Fatal(err)
	}

	got, found, err := FinalResponseFor(root, ref, Checkpoint{}, "new task")
	if err != nil {
		t.Fatal(err)
	}
	if !found || got != "compact answer" {
		t.Fatalf("got=%q found=%v", got, found)
	}
}

func TestFinalResponseForHandlesFirstTurnFileCreation(t *testing.T) {
	root := t.TempDir()
	ref := Ref{Source: "herdr:antigravity_cli", Agent: "agy", Kind: "id", Value: testSessionID}
	checkpoint, err := Capture(root, &ref)
	if err != nil {
		t.Fatal(err)
	}
	if checkpoint.Offset != 0 || checkpoint.FullOffset != 0 {
		t.Fatalf("checkpoint=%+v", checkpoint)
	}
	paths, err := PathsFor(root, ref)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Dir(paths.Full), 0o700); err != nil {
		t.Fatal(err)
	}
	content := event("USER_EXPLICIT", "USER_INPUT", "DONE", wrappedTask("first task")) +
		event("MODEL", "PLANNER_RESPONSE", "DONE", "first answer")
	if err := os.WriteFile(paths.Full, []byte(content), 0o600); err != nil {
		t.Fatal(err)
	}

	got, found, err := FinalResponseFor(root, ref, checkpoint, "first task")
	if err != nil {
		t.Fatal(err)
	}
	if !found || got != "first answer" {
		t.Fatalf("got=%q found=%v", got, found)
	}
}

func TestFinalResponseForUsesIndependentFullOffset(t *testing.T) {
	root := t.TempDir()
	ref := Ref{Source: "herdr:antigravity_cli", Agent: "agy", Kind: "id", Value: testSessionID}
	paths, err := PathsFor(root, ref)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Dir(paths.Full), 0o700); err != nil {
		t.Fatal(err)
	}
	oldCompact := event("USER_EXPLICIT", "USER_INPUT", "DONE", wrappedTask("old task")) + event("MODEL", "PLANNER_RESPONSE", "DONE", "old compact")
	oldFull := event("USER_EXPLICIT", "USER_INPUT", "DONE", wrappedTask("old task")) + event("MODEL", "PLANNER_RESPONSE", "DONE", "old full answer")
	if err := os.WriteFile(paths.Compact, []byte(oldCompact), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(paths.Full, []byte(oldFull), 0o600); err != nil {
		t.Fatal(err)
	}
	checkpoint, err := Capture(root, &ref)
	if err != nil {
		t.Fatal(err)
	}
	newCompact := event("USER_EXPLICIT", "USER_INPUT", "DONE", wrappedTask("new task")) + event("MODEL", "PLANNER_RESPONSE", "DONE", "new compact")
	newFull := event("USER_EXPLICIT", "USER_INPUT", "DONE", wrappedTask("new task")) + event("MODEL", "PLANNER_RESPONSE", "DONE", "new full answer")
	for path, content := range map[string]string{paths.Compact: newCompact, paths.Full: newFull} {
		file, err := os.OpenFile(path, os.O_APPEND|os.O_WRONLY, 0)
		if err != nil {
			t.Fatal(err)
		}
		if _, err := file.WriteString(content); err != nil {
			file.Close()
			t.Fatal(err)
		}
		if err := file.Close(); err != nil {
			t.Fatal(err)
		}
	}

	got, found, err := FinalResponseFor(root, ref, checkpoint, "new task")
	if err != nil {
		t.Fatal(err)
	}
	if !found || got != "new full answer" {
		t.Fatalf("got=%q found=%v", got, found)
	}
}
