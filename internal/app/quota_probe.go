package app

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"time"

	"github.com/kazimshah39/cagy/internal/accounts"
	"github.com/kazimshah39/cagy/internal/quota"
)

type quotaClassification string

const (
	quotaAvailable quotaClassification = "available"
	quotaLow       quotaClassification = "low"
	quotaExhausted quotaClassification = "exhausted"
	quotaUnknown   quotaClassification = "unknown"
)

type quotaProbeResult struct {
	Class             quotaClassification
	Reason            string
	ModelID           string
	Group             string
	WeeklyRemaining   *float64
	FiveHourRemaining *float64
	ObservedAt        time.Time
}

type agyCommandEnvelope struct {
	Status  string `json:"status"`
	Command struct {
		Name string          `json:"name"`
		Data json.RawMessage `json:"data"`
	} `json:"command"`
}

type agyModel struct {
	ID    string `json:"id"`
	Label string `json:"label"`
}

type agyQuota struct {
	Groups []agyQuotaGroup `json:"groups"`
}

type agyQuotaGroup struct {
	Name    string           `json:"name"`
	Buckets []agyQuotaBucket `json:"buckets"`
}

type agyQuotaBucket struct {
	ID                string   `json:"id"`
	Name              string   `json:"name"`
	RemainingFraction *float64 `json:"remaining_fraction"`
}

type agyProbeCommandError struct {
	strong bool
	reason string
}

func (e *agyProbeCommandError) Error() string { return e.reason }

func (a *App) probeAgyQuota(ctx context.Context) quotaProbeResult {
	observedAt := a.now().UTC()
	a.debugf("quota-probe begin source=%q", "agy-canonical")
	modelPayload, err := a.runAgySlashCommand(ctx, "/model")
	if err != nil {
		result := classifyProbeCommandError(err, observedAt)
		a.debugQuotaProbe("agy-canonical", result)
		return result
	}
	var model agyModel
	if err := json.Unmarshal(modelPayload, &model); err != nil {
		result := quotaProbeResult{Class: quotaUnknown, Reason: "model response was invalid", ObservedAt: observedAt}
		a.debugQuotaProbe("agy-canonical", result)
		return result
	}
	modelID := firstNonEmpty(model.ID, model.Label)
	groupName, ok := quotaGroupForModel(model)
	if !ok {
		result := quotaProbeResult{Class: quotaUnknown, Reason: "model quota group is unknown", ModelID: modelID, ObservedAt: observedAt}
		a.debugQuotaProbe("agy-canonical", result)
		return result
	}

	quotaPayload, err := a.runAgySlashCommand(ctx, "/quota")
	if err != nil {
		result := classifyProbeCommandError(err, observedAt)
		result.ModelID = modelID
		result.Group = groupName
		a.debugQuotaProbe("agy-canonical", result)
		return result
	}
	var status agyQuota
	if err := json.Unmarshal(quotaPayload, &status); err != nil {
		result := quotaProbeResult{Class: quotaUnknown, Reason: "quota response was invalid", ModelID: modelID, Group: groupName, ObservedAt: observedAt}
		a.debugQuotaProbe("agy-canonical", result)
		return result
	}
	result := classifyQuota(status, modelID, groupName, observedAt)
	a.debugQuotaProbe("agy-canonical", result)
	return result
}

func (a *App) debugQuotaProbe(source string, result quotaProbeResult) {
	a.debugf("quota-probe result source=%q class=%q reason=%q model=%q group=%q weekly=%q five_hour=%q", source, result.Class, result.Reason, result.ModelID, result.Group, debugQuotaRemaining(result.WeeklyRemaining), debugQuotaRemaining(result.FiveHourRemaining))
}

func debugQuotaRemaining(value *float64) string {
	if value == nil {
		return "unknown"
	}
	return fmt.Sprintf("%.1f%%", *value*100)
}

func classifyProbeCommandError(err error, observedAt time.Time) quotaProbeResult {
	var probeErr *agyProbeCommandError
	if ok := errorAs(err, &probeErr); ok && probeErr.strong {
		return quotaProbeResult{Class: quotaExhausted, Reason: "provider reported quota exhaustion", ObservedAt: observedAt}
	}
	return quotaProbeResult{Class: quotaUnknown, Reason: "agy quota probe failed", ObservedAt: observedAt}
}

// errorAs is a small seam kept here so probe classification stays easy to test.
func errorAs(err error, target **agyProbeCommandError) bool {
	for err != nil {
		if typed, ok := err.(*agyProbeCommandError); ok {
			*target = typed
			return true
		}
		type unwrapper interface{ Unwrap() error }
		wrapped, ok := err.(unwrapper)
		if !ok {
			break
		}
		err = wrapped.Unwrap()
	}
	return false
}

