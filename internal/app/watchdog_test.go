package app

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	proc "github.com/kazimshah39/cagy/internal/process"
	"github.com/kazimshah39/cagy/internal/transcript"
)

func TestRunDeveloperTaskDetectsSilentQuotaExhaustion(t *testing.T) {
	runner := &scriptedRunner{t: t, steps: []runStep{
		{want: []string{"herdr", "agent", "prompt", "developer", "do the work", "--wait", "--timeout", "300000"}, result: jsonError("timeout", "timed out waiting for agent status")},
		{want: []string{"herdr", "agent", "read", "developer", "--source", "recent-unwrapped", "--lines", "400"}, result: textResult("old\n⠋ Working...\n")},
		{want: []string{"herdr", "agent", "get", "developer"}, result: agentJSONWithSession("w1:p2", "w1", "/tmp/project", "working", testConversationID)},
		{want: agyProbeArgs("/model"), result: agyModelResult("gemini-3.8-flash-high", "Gemini 3.8 Flash (High)")},
		{want: agyProbeArgs("/quota"), result: agyQuotaResult("Gemini Models", 0, 0.73)},
	}}
	application := New(runner, &strings.Builder{}, &strings.Builder{})

	result, err := application.runDeveloperTask(context.Background(), "developer", "do the work", "old\n", transcript.Checkpoint{})
	if err != nil {
		t.Fatal(err)
	}
	if !result.quotaExhausted {
		t.Fatal("expected silent quota exhaustion")
	}
	runner.assertDone()
	if countCommand(runner.calls, "herdr", "agent", "prompt") != 1 {
		t.Fatalf("task was not submitted exactly once: %#v", runner.calls)
	}
}

func TestRunDeveloperTaskReturnsTranscriptEvenWhenPromptWaitTimesOut(t *testing.T) {
	brainRoot := t.TempDir()
	writeAgyTranscript(t, brainRoot, testConversationID, "do the work", "Finished safely.")
	runner := &scriptedRunner{t: t, steps: []runStep{
		{want: []string{"herdr", "agent", "prompt", "developer", "do the work", "--wait", "--timeout", "300000"}, result: jsonError("timeout", "timed out waiting for agent status")},
		{want: []string{"herdr", "agent", "read", "developer", "--source", "recent-unwrapped", "--lines", "400"}, result: textResult("old\nFinished safely.\n")},
		{want: []string{"herdr", "agent", "get", "developer"}, result: agentJSONWithSession("w1:p2", "w1", "/tmp/project", "done", testConversationID)},
		{want: agyProbeArgs("/model"), result: agyModelResult("gemini-3.8-flash-high", "Gemini 3.8 Flash (High)")},
		{want: agyProbeArgs("/quota"), result: agyQuotaResult("Gemini Models", 0.8, 0.9)},
		{want: []string{"herdr", "agent", "wait", "developer", "--until", "blocked", "--timeout", "1000"}, result: jsonError("timeout", "timed out")},
		{want: []string{"herdr", "agent", "read", "developer", "--source", "visible", "--lines", "80"}, result: textResult(">\n────────────────────\n? for shortcuts\n")},
	}}
	application := New(runner, &strings.Builder{}, &strings.Builder{})
	application.agyBrainRoot = brainRoot
	application.transcriptWait = time.Second

	result, err := application.runDeveloperTask(context.Background(), "developer", "do the work", "old\n", transcript.Checkpoint{})
	if err != nil {
		t.Fatal(err)
	}
	if result.output != "Finished safely." {
		t.Fatalf("output = %q", result.output)
	}
	runner.assertDone()
	if countCommand(runner.calls, "herdr", "agent", "prompt") != 1 {
		t.Fatalf("task was submitted more than once: %#v", runner.calls)
	}
}

