package tools

import (
	"database/sql"
	"path/filepath"
	"testing"
	"time"

	_ "modernc.org/sqlite"
)

// fixtureDB builds a messages.db covering the cases the tools must handle:
// a direct chat, a group chat, media, own messages, and a repeat sender.
func fixtureDB(t *testing.T) string {
	t.Helper()

	dir := t.TempDir()
	path := filepath.Join(dir, "messages.db")
	db, err := sql.Open("sqlite", "file:"+path)
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	defer db.Close()

	if _, err := db.Exec(`
		CREATE TABLE chats (
			jid TEXT PRIMARY KEY,
			name TEXT,
			last_message_time TIMESTAMP
		);
		CREATE TABLE messages (
			id TEXT,
			chat_jid TEXT,
			sender TEXT,
			content TEXT,
			timestamp TIMESTAMP,
			is_from_me BOOLEAN,
			media_type TEXT,
			filename TEXT,
			url TEXT,
			media_key BLOB,
			file_sha256 BLOB,
			file_enc_sha256 BLOB,
			file_length INTEGER,
			PRIMARY KEY (id, chat_jid)
		);
	`); err != nil {
		t.Fatalf("schema: %v", err)
	}

	aliceJID := "15551234567@s.whatsapp.net"
	groupJID := "120363000000000000@g.us"

	chats := []struct {
		jid  string
		name string
		ts   string
	}{
		{aliceJID, "Alice Example", "2026-04-18 12:05:00"},
		{groupJID, "Weekend Plans", "2026-04-18 12:04:00"},
	}
	for _, c := range chats {
		if _, err := db.Exec("INSERT INTO chats (jid, name, last_message_time) VALUES (?, ?, ?)", c.jid, c.name, c.ts+"+00:00"); err != nil {
			t.Fatalf("insert chat: %v", err)
		}
	}

	messages := []struct {
		id, chatJID, sender, content, ts, mediaType string
		isFromMe                                    bool
	}{
		{"m1", aliceJID, aliceJID, "hey there", "2026-04-18 12:00:00", "", false},
		{"m2", aliceJID, "15550000000", "hi Alice", "2026-04-18 12:01:00", "", true},
		{"m3", aliceJID, aliceJID, "photo attached", "2026-04-18 12:03:00", "image", false},
		{"m4", aliceJID, "15550000000", "thanks!", "2026-04-18 12:05:00", "", true},
		{"m5", groupJID, "15559998888", "who is driving?", "2026-04-18 12:02:00", "", false},
		{"m6", groupJID, "15559997777", "meeting at 9", "2026-04-18 12:04:00", "", false},
	}
	for _, m := range messages {
		if _, err := db.Exec(`
			INSERT INTO messages (id, chat_jid, sender, content, timestamp, is_from_me, media_type)
			VALUES (?, ?, ?, ?, ?, ?, ?)`,
			m.id, m.chatJID, m.sender, m.content, m.ts+"+00:00", m.isFromMe, m.mediaType); err != nil {
			t.Fatalf("insert message: %v", err)
		}
	}

	return path
}

func openFixture(t *testing.T) (*Reader, *sql.DB) {
	t.Helper()
	path := fixtureDB(t)
	db, err := sql.Open("sqlite", "file:"+path)
	if err != nil {
		t.Fatalf("open fixture: %v", err)
	}
	t.Cleanup(func() { db.Close() })
	return NewReader(db), db
}

// TestFormatParity locks the exact text rendering the Python server produced,
// byte for byte. These strings were captured from whatsapp-mcp-server.
func TestFormatParity(t *testing.T) {
	r, _ := openFixture(t)

	ts, _ := time.Parse("2006-01-02 15:04:05", "2026-04-18 12:03:00")

	got := r.formatMessage(Message{
		Timestamp: ts,
		Sender:    "15551234567@s.whatsapp.net",
		ChatName:  "Alice Example",
		Content:   "photo attached",
		IsFromMe:  false,
		ChatJID:   "15551234567@s.whatsapp.net",
		ID:        "m3",
		MediaType: "image",
	}, true)

	want := "[2026-04-18 12:03:00] Chat: Alice Example From: Alice Example: [image - Message ID: m3 - Chat JID: 15551234567@s.whatsapp.net] photo attached\n"
	if got != want {
		t.Fatalf("format mismatch\n got: %q\nwant: %q", got, want)
	}
}

