package app

import (
	"context"
	"errors"
	"fmt"
	"path/filepath"
	"regexp"
	"strings"
	"time"

	"github.com/kazimshah39/cagy/internal/herdr"
)

const (
	developerPaneLabel     = "agy Developer"
	repairPaneLabel        = "agy Repair"
	agySessionStateToken   = "cagy_session_state"
	agySessionStatePending = "pending"
	agySessionStateReady   = "ready"
)

var errAgentReleaseTimeout = errors.New("agy did not stop")

var agyConversationIDPattern = regexp.MustCompile(`^[0-9a-fA-F]{8}-[0-9a-fA-F]{4}-[0-9a-fA-F]{4}-[0-9a-fA-F]{4}-[0-9a-fA-F]{12}$`)

func paneCWD(pane herdr.PaneInfo) (string, error) {
	cwd := strings.TrimSpace(pane.ForegroundCWD)
	if cwd == "" {
		cwd = strings.TrimSpace(pane.CWD)
	}
	if cwd == "" {
		return "", fmt.Errorf("pane %s working directory is unknown", pane.PaneID)
	}
	return filepath.Clean(cwd), nil
}

func agentCWD(agent herdr.AgentInfo) (string, error) {
	cwd := strings.TrimSpace(agent.ForegroundCWD)
	if cwd == "" {
		cwd = strings.TrimSpace(agent.CWD)
	}
	if cwd == "" {
		return "", fmt.Errorf("cagy developer working directory is unknown")
	}
	return filepath.Clean(cwd), nil
}

func (a *App) supervisorPane(ctx context.Context, info runtimeContext) (herdr.PaneInfo, error) {
	a.debugf("lifecycle supervisor-pane begin pane=%q workspace=%q", info.supervisor, info.workspaceID)
	pane, err := a.herdr.GetPane(ctx, info.supervisor)
	if err != nil {
		a.debugf("lifecycle supervisor-pane error pane=%q error=%q", info.supervisor, err)
		return herdr.PaneInfo{}, fmt.Errorf("find cagy supervisor pane: %w", err)
	}
	if pane.WorkspaceID != info.workspaceID {
		return herdr.PaneInfo{}, fmt.Errorf("cagy supervisor belongs to another Herdr workspace")
	}
	if strings.TrimSpace(pane.TabID) == "" {
		return herdr.PaneInfo{}, fmt.Errorf("cagy supervisor tab is missing")
	}
	a.debugf("lifecycle supervisor-pane success pane=%q workspace=%q tab=%q", pane.PaneID, pane.WorkspaceID, pane.TabID)
	return pane, nil
}

func validateDeveloperSession(agent herdr.AgentInfo, info runtimeContext, supervisor herdr.PaneInfo) error {
	if err := validateDeveloper(agent, info.workspaceID, info.project); err != nil {
		return err
	}
	if agent.TabID == "" || agent.TabID != supervisor.TabID {
		return fmt.Errorf("cagy developer belongs to another Herdr tab")
	}
	return nil
}

func validatePaneScope(pane herdr.PaneInfo, info runtimeContext, supervisor herdr.PaneInfo) error {
	if pane.WorkspaceID != info.workspaceID {
		return fmt.Errorf("pane belongs to another Herdr workspace")
	}
	if pane.TabID == "" || pane.TabID != supervisor.TabID {
		return fmt.Errorf("pane belongs to another Herdr tab")
	}
	project, err := paneCWD(pane)
	if err != nil {
		return err
	}
	if project != filepath.Clean(info.project) {
		return fmt.Errorf("pane belongs to another project: %s", project)
	}
	return nil
}

func (a *App) validatedDeveloperPane(ctx context.Context, agent herdr.AgentInfo, info runtimeContext, supervisor herdr.PaneInfo) (herdr.PaneInfo, error) {
	a.debugf("lifecycle validate-developer begin developer=%q pane=%q status=%q has_session=%t", info.developer, agent.PaneID, agent.AgentStatus, agent.AgentSession != nil && strings.TrimSpace(agent.AgentSession.Value) != "")
	if err := validateDeveloperSession(agent, info, supervisor); err != nil {
		a.debugf("lifecycle validate-developer session-error developer=%q pane=%q error=%q", info.developer, agent.PaneID, err)
		return herdr.PaneInfo{}, err
	}
	pane, err := a.herdr.GetPane(ctx, agent.PaneID)
	if err != nil {
		a.debugf("lifecycle validate-developer pane-error developer=%q pane=%q error=%q", info.developer, agent.PaneID, err)
		return herdr.PaneInfo{}, fmt.Errorf("find developer pane %s: %w", agent.PaneID, err)
	}
	if err := validatePaneScope(pane, info, supervisor); err != nil {
		a.debugf("lifecycle validate-developer scope-error developer=%q pane=%q error=%q", info.developer, agent.PaneID, err)
		return herdr.PaneInfo{}, fmt.Errorf("developer pane scope invalid: %w", err)
	}
	owner := pane.Tokens["cagy_owner"]
	role := pane.Tokens["cagy_role"]
	if owner == info.developer && role == "developer" {
		a.debugf("lifecycle validate-developer success developer=%q pane=%q", info.developer, agent.PaneID)
		return pane, nil
	}
	if owner == "" || role == "" {
		return herdr.PaneInfo{}, fmt.Errorf("cagy developer pane ownership is incomplete; missing cagy_owner or cagy_role")
	}
	if owner != info.developer {
		return herdr.PaneInfo{}, fmt.Errorf("cagy developer pane is owned by another developer: %s", owner)
	}
	if role != "developer" {
		return herdr.PaneInfo{}, fmt.Errorf("cagy developer pane has invalid role: %s", role)
	}
	return herdr.PaneInfo{}, fmt.Errorf("cagy developer pane ownership is invalid")
}

