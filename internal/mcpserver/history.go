package mcpserver

import (
	"context"
	"fmt"
	"strings"

	"github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/Bert-Oja/whatsapp-mcp/internal/tools"
)

// HistoryRequester can ask the primary device for older messages in one chat.
// It is separate from Sender so the read-only build does not depend on any
// sending capability.
type HistoryRequester interface {
	RequestOlderMessages(ctx context.Context, chatJID string, count int) (tools.HistoryRequestResult, error)
}

// getOlderMessagesInput asks for more history in one conversation.
type getOlderMessagesInput struct {
	ChatJID string `json:"chat_jid" jsonschema:"the chat or group JID to pull older messages from, as returned by list_chats"`
	Count   int    `json:"count,omitempty" jsonschema:"how many older messages to request, 1-50 (default 50)"`
}

// getOlderMessagesResult reports what the request asked for.
type getOlderMessagesResult struct {
	ChatJID        string `json:"chat_jid"`
	RequestedCount int    `json:"requested_count"`
	MessagesBefore int    `json:"messages_before"`
	AnchorID       string `json:"anchor_id"`
	Bootstrapped   bool   `json:"bootstrapped"`
	Message        string `json:"message"`
}

// getOlderMessages requests one small block of older messages in a chat.
//
// Nothing is fetched automatically: a conversation is only extended when a tool
// call asks for it. Each call walks one page further back, so an agent can
// iterate if it needs more. History arrives asynchronously and is stored by the
// bridge, so the caller should re-read the chat afterwards.
func (s *Server) getOlderMessages(ctx context.Context, _ *mcp.CallToolRequest, in getOlderMessagesInput) (*mcp.CallToolResult, getOlderMessagesResult, error) {
	if s.requester == nil {
		return nil, getOlderMessagesResult{}, fmt.Errorf("history retrieval is not available")
	}
	if strings.TrimSpace(in.ChatJID) == "" {
		return nil, getOlderMessagesResult{}, fmt.Errorf("chat_jid is required")
	}
	count := in.Count
	if count <= 0 {
		count = 50
	}
	if count > 50 {
		return nil, getOlderMessagesResult{}, fmt.Errorf("count must be 1-50, got %d", count)
	}

	res, err := s.requester.RequestOlderMessages(ctx, in.ChatJID, count)
	if err != nil {
		return nil, getOlderMessagesResult{}, fmt.Errorf("requesting older messages: %w", err)
	}

	out := getOlderMessagesResult{
		ChatJID:        res.ChatJID,
		RequestedCount: res.RequestedCount,
		MessagesBefore: res.MessagesBefore,
		AnchorID:       res.AnchorID,
		Bootstrapped:   res.Bootstrapped,
	}
	switch {
	case res.Bootstrapped:
		out.Message = fmt.Sprintf(
			"No local message existed for %s, so history was bootstrapped from a recovered message anchor. Wait a few seconds, then re-read the chat.",
			res.ChatJID)
	default:
		out.Message = fmt.Sprintf(
			"Requested %d older message(s) for %s, walking back from message %s. History arrives asynchronously; re-run list_messages or get_chat shortly. If re-running does not increase the message count, no older history is available.",
			res.RequestedCount, res.ChatJID, res.AnchorID)
	}
	return nil, out, nil
}
