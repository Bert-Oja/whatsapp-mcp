package mcpserver

import (
	"context"
	"testing"

	"github.com/modelcontextprotocol/go-sdk/mcp"
)

// TestGetOlderMessagesIsAvailableReadOnly confirms on-demand history works
// without enabling writes, since it does not send chat messages.
func TestGetOlderMessagesIsAvailableReadOnly(t *testing.T) {
	session, sender := newReadOnlySession(t)
	ctx := context.Background()

	res, err := session.CallTool(ctx, &mcp.CallToolParams{
		Name: "get_older_messages",
		Arguments: map[string]any{
			"chat_jid": "15551234567@s.whatsapp.net",
		},
	})
	if err != nil {
		t.Fatalf("get_older_messages call failed: %v", err)
	}
	if res.IsError {
		t.Fatalf("get_older_messages returned an error: %v", res.Content)
	}
	if sender.lastExtendChat != "15551234567@s.whatsapp.net" {
		t.Errorf("forwarded chat = %q, want the requested JID", sender.lastExtendChat)
	}
	if sender.lastExtendPage != 50 {
		t.Errorf("page size = %d, want the 50 default", sender.lastExtendPage)
	}
}

// TestGetOlderMessagesRejectsOversizedPage enforces the small-page design.
func TestGetOlderMessagesRejectsOversizedPage(t *testing.T) {
	session, _ := newReadOnlySession(t)

	res, err := session.CallTool(context.Background(), &mcp.CallToolParams{
		Name:      "get_older_messages",
		Arguments: map[string]any{"chat_jid": "x@s.whatsapp.net", "count": 500},
	})
	if err == nil && !res.IsError {
		t.Fatal("expected an error for a count above the 50 cap")
	}
}

// TestGetOlderMessagesRequiresChatJID covers the input validation.
func TestGetOlderMessagesRequiresChatJID(t *testing.T) {
	session, _ := newReadOnlySession(t)

	res, err := session.CallTool(context.Background(), &mcp.CallToolParams{
		Name:      "get_older_messages",
		Arguments: map[string]any{},
	})
	if err == nil && !res.IsError {
		t.Fatal("expected an error when chat_jid is missing")
	}
}
