package transcript

import (
	"bufio"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"regexp"
	"strings"
)

const (
	maxTranscriptDelta     = 64 << 20
	transcriptFileName     = "transcript.jsonl"
	fullTranscriptFileName = "transcript_full.jsonl"
)

var (
	conversationIDPattern         = regexp.MustCompile(`^[0-9a-fA-F]{8}-[0-9a-fA-F]{4}-[0-9a-fA-F]{4}-[0-9a-fA-F]{4}-[0-9a-fA-F]{12}$`)
	backgroundTaskStartPattern    = regexp.MustCompile(`(?m)^Tool is running as a background task with task id: ([0-9a-fA-F-]{36}/task-[0-9]+)$`)
	backgroundTaskSenderPattern   = regexp.MustCompile(`\bsender=([0-9a-fA-F-]{36}/task-[0-9]+)\b`)
	backgroundTaskTerminalPattern = regexp.MustCompile(`(?m)^Task(?: \"|: )([0-9a-fA-F-]{36}/task-[0-9]+)(?:\" cancelled\.|\nStatus: (?:COMPLETED|DONE|FAILED|CANCELLED))`)
)

// Ref is the small Herdr session reference needed to find an agy transcript.
type Ref struct {
	Source string
	Agent  string
	Kind   string
	Value  string
}

// Checkpoint records both transcript files because Antigravity stores long,
// untruncated fields in transcript_full.jsonl and a compact copy in
// transcript.jsonl. Each file needs its own pre-send byte offset.
type Checkpoint struct {
	SessionID  string
	Path       string
	Offset     int64
	FullPath   string
	FullOffset int64
}

// Paths contains the two transcript files for one validated agy session.
type Paths struct {
	Compact string
	Full    string
}

// Event contains only the public response fields cagy reads from agy's JSONL.
type Event struct {
	Source  string `json:"source"`
	Type    string `json:"type"`
	Status  string `json:"status"`
	Content string `json:"content"`
}

// ResponseState describes whether the exact task has a safely deliverable
// response or still has transcript-visible background work in flight.
type ResponseState struct {
	Response          string
	Found             bool
	TaskMatched       bool
	BackgroundPending bool
	AwaitingResponse  bool
}

// Capture records the current end of both validated agy transcript files. A
// missing reference or file is valid for agy's first turn because the hook and
// transcript files can appear only after the prompt is submitted.
func Capture(brainRoot string, ref *Ref) (Checkpoint, error) {
	if ref == nil {
		return Checkpoint{}, nil
	}
	paths, err := PathsFor(brainRoot, *ref)
	if err != nil {
		return Checkpoint{}, err
	}
	compactOffset, err := fileOffset(paths.Compact)
	if err != nil {
		return Checkpoint{}, fmt.Errorf("capture agy transcript: %w", err)
	}
	fullOffset, err := fileOffset(paths.Full)
	if err != nil {
		return Checkpoint{}, fmt.Errorf("capture full agy transcript: %w", err)
	}
	return Checkpoint{
		SessionID:  ref.Value,
		Path:       paths.Compact,
		Offset:     compactOffset,
		FullPath:   paths.Full,
		FullOffset: fullOffset,
	}, nil
}

func fileOffset(path string) (int64, error) {
	info, err := os.Stat(path)
	if errors.Is(err, os.ErrNotExist) {
		return 0, nil
	}
	if err != nil {
		return 0, err
	}
	if !info.Mode().IsRegular() {
		return 0, fmt.Errorf("transcript is not a regular file")
	}
	return info.Size(), nil
}

// Path derives the compact transcript location from a strict Herdr session ID.
// It remains available for callers that need the historical compact path.
func Path(brainRoot string, ref Ref) (string, error) {
	paths, err := PathsFor(brainRoot, ref)
	if err != nil {
		return "", err
	}
	return paths.Compact, nil
}