func (a *App) validateExistingDeveloper(ctx context.Context, agent herdr.AgentInfo, info runtimeContext, supervisor herdr.PaneInfo) error {
	_, err := a.validatedDeveloperPane(ctx, agent, info, supervisor)
	return err
}

func (a *App) markPaneRole(ctx context.Context, paneID, owner, role string) error {
	a.debugf("lifecycle pane-role begin pane=%q owner=%q role=%q", paneID, owner, role)
	if err := a.herdr.ReportPaneOwnership(ctx, paneID, paneOwnershipSource, owner, role); err != nil {
		a.debugf("lifecycle pane-role error pane=%q owner=%q role=%q error=%q", paneID, owner, role, err)
		return fmt.Errorf("mark cagy %s pane: %w", role, err)
	}
	a.debugf("lifecycle pane-role success pane=%q owner=%q role=%q", paneID, owner, role)
	return nil
}

func (a *App) markDeveloperPane(ctx context.Context, info runtimeContext, paneID string) error {
	if err := a.markPaneRole(ctx, paneID, info.developer, "developer"); err != nil {
		return err
	}
	if err := a.herdr.ReportAgentDisplay(ctx, paneID, developerDisplaySource, "agy", developerDisplayName); err != nil {
		return fmt.Errorf("label cagy developer: %w", err)
	}
	return nil
}

func exactAgySessionID(agent herdr.AgentInfo) (string, error) {
	if agent.AgentSession == nil || strings.TrimSpace(agent.AgentSession.Value) == "" {
		return "", fmt.Errorf("agy conversation identity is missing")
	}
	if agent.AgentSession.Source != "herdr:antigravity_cli" {
		return "", fmt.Errorf("agy conversation identity source is unsupported: %s", agent.AgentSession.Source)
	}
	if agent.AgentSession.Agent != "agy" {
		return "", fmt.Errorf("agy conversation identity belongs to another agent")
	}
	if agent.AgentSession.Kind != "id" {
		return "", fmt.Errorf("agy conversation identity kind is unsupported: %s", agent.AgentSession.Kind)
	}
	sessionID := strings.TrimSpace(agent.AgentSession.Value)
	if !agyConversationIDPattern.MatchString(sessionID) {
		return "", fmt.Errorf("agy conversation identity is invalid")
	}
	return sessionID, nil
}

func validateAgySessionContinuity(expected string, agent herdr.AgentInfo) error {
	actual, err := exactAgySessionID(agent)
	if err != nil {
		return err
	}
	if strings.TrimSpace(expected) != actual {
		return fmt.Errorf("agy resumed a different conversation: expected %s, got %s", expected, actual)
	}
	return nil
}

type recoveryDeveloperIdentity struct {
	agent   herdr.AgentInfo
	pending bool
}

func (a *App) developerWithExactSession(ctx context.Context, info runtimeContext, expected herdr.AgentInfo) (herdr.AgentInfo, error) {
	supervisor, err := a.supervisorPane(ctx, info)
	if err != nil {
		return herdr.AgentInfo{}, err
	}
	identity, err := a.developerRecoveryIdentityInScope(ctx, info, supervisor, expected, false)
	if err != nil {
		return herdr.AgentInfo{}, err
	}
	return identity.agent, nil
}

func (a *App) developerRecoveryIdentity(ctx context.Context, info runtimeContext, expected herdr.AgentInfo, allowPending bool) (recoveryDeveloperIdentity, error) {
	supervisor, err := a.supervisorPane(ctx, info)
	if err != nil {
		return recoveryDeveloperIdentity{}, err
	}
	return a.developerRecoveryIdentityInScope(ctx, info, supervisor, expected, allowPending)
}