func TestRunDeveloperTaskFalseSettledStateContinuesUntilTranscriptFinal(t *testing.T) {
	brainRoot := t.TempDir()
	writeAgyTaskOnly(t, brainRoot, testConversationID, "do the work")
	runner := &scriptedRunner{t: t, steps: []runStep{
		{want: []string{"herdr", "agent", "prompt", "developer", "do the work", "--wait", "--timeout", "300000"}, result: agentJSONWithSession("w1:p2", "w1", "/tmp/project", "done", testConversationID)},
		{want: []string{"herdr", "agent", "read", "developer", "--source", "recent-unwrapped", "--lines", "400"}, result: textResult("old\nesc to cancel\n")},
		{
			want:   []string{"herdr", "agent", "wait", "developer", "--until", "blocked", "--timeout", "1000"},
			result: jsonError("timeout", "timed out waiting for agent status"),
			before: func() { appendAgyAnswer(t, brainRoot, testConversationID, "Completed after false done.") },
		},
		{want: []string{"herdr", "agent", "read", "developer", "--source", "visible", "--lines", "80"}, result: textResult(">\n────────────────────\n? for shortcuts\n")},
	}}
	application := New(runner, &strings.Builder{}, &strings.Builder{})
	application.agyBrainRoot = brainRoot
	application.transcriptWait = time.Second

	result, err := application.runDeveloperTask(context.Background(), "developer", "do the work", "old\n", transcript.Checkpoint{})
	if err != nil {
		t.Fatal(err)
	}
	if result.output != "Completed after false done." {
		t.Fatalf("output=%q", result.output)
	}
	runner.assertDone()
	if countCommand(runner.calls, "herdr", "agent", "prompt") != 1 {
		t.Fatalf("task was resubmitted: %#v", runner.calls)
	}
}

func TestRunDeveloperTaskToleratesRepeatedTransientSettledStates(t *testing.T) {
	brainRoot := t.TempDir()
	writeAgyTaskOnly(t, brainRoot, testConversationID, "do the work")
	runner := &scriptedRunner{t: t, steps: []runStep{
		{want: []string{"herdr", "agent", "prompt", "developer", "do the work", "--wait", "--timeout", "300000"}, result: agentJSONWithSession("w1:p2", "w1", "/tmp/project", "idle", testConversationID)},
		{want: []string{"herdr", "agent", "read", "developer", "--source", "recent-unwrapped", "--lines", "400"}, result: textResult("old\nesc to cancel\n")},
		{want: []string{"herdr", "agent", "wait", "developer", "--until", "blocked", "--timeout", "1000"}, result: jsonError("timeout", "timed out")},
		{want: []string{"herdr", "agent", "read", "developer", "--source", "visible", "--lines", "80"}, result: textResult("response streaming\nesc to cancel\n")},
		{
			want:   []string{"herdr", "agent", "wait", "developer", "--until", "blocked", "--timeout", "1000"},
			result: jsonError("timeout", "timed out"),
			before: func() { appendAgyAnswer(t, brainRoot, testConversationID, "Finished after repeated false states.") },
		},
		{want: []string{"herdr", "agent", "read", "developer", "--source", "visible", "--lines", "80"}, result: textResult(">\n────────────────────\n? for shortcuts\n")},
	}}
	application := New(runner, &strings.Builder{}, &strings.Builder{})
	application.agyBrainRoot = brainRoot
	application.transcriptWait = time.Second

	result, err := application.runDeveloperTask(context.Background(), "developer", "do the work", "old\n", transcript.Checkpoint{})
	if err != nil {
		t.Fatal(err)
	}
	if result.output != "Finished after repeated false states." {
		t.Fatalf("output=%q", result.output)
	}
	runner.assertDone()
	if countCommand(runner.calls, "herdr", "agent", "prompt") != 1 {
		t.Fatalf("task was resubmitted: %#v", runner.calls)
	}
}

