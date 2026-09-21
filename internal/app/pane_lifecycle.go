package app

import (
	"context"
	"fmt"
	"strings"

	"github.com/kazimshah39/herdr-tandem/internal/herdr"
)

func (a *App) ensureDeveloperReady(ctx context.Context, paneID string) error {
	output, err := a.herdr.ReadPane(ctx, paneID, 200)
	if err != nil {
		return fmt.Errorf("read agy startup screen: %w", err)
	}
	if strings.Contains(strings.ToLower(output), strings.ToLower(a.developerAdapter.TrustPrompt())) {
		if err := a.herdr.SendPaneKeys(ctx, paneID, "enter"); err != nil {
			return fmt.Errorf("accept agy project trust: %w", err)
		}
	}
	if err := a.herdr.WaitPaneMatch(ctx, paneID, a.developerAdapter.ReadyText(), durationMS(developerReadyTimeoutMS)); err != nil {
		return fmt.Errorf("wait for agy readiness: %w", err)
	}
	return nil
}

func (a *App) confirmClosePane(ctx context.Context, paneID string) error {
	for attempt := 0; attempt < 3; attempt++ {
		err := a.herdr.ClosePane(ctx, paneID)
		if err != nil && !herdr.IsCode(err, "pane_not_found") {
			return fmt.Errorf("close developer pane: %w", err)
		}
		_, getErr := a.herdr.GetPane(ctx, paneID)
		if herdr.IsCode(getErr, "pane_not_found") {
			return nil
		}
		if getErr != nil {
			return fmt.Errorf("verify closed developer pane: %w", getErr)
		}
	}
	return fmt.Errorf("developer pane is still open")
}