func (a *App) developerRecoveryIdentityInScope(ctx context.Context, info runtimeContext, supervisor herdr.PaneInfo, expected herdr.AgentInfo, allowPending bool) (recoveryDeveloperIdentity, error) {
	current, err := a.herdr.GetAgent(ctx, info.developer)
	if err != nil {
		return recoveryDeveloperIdentity{}, fmt.Errorf("refresh agy conversation identity: %w", err)
	}
	pane, err := a.validatedDeveloperPane(ctx, current, info, supervisor)
	if err != nil {
		return recoveryDeveloperIdentity{}, err
	}
	if expected.PaneID != "" && current.PaneID != expected.PaneID {
		return recoveryDeveloperIdentity{}, fmt.Errorf("agy developer moved to another pane before recovery")
	}
	currentSessionID, sessionErr := exactAgySessionID(current)
	if sessionErr != nil {
		missing := current.AgentSession == nil || strings.TrimSpace(current.AgentSession.Value) == ""
		expectedMissing := expected.AgentSession == nil || strings.TrimSpace(expected.AgentSession.Value) == ""
		if allowPending && missing && expectedMissing && strings.TrimSpace(pane.Tokens["cagy_session"]) == "" && pane.Tokens[agySessionStateToken] == agySessionStatePending {
			return recoveryDeveloperIdentity{agent: current, pending: true}, nil
		}
		return recoveryDeveloperIdentity{}, fmt.Errorf("cannot resume agy safely: %w", sessionErr)
	}
	if expectedSessionID, expectedErr := exactAgySessionID(expected); expectedErr == nil && expectedSessionID != currentSessionID {
		return recoveryDeveloperIdentity{}, fmt.Errorf("agy conversation changed before recovery: expected %s, got %s", expectedSessionID, currentSessionID)
	}
	return recoveryDeveloperIdentity{agent: current}, nil
}

func (a *App) persistDeveloperSession(ctx context.Context, paneID string, agent herdr.AgentInfo) error {
	a.debugf("lifecycle persist-session begin pane=%q has_session=%t", paneID, agent.AgentSession != nil && strings.TrimSpace(agent.AgentSession.Value) != "")
	sessionID, err := exactAgySessionID(agent)
	if err != nil {
		a.debugf("lifecycle persist-session invalid pane=%q error=%q", paneID, err)
		return err
	}
	if err := a.herdr.ReportPaneSession(ctx, paneID, paneOwnershipSource, sessionID); err != nil {
		a.debugf("lifecycle persist-session error pane=%q error=%q", paneID, err)
		return fmt.Errorf("persist agy conversation identity: %w", err)
	}
	a.debugf("lifecycle persist-session success pane=%q", paneID)
	return nil
}

func (a *App) persistDeveloperSessionIfReported(ctx context.Context, agent herdr.AgentInfo) error {
	if agent.AgentSession == nil || strings.TrimSpace(agent.AgentSession.Value) == "" {
		return nil
	}
	return a.persistDeveloperSession(ctx, agent.PaneID, agent)
}

func (a *App) recordFreshDeveloperSessionState(ctx context.Context, agent herdr.AgentInfo) error {
	if agent.AgentSession != nil && strings.TrimSpace(agent.AgentSession.Value) != "" {
		return a.persistDeveloperSession(ctx, agent.PaneID, agent)
	}
	a.debugf("lifecycle persist-session pending pane=%q", agent.PaneID)
	if err := a.herdr.ReportPaneSessionPending(ctx, agent.PaneID, paneOwnershipSource); err != nil {
		return fmt.Errorf("mark fresh agy conversation as pending: %w", err)
	}
	return nil
}

func (a *App) warnIfDeveloperSessionNotPersisted(ctx context.Context, agent herdr.AgentInfo) {
	if err := a.persistDeveloperSession(ctx, agent.PaneID, agent); err != nil {
		fmt.Fprintf(a.stderr, "cagy warning: could not save agy conversation identity: %v\n", err)
	}
}

func (a *App) markRepairPane(ctx context.Context, info runtimeContext, paneID string) error {
	renameErr := a.herdr.RenamePane(ctx, paneID, repairPaneLabel)
	ownerErr := a.markPaneRole(ctx, paneID, info.developer, "repair")
	if renameErr != nil && ownerErr != nil {
		return fmt.Errorf("rename repair pane: %v; %w", renameErr, ownerErr)
	}
	if renameErr != nil {
		return fmt.Errorf("rename repair pane: %w", renameErr)
	}
	return ownerErr
}

func isShellProcess(name string) bool {
	base := filepath.Base(strings.TrimSpace(name))
	base = strings.TrimPrefix(base, "-")
	switch base {
	case "bash", "zsh", "sh", "fish", "ksh", "csh", "tcsh", "dash":
		return true
	default:
		return false
	}
}

func (a *App) validateShellPane(ctx context.Context, paneID string) error {
	info, err := a.herdr.PaneProcessInfo(ctx, paneID)
	if err != nil {
		return fmt.Errorf("read process info for repair pane %s: %w", paneID, err)
	}
	if len(info.ForegroundProcesses) == 0 {
		return fmt.Errorf("repair pane %s has no readable foreground process", paneID)
	}
	for _, proc := range info.ForegroundProcesses {
		if !isShellProcess(proc.Name) {
			return fmt.Errorf("repair pane %s is running a non-shell process: %s", paneID, proc.Name)
		}
	}
	return nil
}

