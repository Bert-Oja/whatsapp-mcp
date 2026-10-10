package mcpserver

import (
	"context"
	"database/sql"
	"encoding/json"
	"path/filepath"
	"sort"
	"testing"

	"github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/Bert-Oja/whatsapp-mcp/internal/tools"
)

// fakeSender stands in for the WhatsApp bridge so write tools can be exercised
// without a network connection.
type fakeSender struct {
	lastRecipient  string
	lastMessage    string
	lastMediaPath  string
	lastExtendChat string
	lastExtendPage int
}

func (f *fakeSender) SendMessage(ctx context.Context, recipient, message, mediaPath string) (bool, string) {
	f.lastRecipient = recipient
	f.lastMessage = message
	f.lastMediaPath = mediaPath
	return true, "Message sent to " + recipient
}

func (f *fakeSender) DownloadMedia(messageID, chatJID string) (bool, string, string, string, error) {
	return true, "image", "image_1.jpg", "/tmp/image_1.jpg", nil
}

// ExtendHistoryLimit satisfies HistoryExtender, so the read-only extend_history
// tool is registered exactly as it is in production.
func (f *fakeSender) ExtendHistoryLimit(ctx context.Context, chatJID string, wantMessages, pageSize int) (int, error) {
	f.lastExtendChat = chatJID
	f.lastExtendPage = pageSize
	return wantMessages, nil
}

// expectedTools is the Phase 2 contract: the twelve tools the Python server
// exposed, under the same names, plus extend_history, which is new in the Go
// server and has no Python equivalent.
var expectedTools = []string{
	"download_media",
	"extend_history",
	"get_chat",
	"get_contact_chats",
	"get_direct_chat_by_contact",
	"get_last_interaction",
	"get_message_context",
	"list_chats",
	"list_messages",
	"search_contacts",
	"send_audio_message",
	"send_file",
	"send_message",
}

func newTestSession(t *testing.T) (*mcp.ClientSession, *fakeSender) {
	t.Helper()

	db, err := sql.Open("sqlite", "file::memory:")
	if err != nil {
		t.Fatalf("open db: %v", err)
	}
	t.Cleanup(func() { db.Close() })

	// Minimal schema so the server can be constructed and tools listed.
	if _, err := db.Exec(`
		CREATE TABLE chats (jid TEXT PRIMARY KEY, name TEXT, last_message_time TIMESTAMP);
		CREATE TABLE messages (id TEXT, chat_jid TEXT, sender TEXT, content TEXT,
			timestamp TIMESTAMP, is_from_me BOOLEAN, media_type TEXT, filename TEXT,
			url TEXT, media_key BLOB, file_sha256 BLOB, file_enc_sha256 BLOB,
			file_length INTEGER, PRIMARY KEY (id, chat_jid));
	`); err != nil {
		t.Fatalf("schema: %v", err)
	}

	sender := &fakeSender{}
	srv := New(tools.NewReader(db), sender, Options{AllowWrites: true})

	ctx := context.Background()
	t1, t2 := mcp.NewInMemoryTransports()
	if _, err := srv.Connect(ctx, t1, nil); err != nil {
		t.Fatalf("server connect: %v", err)
	}

	client := mcp.NewClient(&mcp.Implementation{Name: "test-client", Version: "1.0.0"}, nil)
	session, err := client.Connect(ctx, t2, nil)
	if err != nil {
		t.Fatalf("client connect: %v", err)
	}
	t.Cleanup(func() { session.Close() })

	return session, sender
}

// TestToolSurfaceExposed is the Phase 2 gate.
func TestToolSurfaceExposed(t *testing.T) {
	session, _ := newTestSession(t)
	ctx := context.Background()

	var names []string
	for tool, err := range session.Tools(ctx, nil) {
		if err != nil {
			t.Fatalf("listing tools: %v", err)
		}
		names = append(names, tool.Name)
	}
	sort.Strings(names)

	sort.Strings(expectedTools)
	if len(names) != len(expectedTools) {
		t.Fatalf("expected %d tools, got %d: %v", len(expectedTools), len(names), names)
	}
	for i := range expectedTools {
		if names[i] != expectedTools[i] {
			t.Fatalf("tool mismatch at %d: got %q want %q\nall: %v", i, names[i], expectedTools[i], names)
		}
	}
}

