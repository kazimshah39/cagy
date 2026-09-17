package quota

import (
	"regexp"
	"strings"
)

var strongPatterns = []*regexp.Regexp{
	regexp.MustCompile(`(?i)\bRESOURCE[_ ]EXHAUSTED\b`),
	regexp.MustCompile(`(?i)\bquota\s+(?:has\s+been\s+)?(?:exceeded|exhausted|depleted)\b`),
	regexp.MustCompile(`(?i)\bout\s+of\s+(?:quota|credits?)\b`),
	regexp.MustCompile(`(?i)\busage\s+limit\s+(?:has\s+been\s+)?(?:reached|exceeded)\b`),
	regexp.MustCompile(`(?i)\brate\s+limit\s+(?:has\s+been\s+)?(?:reached|exceeded)\b`),
	regexp.MustCompile(`(?is)\b429\b.{0,240}\b(?:quota|rate\s+limit)\b`),
	regexp.MustCompile(`(?is)\b(?:quota|rate\s+limit)\b.{0,240}\b429\b`),
}

// Detected returns true only for strong quota/rate-limit error evidence.
func Detected(text string) bool {
	for _, pattern := range strongPatterns {
		if pattern.MatchString(text) {
			return true
		}
	}
	return false
}

// DetectedResponse checks agent output without treating the echoed task as an
// error. agy shows the submitted prompt in its transcript, and that prompt may
// legitimately contain quota-error examples.
func DetectedResponse(output, task string) bool {
	normalizedOutput := strings.Join(strings.Fields(output), " ")
	normalizedTask := strings.Join(strings.Fields(task), " ")
	normalizedOutput = strings.TrimSpace(strings.TrimPrefix(strings.TrimSpace(normalizedOutput), ">"))
	if normalizedTask != "" && strings.HasPrefix(normalizedOutput, normalizedTask) {
		normalizedOutput = strings.TrimSpace(strings.TrimPrefix(normalizedOutput, normalizedTask))
	}
	return Detected(normalizedOutput)
}

// NewOutput extracts text added after a prior terminal snapshot.
func NewOutput(before, after string) string {
	// Preserve an exact append boundary before applying lossy UI cleanup. This
	// keeps legitimate one-line output that happens to look like a divider.
	if before != "" && strings.HasPrefix(after, before) {
		return cleanTerminalDelta(after[len(before):])
	}
	before = stripTrailingTerminalChrome(before)
	after = stripTrailingTerminalChrome(after)
	if before == "" {
		return cleanTerminalDelta(after)
	}
	if strings.HasPrefix(after, before) {
		return cleanTerminalDelta(after[len(before):])
	}

	if delta, ok := outputAfterLineLCS(before, after); ok {
		return cleanTerminalDelta(delta)
	}

	lines := strings.Split(before, "\n")
	for start := len(lines) - 1; start >= 0 && start >= len(lines)-12; start-- {
		tail := strings.Join(lines[start:], "\n")
		if strings.TrimSpace(tail) == "" {
			continue
		}
		if index := strings.LastIndex(after, tail); index >= 0 {
			return cleanTerminalDelta(after[index+len(tail):])
		}
	}

	return cleanTerminalDelta(after)
}

// outputAfterLineLCS aligns terminal snapshots by their longest common line
// subsequence. This handles alternate-screen redraws that add/remove blank
// lines, and it does not confuse repeated response endings with the latest
// response as a last-substring search would.
func outputAfterLineLCS(before, after string) (string, bool) {
	baseline := strings.Split(before, "\n")
	current := strings.Split(after, "\n")
	if len(baseline) == 0 || len(current) == 0 {
		return "", false
	}

	dp := make([][]uint16, len(baseline)+1)
	for index := range dp {
		dp[index] = make([]uint16, len(current)+1)
	}
	for left := len(baseline) - 1; left >= 0; left-- {
		for right := len(current) - 1; right >= 0; right-- {
			if baseline[left] == current[right] {
				dp[left][right] = dp[left+1][right+1] + 1
			} else if dp[left+1][right] >= dp[left][right+1] {
				dp[left][right] = dp[left+1][right]
			} else {
				dp[left][right] = dp[left][right+1]
			}
		}
	}

	left, right := 0, 0
	matched := 0
	lastCurrent := -1
	for left < len(baseline) && right < len(current) {
		if baseline[left] == current[right] && dp[left][right] == dp[left+1][right+1]+1 {
			matched++
			lastCurrent = right
			left++
			right++
		} else if dp[left+1][right] >= dp[left][right+1] {
			left++
		} else {
			right++
		}
	}
	if matched < 3 || lastCurrent < 0 {
		return "", false
	}
	return strings.Join(current[lastCurrent+1:], "\n"), true
}

// stripTrailingTerminalChrome removes only volatile agy UI lines at the end
// of a snapshot. Without this, the repeated footer can look like the newest
// common suffix and hide the actual response inserted just above it.
func cleanTerminalDelta(text string) string {
	text = stripTrailingTerminalChrome(strings.TrimSpace(text))
	lines := strings.Split(text, "\n")
	// A single divider-like line can be real output (for example "-0").
	// Remove leading UI chrome only when it is a complete line followed by
	// more content.
	for len(lines) > 1 && isTrailingTerminalChrome(lines[0]) {
		lines = lines[1:]
	}
	return strings.TrimSpace(strings.Join(lines, "\n"))
}

func stripTrailingTerminalChrome(text string) string {
	lines := strings.Split(strings.TrimRight(text, "\n"), "\n")
	for len(lines) > 1 && isTrailingTerminalChrome(lines[len(lines)-1]) {
		hasContent := false
		for _, line := range lines[:len(lines)-1] {
			if !isTrailingTerminalChrome(line) {
				hasContent = true
				break
			}
		}
		if !hasContent {
			break
		}
		lines = lines[:len(lines)-1]
	}
	return strings.TrimRight(strings.Join(lines, "\n"), "\n")
}

func isTrailingTerminalChrome(line string) bool {
	trimmed := strings.TrimSpace(line)
	if trimmed == "" || trimmed == ">" {
		return true
	}
	if strings.HasPrefix(trimmed, "? for shortcuts") || strings.HasPrefix(trimmed, "esc to cancel") {
		return true
	}
	for _, char := range trimmed {
		if !strings.ContainsRune("─━═-", char) {
			return false
		}
	}
	return trimmed != ""
}