func (a *App) ensureDeveloper(ctx context.Context, info runtimeContext) (herdr.AgentInfo, error) {
	a.debugf("lifecycle ensure-developer begin developer=%q expected_pane=%q", info.developer, info.developerPane)
	supervisor, err := a.supervisorPane(ctx, info)
	if err != nil {
		return herdr.AgentInfo{}, err
	}
	developer, err := a.herdr.GetAgent(ctx, info.developer)
	if err == nil {
		if err := a.validateExistingDeveloper(ctx, developer, info, supervisor); err != nil {
			return herdr.AgentInfo{}, err
		}
		a.debugf("lifecycle ensure-developer existing developer=%q pane=%q status=%q", info.developer, developer.PaneID, developer.AgentStatus)
		return developer, nil
	}
	if !herdr.IsCode(err, "agent_not_found") {
		a.debugf("lifecycle ensure-developer lookup-error developer=%q error=%q", info.developer, err)
		return herdr.AgentInfo{}, fmt.Errorf("find agy developer: %w", err)
	}
	a.debugf("lifecycle ensure-developer repair-needed developer=%q", info.developer)
	return a.repairMissingDeveloper(ctx, info)
}

func (a *App) rollbackStartedAgent(ctx context.Context, info runtimeContext, paneID string, originalErr error) error {
	a.debugf("lifecycle rollback begin developer=%q pane=%q cause=%q", info.developer, paneID, originalErr)
	var cleanupErrs []string
	stopped := false
	if err := a.interruptAndWaitAgent(ctx, info.developer); err != nil {
		cleanupErrs = append(cleanupErrs, fmt.Sprintf("stop agent: %v", err))
	} else {
		stopped = true
	}
	if stopped {
		if err := a.markRepairPane(ctx, info, paneID); err != nil {
			cleanupErrs = append(cleanupErrs, fmt.Sprintf("mark repair pane: %v", err))
		}
	} else {
		if err := a.markPaneRole(ctx, paneID, info.developer, "developer"); err != nil {
			cleanupErrs = append(cleanupErrs, fmt.Sprintf("restore developer ownership: %v", err))
		}
	}
	if len(cleanupErrs) > 0 {
		paneDesc := "developer pane left visible"
		if stopped {
			paneDesc = "repair pane left visible"
		}
		result := fmt.Errorf("%w; rollback cleanup failed: %s; %s", originalErr, strings.Join(cleanupErrs, "; "), paneDesc)
		a.debugf("lifecycle rollback end developer=%q pane=%q stopped=%t ok=false error=%q", info.developer, paneID, stopped, result)
		return result
	}
	result := fmt.Errorf("%w; repair pane left visible", originalErr)
	a.debugf("lifecycle rollback end developer=%q pane=%q stopped=%t ok=true", info.developer, paneID, stopped)
	return result
}

// cleanupFailedAgentStart handles the important case where Herdr's bounded
// start command returns an error after the agy process has already appeared.
// A failed start must leave the owned pane as a reusable shell, not as an
// unregistered TUI that makes every later recovery attempt fail immediately.
func (a *App) cleanupFailedAgentStart(ctx context.Context, info runtimeContext, paneID string) error {
	a.debugf("lifecycle failed-start cleanup begin developer=%q pane=%q", info.developer, paneID)
	existing, err := a.herdr.GetAgent(ctx, info.developer)
	if err == nil {
		if existing.PaneID != paneID {
			return fmt.Errorf("developer target appeared in unexpected pane %s", existing.PaneID)
		}
		if err := a.interruptAndWaitAgent(ctx, info.developer); err != nil {
			return err
		}
		a.debugf("lifecycle failed-start cleanup success developer=%q pane=%q source=%q", info.developer, paneID, "registered-agent")
		return nil
	}
	if !herdr.IsCode(err, "agent_not_found") {
		return err
	}
	if err := a.validateShellPane(ctx, paneID); err == nil {
		a.debugf("lifecycle failed-start cleanup success developer=%q pane=%q source=%q", info.developer, paneID, "already-shell")
		return nil
	}

	// agy's idle TUI normally needs two Ctrl+C presses to exit. Use the same
	// bounded escalation as registered-agent shutdown, but address the pane
	// directly because Herdr never registered this partially started process.
	if err := a.herdr.SendPaneKeys(ctx, paneID, "ctrl+c"); err != nil {
		return fmt.Errorf("interrupt unregistered agy: %w", err)
	}
	if err := a.waitPaneShellFor(ctx, paneID, a.agentStopEscalation); err == nil {
		a.debugf("lifecycle failed-start cleanup success developer=%q pane=%q source=%q", info.developer, paneID, "pane-interrupt-1")
		return nil
	} else if errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
		return err
	}
	if err := a.herdr.SendPaneKeys(ctx, paneID, "ctrl+c"); err != nil {
		return fmt.Errorf("interrupt unregistered agy again: %w", err)
	}
	if err := a.waitPaneShellFor(ctx, paneID, a.agentStopTimeout); err != nil {
		return err
	}
	a.debugf("lifecycle failed-start cleanup success developer=%q pane=%q source=%q", info.developer, paneID, "pane-interrupt-2")
	return nil
}

