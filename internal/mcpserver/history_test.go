package mcpserver

import (
	"context"
	"testing"

	"github.com/modelcontextprotocol/go-sdk/mcp"
)

// TestExtendHistoryIsAvailableReadOnly confirms the escape hatch from the
// backfill cap works without enabling writes, since it does not send messages.
func TestExtendHistoryIsAvailableReadOnly(t *testing.T) {
	session, sender := newReadOnlySession(t)
	ctx := context.Background()

	res, err := session.CallTool(ctx, &mcp.CallToolParams{
		Name: "extend_history",
		Arguments: map[string]any{
			"chat_jid":      "15551234567@s.whatsapp.net",
			"message_limit": 500,
		},
	})
	if err != nil {
		t.Fatalf("extend_history call failed: %v", err)
	}
	if res.IsError {
		t.Fatalf("extend_history returned an error: %v", res.Content)
	}
	if sender.lastExtendChat != "15551234567@s.whatsapp.net" {
		t.Errorf("forwarded chat = %q, want the requested JID", sender.lastExtendChat)
	}
	if sender.lastExtendPage != 50 {
		t.Errorf("page size = %d, want the 50 default", sender.lastExtendPage)
	}
}

// TestExtendHistoryRequiresChatJID covers the input validation.
func TestExtendHistoryRequiresChatJID(t *testing.T) {
	session, _ := newReadOnlySession(t)

	res, err := session.CallTool(context.Background(), &mcp.CallToolParams{
		Name:      "extend_history",
		Arguments: map[string]any{},
	})
	if err == nil && !res.IsError {
		t.Fatal("expected an error when chat_jid is missing")
	}
}
