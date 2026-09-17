package transcript

import (
	"bufio"
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

var conversationIDPattern = regexp.MustCompile(`^[0-9a-fA-F]{8}-[0-9a-fA-F]{4}-[0-9a-fA-F]{4}-[0-9a-fA-F]{4}-[0-9a-fA-F]{12}$`)

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
	paths, err := PathsFor(brainRoot, ref)
	if err != nil {
		return "", false, err
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
		return "", false, fmt.Errorf("stat full agy transcript: %w", err)
	}
	if fullExists {
		return FinalResponse(paths.Full, fullOffset, task)
	}
	return FinalResponse(paths.Compact, compactOffset, task)
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
	// #nosec G304 -- path is derived from a strict UUID below the fixed agy brain root.
	file, err := os.Open(path)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return "", false, nil
		}
		return "", false, fmt.Errorf("open agy transcript: %w", err)
	}
	defer file.Close()

	info, err := file.Stat()
	if err != nil {
		return "", false, fmt.Errorf("stat agy transcript: %w", err)
	}
	if offset < 0 || offset > info.Size() {
		return "", false, fmt.Errorf("agy transcript checkpoint is invalid")
	}
	if info.Size()-offset > maxTranscriptDelta {
		return "", false, fmt.Errorf("agy transcript response is larger than 64 MiB")
	}
	if _, err := file.Seek(offset, io.SeekStart); err != nil {
		return "", false, fmt.Errorf("seek agy transcript: %w", err)
	}

	reader := bufio.NewReader(file)
	matchedTask := false
	lastResponse := ""
	for {
		line, readErr := reader.ReadString('\n')
		if len(line) != 0 {
			var event Event
			if json.Unmarshal([]byte(strings.TrimSpace(line)), &event) == nil {
				switch {
				case event.Source == "USER_EXPLICIT" && event.Type == "USER_INPUT" && event.Status == "DONE":
					matchedTask = isTaskEvent(event, task)
					lastResponse = ""
				case matchedTask && event.Source == "MODEL" && event.Type == "PLANNER_RESPONSE" && event.Status == "DONE" && strings.TrimSpace(event.Content) != "":
					lastResponse = strings.TrimSpace(event.Content)
				}
			}
		}
		if readErr != nil {
			if errors.Is(readErr, io.EOF) {
				break
			}
			return "", false, fmt.Errorf("read agy transcript: %w", readErr)
		}
	}
	if lastResponse == "" {
		return "", false, nil
	}
	return lastResponse, true, nil
}

func isTaskEvent(event Event, task string) bool {
	if event.Source != "USER_EXPLICIT" || event.Type != "USER_INPUT" || event.Status != "DONE" {
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
	return strings.TrimSpace(body[:endIndex]) == strings.TrimSpace(task)
}