// PathsFor derives both transcript locations from a strict Herdr session ID.
func PathsFor(brainRoot string, ref Ref) (Paths, error) {
	if ref.Source != "herdr:antigravity_cli" || ref.Agent != "agy" || ref.Kind != "id" {
		return Paths{}, fmt.Errorf("unsupported agy session reference")
	}
	if !conversationIDPattern.MatchString(ref.Value) {
		return Paths{}, fmt.Errorf("invalid agy conversation ID")
	}
	if brainRoot == "" {
		return Paths{}, fmt.Errorf("agy transcript root is unavailable")
	}
	logs := filepath.Join(brainRoot, ref.Value, ".system_generated", "logs")
	return Paths{
		Compact: filepath.Join(logs, transcriptFileName),
		Full:    filepath.Join(logs, fullTranscriptFileName),
	}, nil
}

// FinalResponseFor reads the exact task's response after the captured offsets.
// The full transcript is authoritative whenever it exists. The compact file is
// used only when Antigravity has not created the full file.
func FinalResponseFor(brainRoot string, ref Ref, checkpoint Checkpoint, task string) (string, bool, error) {
	return FinalResponseForHash(brainRoot, ref, checkpoint, TaskHash(task))
}

// TaskHash returns the stable SHA-256 identifier used to match one exact
// submitted task without persisting the task text itself.
func TaskHash(task string) string {
	sum := sha256.Sum256([]byte(strings.TrimSpace(task)))
	return hex.EncodeToString(sum[:])
}

// FinalResponseForHash reads the exact task response after the captured
// offsets using only the task's SHA-256 identifier. This supports safe
// interrupted-caller recovery without writing prompt text to cagy state.
func FinalResponseForHash(brainRoot string, ref Ref, checkpoint Checkpoint, taskHash string) (string, bool, error) {
	state, err := FinalResponseStateForHash(brainRoot, ref, checkpoint, taskHash)
	return state.Response, state.Found, err
}

// FinalResponseStateForHash reads the exact task state after the captured
// offsets, including transcript-visible background work that must finish
// before an intermediate planner message can be returned safely.
func FinalResponseStateForHash(brainRoot string, ref Ref, checkpoint Checkpoint, taskHash string) (ResponseState, error) {
	paths, err := PathsFor(brainRoot, ref)
	if err != nil {
		return ResponseState{}, err
	}
	compactOffset := int64(0)
	fullOffset := int64(0)
	if checkpoint.SessionID == ref.Value {
		if checkpoint.Path == paths.Compact {
			compactOffset = checkpoint.Offset
		}
		if checkpoint.FullPath == paths.Full {
			fullOffset = checkpoint.FullOffset
		}
	}

	fullExists, err := regularFileExists(paths.Full)
	if err != nil {
		return ResponseState{}, fmt.Errorf("stat full agy transcript: %w", err)
	}
	if fullExists {
		return FinalResponseStateHash(paths.Full, fullOffset, taskHash)
	}
	return FinalResponseStateHash(paths.Compact, compactOffset, taskHash)
}

func regularFileExists(path string) (bool, error) {
	info, err := os.Stat(path)
	if errors.Is(err, os.ErrNotExist) {
		return false, nil
	}
	if err != nil {
		return false, err
	}
	if !info.Mode().IsRegular() {
		return false, fmt.Errorf("transcript is not a regular file")
	}
	return true, nil
}

// FinalResponse reads only events appended after offset. It returns the last
// complete, non-empty planner response after the exact task's user event.
func FinalResponse(path string, offset int64, task string) (string, bool, error) {
	return FinalResponseHash(path, offset, TaskHash(task))
}

// FinalResponseHash reads only events appended after offset and matches the
// submitted USER_INPUT by its SHA-256 identifier.
func FinalResponseHash(path string, offset int64, taskHash string) (string, bool, error) {
	state, err := FinalResponseStateHash(path, offset, taskHash)
	return state.Response, state.Found, err
}

