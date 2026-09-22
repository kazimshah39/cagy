package app

import (
	"context"
	"fmt"
	"strings"
	"time"

	"github.com/kazimshah39/herdr-tandem/internal/herdr"
)

type sidebarMode string

const (
	sidebarModeCompact  sidebarMode = "compact"
	sidebarModeExpanded sidebarMode = "expanded"
	sidebarModeEnv                  = "HERDR_TANDEM_SIDEBAR_MODE"
)

type sidebarRepresentative string

const (
	sidebarRepresentativeSupervisor sidebarRepresentative = "supervisor"
	sidebarRepresentativeDeveloper  sidebarRepresentative = "developer"
)

func parseSidebarMode(value string) (sidebarMode, error) {
	mode := sidebarMode(strings.TrimSpace(value))
	switch mode {
	case sidebarModeCompact, sidebarModeExpanded:
		return mode, nil
	default:
		return "", fmt.Errorf("sidebar mode %q is unsupported", value)
	}
}

func (m sidebarMode) supervisorDisplayName() string {
	if m == sidebarModeExpanded {
		return expandedSupervisorDisplayName
	}
	return compactDisplayName
}

func (m sidebarMode) developerDisplayName() string {
	if m == sidebarModeExpanded {
		return developerDisplayName
	}
	return compactDisplayName
}

func (a *App) runtimeSidebarRecord(info runtimeContext) (runtimeRecord, sidebarMode, error) {
	record, exists, err := a.runtimeManager().FindForScope(info)
	if err != nil {
		return runtimeRecord{}, "", fmt.Errorf("read herdr-tandem runtime sidebar mode: %w", err)
	}
	if !exists {
		return runtimeRecord{}, "", fmt.Errorf("herdr-tandem runtime record is missing; restart the supervisor")
	}
	mode, err := parseSidebarMode(record.SidebarMode)
	if err != nil {
		return runtimeRecord{}, "", err
	}
	if environment := strings.TrimSpace(a.getenv(sidebarModeEnv)); environment != "" {
		environmentMode, err := parseSidebarMode(environment)
		if err != nil {
			return runtimeRecord{}, "", err
		}
		if environmentMode != mode {
			return runtimeRecord{}, "", fmt.Errorf("herdr-tandem sidebar mode is stale; restart the supervisor")
		}
	}
	return record, mode, nil
}

func (a *App) runtimeSidebarMode(info runtimeContext) (sidebarMode, error) {
	_, mode, err := a.runtimeSidebarRecord(info)
	return mode, err
}

func (a *App) setPaneSidebarVisibility(ctx context.Context, paneID string, visibility herdr.PaneVisibility) error {
	if strings.TrimSpace(paneID) == "" {
		return fmt.Errorf("sidebar pane ID is missing")
	}
	return a.reportSidebarVisibility(ctx, paneID, visibility)
}

// setSidebarRepresentative preserves at least one visible row. The destination
// is shown before the previous representative is hidden. If the first write
// fails the second write is not attempted; if the second fails both rows may be
// visible, which is safer than hiding both.
func (a *App) setSidebarRepresentative(ctx context.Context, mode sidebarMode, supervisorPane, developerPane string, representative sidebarRepresentative) error {
	if mode == sidebarModeExpanded {
		if err := a.setPaneSidebarVisibility(ctx, supervisorPane, herdr.PaneVisible); err != nil {
			return err
		}
		return a.setPaneSidebarVisibility(ctx, developerPane, herdr.PaneVisible)
	}
	if mode != sidebarModeCompact {
		return fmt.Errorf("sidebar mode %q is unsupported", mode)
	}
	switch representative {
	case sidebarRepresentativeDeveloper:
		if err := a.setPaneSidebarVisibility(ctx, developerPane, herdr.PaneVisible); err != nil {
			return err
		}
		return a.setPaneSidebarVisibility(ctx, supervisorPane, herdr.PaneHidden)
	case sidebarRepresentativeSupervisor:
		if err := a.setPaneSidebarVisibility(ctx, supervisorPane, herdr.PaneVisible); err != nil {
			return err
		}
		return a.setPaneSidebarVisibility(ctx, developerPane, herdr.PaneHidden)
	default:
		return fmt.Errorf("sidebar representative %q is unsupported", representative)
	}
}

