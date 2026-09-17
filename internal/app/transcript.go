package app

import (
	"fmt"
	"os"
	"path/filepath"

	"github.com/kazimshah39/cagy/internal/herdr"
	"github.com/kazimshah39/cagy/internal/transcript"
)

func defaultAgyBrainRoot() string {
	home, err := os.UserHomeDir()
	if err != nil {
		return ""
	}
	return filepath.Join(home, ".gemini", "antigravity-cli", "brain")
}

func (a *App) transcriptCheckpoint(agent herdr.AgentInfo) (transcript.Checkpoint, error) {
	return transcript.Capture(a.agyBrainRoot, transcriptRef(agent.AgentSession))
}

func transcriptRef(session *herdr.AgentSessionInfo) *transcript.Ref {
	if session == nil {
		return nil
	}
	return &transcript.Ref{
		Source: session.Source,
		Agent:  session.Agent,
		Kind:   session.Kind,
		Value:  session.Value,
	}
}

func (a *App) completedTranscriptResponse(checkpoint transcript.Checkpoint, agent herdr.AgentInfo, task string) (string, bool, error) {
	ref := transcriptRef(agent.AgentSession)
	if ref == nil {
		return "", false, fmt.Errorf("agy transcript was not reported; run: herdr integration install antigravity-cli")
	}
	return transcript.FinalResponseFor(a.agyBrainRoot, *ref, checkpoint, task)
}
