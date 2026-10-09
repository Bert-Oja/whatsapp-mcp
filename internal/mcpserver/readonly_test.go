package mcpserver

import (
	"context"
	"database/sql"
	"sort"
	"strings"
	"testing"

	"github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/Bert-Oja/whatsapp-mcp/internal/tools"
)

// newReadOnlySession builds a session with writes disabled (the default).
func newReadOnlySession(t *testing.T) (*mcp.ClientSession, *fakeSender) {
	t.Helper()

	db, err := sql.Open("sqlite", "file::memory:")
	if err != nil {
		t.Fatalf("open db: %v", err)
	}
	t.Cleanup(func() { db.Close() })

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
	srv := New(tools.NewReader(db), sender, Options{AllowWrites: false})

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

func listToolNames(t *testing.T, session *mcp.ClientSession) []string {
	t.Helper()
	var names []string
	for tool, err := range session.Tools(context.Background(), nil) {
		if err != nil {
			t.Fatalf("listing tools: %v", err)
		}
		names = append(names, tool.Name)
	}
	sort.Strings(names)
	return names
}

// TestReadOnlyExposesNoWriteTools is the core guarantee: with writes disabled,
// the three sending tools must not be visible at all.
func TestReadOnlyExposesNoWriteTools(t *testing.T) {
	session, _ := newReadOnlySession(t)
	names := listToolNames(t, session)

	for _, writeTool := range WriteToolNames() {
		for _, got := range names {
			if got == writeTool {
				t.Errorf("read-only server exposed write tool %q", writeTool)
			}
		}
	}

	// And the read tools must all still be present.
	for _, want := range ReadOnlyToolNames() {
		found := false
		for _, got := range names {
			if got == want {
				found = true
				break
			}
		}
		if !found {
			t.Errorf("read tool %q missing in read-only mode", want)
		}
	}

	if len(names) != len(ReadOnlyToolNames()) {
		t.Errorf("read-only mode exposed %d tools, want %d: %v", len(names), len(ReadOnlyToolNames()), names)
	}
}

// TestReadOnlyCannotSendEvenIfToolCalled is the defence-in-depth check: even a
// direct call to a send tool name must be refused and must not reach WhatsApp.
func TestReadOnlyCannotSendEvenIfToolCalled(t *testing.T) {
	session, sender := newReadOnlySession(t)
	ctx := context.Background()

	res, err := session.CallTool(ctx, &mcp.CallToolParams{
		Name: "send_message",
		Arguments: map[string]any{
			"recipient": "15551234567",
			"message":   "this must not be sent",
		},
	})
	// The call must either be an error, or report a failure. It must not
	// silently succeed.
	if err == nil && res != nil && !res.IsError {
		text := ""
		for _, c := range res.Content {
			if tc, ok := c.(*mcp.TextContent); ok {
				text += tc.Text
			}
		}
		if !strings.Contains(strings.ToLower(text), "write") &&
			!strings.Contains(strings.ToLower(text), "not found") &&
			!strings.Contains(strings.ToLower(text), "unknown tool") {
			t.Errorf("read-only send_message did not report refusal; content=%q", text)
		}
	}

	if sender.lastRecipient != "" || sender.lastMessage != "" {
		t.Fatalf("read-only mode reached the WhatsApp sender: %+v", sender)
	}
}

// TestGuardRefusesSendDirectly exercises the guard independently of the tool
// registry, which is what protects against future code paths.
func TestGuardRefusesSendDirectly(t *testing.T) {
	inner := &fakeSender{}
	g := newGuard(inner, false)

	ok, msg := g.SendMessage(context.Background(), "15551234567", "hello", "")
	if ok {
		t.Fatal("guard allowed a send in read-only mode")
	}
	if !strings.Contains(msg, "writes are disabled") {
		t.Fatalf("unexpected refusal message: %q", msg)
	}
	if inner.lastRecipient != "" {
		t.Fatal("guard forwarded the send to the underlying sender")
	}

	// Media sends must be blocked too.
	okMedia, _ := g.SendMessage(context.Background(), "15551234567", "", "/tmp/x.jpg")
	if okMedia {
		t.Fatal("guard allowed a media send in read-only mode")
	}
	if inner.lastMediaPath != "" {
		t.Fatal("guard forwarded the media send")
	}
}

// TestGuardAllowsDownload confirms read operations still work through the guard.
func TestGuardAllowsDownload(t *testing.T) {
	g := newGuard(&fakeSender{}, false)

	ok, mediaType, _, path, err := g.DownloadMedia("m1", "chat@s.whatsapp.net")
	if err != nil || !ok {
		t.Fatalf("download should pass through the guard: ok=%v err=%v", ok, err)
	}
	if mediaType != "image" || path == "" {
		t.Fatalf("unexpected download result: %s %s", mediaType, path)
	}
}

// TestWriteModeExposesWrites confirms enabling writes behaves as expected.
func TestWriteModeExposesWrites(t *testing.T) {
	session, sender := newTestSession(t) // helper from server_test.go, AllowWrites: true
	names := listToolNames(t, session)

	for _, want := range WriteToolNames() {
		found := false
		for _, got := range names {
			if got == want {
				found = true
			}
		}
		if !found {
			t.Errorf("write tool %q missing when writes are enabled", want)
		}
	}
	if len(names) != len(ReadOnlyToolNames())+len(WriteToolNames()) {
		t.Errorf("expected %d tools, got %d: %v", len(ReadOnlyToolNames())+len(WriteToolNames()), len(names), names)
	}

	// And a send does reach the sender.
	if _, err := session.CallTool(context.Background(), &mcp.CallToolParams{
		Name:      "send_message",
		Arguments: map[string]any{"recipient": "15551234567", "message": "ok"},
	}); err != nil {
		t.Fatalf("CallTool: %v", err)
	}
	if sender.lastMessage != "ok" {
		t.Fatalf("write mode did not reach the sender: %+v", sender)
	}
}

// TestReadOnlyAnnotations verifies the advertised hints, which clients use to
// make permission decisions.
func TestReadOnlyAnnotations(t *testing.T) {
	session, _ := newReadOnlySession(t)

	for tool, err := range session.Tools(context.Background(), nil) {
		if err != nil {
			t.Fatalf("listing tools: %v", err)
		}
		if tool.Annotations == nil || !tool.Annotations.ReadOnlyHint {
			t.Errorf("read-only mode: tool %q not annotated ReadOnlyHint", tool.Name)
		}
	}
}

// TestToolPartition guards against a tool being listed in both sets.
func TestToolPartition(t *testing.T) {
	for _, w := range WriteToolNames() {
		for _, r := range ReadOnlyToolNames() {
			if w == r {
				t.Fatalf("tool %q is in both the read-only and write sets", w)
			}
		}
	}
}
