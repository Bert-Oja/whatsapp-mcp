package mcpserver

import (
	"context"
	"encoding/json"
	"testing"

	"github.com/modelcontextprotocol/go-sdk/mcp"
)

// TestStructuredContentIsAlwaysAnObject guards a whole class of client-visible
// breakage.
//
// The MCP spec requires structuredContent to be a JSON object. A tool whose Go
// handler returns a slice produces an array root, which clients reject outright
// (Pi reports "Invalid MCP tools/call structured content") even though the
// text content is valid. That made list_chats unusable, so every tool is
// checked here rather than trusting the handler signatures.
func TestStructuredContentIsAlwaysAnObject(t *testing.T) {
	session, _ := newTestSession(t)
	ctx := context.Background()

	// Arguments that reach the reader for every read tool. Tools that need a
	// real row still return a valid (possibly empty) object.
	args := map[string]map[string]any{
		"search_contacts":            {"query": "Alice"},
		"list_chats":                 {"limit": 5},
		"get_chat":                   {"chat_jid": "15551234567@s.whatsapp.net"},
		"get_direct_chat_by_contact": {"phone_number": "15551234567"},
		"get_contact_chats":          {"jid": "15551234567@s.whatsapp.net"},
		"get_last_interaction":       {"jid": "15551234567@s.whatsapp.net"},
		"list_messages":              {"chat_jid": "15551234567@s.whatsapp.net"},
		"get_older_messages":         {"chat_jid": "15551234567@s.whatsapp.net", "count": 10},
	}

	for name, a := range args {
		t.Run(name, func(t *testing.T) {
			res, err := session.CallTool(ctx, &mcp.CallToolParams{Name: name, Arguments: a})
			if err != nil {
				// Transport-level rejection is exactly the failure this test is
				// meant to catch.
				t.Fatalf("call failed: %v", err)
			}
			if res.IsError {
				// A domain error (missing row, unparsable id) is fine; what
				// matters is that the protocol shape was accepted.
				return
			}
			if res.StructuredContent == nil {
				t.Skip("tool returned no structured content")
			}

			raw, err := json.Marshal(res.StructuredContent)
			if err != nil {
				t.Fatalf("marshalling structured content: %v", err)
			}
			if len(raw) == 0 || raw[0] != '{' {
				t.Fatalf("structuredContent root must be an object, got: %s", truncate(string(raw), 120))
			}
		})
	}
}

func truncate(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[:n] + "..."
}