func TestFormatOwnMessageIsFromMe(t *testing.T) {
	r, _ := openFixture(t)
	ts, _ := time.Parse("2006-01-02 15:04:05", "2026-04-18 12:01:00")

	got := r.formatMessage(Message{
		Timestamp: ts,
		Sender:    "15550000000",
		ChatName:  "Alice Example",
		Content:   "hi Alice",
		IsFromMe:  true,
		ChatJID:   "15551234567@s.whatsapp.net",
		ID:        "m2",
	}, true)

	want := "[2026-04-18 12:01:00] Chat: Alice Example From: Me: hi Alice\n"
	if got != want {
		t.Fatalf("format mismatch\n got: %q\nwant: %q", got, want)
	}
}

func TestListMessagesEmptyPlaceholder(t *testing.T) {
	r, _ := openFixture(t)
	got, err := r.ListMessages(ListMessagesParams{Limit: 20, Query: "zzz-no-such-message"})
	if err != nil {
		t.Fatalf("ListMessages: %v", err)
	}
	if got != "No messages to display." {
		t.Fatalf("expected placeholder, got %q", got)
	}
}

// TestListMessagesContextOrdering verifies that context expansion preserves the
// Python ordering: before messages, the match, then after messages.
func TestListMessagesContextOrdering(t *testing.T) {
	r, _ := openFixture(t)

	got, err := r.ListMessages(ListMessagesParams{
		ChatJID:        "15551234567@s.whatsapp.net",
		Query:          "photo attached",
		Limit:          20,
		IncludeContext: true,
		ContextBefore:  1,
		ContextAfter:   1,
	})
	if err != nil {
		t.Fatalf("ListMessages: %v", err)
	}

	want := "[2026-04-18 12:01:00] Chat: Alice Example From: Me: hi Alice\n" +
		"[2026-04-18 12:03:00] Chat: Alice Example From: Alice Example: [image - Message ID: m3 - Chat JID: 15551234567@s.whatsapp.net] photo attached\n" +
		"[2026-04-18 12:05:00] Chat: Alice Example From: Me: thanks!\n"

	if got != want {
		t.Fatalf("context ordering mismatch\n got: %q\nwant: %q", got, want)
	}
}

func TestSearchContactsExcludesGroups(t *testing.T) {
	r, _ := openFixture(t)

	contacts, err := r.SearchContacts("alice")
	if err != nil {
		t.Fatalf("SearchContacts: %v", err)
	}
	if len(contacts) != 1 {
		t.Fatalf("expected 1 contact, got %d", len(contacts))
	}
	if contacts[0].PhoneNumber != "15551234567" || contacts[0].Name != "Alice Example" {
		t.Fatalf("unexpected contact: %+v", contacts[0])
	}
}

func TestListChatsSorting(t *testing.T) {
	r, _ := openFixture(t)

	byActivity, err := r.ListChats(ListChatsParams{Limit: 20, IncludeLastMessage: true, SortBy: "last_active"})
	if err != nil {
		t.Fatalf("ListChats: %v", err)
	}
	if len(byActivity) != 2 || byActivity[0].JID != "15551234567@s.whatsapp.net" {
		t.Fatalf("last_active ordering wrong: %+v", byActivity)
	}

	byName, err := r.ListChats(ListChatsParams{Limit: 20, IncludeLastMessage: true, SortBy: "name"})
	if err != nil {
		t.Fatalf("ListChats: %v", err)
	}
	if len(byName) != 2 || byName[0].Name != "Alice Example" {
		t.Fatalf("name ordering wrong: %+v", byName)
	}
}

func TestGetMessageContext(t *testing.T) {
	r, _ := openFixture(t)

	ctx, err := r.GetMessageContext("m3", 1, 1)
	if err != nil {
		t.Fatalf("GetMessageContext: %v", err)
	}
	if ctx.Message.ID != "m3" {
		t.Fatalf("wrong target: %+v", ctx.Message)
	}
	if len(ctx.Before) != 1 || ctx.Before[0].ID != "m2" {
		t.Fatalf("wrong before: %+v", ctx.Before)
	}
	if len(ctx.After) != 1 || ctx.After[0].ID != "m4" {
		t.Fatalf("wrong after: %+v", ctx.After)
	}
}

func TestGetLastInteraction(t *testing.T) {
	r, _ := openFixture(t)

	got, err := r.GetLastInteraction("15551234567@s.whatsapp.net")
	if err != nil {
		t.Fatalf("GetLastInteraction: %v", err)
	}
	want := "[2026-04-18 12:05:00] Chat: Alice Example From: Me: thanks!\n"
	if got != want {
		t.Fatalf("mismatch\n got: %q\nwant: %q", got, want)
	}

	// A contact with no interaction returns empty, like Python's None.
	empty, err := r.GetLastInteraction("nobody@s.whatsapp.net")
	if err != nil {
		t.Fatalf("GetLastInteraction (empty): %v", err)
	}
	if empty != "" {
		t.Fatalf("expected empty, got %q", empty)
	}
}

