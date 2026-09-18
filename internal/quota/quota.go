package quota

import (
	"regexp"
	"strings"
)

var strongPatterns = []*regexp.Regexp{
	// 1. gRPC / API status codes and standalone error lines
	regexp.MustCompile(`(?im)^\s*(?:>\s*)?RESOURCE[_ ]EXHAUSTED(?:\s*[:\-].*)?$`),
	regexp.MustCompile(`(?im)^\s*(?:>\s*)?(?:rpc\s+error|error|api\s+error|provider\s+error|runtime\s+error|fatal|exception)\s*[:\-]\s*(?:.*?\b)?RESOURCE[_ ]EXHAUSTED\b`),
	regexp.MustCompile(`(?i)\bcode\s*[:=]\s*ResourceExhausted\b`),
	regexp.MustCompile(`(?i)\bexceptions?\.(?:ResourceExhausted|RESOURCE_EXHAUSTED)\b`),

	// 2. Explicit HTTP 429 status lines or API errors
	regexp.MustCompile(`(?im)^\s*(?:>\s*)?(?:(?:api|provider|runtime|rpc)\s+error|error|fatal|exception|failed)\s*[:\-]?\s*(?:.*?\b)?(?:HTTP\s*)?429\b`),
	regexp.MustCompile(`(?im)^\s*(?:>\s*)?(?:HTTP\s+)?429\s*[:\-]\s*(?:quota|rate\s*limit|project\s*quota|too\s*many|RESOURCE[_ ]EXHAUSTED).*\s*$`),
	regexp.MustCompile(`(?im)^\s*(?:>\s*)?(?:HTTP\s+)?429\s+(?:Too Many Requests|RESOURCE_EXHAUSTED)\b`),
	regexp.MustCompile(`(?i)\bHTTP\s+429\s*[:\-]\s*`),
	regexp.MustCompile(`(?im)^\s*(?:>\s*)?rate\s+limit\s+error\b.*?\b429\b`),
	regexp.MustCompile(`(?im)^\s*(?:>\s*)?(?:api|provider|runtime)\s+error\s*[:\-]?\s*(?:.*?\b)?429\b`),

	// 3. Provider error banners and runtime error prefixes
	regexp.MustCompile(`(?i)(?:^|\n|[\.!\?]\s*|\b)(?:error|api\s+error|provider\s+error|runtime\s+error|fatal|exception)\s*[:\-]\s*(?:.*?\b)?(?:quota\s+(?:has\s+been\s+)?(?:exceeded|exhausted|depleted)|out\s+of\s+(?:quota|credits?)|usage\s+limit\s+(?:has\s+been\s+)?(?:reached|exceeded)|rate\s+limit\s+(?:has\s+been\s+)?(?:reached|exceeded))\b`),

	// 4. Direct user account / quota exhaustion notifications from the provider
	regexp.MustCompile(`(?i)\b(?:you\s+have\s+exceeded\s+your\s+(?:current\s+)?quota|you\s+are\s+out\s+of\s+quota|account\s+has\s+exceeded\s+its\s+usage\s+limit|insufficient\s+quota\s+for\s+model|exceeded\s+your\s+current\s+quota)\b`),

	// 5. Standalone error lines (line that is purely a quota error announcement)
	regexp.MustCompile(`(?im)^\s*(?:>\s*)?(?:quota\s+(?:has\s+been\s+)?(?:exceeded|exhausted|depleted)|out\s+of\s+(?:quota|credits?)|usage\s+limit\s+(?:has\s+been\s+)?(?:reached|exceeded)|rate\s+limit\s+(?:has\s+been\s+)?(?:reached|exceeded))\s*(?:for\s+this\s+model)?\s*[\.\!]?\s*$`),
	regexp.MustCompile(`(?im)^\s*(?:>\s*)?429\s*[:\-]\s*(?:quota|rate\s*limit).*\s*$`),
}

