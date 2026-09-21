package developer

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/kazimshah39/herdr-tandem/internal/herdr"
)

const AgyID = "agy"

type Adapter interface {
	ID() string
	DisplayName() string
	Executable() string
	HerdrAgent() string
	SessionSource() string
	ReadyText() string
	TrustPrompt() string
	TranscriptRoot() string
	RequiresProviderService() bool
	StartSpec(name, paneID, sessionID string) (herdr.AgentStartSpec, error)
	SessionID(herdr.AgentInfo) (string, error)
}

type Agy struct{}

func (Agy) ID() string                    { return AgyID }
func (Agy) DisplayName() string           { return "agy" }
func (Agy) Executable() string            { return "agy" }
func (Agy) HerdrAgent() string            { return "agy" }
func (Agy) SessionSource() string         { return "herdr:antigravity_cli" }
func (Agy) ReadyText() string             { return "? for shortcuts" }
func (Agy) TrustPrompt() string           { return "trust the contents of this project" }
func (Agy) RequiresProviderService() bool { return true }
func (Agy) TranscriptRoot() string {
	home, err := os.UserHomeDir()
	if err != nil {
		return ""
	}
	return filepath.Join(home, ".gemini", "antigravity-cli", "brain")
}

func (a Agy) StartSpec(name, paneID, sessionID string) (herdr.AgentStartSpec, error) {
	sessionID = strings.TrimSpace(sessionID)
	spec := herdr.AgentStartSpec{
		Name: name, Kind: a.HerdrAgent(), PaneID: paneID, TimeoutMS: 60000,
		Args: []string{"--dangerously-skip-permissions", "--mode", "accept-edits"},
	}
	if sessionID != "" {
		spec.Args = append([]string{"--conversation", sessionID}, spec.Args...)
		spec.ExpectedSession = &herdr.AgentSessionInfo{Source: a.SessionSource(), Agent: a.HerdrAgent(), Kind: "id", Value: sessionID}
	}
	return spec, nil
}

func (a Agy) SessionID(agent herdr.AgentInfo) (string, error) {
	if agent.AgentSession == nil || strings.TrimSpace(agent.AgentSession.Value) == "" {
		return "", fmt.Errorf("agy conversation identity is missing")
	}
	if agent.AgentSession.Source != a.SessionSource() {
		return "", fmt.Errorf("agy conversation identity source is unsupported: %s", agent.AgentSession.Source)
	}
	if agent.AgentSession.Agent != a.HerdrAgent() {
		return "", fmt.Errorf("agy conversation identity belongs to %s", agent.AgentSession.Agent)
	}
	if agent.AgentSession.Kind != "id" {
		return "", fmt.Errorf("agy conversation identity kind is unsupported: %s", agent.AgentSession.Kind)
	}
	return strings.TrimSpace(agent.AgentSession.Value), nil
}

func Resolve(id string) (Adapter, error) {
	if strings.TrimSpace(id) == "" || id == AgyID {
		return Agy{}, nil
	}
	return nil, fmt.Errorf("unsupported developer %q; only agy is supported", id)
}