func TestGetDirectChatByContact(t *testing.T) {
	r, _ := openFixture(t)

	chat, err := r.GetDirectChatByContact("15551234567")
	if err != nil {
		t.Fatalf("GetDirectChatByContact: %v", err)
	}
	if chat == nil || chat.JID != "15551234567@s.whatsapp.net" {
		t.Fatalf("unexpected chat: %+v", chat)
	}
}

// TestChatKindClassification covers the thread/group/community signal an agent
// uses to decide whether a chat needs deeper history fetched.
func TestChatKindClassification(t *testing.T) {
	cases := []struct {
		jid, name, want string
	}{
		{"15551234567@s.whatsapp.net", "Alice Example", "direct"},
		{"100000000000001@lid", "Alice Example", "direct"},
		{"120363000000000002@g.us", "Weekend Plans", "group"},
		{"120363000000000002@g.us", "Weekend Plans [Community]", "community"},
		{"120363000000000001@g.us", "Announcements [Community: Neighbours]", "community"},
		{"status@broadcast", "", "broadcast"},
	}
	for _, c := range cases {
		if got := ChatKind(c.jid, c.name); got != c.want {
			t.Errorf("ChatKind(%q, %q) = %q, want %q", c.jid, c.name, got, c.want)
		}
	}
}

// TestChatReportsStoredRangeAndCount verifies an agent can tell which messages
// are held locally, and therefore whether it needs to request older ones.
func TestChatReportsStoredRangeAndCount(t *testing.T) {
	r, _ := openFixture(t)

	got, err := r.GetChat("15551234567@s.whatsapp.net", true)
	if err != nil {
		t.Fatalf("GetChat: %v", err)
	}
	if got == nil {
		t.Fatal("GetChat returned nil")
	}
	if got.Kind != "direct" {
		t.Errorf("Kind = %q, want direct", got.Kind)
	}
	if got.MessageCount != 4 {
		t.Errorf("MessageCount = %d, want 4", got.MessageCount)
	}
	if got.StoredFrom == nil || got.StoredTo == nil {
		t.Fatal("StoredFrom/StoredTo should be set for a chat with messages")
	}
	wantFrom, _ := time.Parse("2006-01-02 15:04:05", "2026-04-18 12:00:00")
	wantTo, _ := time.Parse("2006-01-02 15:04:05", "2026-04-18 12:05:00")
	if !got.StoredFrom.Equal(wantFrom) {
		t.Errorf("StoredFrom = %v, want %v", got.StoredFrom, wantFrom)
	}
	if !got.StoredTo.Equal(wantTo) {
		t.Errorf("StoredTo = %v, want %v", got.StoredTo, wantTo)
	}
}

// TestGroupChatIsClassifiedAsGroup covers the group fixture.
func TestGroupChatIsClassifiedAsGroup(t *testing.T) {
	r, _ := openFixture(t)

	got, err := r.GetChat("120363000000000000@g.us", true)
	if err != nil {
		t.Fatalf("GetChat: %v", err)
	}
	if got.Kind != "group" {
		t.Errorf("Kind = %q, want group", got.Kind)
	}
	if got.MessageCount != 2 {
		t.Errorf("MessageCount = %d, want 2", got.MessageCount)
	}
}

// TestChatWithoutMessagesHasNoStoredRange covers an empty chat, where an agent
// should be told there is nothing local rather than given bogus times.
func TestChatWithoutMessagesHasNoStoredRange(t *testing.T) {
	r, db := openFixture(t)

	chat := "15551234567@s.whatsapp.net"
	if _, err := db.Exec("INSERT INTO chats (jid, name, last_message_time) VALUES (?, ?, ?)",
		chat, "Alice Example", "2026-04-18 12:00:00+00:00"); err != nil {
		t.Fatalf("insert chat: %v", err)
	}

	got, err := r.GetChat(chat, true)
	if err != nil {
		t.Fatalf("GetChat: %v", err)
	}
	if got.MessageCount != 0 {
		t.Errorf("MessageCount = %d, want 0", got.MessageCount)
	}
	if got.StoredFrom != nil || got.StoredTo != nil {
		t.Error("StoredFrom/StoredTo should be nil for a chat with no messages")
	}
}
