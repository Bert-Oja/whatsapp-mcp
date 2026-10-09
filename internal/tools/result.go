package tools

// TextResult is a plain-text tool result. The message-reading tools return
// formatted text rather than structured data, matching the Python server.
type TextResult struct {
	Text string `json:"text"`
}