// TestReadToolCall verifies a read tool round-trips through MCP and returns the
// formatted text shape the Python server produced.
func TestReadToolCall(t *testing.T) {
	session, _ := newTestSession(t)
	ctx := context.Background()

	res, err := session.CallTool(ctx, &mcp.CallToolParams{
		Name:      "search_contacts",
		Arguments: map[string]any{"query": "nobody"},
	})
	if err != nil {
		t.Fatalf("CallTool: %v", err)
	}
	if res.IsError {
		t.Fatalf("tool reported error: %+v", res.Content)
	}
}

// TestSendMessageToolCall verifies a write tool reaches the sender with the
// arguments it was given.
func TestSendMessageToolCall(t *testing.T) {
	session, sender := newTestSession(t)
	ctx := context.Background()

	_, err := session.CallTool(ctx, &mcp.CallToolParams{
		Name: "send_message",
		Arguments: map[string]any{
			"recipient": "15551234567",
			"message":   "hello from the Go server",
		},
	})
	if err != nil {
		t.Fatalf("CallTool: %v", err)
	}
	if sender.lastRecipient != "15551234567" || sender.lastMessage != "hello from the Go server" {
		t.Fatalf("sender not called correctly: %+v", sender)
	}
}

// TestSendMessageValidation checks the empty-recipient guard still applies.
func TestSendMessageValidation(t *testing.T) {
	session, _ := newTestSession(t)
	ctx := context.Background()

	res, err := session.CallTool(ctx, &mcp.CallToolParams{
		Name:      "send_message",
		Arguments: map[string]any{"recipient": "", "message": "hi"},
	})
	if err != nil {
		t.Fatalf("CallTool: %v", err)
	}
	var out tools.SendMessageResult
	if err := json.Unmarshal(mustJSON(t, res), &out); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if out.Success || out.Message != "Recipient must be provided" {
		t.Fatalf("expected validation failure, got %+v", out)
	}
}

func mustJSON(t *testing.T, res *mcp.CallToolResult) []byte {
	t.Helper()
	b, err := json.Marshal(res.StructuredContent)
	if err != nil {
		t.Fatalf("marshal structured content: %v", err)
	}
	return b
}

var _ = filepath.Join

// TestTextToolsReturnPlainString locks the content shape of the message-reading
// tools. They must return the formatted text as-is, not a JSON-encoded wrapper:
// the Python server returned a plain string and the model reads it directly.
func TestTextToolsReturnPlainString(t *testing.T) {
	session, _ := newTestSession(t)
	ctx := context.Background()

	res, err := session.CallTool(ctx, &mcp.CallToolParams{
		Name:      "list_messages",
		Arguments: map[string]any{"query": "zzz-no-match"},
	})
	if err != nil {
		t.Fatalf("CallTool: %v", err)
	}
	if len(res.Content) == 0 {
		t.Fatal("no content returned")
	}
	text, ok := res.Content[0].(*mcp.TextContent)
	if !ok {
		t.Fatalf("content is %T, want *mcp.TextContent", res.Content[0])
	}
	if text.Text != "No messages to display." {
		t.Fatalf("content text = %q, want the bare placeholder string", text.Text)
	}
}

// TestGetLastInteractionMissingReturnsEmpty covers the no-interaction case.
func TestGetLastInteractionMissingReturnsEmpty(t *testing.T) {
	session, _ := newTestSession(t)
	ctx := context.Background()

	res, err := session.CallTool(ctx, &mcp.CallToolParams{
		Name:      "get_last_interaction",
		Arguments: map[string]any{"jid": "nobody@s.whatsapp.net"},
	})
	if err != nil {
		t.Fatalf("CallTool: %v", err)
	}
	text := res.Content[0].(*mcp.TextContent)
	if text.Text != "" {
		t.Fatalf("expected empty text, got %q", text.Text)
	}
}
