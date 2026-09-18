package app

import (
	"context"
	"fmt"
	"path/filepath"
	"regexp"
	"strings"
	"time"

	"github.com/kazimshah39/cagy/internal/herdr"
)

const (
	developerPaneLabel     = "agy Developer"
	recoveryPaneLabel      = "agy Recovery"
	agySessionStateToken   = "cagy_session_state"
	agySessionStatePending = "pending"
	agySessionStateReady   = "ready"
)

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
	pane, err := a.herdr.GetPane(ctx, info.supervisor)
	if err != nil {
		return herdr.PaneInfo{}, fmt.Errorf("find cagy supervisor pane: %w", err)
	}
	if pane.WorkspaceID != info.workspaceID {
		return herdr.PaneInfo{}, fmt.Errorf("cagy supervisor belongs to another Herdr workspace")
	}
	if strings.TrimSpace(pane.TabID) == "" {
		return herdr.PaneInfo{}, fmt.Errorf("cagy supervisor tab is missing")
	}
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
	if err := validateDeveloperSession(agent, info, supervisor); err != nil {
		return herdr.PaneInfo{}, err
	}
	pane, err := a.herdr.GetPane(ctx, agent.PaneID)
	if err != nil {
		return herdr.PaneInfo{}, fmt.Errorf("find developer pane %s: %w", agent.PaneID, err)
	}
	if err := validatePaneScope(pane, info, supervisor); err != nil {
		return herdr.PaneInfo{}, fmt.Errorf("developer pane scope invalid: %w", err)
	}
	owner := pane.Tokens["cagy_owner"]
	role := pane.Tokens["cagy_role"]
	if owner == info.developer && role == "developer" {
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
	if err := a.herdr.ReportPaneOwnership(ctx, paneID, paneOwnershipSource, owner, role); err != nil {
		return fmt.Errorf("mark cagy %s pane: %w", role, err)
	}
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
	sessionID, err := exactAgySessionID(agent)
	if err != nil {
		return err
	}
	if err := a.herdr.ReportPaneSession(ctx, paneID, paneOwnershipSource, sessionID); err != nil {
		return fmt.Errorf("persist agy conversation identity: %w", err)
	}
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

func (a *App) markRecoveryPane(ctx context.Context, info runtimeContext, paneID string) error {
	renameErr := a.herdr.RenamePane(ctx, paneID, recoveryPaneLabel)
	ownerErr := a.markPaneRole(ctx, paneID, info.developer, "recovery")
	if renameErr != nil && ownerErr != nil {
		return fmt.Errorf("rename recovery pane: %v; %w", renameErr, ownerErr)
	}
	if renameErr != nil {
		return fmt.Errorf("rename recovery pane: %w", renameErr)
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
	supervisor, err := a.supervisorPane(ctx, info)
	if err != nil {
		return herdr.AgentInfo{}, err
	}
	developer, err := a.herdr.GetAgent(ctx, info.developer)
	if err == nil {
		if err := a.validateExistingDeveloper(ctx, developer, info, supervisor); err != nil {
			return herdr.AgentInfo{}, err
		}
		return developer, nil
	}
	if !herdr.IsCode(err, "agent_not_found") {
		return herdr.AgentInfo{}, fmt.Errorf("find agy developer: %w", err)
	}
	return a.repairMissingDeveloper(ctx, info)
}

func (a *App) rollbackStartedAgent(ctx context.Context, info runtimeContext, paneID string, originalErr error) error {
	var cleanupErrs []string
	stopped := false
	if err := a.herdr.SendAgentKeys(ctx, info.developer, "ctrl+c"); err != nil {
		cleanupErrs = append(cleanupErrs, fmt.Sprintf("stop agent: %v", err))
	} else if err := a.waitAgentReleased(ctx, info.developer); err != nil {
		cleanupErrs = append(cleanupErrs, fmt.Sprintf("wait agent release: %v", err))
	} else {
		stopped = true
	}
	if stopped {
		if err := a.markRecoveryPane(ctx, info, paneID); err != nil {
			cleanupErrs = append(cleanupErrs, fmt.Sprintf("mark recovery pane: %v", err))
		}
	} else {
		if err := a.markPaneRole(ctx, paneID, info.developer, "developer"); err != nil {
			cleanupErrs = append(cleanupErrs, fmt.Sprintf("restore developer ownership: %v", err))
		}
	}
	if len(cleanupErrs) > 0 {
		paneDesc := "developer pane left visible"
		if stopped {
			paneDesc = "recovery pane left visible"
		}
		return fmt.Errorf("%w; rollback cleanup failed: %s; %s", originalErr, strings.Join(cleanupErrs, "; "), paneDesc)
	}
	return fmt.Errorf("%w; recovery pane left visible", originalErr)
}

func (a *App) repairMissingDeveloper(ctx context.Context, info runtimeContext) (herdr.AgentInfo, error) {
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

	sessionID := strings.TrimSpace(pane.Tokens["cagy_session"])
	if !created {
		if sessionID == "" {
			return herdr.AgentInfo{}, fmt.Errorf("repair pane has no saved agy conversation identity; refusing to guess with --continue")
		}
		if !agyConversationIDPattern.MatchString(sessionID) {
			return herdr.AgentInfo{}, fmt.Errorf("repair pane has an invalid agy conversation identity; recovery pane left visible")
		}
		if pane.Tokens[agySessionStateToken] != agySessionStateReady {
			return herdr.AgentInfo{}, fmt.Errorf("repair pane has an invalid agy conversation state; recovery pane left visible")
		}
	}
	if err := a.markRecoveryPane(ctx, info, pane.PaneID); err != nil {
		if created {
			_ = a.herdr.ClosePane(ctx, pane.PaneID)
		}
		return herdr.AgentInfo{}, err
	}
	_ = a.herdr.RenamePane(ctx, pane.PaneID, developerPaneLabel)
	var developer herdr.AgentInfo
	if sessionID != "" {
		developer, err = a.herdr.StartAgyWithSession(ctx, info.developer, pane.PaneID, sessionID)
	} else {
		developer, err = a.herdr.StartAgy(ctx, info.developer, pane.PaneID)
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
		}
		_ = a.markRecoveryPane(ctx, info, pane.PaneID)
		return herdr.AgentInfo{}, fmt.Errorf("repair missing agy developer: %w; recovery pane left visible", err)
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
	if err := a.ensureAgyReady(ctx, pane.PaneID); err != nil {
		return herdr.AgentInfo{}, a.rollbackStartedAgent(ctx, info, pane.PaneID, fmt.Errorf("prepare repaired agy developer: %w", err))
	}
	return developer, nil
}

func (a *App) findRepairPane(ctx context.Context, info runtimeContext, supervisor herdr.PaneInfo) (herdr.PaneInfo, error) {
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
		if owner == info.developer && (role == "developer" || role == "recovery") {
			if pane.Agent != "" {
				return herdr.PaneInfo{}, fmt.Errorf("owned repair pane %s is running another agent", pane.PaneID)
			}
			owned = append(owned, pane)
		}
	}
	if len(owned) > 1 {
		return herdr.PaneInfo{}, fmt.Errorf("multiple cagy-owned repair panes found; check the right panes")
	}
	if len(owned) == 1 {
		if err := a.validateShellPane(ctx, owned[0].PaneID); err != nil {
			return herdr.PaneInfo{}, err
		}
		return owned[0], nil
	}
	return herdr.PaneInfo{}, nil
}

func (a *App) restartFreshDeveloperInPlace(ctx context.Context, info runtimeContext, developer herdr.AgentInfo) (herdr.AgentInfo, error) {
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
	if err := a.herdr.SendAgentKeys(ctx, info.developer, "ctrl+c"); err != nil {
		return herdr.AgentInfo{}, fmt.Errorf("stop quota-limited fresh developer: %w", err)
	}
	if err := a.waitAgentReleased(ctx, info.developer); err != nil {
		return herdr.AgentInfo{}, err
	}

	restarted, err := a.herdr.StartAgy(ctx, info.developer, paneID)
	if err != nil {
		_ = a.markRecoveryPane(ctx, info, paneID)
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
	return restarted, nil
}

func (a *App) restartDeveloperInPlace(ctx context.Context, info runtimeContext, developer herdr.AgentInfo) (herdr.AgentInfo, error) {
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
	if err := a.herdr.SendAgentKeys(ctx, info.developer, "ctrl+c"); err != nil {
		return herdr.AgentInfo{}, fmt.Errorf("stop quota-limited developer: %w", err)
	}
	if err := a.waitAgentReleased(ctx, info.developer); err != nil {
		return herdr.AgentInfo{}, err
	}

	resumed, err := a.herdr.StartAgyWithSession(ctx, info.developer, paneID, sessionID)
	if err != nil {
		_ = a.markRecoveryPane(ctx, info, paneID)
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
	return resumed, nil
}

func (a *App) waitAgentReleased(ctx context.Context, target string) error {
	timeout := a.agentStopTimeout
	if timeout <= 0 {
		timeout = 10 * time.Second
	}
	deadline := time.NewTimer(timeout)
	defer deadline.Stop()
	poll := a.developerPoll
	if poll <= 0 {
		poll = 10 * time.Millisecond
	}
	ticker := time.NewTicker(poll)
	defer ticker.Stop()
	for {
		_, err := a.herdr.GetAgent(ctx, target)
		if herdr.IsCode(err, "agent_not_found") {
			return nil
		}
		if err != nil {
			return fmt.Errorf("wait for agy to stop: %w", err)
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-deadline.C:
			return fmt.Errorf("agy did not stop within %s; developer pane was kept", timeout.Round(time.Millisecond))
		case <-ticker.C:
		}
	}
}
