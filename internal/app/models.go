package app

import (
	"context"
	"fmt"
	"strings"

	dev "github.com/kazimshah39/herdr-tandem/internal/developer"
	"github.com/kazimshah39/herdr-tandem/internal/supervisor"
)

const (
	DefaultAgySupervisorModel = supervisor.DefaultSupervisorModel
	DefaultAgyDeveloperModel  = dev.DefaultDeveloperModel
)

func (a *App) applyDefaultModels() {
	if a.developerModel == "" {
		a.developerModel = DefaultAgyDeveloperModel
	}
	if a.supervisor.ID() == supervisor.AgyID && a.supervisorModel == "" {
		a.supervisorModel = DefaultAgySupervisorModel
	}
}

func (a *App) validateModelCapabilities(ctx context.Context) error {
	a.applyDefaultModels()
	if a.developerAdapter.ID() == dev.AgyID && a.developerModel != "" {
		if err := a.checkAgyModel(ctx, "developer", a.developerModel); err != nil {
			return err
		}
	}
	if a.supervisor.ID() == supervisor.AgyID && a.supervisorModel != "" {
		if err := a.checkAgyModel(ctx, "supervisor", a.supervisorModel); err != nil {
			return err
		}
	}
	return nil
}

func (a *App) loadAgyModels(ctx context.Context) (map[string]struct{}, error) {
	if a.agyModelsChecked {
		return a.availableAgyModels, a.availableAgyModelsErr
	}
	a.agyModelsChecked = true
	result, err := a.runner.Run(ctx, a.developerAdapter.Executable(), "models")
	if err != nil {
		a.availableAgyModelsErr = fmt.Errorf("failed to query agy available models: %w", err)
		return nil, a.availableAgyModelsErr
	}
	if result.ExitCode != 0 {
		a.availableAgyModelsErr = fmt.Errorf("agy models command exited with status %d", result.ExitCode)
		return nil, a.availableAgyModelsErr
	}
	a.availableAgyModels = parseAgyModelsOutput(result.Stdout)
	return a.availableAgyModels, nil
}

func isSpinnerRune(r rune) bool {
	return r >= '\u2800' && r <= '\u28ff'
}

func isValidAgyModelID(s string) bool {
	if len(s) == 0 || len(s) > 128 {
		return false
	}
	first := s[0]
	if (first < 'a' || first > 'z') && (first < '0' || first > '9') {
		return false
	}
	last := s[len(s)-1]
	if (last < 'a' || last > 'z') && (last < '0' || last > '9') {
		return false
	}
	for i := 0; i < len(s); i++ {
		c := s[i]
		if (c >= 'a' && c <= 'z') || (c >= '0' && c <= '9') || c == '-' || c == '.' || c == '_' {
			continue
		}
		return false
	}
	switch s {
	case "error", "warning", "info", "status", "failed", "fatal",
		"fetching", "available", "supported", "unsupported", "unavailable",
		"model", "models", "model-id", "model_id", "note", "help",
		"table", "column", "name", "id", "display-name", "display_name",
		"description", "desc", "alias", "aliases", "provider", "context",
		"default", "current", "active", "deprecated", "disabled":
		return false
	}
	return true
}

func parseAgyModelsOutput(output string) map[string]struct{} {
	models := make(map[string]struct{})
	normalized := strings.ReplaceAll(output, "\r\n", "\n")
	normalized = strings.ReplaceAll(normalized, "\r", "\n")

	for _, rawLine := range strings.Split(normalized, "\n") {
		line := strings.TrimSpace(rawLine)
		if line == "" {
			continue
		}

		// Skip progress, header, and error/status indicators
		lower := strings.ToLower(line)
		if strings.Contains(lower, "available models") || strings.HasPrefix(lower, "fetching") {
			continue
		}
		if strings.HasPrefix(lower, "error:") || strings.HasPrefix(lower, "warning:") ||
			strings.HasPrefix(lower, "failed:") || strings.HasPrefix(lower, "status:") ||
			strings.HasPrefix(lower, "info:") || strings.HasPrefix(lower, "note:") ||
			strings.HasPrefix(lower, "fatal:") || strings.HasPrefix(lower, "model:") {
			continue
		}
		if strings.Contains(lower, "unavailable") || strings.Contains(lower, "offline") || strings.Contains(lower, "disabled") {
			continue
		}
		upper := strings.ToUpper(line)
		if strings.HasPrefix(upper, "MODEL") && strings.Contains(upper, "ID") {
			continue
		}

		// Strip leading spinner runes if present (e.g. Braille spinners: ⠋, ⠸)
		line = strings.TrimLeftFunc(line, isSpinnerRune)
		line = strings.TrimSpace(line)
		if line == "" {
			continue
		}

		// Re-check after stripping spinner
		lower = strings.ToLower(line)
		if strings.Contains(lower, "available models") || strings.HasPrefix(lower, "fetching") {
			continue
		}
		if strings.HasPrefix(lower, "error:") || strings.HasPrefix(lower, "warning:") ||
			strings.HasPrefix(lower, "failed:") || strings.HasPrefix(lower, "status:") ||
			strings.HasPrefix(lower, "info:") || strings.HasPrefix(lower, "note:") ||
			strings.HasPrefix(lower, "fatal:") || strings.HasPrefix(lower, "model:") {
			continue
		}
		if strings.Contains(lower, "unavailable") || strings.Contains(lower, "offline") || strings.Contains(lower, "disabled") {
			continue
		}

		fields := strings.Fields(line)
		if len(fields) == 0 {
			continue
		}

		idCandidate := fields[0]
		if !isValidAgyModelID(idCandidate) {
			continue
		}

		// If there are multiple fields, verify it is a recognized tabular listing row
		// (separated by a TAB or at least two spaces from the description),
		// rather than arbitrary prose.
		if len(fields) > 1 {
			rest := line[len(idCandidate):]
			if !strings.HasPrefix(rest, "\t") && !strings.HasPrefix(rest, "  ") {
				// Not a tab-separated or column-padded table row; reject as prose
				continue
			}
		}

		models[idCandidate] = struct{}{}
	}
	return models
}

func (a *App) checkAgyModel(ctx context.Context, role string, model string) error {
	if model == "" {
		return nil
	}
	models, err := a.loadAgyModels(ctx)
	if err != nil {
		return err
	}
	if _, ok := models[model]; !ok {
		return fmt.Errorf("agy %s model is not available; run 'agy models' to list supported models", role)
	}
	return nil
}