// FinalResponseStateHash returns detailed completion state for one exact task.
func FinalResponseStateHash(path string, offset int64, taskHash string) (ResponseState, error) {
	// #nosec G304 -- path is derived from a strict UUID below the fixed agy brain root.
	file, err := os.Open(path)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return ResponseState{}, nil
		}
		return ResponseState{}, fmt.Errorf("open agy transcript: %w", err)
	}
	defer file.Close()

	info, err := file.Stat()
	if err != nil {
		return ResponseState{}, fmt.Errorf("stat agy transcript: %w", err)
	}
	if offset < 0 || offset > info.Size() {
		return ResponseState{}, fmt.Errorf("agy transcript checkpoint is invalid")
	}
	if info.Size()-offset > maxTranscriptDelta {
		return ResponseState{}, fmt.Errorf("agy transcript response is larger than 64 MiB")
	}
	if _, err := file.Seek(offset, io.SeekStart); err != nil {
		return ResponseState{}, fmt.Errorf("seek agy transcript: %w", err)
	}

	reader := bufio.NewReader(file)
	matchedTask := false
	lastResponse := ""
	awaitingResponse := false
	pendingBackgroundTasks := make(map[string]struct{})
	for {
		line, readErr := reader.ReadString('\n')
		if len(line) != 0 {
			var event Event
			if json.Unmarshal([]byte(strings.TrimSpace(line)), &event) == nil {
				switch {
				case event.Source == "USER_EXPLICIT" && event.Type == "USER_INPUT" && event.Status == "DONE":
					matchedTask = isTaskHashEvent(event, taskHash)
					lastResponse = ""
					awaitingResponse = false
					pendingBackgroundTasks = make(map[string]struct{})
				case matchedTask && event.Source == "MODEL" && event.Type == "GENERIC" && event.Status == "RUNNING":
					if taskID := backgroundTaskID(backgroundTaskStartPattern, event.Content); taskID != "" {
						pendingBackgroundTasks[taskID] = struct{}{}
					}
				case matchedTask && event.Source == "SYSTEM" && event.Type == "SYSTEM_MESSAGE" && event.Status == "DONE":
					if taskID := backgroundTaskID(backgroundTaskSenderPattern, event.Content); taskID != "" {
						if _, pending := pendingBackgroundTasks[taskID]; pending {
							delete(pendingBackgroundTasks, taskID)
							lastResponse = ""
							awaitingResponse = true
						}
					}
				case matchedTask && event.Source == "MODEL" && event.Type == "GENERIC" && event.Status == "DONE":
					if taskID := backgroundTaskID(backgroundTaskTerminalPattern, event.Content); taskID != "" {
						if _, pending := pendingBackgroundTasks[taskID]; pending {
							delete(pendingBackgroundTasks, taskID)
							lastResponse = ""
							awaitingResponse = true
						}
					}
				case matchedTask && event.Source == "MODEL" && event.Type == "PLANNER_RESPONSE" && event.Status == "DONE" && strings.TrimSpace(event.Content) != "":
					lastResponse = strings.TrimSpace(event.Content)
					awaitingResponse = false
				}
			}
		}
		if readErr != nil {
			if errors.Is(readErr, io.EOF) {
				break
			}
			return ResponseState{}, fmt.Errorf("read agy transcript: %w", readErr)
		}
	}
	state := ResponseState{
		Response:          lastResponse,
		TaskMatched:       matchedTask,
		BackgroundPending: len(pendingBackgroundTasks) != 0,
		AwaitingResponse:  awaitingResponse,
	}
	state.Found = state.Response != "" && !state.BackgroundPending && !state.AwaitingResponse
	if !state.Found {
		state.Response = ""
	}
	return state, nil
}

func backgroundTaskID(pattern *regexp.Regexp, content string) string {
	match := pattern.FindStringSubmatch(content)
	if len(match) < 2 {
		return ""
	}
	return match[1]
}

func isTaskEvent(event Event, task string) bool {
	return isTaskHashEvent(event, TaskHash(task))
}

func isTaskHashEvent(event Event, taskHash string) bool {
	if event.Source != "USER_EXPLICIT" || event.Type != "USER_INPUT" || event.Status != "DONE" {
		return false
	}
	if len(taskHash) != sha256.Size*2 {
		return false
	}
	if _, err := hex.DecodeString(taskHash); err != nil {
		return false
	}
	const start = "<USER_REQUEST>\n"
	const end = "\n</USER_REQUEST>"
	startIndex := strings.Index(event.Content, start)
	if startIndex < 0 {
		return false
	}
	body := event.Content[startIndex+len(start):]
	endIndex := strings.Index(body, end)
	if endIndex < 0 {
		return false
	}
	return TaskHash(body[:endIndex]) == taskHash
}
