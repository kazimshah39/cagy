package app

// continuationPrompt intentionally contains no original task text. It is used
// only after cagy has proven that a stalled visible task was interrupted.
func continuationPrompt(_ string) string {
	return "Inspect the current working tree and the conversation context. Continue the interrupted work without repeating the original request. Complete it if possible, then give a concise final response."
}
