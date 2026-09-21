package app

import (
	"fmt"

	"github.com/kazimshah39/herdr-tandem/internal/herdr"
	"github.com/kazimshah39/herdr-tandem/internal/transcript"
)

func (a *App) transcriptCheckpoint(agent herdr.AgentInfo) (transcript.Checkpoint, error) {
	return transcript.Capture(a.transcriptRoot, transcriptRef(agent.AgentSession))
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
	state, err := a.completedTranscriptState(checkpoint, agent, task)
	return state.Response, state.Found, err
}

func (a *App) completedTranscriptState(checkpoint transcript.Checkpoint, agent herdr.AgentInfo, task string) (transcript.ResponseState, error) {
	ref := transcriptRef(agent.AgentSession)
	if ref == nil {
		return transcript.ResponseState{}, fmt.Errorf("agy transcript was not reported; run: herdr integration install antigravity-cli")
	}
	return transcript.FinalResponseStateForHash(a.transcriptRoot, *ref, checkpoint, transcript.TaskHash(task))
}