func (a *App) runAgySlashCommand(ctx context.Context, command string) (json.RawMessage, error) {
	a.debugf("quota-command begin command=%q timeout=%s", command, 30*time.Second)
	probeCtx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()
	result, err := a.runner.Run(probeCtx, "agy", "-p", command, "--output-format", "json", "--print-timeout", "30s")
	if err != nil {
		a.debugf("quota-command process-error command=%q error=%q", command, err)
		return nil, &agyProbeCommandError{reason: "agy command could not run"}
	}
	combined := strings.TrimSpace(result.Stderr + "\n" + result.Stdout)
	if result.ExitCode != 0 {
		a.debugf("quota-command failed command=%q exit=%d stdout_bytes=%d stderr_bytes=%d strong_quota=%t", command, result.ExitCode, len(result.Stdout), len(result.Stderr), quota.Detected(combined))
		return nil, &agyProbeCommandError{strong: quota.Detected(combined), reason: "agy command failed"}
	}
	var response agyCommandEnvelope
	if err := json.Unmarshal([]byte(strings.TrimSpace(result.Stdout)), &response); err != nil {
		a.debugf("quota-command invalid-json command=%q exit=%d stdout_bytes=%d stderr_bytes=%d strong_quota=%t", command, result.ExitCode, len(result.Stdout), len(result.Stderr), quota.Detected(combined))
		return nil, &agyProbeCommandError{strong: quota.Detected(combined), reason: "agy command returned invalid JSON"}
	}
	if !strings.EqualFold(response.Status, "SUCCESS") {
		a.debugf("quota-command unsuccessful command=%q status=%q strong_quota=%t", command, response.Status, quota.Detected(combined))
		return nil, &agyProbeCommandError{strong: quota.Detected(combined), reason: "agy command did not succeed"}
	}
	if len(response.Command.Data) == 0 || string(response.Command.Data) == "null" {
		a.debugf("quota-command empty command=%q", command)
		return nil, &agyProbeCommandError{reason: "agy command returned no data"}
	}
	a.debugf("quota-command success command=%q response_name=%q data_bytes=%d", command, response.Command.Name, len(response.Command.Data))
	return response.Command.Data, nil
}
func classifyQuota(status agyQuota, modelID, expectedGroup string, observedAt time.Time) quotaProbeResult {
	result := quotaProbeResult{Class: quotaUnknown, Reason: "quota group was not found", ModelID: modelID, Group: expectedGroup, ObservedAt: observedAt}
	for _, group := range status.Groups {
		if !strings.EqualFold(strings.TrimSpace(group.Name), expectedGroup) {
			continue
		}
		for _, bucket := range group.Buckets {
			if bucket.RemainingFraction == nil || *bucket.RemainingFraction < 0 || *bucket.RemainingFraction > 1 {
				continue
			}
			value := *bucket.RemainingFraction
			bucketName := strings.ToLower(bucket.ID + " " + bucket.Name)
			switch {
			case strings.Contains(bucketName, "weekly"), strings.Contains(bucketName, "week"):
				result.WeeklyRemaining = &value
			case strings.Contains(bucketName, "5h"), strings.Contains(bucketName, "5-hour"), strings.Contains(bucketName, "5 hour"):
				result.FiveHourRemaining = &value
			}
		}
		if result.WeeklyRemaining == nil || result.FiveHourRemaining == nil {
			result.Reason = "quota buckets were incomplete"
			return result
		}
		switch {
		case *result.WeeklyRemaining <= 0 || *result.FiveHourRemaining <= 0:
			result.Class, result.Reason = quotaExhausted, "quota is exhausted"
		case *result.WeeklyRemaining <= accounts.WeeklySwitchThreshold || *result.FiveHourRemaining <= accounts.FiveHourSwitchThreshold:
			result.Class, result.Reason = quotaLow, "quota is below the safe threshold"
		default:
			result.Class, result.Reason = quotaAvailable, "quota is available"
		}
		return result
	}
	return result
}

func (result quotaProbeResult) snapshot() accounts.QuotaSnapshot {
	class := accounts.QuotaUnknown
	switch result.Class {
	case quotaAvailable:
		class = accounts.QuotaAvailable
	case quotaLow:
		class = accounts.QuotaLow
	case quotaExhausted:
		class = accounts.QuotaExhausted
	}
	return accounts.QuotaSnapshot{Class: class, Reason: result.Reason, ModelID: result.ModelID, Group: result.Group, WeeklyRemaining: result.WeeklyRemaining, FiveHourRemaining: result.FiveHourRemaining, ObservedAt: result.ObservedAt}
}

func quotaGroupForModel(model agyModel) (string, bool) {
	name := strings.ToLower(model.ID + " " + model.Label)
	switch {
	case strings.Contains(name, "gemini"):
		return "Gemini Models", true
	case strings.Contains(name, "claude"), strings.Contains(name, "gpt"), strings.Contains(name, "opus"), strings.Contains(name, "sonnet"):
		return "Claude and GPT models", true
	default:
		return "", false
	}
}

func firstNonEmpty(values ...string) string {
	for _, value := range values {
		if strings.TrimSpace(value) != "" {
			return value
		}
	}
	return "unknown"
}

func (result quotaProbeResult) errIfUnknown() error {
	if result.Class == quotaUnknown {
		return fmt.Errorf("quota state is unknown: %s", result.Reason)
	}
	return nil
}

// agyQuotaExhausted is retained temporarily for callers migrated in T14/T17.
// Unknown probe results are returned as errors and never trigger switching.
func (a *App) agyQuotaExhausted(ctx context.Context) (bool, error) {
	result := a.probeAgyQuota(ctx)
	if err := result.errIfUnknown(); err != nil {
		return false, err
	}
	return result.Class == quotaLow || result.Class == quotaExhausted, nil
}

func quotaGroupExhausted(status agyQuota, expectedGroup string) (bool, error) {
	result := classifyQuota(status, "unknown", expectedGroup, time.Now().UTC())
	if err := result.errIfUnknown(); err != nil {
		return false, err
	}
	return result.Class == quotaLow || result.Class == quotaExhausted, nil
}

// probeDeveloperQuota asks the already-authenticated agy CLI for the active
// session's quota. It deliberately avoids cagy account snapshots and the
// canonical Keychain item, so periodic watchdog probes cannot trigger a macOS
// password dialog.
func (a *App) probeDeveloperQuota(ctx context.Context, developer string) quotaProbeResult {
	a.debugf("quota-probe developer begin developer=%q source=%q", developer, "agy-current-session")
	return a.probeAgyQuota(ctx)
}