func TestRunDeveloperTaskDoesNotReturnIntermediatePlannerUpdate(t *testing.T) {
	brainRoot := t.TempDir()
	writeAgyTaskOnly(t, brainRoot, testConversationID, "do the work")
	appendAgyAnswer(t, brainRoot, testConversationID, "I am still checking the files.")
	runner := &scriptedRunner{t: t, steps: []runStep{
		{want: []string{"herdr", "agent", "prompt", "developer", "do the work", "--wait", "--timeout", "300000"}, result: agentJSONWithSession("w1:p2", "w1", "/tmp/project", "done", testConversationID)},
		{want: []string{"herdr", "agent", "read", "developer", "--source", "recent-unwrapped", "--lines", "400"}, result: textResult("old\nI am still checking the files.\nesc to cancel\n")},
		{want: []string{"herdr", "agent", "wait", "developer", "--until", "blocked", "--timeout", "1000"}, result: jsonError("timeout", "timed out")},
		{want: []string{"herdr", "agent", "read", "developer", "--source", "visible", "--lines", "80"}, result: textResult("I am still checking the files.\n────────────────────\nesc to cancel\n")},
		{
			want:   []string{"herdr", "agent", "wait", "developer", "--until", "blocked", "--timeout", "1000"},
			result: jsonError("timeout", "timed out"),
			before: func() { appendAgyAnswer(t, brainRoot, testConversationID, "Finished after the tools completed.") },
		},
		{want: []string{"herdr", "agent", "read", "developer", "--source", "visible", "--lines", "80"}, result: textResult(">\n────────────────────\n? for shortcuts\n")},
	}}
	application := New(runner, &strings.Builder{}, &strings.Builder{})
	application.agyBrainRoot = brainRoot
	application.transcriptWait = time.Second

	result, err := application.runDeveloperTask(context.Background(), "developer", "do the work", "old\n", transcript.Checkpoint{})
	if err != nil {
		t.Fatal(err)
	}
	if result.output != "Finished after the tools completed." {
		t.Fatalf("output=%q", result.output)
	}
	runner.assertDone()
}

func TestRunDeveloperTaskDoesNotReturnTerminalScrollbackWhenBlocked(t *testing.T) {
	runner := &scriptedRunner{t: t, steps: []runStep{
		{want: []string{"herdr", "agent", "prompt", "developer", "do the work", "--wait", "--timeout", "300000"}, result: agentJSONWithSession("w1:p2", "w1", "/tmp/project", "blocked", testConversationID)},
		{want: []string{"herdr", "agent", "read", "developer", "--source", "recent-unwrapped", "--lines", "400"}, result: textResult("old\ninternal tool details\n")},
	}}
	application := New(runner, &strings.Builder{}, &strings.Builder{})

	result, err := application.runDeveloperTask(context.Background(), "developer", "do the work", "old\n", transcript.Checkpoint{})
	if err != nil {
		t.Fatal(err)
	}
	if result.agent.AgentStatus != "blocked" || result.output != "" {
		t.Fatalf("result=%+v", result)
	}
	runner.assertDone()
}

func TestRunDeveloperTaskBlockedWaitDoesNotReturnTerminalScrollback(t *testing.T) {
	runner := &scriptedRunner{t: t, steps: []runStep{
		{want: []string{"herdr", "agent", "prompt", "developer", "do the work", "--wait", "--timeout", "300000"}, result: agentJSONWithSession("w1:p2", "w1", "/tmp/project", "done", testConversationID)},
		{want: []string{"herdr", "agent", "read", "developer", "--source", "recent-unwrapped", "--lines", "400"}, result: textResult("old\ninternal tool details\n")},
		{want: []string{"herdr", "agent", "wait", "developer", "--until", "blocked", "--timeout", "1000"}, result: agentJSONWithSession("w1:p2", "w1", "/tmp/project", "blocked", testConversationID)},
	}}
	application := New(runner, &strings.Builder{}, &strings.Builder{})

	result, err := application.runDeveloperTask(context.Background(), "developer", "do the work", "old\n", transcript.Checkpoint{})
	if err != nil {
		t.Fatal(err)
	}
	if result.agent.AgentStatus != "blocked" || result.output != "" {
		t.Fatalf("result=%+v", result)
	}
	runner.assertDone()
}

