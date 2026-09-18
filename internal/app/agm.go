package app

import (
	"context"
	"fmt"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"time"
)

const (
	agmRefreshInterval  = time.Hour
	agmRefreshTimeoutMS = 30 * 60 * 1000
	agmRefreshStateName = "cagy-agm-refresh-all.timestamp"
)

var agmRefreshSummaryPattern = regexp.MustCompile(`(?m)Completed:\s+(\d+)\s+successful(?:,\s+(\d+)\s+failed)?`)

// maybeRefreshAll refreshes AGM's cached quotas at most once per hour. It is
// called only from visible recovery work, never from a daemon or idle timer.
func (a *App) maybeRefreshAll(ctx context.Context, paneID string) (bool, string, error) {
	if !a.agmRefreshDue() {
		return false, "", nil
	}

	output, status, err := a.runMarkedWithTimeout(ctx, paneID, "agm refresh-all", "REFRESH", agmRefreshTimeoutMS)
	if err != nil {
		return true, output, err
	}
	if status != 0 {
		return true, output, fmt.Errorf("agm refresh-all exited with status %d", status)
	}
	ok, _, parsed := agmRefreshSummary(output)
	if !parsed || ok == 0 {
		return true, output, fmt.Errorf("agm refresh-all did not confirm a successful account refresh")
	}
	if err := a.recordAGMRefresh(a.now()); err != nil {
		return true, output, err
	}
	return true, output, nil
}

func (a *App) agmRefreshDue() bool {
	exists, err := inspectPrivateStateDir(a.stateDir)
	if err != nil || !exists {
		return true
	}
	data, exists, err := readPrivateStateFile(filepath.Join(a.stateDir, agmRefreshStateName), 256)
	if err != nil || !exists {
		return true
	}
	lastRefresh, err := time.Parse(time.RFC3339Nano, strings.TrimSpace(string(data)))
	if err != nil || lastRefresh.After(a.now()) {
		return true
	}
	return a.now().Sub(lastRefresh) >= agmRefreshInterval
}

func (a *App) recordAGMRefresh(at time.Time) error {
	data := []byte(at.UTC().Format(time.RFC3339Nano) + "\n")
	if err := writePrivateStateFile(a.stateDir, agmRefreshStateName, data); err != nil {
		return fmt.Errorf("save AGM refresh time: %w", err)
	}
	return nil
}

func agmRefreshSummary(output string) (successful, failed int, ok bool) {
	match := agmRefreshSummaryPattern.FindStringSubmatch(output)
	if len(match) != 3 {
		return 0, 0, false
	}
	successful, err := strconv.Atoi(match[1])
	if err != nil {
		return 0, 0, false
	}
	if match[2] != "" {
		failed, err = strconv.Atoi(match[2])
		if err != nil {
			return 0, 0, false
		}
	}
	return successful, failed, true
}