var (
	thoughtBlockPattern    = regexp.MustCompile(`(?is)<\s*thinking\s*>.*?<\s*/\s*thinking\s*>`)
	thinkingLinePattern    = regexp.MustCompile(`(?im)^\s*(?:Thinking Process|Thought|Thinking)\s*:.*$`)
	fencedCodeBlockPattern = regexp.MustCompile("(?s)```.*?```")
	promptBoxPattern       = regexp.MustCompile(`(?is)\s*╭[─━\s]*Prompt.*?\n╰[─━\s]*╯\s*`)
	userRequestPattern     = regexp.MustCompile(`(?is)\s*<USER_REQUEST>.*?</USER_REQUEST>\s*`)
)

// Detected returns true only for strong quota/rate-limit error evidence.
func Detected(text string) bool {
	for _, pattern := range strongPatterns {
		if pattern.MatchString(text) {
			return true
		}
	}
	return false
}

// cleanEchoedTask removes only the echoed task prompt at the turn boundary,
// leaving subsequent provider error messages intact even if they contain the
// same error phrases as the prompt.
func cleanEchoedTask(output, task string) string {
	cleaned := output

	// 1. Remove prompt box at the boundary if present near the start
	if loc := promptBoxPattern.FindStringIndex(cleaned); loc != nil && loc[0] <= 100 {
		return cleaned[:loc[0]] + "\n" + cleaned[loc[1]:]
	}

	// 2. Remove <USER_REQUEST> block if present near the start
	if loc := userRequestPattern.FindStringIndex(cleaned); loc != nil && loc[0] <= 100 {
		return cleaned[:loc[0]] + "\n" + cleaned[loc[1]:]
	}

	words := strings.Fields(task)
	if len(words) == 0 {
		return cleaned
	}

	// 3. Build regex matching the task words joined by arbitrary whitespace, indents, or borders
	var pattern strings.Builder
	for i, w := range words {
		if i > 0 {
			pattern.WriteString(`[\s│|║┃╭╮╰╯─━]+`)
		}
		pattern.WriteString(regexp.QuoteMeta(w))
	}

	// Match prompt starting with '>' near the beginning of output
	if promptRe, err := regexp.Compile(`(?i)(?:^|\n)\s*>\s*` + pattern.String()); err == nil {
		if loc := promptRe.FindStringIndex(cleaned); loc != nil && loc[0] <= 100 {
			return cleaned[:loc[0]] + "\n" + cleaned[loc[1]:]
		}
	}

	// Match prompt words near the beginning of output
	if taskRe, err := regexp.Compile(`(?i)(?:^|\n)\s*` + pattern.String()); err == nil {
		if loc := taskRe.FindStringIndex(cleaned); loc != nil && loc[0] <= 100 {
			return cleaned[:loc[0]] + "\n" + cleaned[loc[1]:]
		}
	}

	return cleaned
}

func cleanModelReasoning(output string) string {
	result := thoughtBlockPattern.ReplaceAllString(output, "")
	result = thinkingLinePattern.ReplaceAllString(result, "")
	result = fencedCodeBlockPattern.ReplaceAllString(result, "")
	return result
}