func TestRunDeveloperTaskPromptStallDoesNotConsumeFiveMinuteSegment(t *testing.T) {
	brainRoot := t.TempDir()
	writeAgyTaskOnly(t, brainRoot, testConversationID, "do the work")
	runner := &scriptedRunner{t: t, steps: []runStep{
		{want: []string{"herdr", "agent", "prompt", "developer", "do the work", "--wait", "--timeout", "300000"}, result: jsonError("agent_prompt_stalled", "no activity observed")},
		{want: []string{"herdr", "agent", "read", "developer", "--source", "recent-unwrapped", "--lines", "400"}, result: textResult("old\n")},
		{want: []string{"herdr", "agent", "get", "developer"}, result: agentJSONWithSession("w1:p2", "w1", "/tmp/project", "working", testConversationID)},
		{
			want:   []string{"herdr", "agent", "wait", "developer", "--until", "blocked", "--timeout", "1000"},
			result: jsonError("timeout", "timed out"),
			before: func() { appendAgyAnswer(t, brainRoot, testConversationID, "Finished after the stalled status signal.") },
		},
		{want: []string{"herdr", "agent", "read", "developer", "--source", "visible", "--lines", "80"}, result: textResult(">\n────────────────────\n? for shortcuts\n")},
	}}
	application := New(runner, &strings.Builder{}, &strings.Builder{})
	application.agyBrainRoot = brainRoot
	application.transcriptWait = time.Second

	result, err := application.runDeveloperTask(context.Background(), "developer", "do the work", "old\n", transcript.Checkpoint{})
	if err != nil {
		t.Fatal(err)
	}
	if result.output != "Finished after the stalled status signal." || result.quotaExhausted {
		t.Fatalf("result=%+v", result)
	}
	runner.assertDone()
	if countCommand(runner.calls, "agy", "-p") != 0 {
		t.Fatalf("a five-minute quota probe ran after a five-second stall: %#v", runner.calls)
	}
}

func TestRunDeveloperTaskIgnoresOldQuotaTextOnVisibleScreen(t *testing.T) {
	brainRoot := t.TempDir()
	writeAgyTranscript(t, brainRoot, testConversationID, "do the work", "Finished safely.")
	runner := &scriptedRunner{t: t, steps: []runStep{
		{want: []string{"herdr", "agent", "prompt", "developer", "do the work", "--wait", "--timeout", "300000"}, result: agentJSONWithSession("w1:p2", "w1", "/tmp/project", "done", testConversationID)},
		{want: []string{"herdr", "agent", "read", "developer", "--source", "recent-unwrapped", "--lines", "400"}, result: textResult("old\nFinished safely.\n")},
		{want: []string{"herdr", "agent", "wait", "developer", "--until", "blocked", "--timeout", "1000"}, result: jsonError("timeout", "timed out")},
		{want: []string{"herdr", "agent", "read", "developer", "--source", "visible", "--lines", "80"}, result: textResult("Old turn: RESOURCE_EXHAUSTED quota exceeded\n────────────────────\n>\n────────────────────\n? for shortcuts\n")},
	}}
	application := New(runner, &strings.Builder{}, &strings.Builder{})
	application.agyBrainRoot = brainRoot
	application.transcriptWait = time.Second

	result, err := application.runDeveloperTask(context.Background(), "developer", "do the work", "old\n", transcript.Checkpoint{})
	if err != nil {
		t.Fatal(err)
	}
	if result.output != "Finished safely." || result.quotaExhausted {
		t.Fatalf("result=%+v", result)
	}
	runner.assertDone()
}

