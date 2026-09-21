package app

import (
	"context"
	"fmt"
	"strings"
	"time"

	"github.com/kazimshah39/herdr-tandem/internal/herdr"
)

func (a *App) developerReadyWaitTimeout() time.Duration {
	if a.developerReadyTimeout > 0 {
		return a.developerReadyTimeout
	}
	return time.Duration(developerReadyTimeoutMS) * time.Millisecond
}

func (a *App) ensureDeveloperReady(ctx context.Context, paneID string) error {
	timeout := a.developerReadyWaitTimeout()
	deadline := time.Now().Add(timeout)
	trustPrompt := strings.ToLower(a.developerAdapter.TrustPrompt())
	readyText := a.developerAdapter.ReadyText()
	trustAccepted := false

	for {
		output, err := a.herdr.ReadPane(ctx, paneID, 200)
		if err != nil {
			return fmt.Errorf("read agy startup screen: %w", err)
		}
		lower := strings.ToLower(output)
		if strings.Contains(lower, strings.ToLower(readyText)) {
			return nil
		}
		if !trustAccepted && trustPrompt != "" && strings.Contains(lower, trustPrompt) {
			if err := a.herdr.SendPaneKeys(ctx, paneID, "enter"); err != nil {
				return fmt.Errorf("accept agy project trust: %w", err)
			}
			trustAccepted = true
		}

		remaining := time.Until(deadline)
		if remaining <= 0 {
			break
		}
		waitDuration := 1 * time.Second
		if trustAccepted {
			waitDuration = remaining
		} else if waitDuration > remaining {
			waitDuration = remaining
		}

		waitMS := int(waitDuration.Milliseconds())
		if waitMS < 1 {
			waitMS = 1
		}
		waitErr := a.herdr.WaitPaneMatch(ctx, paneID, readyText, waitMS)
		if waitErr == nil {
			return nil
		}
		if ctx.Err() != nil {
			return ctx.Err()
		}
		if !herdr.IsCode(waitErr, "timeout") && !strings.Contains(strings.ToLower(waitErr.Error()), "timeout") && !strings.Contains(strings.ToLower(waitErr.Error()), "timed out") {
			return fmt.Errorf("wait for agy readiness: %w", waitErr)
		}
	}
	return fmt.Errorf("wait for agy readiness: timeout: timed out waiting for output match")
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
