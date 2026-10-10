package tools

// HistoryRequestResult describes what an on-demand history request did.
//
// It lives here rather than in the bridge or the MCP layer because both need
// it: the bridge produces it, and the MCP tool reports it to the caller.
type HistoryRequestResult struct {
	// ChatJID is the canonical chat the request was made for.
	ChatJID string
	// RequestedCount is how many older messages were asked for.
	RequestedCount int
	// AnchorID is the message the walk started from. A repeated value across
	// calls means the previous request produced nothing.
	AnchorID string
	// MessagesBefore is the chat's message count when the request was made.
	MessagesBefore int
	// Bootstrapped reports that the chat had no local message to anchor on, so
	// a recovered anchor was used instead.
	Bootstrapped bool
}