func TestRunDeveloperTaskWaitsForTranscriptBackgroundTask(t *testing.T) {
	brainRoot := t.TempDir()
	task := "wait for background completion"
	taskID := testConversationID + "/task-741"
	writeAgyTaskOnly(t, brainRoot, testConversationID, task)
	appendAgyEvent(t, brainRoot, testConversationID, "MODEL", "GENERIC", "RUNNING", "Tool is running as a background task with task id: "+taskID)
	appendAgyAnswer(t, brainRoot, testConversationID, "Still waiting.")
	steps := []runStep{
		{want: []string{"herdr", "agent", "prompt", "developer", task, "--wait", "--timeout", "300000"}, result: agentJSONWithSession("w1:p2", "w1", "/tmp/project", "done", testConversationID)},
		{want: []string{"herdr", "agent", "read", "developer", "--source", "recent-unwrapped", "--lines", "400"}, result: textResult("old\nStill waiting.\n")},
		{want: []string{"herdr", "agent", "wait", "developer", "--until", "blocked", "--timeout", "1000"}, result: jsonError("timeout", "timed out")},
		{want: []string{"herdr", "agent", "read", "developer", "--source", "visible", "--lines", "80"}, result: textResult(">\n? for shortcuts\n")},
		{want: []string{"herdr", "agent", "wait", "developer", "--until", "blocked", "--timeout", "1000"}, before: func() {
			appendAgyEvent(t, brainRoot, testConversationID, "SYSTEM", "SYSTEM_MESSAGE", "DONE", "sender="+taskID+" content=finished")
		}, result: jsonError("timeout", "timed out")},
		{want: []string{"herdr", "agent", "read", "developer", "--source", "visible", "--lines", "80"}, result: textResult(">\n? for shortcuts\n")},
		{want: []string{"herdr", "agent", "wait", "developer", "--until", "blocked", "--timeout", "1000"}, before: func() {
			appendAgyAnswer(t, brainRoot, testConversationID, "Final background answer.")
		}, result: jsonError("timeout", "timed out")},
		{want: []string{"herdr", "agent", "read", "developer", "--source", "visible", "--lines", "80"}, result: textResult(">\n? for shortcuts\n")},
	}
	runner := &scriptedRunner{t: t, steps: steps}
	application := New(runner, &strings.Builder{}, &strings.Builder{})
	application.agyBrainRoot = brainRoot
	application.transcriptWait = time.Second
	application.missingTranscriptWait = 5 * time.Second

	result, err := application.runDeveloperTask(context.Background(), "developer", task, "old\n", transcript.Checkpoint{})
	if err != nil {
		t.Fatal(err)
	}
	if result.output != "Final background answer." {
		t.Fatalf("result=%+v", result)
	}
	runner.assertDone()
}

func TestRunDeveloperTaskStableIdleWithoutTranscriptFailsClearly(t *testing.T) {
	brainRoot := t.TempDir()
	writeAgyTaskOnly(t, brainRoot, testConversationID, "do the work")
	steps := []runStep{
		{want: []string{"herdr", "agent", "prompt", "developer", "do the work", "--wait", "--timeout", "300000"}, result: agentJSONWithSession("w1:p2", "w1", "/tmp/project", "done", testConversationID)},
		{want: []string{"herdr", "agent", "read", "developer", "--source", "recent-unwrapped", "--lines", "400"}, result: textResult("old\n? for shortcuts\n")},
	}
	for range 3 {
		steps = append(steps,
			runStep{want: []string{"herdr", "agent", "wait", "developer", "--until", "blocked", "--timeout", "1000"}, result: jsonError("timeout", "timed out")},
			runStep{want: []string{"herdr", "agent", "read", "developer", "--source", "visible", "--lines", "80"}, result: textResult(">\n? for shortcuts\n")},
		)
	}
	runner := &scriptedRunner{t: t, steps: steps}
	application := New(runner, &strings.Builder{}, &strings.Builder{})
	application.agyBrainRoot = brainRoot
	application.missingTranscriptWait = 3 * time.Second

	_, err := application.runDeveloperTask(context.Background(), "developer", "do the work", "old\n", transcript.Checkpoint{})
	if err == nil || !strings.Contains(err.Error(), "without a complete transcript response") {
		t.Fatalf("error=%v", err)
	}
	runner.assertDone()
	if countCommand(runner.calls, "herdr", "agent", "prompt") != 1 {
		t.Fatalf("task was resubmitted: %#v", runner.calls)
	}
}

