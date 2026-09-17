package quota

import (
	"strings"
	"testing"
)

func TestDetectedStrongSignals(t *testing.T) {
	t.Parallel()
	cases := []string{
		"RESOURCE_EXHAUSTED",
		"Quota exceeded for this model",
		"quota has been exhausted",
		"out of credits",
		"usage limit reached",
		"rate limit exceeded",
		"HTTP 429: project quota unavailable",
		"rate limit error from API status 429",
	}
	for _, input := range cases {
		if !Detected(input) {
			t.Errorf("expected quota detection for %q", input)
		}
	}
}

func TestDetectedRejectsDiscussion(t *testing.T) {
	t.Parallel()
	cases := []string{
		"Please implement quota recovery.",
		"This function stores quota values.",
		"The endpoint returned 429 records.",
		"The rate limit setting is documented here.",
	}
	for _, input := range cases {
		if Detected(input) {
			t.Errorf("unexpected quota detection for %q", input)
		}
	}
}

func TestDetectedResponseIgnoresQuotaSignalInsideEchoedTask(t *testing.T) {
	task := "Write a test for RESOURCE_EXHAUSTED: quota exceeded."
	output := "> Write a test for RESOURCE_EXHAUSTED: quota exceeded.\n\n  Implemented the test successfully."
	if DetectedResponse(output, task) {
		t.Fatal("echoed task was treated as a quota error")
	}
}

func TestDetectedResponseFindsQuotaSignalAfterEchoedTask(t *testing.T) {
	task := "Run the build."
	output := "> Run the build.\n\n  RESOURCE_EXHAUSTED: quota exceeded"
	if !DetectedResponse(output, task) {
		t.Fatal("agent quota error was not detected")
	}
}

func TestDetectedResponseHandlesWrappedTask(t *testing.T) {
	task := "Create the feature and mention RESOURCE_EXHAUSTED: quota exceeded in its test fixture."
	output := "> Create the feature and mention RESOURCE_EXHAUSTED: quota exceeded\n  in its test fixture.\n\n  Feature complete."
	if DetectedResponse(output, task) {
		t.Fatal("wrapped echoed task was treated as a quota error")
	}
}

func TestNewOutput(t *testing.T) {
	t.Parallel()
	if got := NewOutput("old\n", "old\nnew\n"); got != "new" {
		t.Fatalf("prefix delta = %q", got)
	}

	before := "discarded\nshared tail"
	after := "new viewport\nshared tail\nlatest result"
	if got := NewOutput(before, after); got != "latest result" {
		t.Fatalf("tail delta = %q", got)
	}
}

func FuzzDetectedDoesNotPanic(f *testing.F) {
	for _, seed := range []string{"", "quota", "RESOURCE_EXHAUSTED", "429 quota", "normal output"} {
		f.Add(seed)
	}
	f.Fuzz(func(t *testing.T, input string) {
		_ = Detected(input)
	})
}

func FuzzNewOutputPrefix(f *testing.F) {
	f.Add("before", "after")
	f.Add("", "result")
	f.Add("line one\nline two", "new line")
	f.Fuzz(func(t *testing.T, before, suffix string) {
		// Terminal transcript additions start on a line boundary. Without one,
		// arbitrary fuzz bytes can fuse with UI chrome and make the intended
		// boundary unknowable.
		if before != "" && suffix != "" && !strings.HasSuffix(before, "\n") && !strings.HasPrefix(suffix, "\n") {
			return
		}
		after := before + suffix
		got := NewOutput(before, after)
		want := cleanTerminalDelta(suffix)
		if got != want {
			t.Fatalf("got=%q want=%q", got, want)
		}
	})
}

func TestNewOutputIgnoresRepeatedAgyFooter(t *testing.T) {
	before := `> Create first.txt

  I have created first.txt.

  No other files were changed.

────────────────────────────────────────────────────────────
>
────────────────────────────────────────────────────────────
? for shortcuts                         accept-edits · Gemini 3.8 Flash · high`
	after := `> Create first.txt
  I have created first.txt.

  No other files were changed.

────────────────────────────────────────────────────────────
> Create second.txt

  I have created second.txt.

  No other files were changed.

────────────────────────────────────────────────────────────
>
────────────────────────────────────────────────────────────
? for shortcuts                         accept-edits · Gemini 3.8 Flash · high`

	got := NewOutput(before, after)
	want := "> Create second.txt\n\n  I have created second.txt.\n\n  No other files were changed."
	if got != want {
		t.Fatalf("NewOutput() = %q, want %q", got, want)
	}
}