func (a *App) waitPaneShellFor(ctx context.Context, paneID string, timeout time.Duration) error {
	if timeout <= 0 {
		timeout = 15 * time.Second
	}
	deadline := time.NewTimer(timeout)
	defer deadline.Stop()
	poll := a.developerPoll
	if poll <= 0 || poll > 250*time.Millisecond {
		poll = 250 * time.Millisecond
	}
	ticker := time.NewTicker(poll)
	defer ticker.Stop()
	var lastErr error
	for {
		if err := a.validateShellPane(ctx, paneID); err == nil {
			return nil
		} else {
			lastErr = err
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-deadline.C:
			return fmt.Errorf("developer pane did not return to a shell within %s: %w", timeout.Round(time.Millisecond), lastErr)
		case <-ticker.C:
		}
	}
}

func (a *App) repairMissingDeveloper(ctx context.Context, info runtimeContext) (herdr.AgentInfo, error) {
	a.debugf("lifecycle repair begin developer=%q project=%q", info.developer, info.project)
	supervisor, err := a.supervisorPane(ctx, info)
	if err != nil {
		return herdr.AgentInfo{}, err
	}
	pane, err := a.findRepairPane(ctx, info, supervisor)
	if err != nil {
		return herdr.AgentInfo{}, err
	}
	created := false
	if pane.PaneID == "" {
		pane, err = a.herdr.SplitRight(ctx, info.project)
		if err != nil {
			return herdr.AgentInfo{}, fmt.Errorf("create replacement developer pane: %w", err)
		}
		if err := validatePaneScope(pane, info, supervisor); err != nil {
			_ = a.herdr.ClosePane(ctx, pane.PaneID)
			return herdr.AgentInfo{}, fmt.Errorf("replacement developer pane is unsafe: %w", err)
		}
		created = true
	}
	a.debugf("lifecycle repair pane developer=%q pane=%q created=%t", info.developer, pane.PaneID, created)

	sessionID := strings.TrimSpace(pane.Tokens["cagy_session"])
	a.debugf("lifecycle repair session developer=%q pane=%q saved_session=%t", info.developer, pane.PaneID, sessionID != "")
	if !created {
		if sessionID == "" {
			return herdr.AgentInfo{}, fmt.Errorf("repair pane has no saved agy conversation identity; refusing to guess with --continue")
		}
		if !agyConversationIDPattern.MatchString(sessionID) {
			return herdr.AgentInfo{}, fmt.Errorf("repair pane has an invalid agy conversation identity; repair pane left visible")
		}
		if pane.Tokens[agySessionStateToken] != agySessionStateReady {
			return herdr.AgentInfo{}, fmt.Errorf("repair pane has an invalid agy conversation state; repair pane left visible")
		}
	}
	if err := a.markRepairPane(ctx, info, pane.PaneID); err != nil {
		if created {
			_ = a.herdr.ClosePane(ctx, pane.PaneID)
		}
		return herdr.AgentInfo{}, err
	}
	_ = a.herdr.RenamePane(ctx, pane.PaneID, developerPaneLabel)
	var developer herdr.AgentInfo
	// Repair uses agy's current session directly. It must not touch the
	// canonical Keychain item because that can show a macOS password prompt.
	if sessionID != "" {
		developer, err = a.herdr.StartAgyWithSession(ctx, info.developer, pane.PaneID, sessionID)
	} else {
		developer, err = a.herdr.StartAgy(ctx, info.developer, pane.PaneID)
	}
	if err == nil {
		err = a.ensureAgyReady(ctx, pane.PaneID)
	}
	a.debugf("lifecycle repair start-result developer=%q pane=%q agent_started=%t error=%q", info.developer, pane.PaneID, developer.PaneID != "", err)
	if err != nil && developer.PaneID != "" {
		return herdr.AgentInfo{}, a.rollbackStartedAgent(ctx, info, pane.PaneID, fmt.Errorf("bind repaired agy developer account: %w", err))
	}
	if err != nil {
		if herdr.IsCode(err, "agent_name_taken") {
			if existing, getErr := a.herdr.GetAgent(ctx, info.developer); getErr == nil {
				if validateErr := a.validateExistingDeveloper(ctx, existing, info, supervisor); validateErr == nil {
					if created {
						if closeErr := a.confirmClosePane(ctx, pane.PaneID); closeErr != nil {
							return herdr.AgentInfo{}, fmt.Errorf("clean up redundant developer pane %s: %w", pane.PaneID, closeErr)
						}
					}
					return existing, nil
				}
			}
			_ = a.markRepairPane(ctx, info, pane.PaneID)
			return herdr.AgentInfo{}, fmt.Errorf("repair missing agy developer: %w; repair pane left visible", err)
		}
		cleanupErr := a.cleanupFailedAgentStart(ctx, info, pane.PaneID)
		_ = a.markRepairPane(ctx, info, pane.PaneID)
		if cleanupErr != nil {
			return herdr.AgentInfo{}, fmt.Errorf("repair missing agy developer: %w; cleanup failed: %v; repair pane left visible", err, cleanupErr)
		}
		return herdr.AgentInfo{}, fmt.Errorf("repair missing agy developer: %w; repair pane left visible", err)
	}
	if sessionID != "" {
		if err := validateAgySessionContinuity(sessionID, developer); err != nil {
			return herdr.AgentInfo{}, a.rollbackStartedAgent(ctx, info, pane.PaneID, err)
		}
	}
	if err := validateDeveloperSession(developer, info, supervisor); err != nil {
		return herdr.AgentInfo{}, a.rollbackStartedAgent(ctx, info, pane.PaneID, err)
	}
	if err := a.markDeveloperPane(ctx, info, pane.PaneID); err != nil {
		return herdr.AgentInfo{}, a.rollbackStartedAgent(ctx, info, pane.PaneID, err)
	}
	if sessionID != "" {
		if err := a.persistDeveloperSession(ctx, pane.PaneID, developer); err != nil {
			return herdr.AgentInfo{}, a.rollbackStartedAgent(ctx, info, pane.PaneID, err)
		}
	} else if err := a.recordFreshDeveloperSessionState(ctx, developer); err != nil {
		return herdr.AgentInfo{}, a.rollbackStartedAgent(ctx, info, pane.PaneID, err)
	}
	a.debugf("lifecycle repair success developer=%q pane=%q status=%q resumed=%t", info.developer, developer.PaneID, developer.AgentStatus, sessionID != "")
	return developer, nil
}