func TestRunDeveloperTaskProbeFailureDoesNotSwitchBlindly(t *testing.T) {
	brainRoot := t.TempDir()
	writeAgyTaskOnly(t, brainRoot, testConversationID, "do the work")
	runner := &scriptedRunner{t: t, steps: []runStep{
		{want: []string{"herdr", "agent", "prompt", "developer", "do the work", "--wait", "--timeout", "300000"}, result: jsonError("timeout", "timed out waiting for agent status")},
		{want: []string{"herdr", "agent", "read", "developer", "--source", "recent-unwrapped", "--lines", "400"}, result: textResult("old\n⠋ Working...\n")},
		{want: []string{"herdr", "agent", "get", "developer"}, result: agentJSONWithSession("w1:p2", "w1", "/tmp/project", "working", testConversationID)},
		{want: agyProbeArgs("/model"), result: textResult("not-json")},
		{
			want:   []string{"herdr", "agent", "wait", "developer", "--until", "blocked", "--timeout", "1000"},
			result: jsonError("timeout", "timed out"),
			before: func() { appendAgyAnswer(t, brainRoot, testConversationID, "Finished after the probe failed.") },
		},
		{want: []string{"herdr", "agent", "read", "developer", "--source", "visible", "--lines", "80"}, result: textResult(">\n────────────────────\n? for shortcuts\n")},
	}}
	application := New(runner, &strings.Builder{}, &strings.Builder{})
	application.agyBrainRoot = brainRoot
	application.transcriptWait = time.Second

	result, err := application.runDeveloperTask(context.Background(), "developer", "do the work", "old\n", transcript.Checkpoint{})
	if err != nil {
		t.Fatal(err)
	}
	if result.quotaExhausted {
		t.Fatal("probe failure must not be treated as empty quota")
	}
	if result.output != "Finished after the probe failed." {
		t.Fatalf("output = %q", result.output)
	}
	runner.assertDone()
}

func TestRunDeveloperTaskStopsAfterThirtyMinuteWaitBudget(t *testing.T) {
	if developerWaitSegmentMS != 5*60*1000 || developerMaxWaits != 6 {
		t.Fatalf("wait policy: segment=%d max=%d", developerWaitSegmentMS, developerMaxWaits)
	}
	runner := &watchdogLoopRunner{}
	var stderr strings.Builder
	application := New(runner, &strings.Builder{}, &stderr)
	application.developerPoll = 5 * time.Minute

	_, err := application.runDeveloperTask(context.Background(), "developer", "do the work", "old\n", transcript.Checkpoint{})
	if err == nil || !strings.Contains(err.Error(), "30 minutes") {
		t.Fatalf("error = %v", err)
	}
	if runner.promptCalls != 1 {
		t.Fatalf("prompt calls = %d", runner.promptCalls)
	}
	if runner.waitCalls != developerMaxWaits-1 {
		t.Fatalf("wait calls = %d, want %d", runner.waitCalls, developerMaxWaits-1)
	}
	if runner.modelCalls != developerMaxWaits || runner.quotaCalls != developerMaxWaits {
		t.Fatalf("probe calls: model=%d quota=%d want=%d", runner.modelCalls, runner.quotaCalls, developerMaxWaits)
	}
	status := stderr.String()
	if !strings.Contains(status, "submitting one task") || !strings.Contains(status, "still running after 5m0s") || !strings.Contains(status, "still running after 30m0s") {
		t.Fatalf("missing lifecycle heartbeat in stderr: %q", status)
	}
}

func TestAgyVisibleStateReadsOnlyTheFooter(t *testing.T) {
	tests := []struct {
		name   string
		screen string
		want   string
	}{
		{
			name:   "working marker wins in footer",
			screen: ">\n────────────────────\n? for shortcuts · esc to cancel\n",
			want:   "working",
		},
		{
			name:   "background task is still working",
			screen: ">\n────────────────────\n? for shortcuts · 1 task(s) · /tasks\n",
			want:   "working",
		},
		{
			name:   "real idle footer",
			screen: ">\n────────────────────\n? for shortcuts · Gemini\n",
			want:   "idle",
		},
		{
			name:   "unknown footer",
			screen: ">\n────────────────────\nstarting up\n",
			want:   "unknown",
		},
		{
			name:   "quoted marker outside footer is ignored",
			screen: "The words esc to cancel were documented above.\n────────────────────\n>\n────────────────────\n? for shortcuts · Gemini\n",
			want:   "idle",
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			if got := agyVisibleState(test.screen); got != test.want {
				t.Fatalf("state=%q want=%q", got, test.want)
			}
		})
	}
}

