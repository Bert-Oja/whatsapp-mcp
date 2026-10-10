package mcpserver

import (
	"context"
	"fmt"

	"github.com/modelcontextprotocol/go-sdk/mcp"
)

// HistoryExtender can request older messages for a chat from the primary
// device. It is a separate interface from Sender so the read-only build does
// not depend on any sending capability.
type HistoryExtender interface {
	// ExtendHistoryLimit raises the cap for one chat and requests a page of
	// older messages. It returns the new cap and how many messages were
	// requested.
	ExtendHistoryLimit(ctx context.Context, chatJID string, wantMessages, pageSize int) (int, error)
}

// extendHistoryResult is returned by the extend_history tool.
type extendHistoryResult struct {
	ChatJID        string `json:"chat_jid"`
	MessageLimit   int    `json:"message_limit"`
	RequestedCount int    `json:"requested_count"`
	Message        string `json:"message"`
}

type extendHistoryInput struct {
	ChatJID        string `json:"chat_jid" jsonschema:"the chat JID to pull older messages for, as returned by list_chats"`
	MessageLimit   int    `json:"message_limit,omitempty" jsonschema:"raise the per-chat cap to this many messages (0 keeps the current cap)"`
	RequestedCount int    `json:"requested_count,omitempty" jsonschema:"how many older messages to request in this page (default 50)"`
}

// extendHistory asks the primary device for older messages in one chat.
//
// Backfill is capped by default so a single connect does not sweep the whole
// account. This tool is the deliberate escape hatch: it raises the cap for one
// conversation and requests a page immediately.
func (s *Server) extendHistory(ctx context.Context, _ *mcp.CallToolRequest, in extendHistoryInput) (*mcp.CallToolResult, any, error) {
	if s.extender == nil {
		return nil, nil, fmt.Errorf("history extension is not available")
	}
	if in.ChatJID == "" {
		return nil, nil, fmt.Errorf("chat_jid is required")
	}
	count := in.RequestedCount
	if count <= 0 {
		count = 50
	}

	limit, err := s.extender.ExtendHistoryLimit(ctx, in.ChatJID, in.MessageLimit, count)
	if err != nil {
		return nil, nil, fmt.Errorf("requesting older messages: %w", err)
	}

	// History arrives asynchronously, so this reports the request, not delivery.
	msg := fmt.Sprintf("Requested %d older message(s) for %s. History arrives asynchronously; re-run list_messages shortly to see them.", count, in.ChatJID)
	if limit > 0 {
		msg = fmt.Sprintf("Per-chat cap is now %d messages. %s", limit, msg)
	}

	out := extendHistoryResult{
		ChatJID:        in.ChatJID,
		MessageLimit:   limit,
		RequestedCount: count,
		Message:        msg,
	}
	return nil, out, nil
}