func (a *App) findRepairPane(ctx context.Context, info runtimeContext, supervisor herdr.PaneInfo) (herdr.PaneInfo, error) {
	a.debugf("lifecycle repair-pane-search begin developer=%q workspace=%q", info.developer, info.workspaceID)
	panes, err := a.herdr.ListPanes(ctx, info.workspaceID)
	if err != nil {
		return herdr.PaneInfo{}, fmt.Errorf("list cagy repair panes: %w", err)
	}
	var owned []herdr.PaneInfo
	for _, pane := range panes {
		if pane.PaneID == info.supervisor {
			continue
		}
		if validatePaneScope(pane, info, supervisor) != nil {
			continue
		}
		owner := pane.Tokens["cagy_owner"]
		role := pane.Tokens["cagy_role"]
		if owner == info.developer && (role == "developer" || role == "repair") {
			if pane.Agent != "" {
				return herdr.PaneInfo{}, fmt.Errorf("owned repair pane %s is running another agent", pane.PaneID)
			}
			owned = append(owned, pane)
		}
	}
	if len(owned) > 1 {
		a.debugf("lifecycle repair-pane-search ambiguous developer=%q candidates=%d", info.developer, len(owned))
		return herdr.PaneInfo{}, fmt.Errorf("multiple cagy-owned repair panes found; check the right panes")
	}
	if len(owned) == 1 {
		if err := a.validateShellPane(ctx, owned[0].PaneID); err != nil {
			return herdr.PaneInfo{}, err
		}
		a.debugf("lifecycle repair-pane-search found developer=%q pane=%q", info.developer, owned[0].PaneID)
		return owned[0], nil
	}
	a.debugf("lifecycle repair-pane-search none developer=%q panes_seen=%d", info.developer, len(panes))
	return herdr.PaneInfo{}, nil
}

// startDeveloperInPlace starts agy in an already-stopped, cagy-owned pane.
// A non-empty sessionID resumes that exact conversation; an empty sessionID
// creates a fresh conversation. Account recovery uses this only after the
// previous developer has been confirmed released.
func (a *App) startDeveloperInPlace(ctx context.Context, info runtimeContext, paneID, sessionID string) (herdr.AgentInfo, error) {
	a.debugf("lifecycle start-in-place begin developer=%q pane=%q resume=%t", info.developer, paneID, sessionID != "")
	supervisor, err := a.supervisorPane(ctx, info)
	if err != nil {
		return herdr.AgentInfo{}, err
	}
	pane, err := a.herdr.GetPane(ctx, paneID)
	if err != nil {
		return herdr.AgentInfo{}, fmt.Errorf("read developer pane before restart: %w", err)
	}
	if err := validatePaneScope(pane, info, supervisor); err != nil {
		return herdr.AgentInfo{}, fmt.Errorf("developer pane is unsafe before restart: %w", err)
	}
	if pane.Agent != "" {
		return herdr.AgentInfo{}, fmt.Errorf("developer pane is not an available shell")
	}
	if err := a.validateShellPane(ctx, paneID); err != nil {
		return herdr.AgentInfo{}, err
	}
	if err := a.markDeveloperPane(ctx, info, paneID); err != nil {
		return herdr.AgentInfo{}, err
	}

	var started herdr.AgentInfo
	if sessionID != "" {
		if !agyConversationIDPattern.MatchString(sessionID) {
			return herdr.AgentInfo{}, errors.New("agy conversation identity is invalid")
		}
		started, err = a.herdr.StartAgyWithSession(ctx, info.developer, paneID, sessionID)
	} else {
		started, err = a.herdr.StartAgy(ctx, info.developer, paneID)
	}
	if err != nil {
		cleanupErr := a.cleanupFailedAgentStart(ctx, info, paneID)
		_ = a.markRepairPane(ctx, info, paneID)
		if cleanupErr != nil {
			return herdr.AgentInfo{}, fmt.Errorf("start agy in developer pane: %w; cleanup failed: %v", err, cleanupErr)
		}
		return herdr.AgentInfo{}, fmt.Errorf("start agy in developer pane: %w", err)
	}
	if sessionID != "" {
		if err := validateAgySessionContinuity(sessionID, started); err != nil {
			return herdr.AgentInfo{}, a.rollbackStartedAgent(ctx, info, paneID, err)
		}
	}
	if err := validateDeveloperSession(started, info, supervisor); err != nil {
		return herdr.AgentInfo{}, a.rollbackStartedAgent(ctx, info, paneID, err)
	}
	_ = a.herdr.RenamePane(ctx, paneID, developerPaneLabel)
	if err := a.markDeveloperPane(ctx, info, paneID); err != nil {
		return herdr.AgentInfo{}, a.rollbackStartedAgent(ctx, info, paneID, err)
	}
	if sessionID != "" {
		if err := a.persistDeveloperSession(ctx, paneID, started); err != nil {
			return herdr.AgentInfo{}, a.rollbackStartedAgent(ctx, info, paneID, err)
		}
	} else if err := a.recordFreshDeveloperSessionState(ctx, started); err != nil {
		return herdr.AgentInfo{}, a.rollbackStartedAgent(ctx, info, paneID, err)
	}
	if err := a.ensureAgyReady(ctx, paneID); err != nil {
		return herdr.AgentInfo{}, a.rollbackStartedAgent(ctx, info, paneID, fmt.Errorf("prepare restarted agy: %w", err))
	}
	a.debugf("lifecycle start-in-place success developer=%q pane=%q status=%q resume=%t", info.developer, started.PaneID, started.AgentStatus, sessionID != "")
	return started, nil
}