func compactRepresentativeForStatus(status string, managedTask bool) sidebarRepresentative {
	if !managedTask {
		return sidebarRepresentativeSupervisor
	}
	switch strings.TrimSpace(status) {
	case "idle", "done", "not_running", "absent":
		return sidebarRepresentativeSupervisor
	case "working", "blocked", "unknown", "":
		return sidebarRepresentativeDeveloper
	default:
		return sidebarRepresentativeDeveloper
	}
}

func (a *App) validateSidebarPanes(ctx context.Context, info runtimeContext, developerPane string) (string, bool, error) {
	supervisor, err := a.supervisorPane(ctx, info)
	if err != nil {
		return "", false, err
	}
	developer, err := a.herdr.GetAgent(ctx, info.developer)
	if err == nil {
		pane, validateErr := a.validatedDeveloperPane(ctx, developer, info, supervisor)
		if validateErr != nil {
			return "", false, validateErr
		}
		return pane.PaneID, true, nil
	}
	if !herdr.IsCode(err, "agent_not_found") {
		return "", false, fmt.Errorf("validate developer sidebar pane: %w", err)
	}
	if strings.TrimSpace(developerPane) == "" {
		return "", false, nil
	}
	pane, err := a.herdr.GetPane(ctx, developerPane)
	if herdr.IsCode(err, "pane_not_found") {
		return "", false, nil
	}
	if err != nil {
		return "", false, fmt.Errorf("validate developer sidebar pane: %w", err)
	}
	if err := validatePaneScope(pane, info, supervisor); err != nil {
		return "", false, err
	}
	if pane.Tokens["herdr_tandem_owner"] != info.developer {
		return "", false, fmt.Errorf("developer sidebar pane ownership is invalid")
	}
	role := pane.Tokens["herdr_tandem_role"]
	if role != "developer" && role != "repair" {
		return "", false, fmt.Errorf("developer sidebar pane role is invalid")
	}
	return pane.PaneID, false, nil
}

func (a *App) applyVerifiedSidebarRepresentative(ctx context.Context, mode sidebarMode, info runtimeContext, developerPane string, representative sidebarRepresentative) error {
	validatedPane, developerRunning, err := a.validateSidebarPanes(ctx, info, developerPane)
	if err != nil {
		return err
	}
	if validatedPane == "" {
		if representative == sidebarRepresentativeDeveloper {
			return fmt.Errorf("developer sidebar pane is missing")
		}
		return a.setPaneSidebarVisibility(ctx, info.supervisor, herdr.PaneVisible)
	}
	if !developerRunning && representative == sidebarRepresentativeDeveloper {
		return fmt.Errorf("developer is not running")
	}
	return a.setSidebarRepresentative(ctx, mode, info.supervisor, validatedPane, representative)
}

func (a *App) warnSidebarTransition(ctx context.Context, mode sidebarMode, info runtimeContext, developerPane string, representative sidebarRepresentative) {
	if err := a.applyVerifiedSidebarRepresentative(ctx, mode, info, developerPane, representative); err != nil {
		a.debugf("sidebar transition mode=%q representative=%q supervisor_pane=%q developer_pane=%q ok=false error=%q", mode, representative, info.supervisor, developerPane, err)
		fmt.Fprintf(a.stderr, "herdr-tandem warning: sidebar state could not be updated: %v\n", err)
		return
	}
	a.debugf("sidebar transition mode=%q representative=%q supervisor_pane=%q developer_pane=%q ok=true", mode, representative, info.supervisor, developerPane)
}

func (a *App) reconcileSidebarAfterManagedTask(ctx context.Context, mode sidebarMode, info runtimeContext, developerPane string, completed bool) {
	refreshCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 5*time.Second)
	defer cancel()
	ctx = refreshCtx
	if completed {
		a.warnSidebarTransition(ctx, mode, info, developerPane, sidebarRepresentativeSupervisor)
		return
	}
	status := "unknown"
	developer, err := a.herdr.GetAgent(ctx, info.developer)
	if err == nil {
		status = developer.AgentStatus
		if developer.PaneID != "" {
			developerPane = developer.PaneID
		}
	} else if herdr.IsCode(err, "agent_not_found") {
		status = "absent"
	}
	representative := compactRepresentativeForStatus(status, true)
	a.warnSidebarTransition(ctx, mode, info, developerPane, representative)
}