var responsePatterns = []*regexp.Regexp{
	// 1. Standalone or error-prefixed gRPC / API status code lines
	regexp.MustCompile(`(?im)^\s*(?:>\s*)?RESOURCE[_ ]EXHAUSTED(?:\s*[:\-].*)?$`),
	regexp.MustCompile(`(?im)^\s*(?:>\s*)?(?:rpc\s+error|error|api\s+error|provider\s+error|runtime\s+error|fatal|exception)\s*[:\-]\s*(?:.*?\b)?RESOURCE[_ ]EXHAUSTED\b`),
	regexp.MustCompile(`(?im)^\s*(?:>\s*)?(?:(?:rpc\s+error|error|api\s+error|provider\s+error|runtime\s+error|fatal|exception)\s*[:\-].*?\b)?code\s*[:=]\s*ResourceExhausted\b`),
	regexp.MustCompile(`(?im)^\s*(?:>\s*)?(?:(?:(?:Traceback|raise|fatal|error|api\s+error|provider\s+error|runtime\s+error|exception)\b.*?\b)?[a-zA-Z0-9_\.]*exceptions?\.(?:ResourceExhausted|RESOURCE_EXHAUSTED)\s*[:\-].*|[a-zA-Z0-9_\.]*exceptions?\.(?:ResourceExhausted|RESOURCE_EXHAUSTED)\s*$)`),

	// 2. Explicit HTTP 429 status lines or API errors
	regexp.MustCompile(`(?im)^\s*(?:>\s*)?(?:(?:api|provider|runtime|rpc)\s+error|error|fatal|exception|failed)\s*[:\-]?\s*(?:.*?\b)?(?:HTTP\s*)?429\b`),
	regexp.MustCompile(`(?im)^\s*(?:>\s*)?(?:HTTP\s+)?429\s*[:\-]\s*(?:quota|rate\s*limit|project\s*quota|too\s*many|RESOURCE[_ ]EXHAUSTED).*\s*$`),
	regexp.MustCompile(`(?im)^\s*(?:>\s*)?(?:HTTP\s+)?429\s+(?:Too Many Requests|RESOURCE_EXHAUSTED)\b`),
	regexp.MustCompile(`(?im)^\s*(?:>\s*)?HTTP\s+429\s*[:\-]\s*.*$`),
	regexp.MustCompile(`(?im)^\s*(?:>\s*)?rate\s+limit\s+error\b.*?\b429\b`),
	regexp.MustCompile(`(?im)^\s*(?:>\s*)?(?:api|provider|runtime)\s+error\s*[:\-]?\s*(?:.*?\b)?429\b`),

	// 3. Provider error banners and runtime error prefixes
	regexp.MustCompile(`(?im)^\s*(?:>\s*)?(?:error|api\s+error|provider\s+error|runtime\s+error|fatal|exception)\s*[:\-]\s*(?:.*?\b)?(?:quota\s+(?:has\s+been\s+)?(?:exceeded|exhausted|depleted)|out\s+of\s+(?:quota|credits?)|usage\s+limit\s+(?:has\s+been\s+)?(?:reached|exceeded)|rate\s+limit\s+(?:has\s+been\s+)?(?:reached|exceeded))\b`),

	// 4. Direct user account / quota exhaustion notifications from the provider
	regexp.MustCompile(`(?im)^\s*(?:>\s*)?(?:(?:error|api\s+error|provider\s+error|runtime\s+error|fatal|exception)\s*[:\-]\s*)?(?:you\s+have\s+exceeded\s+your\s+(?:current\s+)?quota|you\s+are\s+out\s+of\s+quota|account\s+has\s+exceeded\s+its\s+usage\s+limit|insufficient\s+quota\s+for\s+model|exceeded\s+your\s+current\s+quota)\b`),

	// 5. Standalone error lines (line that is purely a quota error announcement)
	regexp.MustCompile(`(?im)^\s*(?:>\s*)?(?:quota\s+(?:has\s+been\s+)?(?:exceeded|exhausted|depleted)|out\s+of\s+(?:quota|credits?)|usage\s+limit\s+(?:has\s+been\s+)?(?:reached|exceeded)|rate\s+limit\s+(?:has\s+been\s+)?(?:reached|exceeded))\s*(?:for\s+this\s+model)?\s*[\.\!]?\s*$`),
	regexp.MustCompile(`(?im)^\s*(?:>\s*)?429\s*[:\-]\s*(?:quota|rate\s*limit).*\s*$`),
}

// DetectedResponse checks agent output without treating the echoed task,
// model reasoning, code blocks, or successful descriptive prose as an error.
func DetectedResponse(output, task string) bool {
	cleaned := cleanEchoedTask(output, task)
	cleaned = cleanModelReasoning(cleaned)
	for _, pattern := range responsePatterns {
		if pattern.MatchString(cleaned) {
			return true
		}
	}
	return false
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