func (a *App) restartFreshDeveloperInPlace(ctx context.Context, info runtimeContext, developer herdr.AgentInfo) (herdr.AgentInfo, error) {
	a.debugf("lifecycle restart-fresh begin developer=%q pane=%q", info.developer, developer.PaneID)
	supervisor, err := a.supervisorPane(ctx, info)
	if err != nil {
		return herdr.AgentInfo{}, err
	}
	identity, err := a.developerRecoveryIdentityInScope(ctx, info, supervisor, developer, true)
	if err != nil {
		return herdr.AgentInfo{}, fmt.Errorf("recheck fresh agy session before restart: %w", err)
	}
	if !identity.pending {
		return herdr.AgentInfo{}, fmt.Errorf("agy conversation appeared before fresh restart; developer was kept")
	}
	developer = identity.agent
	paneID := developer.PaneID
	if err := a.markDeveloperPane(ctx, info, paneID); err != nil {
		return herdr.AgentInfo{}, err
	}
	if err := a.interruptAndWaitAgent(ctx, info.developer); err != nil {
		return herdr.AgentInfo{}, fmt.Errorf("stop quota-limited fresh developer: %w", err)
	}

	restarted, err := a.herdr.StartAgy(ctx, info.developer, paneID)
	if err != nil {
		_ = a.markRepairPane(ctx, info, paneID)
		return herdr.AgentInfo{}, fmt.Errorf("restart fresh agy in developer pane: %w; next ask will retry repair", err)
	}
	if err := validateDeveloperSession(restarted, info, supervisor); err != nil {
		return herdr.AgentInfo{}, a.rollbackStartedAgent(ctx, info, paneID, err)
	}
	_ = a.herdr.RenamePane(ctx, paneID, developerPaneLabel)
	if err := a.markDeveloperPane(ctx, info, paneID); err != nil {
		return herdr.AgentInfo{}, a.rollbackStartedAgent(ctx, info, paneID, err)
	}
	if err := a.recordFreshDeveloperSessionState(ctx, restarted); err != nil {
		return herdr.AgentInfo{}, a.rollbackStartedAgent(ctx, info, paneID, err)
	}
	if err := a.ensureAgyReady(ctx, paneID); err != nil {
		return herdr.AgentInfo{}, a.rollbackStartedAgent(ctx, info, paneID, fmt.Errorf("prepare restarted fresh agy: %w", err))
	}
	a.debugf("lifecycle restart-fresh success developer=%q pane=%q status=%q", info.developer, restarted.PaneID, restarted.AgentStatus)
	return restarted, nil
}

