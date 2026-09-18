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

func TestDetectedResponseIgnoresModelReasoningAndDiscussion(t *testing.T) {
	task := "Write rate limit handler"
	cases := []string{
		`Thinking Process:
1. The user asks for rate limit error handling.
2. When the API returns 429 rate limit exceeded or quota exhausted, the client should back off.
3. Writing the response now.

Here is the implementation:
The client catches rate limit exceeded and retries up to 3 times.`,
		`<thinking>
Checking if the error code is RESOURCE_EXHAUSTED.
If it is HTTP 429 rate limit exceeded, we should retry.
</thinking>
Here is the code:
func handle(err error) {
    if err == codes.ResourceExhausted {
        log.Println("rate limit exceeded, retrying")
    }
}`,
		`I have added the error handling code:
` + "```go" + `
if resp.StatusCode == 429 {
    // quota exceeded handling
    time.Sleep(backoff)
}
` + "```",
	}
	for i, c := range cases {
		if DetectedResponse(c, task) {
			t.Errorf("case %d: model reasoning or code discussing rate limits was falsely treated as quota error: %s", i, c)
		}
	}
}

func TestDetectedResponseIgnoresEchoedTaskInBox(t *testing.T) {
	task := "Fix the bug where the server crashes with RESOURCE_EXHAUSTED: quota exceeded"
	output := `╭─ Prompt ────────────────────────────────────────────────────────────╮
│ Fix the bug where the server crashes with RESOURCE_EXHAUSTED:       │
│ quota exceeded                                                      │
╰─────────────────────────────────────────────────────────────────────╯
I have inspected the bug and fixed the crash.`
	if DetectedResponse(output, task) {
		t.Fatal("echoed task in prompt box was falsely treated as quota error")
	}
}

func TestDetectedResponseDetectsRealProviderError(t *testing.T) {
	task := "Implement feature"
	cases := []string{
		"> Implement feature\n\nAPI Error: 429 RESOURCE_EXHAUSTED: quota exceeded for model gemini-3.8-flash",
		"> Implement feature\n\nError: You have exceeded your current quota. Please check your plan.",
		"> Implement feature\n\nHTTP 429: project quota unavailable",
		"> Implement feature\n\nError: rate limit exceeded",
		"> Implement feature\n\nrate limit error from API status 429",
		"> Implement feature\n\nrpc error: code = ResourceExhausted desc = Quota exceeded for quota metric 'Queries' and limit 'Queries per minute'",
		"> Implement feature\n\nFATAL: usage limit reached for account test@example.com",
		"> Implement feature\n\n429 Too Many Requests",
		"> Implement feature\n\nHTTP 429: Too Many Requests",
		"> Implement feature\n\n429: quota exhausted",
		"> Implement feature\n\ngoogle.api_core.exceptions.ResourceExhausted: 429 Quota exceeded for metric",
		"> Implement feature\n\nexceptions.ResourceExhausted: rate limit reached",
	}
	for _, c := range cases {
		if !DetectedResponse(c, task) {
			t.Errorf("expected quota detection for real provider error: %s", c)
		}
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

func TestDetectedResponseRejectsProseAndCodeDiscussingQuotaMatcher(t *testing.T) {
	task := "Implement a quota detector and matcher"
	cases := []struct {
		name   string
		output string
	}{
		{
			name: "prose mentioning resource exhausted and 429",
			output: `> Implement a quota detector and matcher

I have implemented the detector function. It checks for RESOURCE_EXHAUSTED and HTTP 429 status codes without false positives. All unit tests are passing now.`,
		},
		{
			name: "prose with error keyword in descriptive context",
			output: `> Implement a quota detector and matcher

The previous code had an issue where discussing a rate limit error or 429 response triggered false recovery. I fixed the regular expressions.`,
		},
		{
			name: "prose mentioning HTTP 429 Too Many Requests outside code blocks",
			output: `> Implement a quota detector and matcher

I implemented handling for HTTP 429: Too Many Requests in the client retry loop.`,
		},
		{
			name: "prose mentioning code = ResourceExhausted outside code blocks",
			output: `> Implement a quota detector and matcher

The parser recognizes code = ResourceExhausted when parsing status objects.`,
		},
		{
			name: "prose mentioning exceptions.ResourceExhausted outside code blocks",
			output: `> Implement a quota detector and matcher

In the API wrapper, we catch exceptions.ResourceExhausted and log a warning.`,
		},
		{
			name:   "fenced markdown code block with error strings",
			output: "> Implement a quota detector and matcher\n\nHere is the implementation:\n```go\nfunc isQuotaError(err error) bool {\n\tif strings.Contains(err.Error(), \"RESOURCE_EXHAUSTED\") || strings.Contains(err.Error(), \"429\") {\n\t\treturn true\n\t}\n\treturn false\n}\n```\nAll tests pass.",
		},
	}

	for _, tc := range cases {
		tc := tc
		t.Run(tc.name, func(t *testing.T) {
			if DetectedResponse(tc.output, task) {
				t.Fatalf("unexpected quota error detection for: %s", tc.output)
			}
		})
	}
}

func TestDetectedResponseDetectsErrorWhenTaskContainsErrorPhrase(t *testing.T) {
	task := "Investigate RESOURCE_EXHAUSTED and HTTP 429 failures in the backend service"
	cases := []struct {
		name   string
		output string
	}{
		{
			name: "task contains error phrase followed by real provider error banner",
			output: `> Investigate RESOURCE_EXHAUSTED and HTTP 429 failures in the backend service

API Error: 429 RESOURCE_EXHAUSTED: quota exceeded for model gemini-3.8-flash`,
		},
		{
			name:   "task contains error phrase followed by standalone resource exhausted line",
			output: "> Investigate RESOURCE_EXHAUSTED and HTTP 429 failures in the backend service\n\nRESOURCE_EXHAUSTED",
		},
		{
			name: "task contains error phrase followed by rpc error line",
			output: `> Investigate RESOURCE_EXHAUSTED and HTTP 429 failures in the backend service

rpc error: code = ResourceExhausted desc = RESOURCE_EXHAUSTED: quota exceeded`,
		},
		{
			name: "task contains error phrase followed by 429 quota line",
			output: `> Investigate RESOURCE_EXHAUSTED and HTTP 429 failures in the backend service

429: quota exhausted`,
		},
	}

	for _, tc := range cases {
		tc := tc
		t.Run(tc.name, func(t *testing.T) {
			if !DetectedResponse(tc.output, task) {
				t.Fatalf("expected quota error detection for: %s", tc.output)
			}
		})
	}
}