func TestQuotaGroupForModel(t *testing.T) {
	tests := []struct {
		model agyModel
		group string
		ok    bool
	}{
		{model: agyModel{ID: "gemini-3.8-flash-high"}, group: "Gemini Models", ok: true},
		{model: agyModel{Label: "Claude Opus 4.1"}, group: "Claude and GPT models", ok: true},
		{model: agyModel{ID: "gpt-5.4"}, group: "Claude and GPT models", ok: true},
		{model: agyModel{ID: "unknown-model"}, ok: false},
	}
	for _, test := range tests {
		group, ok := quotaGroupForModel(test.model)
		if group != test.group || ok != test.ok {
			t.Fatalf("model=%+v group=%q ok=%v", test.model, group, ok)
		}
	}
}

func TestQuotaGroupExhaustedUsesSeparateWeeklyAndFiveHourThresholds(t *testing.T) {
	tests := []struct {
		name      string
		weekly    float64
		fiveHour  float64
		exhausted bool
	}{
		{name: "both safely above", weekly: 0.031, fiveHour: 0.021, exhausted: false},
		{name: "weekly at boundary", weekly: 0.03, fiveHour: 0.9, exhausted: true},
		{name: "weekly below boundary", weekly: 0.029, fiveHour: 0.9, exhausted: true},
		{name: "five hour at boundary", weekly: 0.9, fiveHour: 0.02, exhausted: true},
		{name: "five hour below boundary", weekly: 0.9, fiveHour: 0.019, exhausted: true},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			weekly := test.weekly
			fiveHour := test.fiveHour
			status := agyQuota{Groups: []agyQuotaGroup{{
				Name: "Gemini Models",
				Buckets: []agyQuotaBucket{
					{ID: "gemini-weekly", RemainingFraction: &weekly},
					{ID: "gemini-5h", RemainingFraction: &fiveHour},
				},
			}}}
			exhausted, err := quotaGroupExhausted(status, "Gemini Models")
			if err != nil {
				t.Fatal(err)
			}
			if exhausted != test.exhausted {
				t.Fatalf("exhausted=%v want=%v", exhausted, test.exhausted)
			}
		})
	}
}

func TestQuotaGroupRequiresBothTimeBuckets(t *testing.T) {
	weekly := 0.8
	status := agyQuota{Groups: []agyQuotaGroup{{
		Name:    "Gemini Models",
		Buckets: []agyQuotaBucket{{ID: "gemini-weekly", RemainingFraction: &weekly}},
	}}}
	exhausted, err := quotaGroupExhausted(status, "Gemini Models")
	if err == nil || exhausted {
		t.Fatalf("exhausted=%v err=%v", exhausted, err)
	}
}

type watchdogLoopRunner struct {
	promptCalls int
	waitCalls   int
	modelCalls  int
	quotaCalls  int
}

func (r *watchdogLoopRunner) LookPath(name string) (string, error) { return "/bin/" + name, nil }

func (r *watchdogLoopRunner) Run(_ context.Context, args ...string) (proc.Result, error) {
	switch {
	case hasPrefix(args, "herdr", "agent", "prompt"):
		r.promptCalls++
		return jsonError("timeout", "timed out waiting for agent status"), nil
	case hasPrefix(args, "herdr", "agent", "wait"):
		r.waitCalls++
		return jsonError("timeout", "timed out waiting for agent status"), nil
	case hasPrefix(args, "herdr", "agent", "get"):
		return agentJSONWithSession("w1:p2", "w1", "/tmp/project", "working", testConversationID), nil
	case hasPrefix(args, "herdr", "agent", "read"):
		return textResult("old\n⠋ Working...\n"), nil
	case slicesEqual(args, agyProbeArgs("/model")):
		r.modelCalls++
		return agyModelResult("gemini-3.8-flash-high", "Gemini 3.8 Flash (High)"), nil
	case slicesEqual(args, agyProbeArgs("/quota")):
		r.quotaCalls++
		return agyQuotaResult("Gemini Models", 0.8, 0.9), nil
	default:
		return proc.Result{}, errors.New("unexpected command: " + strings.Join(args, " "))
	}
}

func (r *watchdogLoopRunner) RunAttached(_ []string, _ []string) error { return nil }