func (a *App) restartDeveloperInPlace(ctx context.Context, info runtimeContext, developer herdr.AgentInfo) (herdr.AgentInfo, error) {
	a.debugf("lifecycle restart-resume begin developer=%q pane=%q has_session=%t", info.developer, developer.PaneID, developer.AgentSession != nil && strings.TrimSpace(developer.AgentSession.Value) != "")
	supervisor, err := a.supervisorPane(ctx, info)
	if err != nil {
		return herdr.AgentInfo{}, err
	}
	identity, err := a.developerRecoveryIdentityInScope(ctx, info, supervisor, developer, false)
	if err != nil {
		return herdr.AgentInfo{}, fmt.Errorf("recheck agy conversation before restart: %w", err)
	}
	developer = identity.agent
	sessionID, err := exactAgySessionID(developer)
	if err != nil {
		return herdr.AgentInfo{}, fmt.Errorf("cannot restart agy safely: %w", err)
	}
	paneID := developer.PaneID
	if err := a.markDeveloperPane(ctx, info, paneID); err != nil {
		return herdr.AgentInfo{}, err
	}
	if err := a.interruptAndWaitAgent(ctx, info.developer); err != nil {
		return herdr.AgentInfo{}, fmt.Errorf("stop quota-limited developer: %w", err)
	}

	resumed, err := a.herdr.StartAgyWithSession(ctx, info.developer, paneID, sessionID)
	if err != nil {
		_ = a.markRepairPane(ctx, info, paneID)
		return herdr.AgentInfo{}, fmt.Errorf("restart agy in developer pane: %w; next ask will retry repair", err)
	}
	if err := validateAgySessionContinuity(sessionID, resumed); err != nil {
		return herdr.AgentInfo{}, a.rollbackStartedAgent(ctx, info, paneID, err)
	}
	if err := validateDeveloperSession(resumed, info, supervisor); err != nil {
		return herdr.AgentInfo{}, a.rollbackStartedAgent(ctx, info, paneID, err)
	}
	_ = a.herdr.RenamePane(ctx, paneID, developerPaneLabel)
	if err := a.markDeveloperPane(ctx, info, paneID); err != nil {
		return herdr.AgentInfo{}, a.rollbackStartedAgent(ctx, info, paneID, err)
	}
	if err := a.persistDeveloperSession(ctx, paneID, resumed); err != nil {
		return herdr.AgentInfo{}, a.rollbackStartedAgent(ctx, info, paneID, err)
	}
	if err := a.ensureAgyReady(ctx, paneID); err != nil {
		return herdr.AgentInfo{}, a.rollbackStartedAgent(ctx, info, paneID, fmt.Errorf("prepare restarted agy: %w", err))
	}
	a.debugf("lifecycle restart-resume success developer=%q pane=%q status=%q", info.developer, resumed.PaneID, resumed.AgentStatus)
	return resumed, nil
}

func (a *App) interruptAndWaitAgent(ctx context.Context, target string) error {
	a.debugf("lifecycle agent-stop interrupt developer=%q attempt=1", target)
	if err := a.herdr.SendAgentKeys(ctx, target, "ctrl+c"); err != nil {
		if herdr.IsCode(err, "agent_not_found") {
			return nil
		}
		return err
	}
	grace := a.agentStopEscalation
	if grace <= 0 {
		grace = 1500 * time.Millisecond
	}
	if err := a.waitAgentReleasedFor(ctx, target, grace); err == nil {
		return nil
	} else if !errors.Is(err, errAgentReleaseTimeout) {
		return err
	}

	// agy's idle TUI commonly treats the first Ctrl+C as cancellation and the
	// second as exit. Escalate automatically so users never need to run the
	// stop command twice.
	a.debugf("lifecycle agent-stop interrupt developer=%q attempt=2", target)
	if err := a.herdr.SendAgentKeys(ctx, target, "ctrl+c"); err != nil {
		if herdr.IsCode(err, "agent_not_found") {
			return nil
		}
		return err
	}
	return a.waitAgentReleased(ctx, target)
}

func (a *App) waitAgentReleased(ctx context.Context, target string) error {
	timeout := a.agentStopTimeout
	if timeout <= 0 {
		timeout = 15 * time.Second
	}
	return a.waitAgentReleasedFor(ctx, target, timeout)
}

func (a *App) waitAgentReleasedFor(ctx context.Context, target string, timeout time.Duration) error {
	if timeout <= 0 {
		return fmt.Errorf("%w; developer pane was kept", errAgentReleaseTimeout)
	}
	deadline := time.NewTimer(timeout)
	defer deadline.Stop()
	poll := a.developerPoll
	if poll <= 0 {
		poll = 10 * time.Millisecond
	}
	ticker := time.NewTicker(poll)
	defer ticker.Stop()
	a.debugf("lifecycle wait-agent-stop begin developer=%q timeout=%s poll=%s", target, timeout.Round(time.Millisecond), poll.Round(time.Millisecond))
	for {
		_, err := a.herdr.GetAgent(ctx, target)
		if herdr.IsCode(err, "agent_not_found") {
			a.debugf("lifecycle wait-agent-stop success developer=%q", target)
			return nil
		}
		if err != nil {
			a.debugf("lifecycle wait-agent-stop error developer=%q error=%q", target, err)
			return fmt.Errorf("wait for agy to stop: %w", err)
		}
		select {
		case <-ctx.Done():
			a.debugf("lifecycle wait-agent-stop cancelled developer=%q error=%q", target, ctx.Err())
			return ctx.Err()
		case <-deadline.C:
			a.debugf("lifecycle wait-agent-stop timeout developer=%q timeout=%s", target, timeout.Round(time.Millisecond))
			return fmt.Errorf("%w within %s; developer pane was kept", errAgentReleaseTimeout, timeout.Round(time.Millisecond))
		case <-ticker.C:
		}
	}
}
