package tools

import (
	"database/sql"
	"encoding/json"
	"os"
	"path/filepath"
	"reflect"
	"testing"

	_ "modernc.org/sqlite"
)

// writeParityFixture creates a messages.db on disk and returns its path.
func writeParityFixture(t *testing.T, dir string) string {
	t.Helper()

	db, err := sql.Open("sqlite", "file:"+filepath.Join(dir, "messages.db"))
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	defer db.Close()

	if _, err := db.Exec(`
		CREATE TABLE chats (jid TEXT PRIMARY KEY, name TEXT, last_message_time TIMESTAMP);
		CREATE TABLE messages (
			id TEXT, chat_jid TEXT, sender TEXT, content TEXT, timestamp TIMESTAMP,
			is_from_me BOOLEAN, media_type TEXT, filename TEXT, url TEXT,
			media_key BLOB, file_sha256 BLOB, file_enc_sha256 BLOB, file_length INTEGER,
			PRIMARY KEY (id, chat_jid));
	`); err != nil {
		t.Fatalf("schema: %v", err)
	}

	alice := "15551234567@s.whatsapp.net"
	// Timestamps are written in the legacy mattn/go-sqlite3 on-disk format,
	// exactly as the existing database stores them.
	if _, err := db.Exec("INSERT INTO chats VALUES (?, ?, ?)", alice, "Alice Example", "2026-04-18 12:05:00+00:00"); err != nil {
		t.Fatalf("chat: %v", err)
	}
	// A second chat proves group filtering.
	if _, err := db.Exec("INSERT INTO chats VALUES (?, ?, ?)", "120363@g.us", "Weekend Plans", "2026-04-18 12:04:00+00:00"); err != nil {
		t.Fatalf("chat2: %v", err)
	}

	rows := [][]any{
		{"m1", alice, alice, "hey there", "2026-04-18 12:00:00+00:00", false, ""},
		{"m2", alice, "15550000000", "hi Alice", "2026-04-18 12:01:00+00:00", true, ""},
		{"m3", alice, alice, "photo attached", "2026-04-18 12:03:00+00:00", false, "image"},
		{"m4", alice, "15550000000", "thanks!", "2026-04-18 12:05:00+00:00", true, ""},
	}
	for _, r := range rows {
		if _, err := db.Exec(`INSERT INTO messages (id, chat_jid, sender, content, timestamp, is_from_me, media_type)
			VALUES (?, ?, ?, ?, ?, ?, ?)`, r...); err != nil {
			t.Fatalf("message: %v", err)
		}
	}

	return filepath.Join(dir, "messages.db")
}

// TestParityAgainstPythonServer is the Phase 2 acceptance gate: it compares the
// Go implementation against output captured from the real Python server.
//
// The golden file records the original Python server's output, captured before
// that server was removed. It is a frozen contract: the test rebuilds the same
// fixture database it was generated from (see writeParityFixture) and compares
// each tool's output.
//
// A mismatch means the Go implementation has drifted from the behaviour it
// replaced. The generator script is gone, so the file can no longer be
// regenerated; update it by hand only when the change is deliberate.
func TestParityAgainstPythonServer(t *testing.T) {
	oraclePath := filepath.Join("testdata", "python_oracle.json")
	oracleBytes, err := os.ReadFile(oraclePath)
	if err != nil {
		t.Skipf("parity oracle not present: %v", err)
	}
	var want map[string]json.RawMessage
	if err := json.Unmarshal(oracleBytes, &want); err != nil {
		t.Fatalf("parsing oracle: %v", err)
	}

	dir := t.TempDir()
	dbPath := writeParityFixture(t, dir)

	db, err := sql.Open("sqlite", "file:"+dbPath)
	if err != nil {
		t.Fatalf("open fixture: %v", err)
	}
	defer db.Close()
	r := NewReader(db)

	alice := "15551234567@s.whatsapp.net"

	got := map[string]any{}
	ctx, err := r.GetMessageContext("m3", 1, 1)
	if err != nil {
		t.Fatalf("context: %v", err)
	}
	got["format_image"] = r.formatMessage(ctx.Message, true)

	ctx2, err := r.GetMessageContext("m2", 1, 1)
	if err != nil {
		t.Fatalf("context m2: %v", err)
	}
	got["format_own"] = r.formatMessage(ctx2.Message, true)

	list, err := r.ListMessages(ListMessagesParams{
		ChatJID: alice, Query: "photo attached", Limit: 20,
		IncludeContext: true, ContextBefore: 1, ContextAfter: 1,
	})
	if err != nil {
		t.Fatalf("list: %v", err)
	}
	got["list_context"] = list

	empty, err := r.ListMessages(ListMessagesParams{ChatJID: alice, Query: "zzz-no-such-message", Limit: 20})
	if err != nil {
		t.Fatalf("list empty: %v", err)
	}
	got["empty"] = empty

	last, err := r.GetLastInteraction(alice)
	if err != nil {
		t.Fatalf("last interaction: %v", err)
	}
	got["last_interaction"] = last

	contacts, err := r.SearchContacts("alice")
	if err != nil {
		t.Fatalf("search contacts: %v", err)
	}
	contactRows := []map[string]string{}
	for _, c := range contacts {
		contactRows = append(contactRows, map[string]string{
			"phone_number": c.PhoneNumber, "name": c.Name, "jid": c.JID,
		})
	}
	got["search_contacts"] = contactRows

	byActivity, err := r.ListChats(ListChatsParams{Limit: 20, IncludeLastMessage: true, SortBy: "last_active"})
	if err != nil {
		t.Fatalf("list chats: %v", err)
	}
	activityRows := []map[string]any{}
	for _, c := range byActivity {
		var lastMsg any
		if c.LastMessage != "" {
			lastMsg = c.LastMessage
		}
		activityRows = append(activityRows, map[string]any{
			"jid": c.JID, "name": c.Name, "last_message": lastMsg,
		})
	}
	got["list_chats_last_active"] = activityRows

	byName, err := r.ListChats(ListChatsParams{Limit: 20, IncludeLastMessage: true, SortBy: "name"})
	if err != nil {
		t.Fatalf("list chats by name: %v", err)
	}
	nameRows := []map[string]string{}
	for _, c := range byName {
		nameRows = append(nameRows, map[string]string{"jid": c.JID, "name": c.Name})
	}
	got["list_chats_by_name"] = nameRows

	chat, err := r.GetChat(alice, true)
	if err != nil {
		t.Fatalf("get chat: %v", err)
	}
	got["get_chat"] = map[string]string{"jid": chat.JID, "name": chat.Name, "last_message": chat.LastMessage}

	direct, err := r.GetDirectChatByContact("15551234567")
	if err != nil {
		t.Fatalf("direct chat: %v", err)
	}
	got["direct_chat"] = map[string]string{"jid": direct.JID, "name": direct.Name}

	for key, wantRaw := range want {
		gotRaw, err := json.Marshal(got[key])
		if err != nil {
			t.Errorf("%s: marshalling Go result: %v", key, err)
			continue
		}
		if !jsonEqual(wantRaw, gotRaw) {
			t.Errorf("%s mismatch\n python: %s\n go:     %s", key, wantRaw, gotRaw)
		}
	}
}

// jsonEqual compares two JSON documents ignoring key order and whitespace.
func jsonEqual(a, b []byte) bool {
	var av, bv any
	if err := json.Unmarshal(a, &av); err != nil {
		return false
	}
	if err := json.Unmarshal(b, &bv); err != nil {
		return false
	}
	return reflect.DeepEqual(av, bv)
}