func writeAgyTaskOnly(t *testing.T, brainRoot, sessionID, task string) {
	t.Helper()
	path := filepath.Join(brainRoot, sessionID, ".system_generated", "logs", "transcript.jsonl")
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		t.Fatal(err)
	}
	file, err := os.Create(path)
	if err != nil {
		t.Fatal(err)
	}
	defer file.Close()
	encoder := json.NewEncoder(file)
	if err := encoder.Encode(map[string]string{
		"source":  "USER_EXPLICIT",
		"type":    "USER_INPUT",
		"status":  "DONE",
		"content": "<USER_REQUEST>\n" + task + "\n</USER_REQUEST>",
	}); err != nil {
		t.Fatal(err)
	}
}

func appendAgyAnswer(t *testing.T, brainRoot, sessionID, answer string) {
	t.Helper()
	path := filepath.Join(brainRoot, sessionID, ".system_generated", "logs", "transcript.jsonl")
	file, err := os.OpenFile(path, os.O_APPEND|os.O_WRONLY, 0)
	if err != nil {
		t.Fatal(err)
	}
	defer file.Close()
	encoder := json.NewEncoder(file)
	if err := encoder.Encode(map[string]string{
		"source":  "MODEL",
		"type":    "PLANNER_RESPONSE",
		"status":  "DONE",
		"content": answer,
	}); err != nil {
		t.Fatal(err)
	}
}

func appendAgyEvent(t *testing.T, brainRoot, sessionID, source, eventType, status, content string) {
	t.Helper()
	path := filepath.Join(brainRoot, sessionID, ".system_generated", "logs", "transcript.jsonl")
	file, err := os.OpenFile(path, os.O_APPEND|os.O_WRONLY, 0)
	if err != nil {
		t.Fatal(err)
	}
	defer file.Close()
	encoder := json.NewEncoder(file)
	if err := encoder.Encode(map[string]string{
		"source":  source,
		"type":    eventType,
		"status":  status,
		"content": content,
	}); err != nil {
		t.Fatal(err)
	}
}

func agyProbeArgs(command string) []string {
	return []string{"agy", "-p", command, "--output-format", "json", "--print-timeout", "30s"}
}

func agyModelResult(id, label string) proc.Result {
	return textResult(fmt.Sprintf(`{"status":"SUCCESS","usage":{"total_tokens":0},"command":{"name":"model","data":{"id":%q,"label":%q,"effort":"high"}}}`, id, label))
}

func agyQuotaResult(group string, weekly, fiveHour float64) proc.Result {
	payload := map[string]any{
		"status": "SUCCESS",
		"usage":  map[string]any{"total_tokens": 0},
		"command": map[string]any{
			"name": "usage",
			"data": map[string]any{"groups": []any{map[string]any{
				"name": group,
				"buckets": []any{
					map[string]any{"id": "gemini-weekly", "remaining_fraction": weekly},
					map[string]any{"id": "gemini-5h", "remaining_fraction": fiveHour},
				},
			}}},
		},
	}
	encoded, err := json.Marshal(payload)
	if err != nil {
		panic(err)
	}
	return textResult(string(encoded))
}

func countCommand(calls [][]string, prefix ...string) int {
	count := 0
	for _, call := range calls {
		if hasPrefix(call, prefix...) {
			count++
		}
	}
	return count
}

func hasPrefix(values []string, prefix ...string) bool {
	if len(values) < len(prefix) {
		return false
	}
	for index, value := range prefix {
		if values[index] != value {
			return false
		}
	}
	return true
}

func TestAgyQuotaExhaustedParsesMachineReadableCommands(t *testing.T) {
	runner := &scriptedRunner{t: t, steps: []runStep{
		{want: agyProbeArgs("/model"), result: agyModelResult("gemini-3.8-flash-high", "Gemini 3.8 Flash (High)")},
		{want: agyProbeArgs("/quota"), result: agyQuotaResult("Gemini Models", 0, 0.73)},
	}}
	application := New(runner, &strings.Builder{}, &strings.Builder{})
	exhausted, err := application.agyQuotaExhausted(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if !exhausted {
		t.Fatal("expected quota exhaustion")
	}
	runner.assertDone()
}
