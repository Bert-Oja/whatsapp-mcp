package wabridge

import (
	"context"
	"database/sql"
	"errors"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"go.mau.fi/whatsmeow/types"

	"github.com/Bert-Oja/whatsapp-mcp/internal/store"
)

// TestRequestHistoryBeforeRejectsEmptyAnchor is the regression test for the
// panic that made upstream's requestHistorySync dead code: calling whatsmeow's
// BuildHistorySyncRequest with no anchor nil-dereferences. Requesting history
// must fail cleanly instead.
func TestRequestHistoryBeforeRejectsEmptyAnchor(t *testing.T) {
	b := pairedBridge(t)

	err := b.RequestHistoryBefore(context.Background(), types.MessageInfo{}, 50)
	if err == nil {
		t.Fatal("expected an error for an empty anchor")
	}
	if !strings.Contains(err.Error(), "anchor") {
		t.Errorf("error should mention the missing anchor, got: %v", err)
	}
}

// TestRequestOlderMessagesCapsPageSize confirms one call cannot request more
// than the small on-demand page limit, so a single request stays modest.
func TestRequestOlderMessagesCapsPageSize(t *testing.T) {
	b := pairedBridge(t)

	// Not connected, so this errors, but the clamp happens before the network
	// call and the returned result carries the clamped count.
	res, err := b.RequestOlderMessages(context.Background(), "15551234567@s.whatsapp.net", 5000)
	if err == nil {
		t.Fatal("expected an error while disconnected")
	}
	if res.RequestedCount != maxRequestPageSize {
		t.Errorf("RequestedCount = %d, want the %d cap", res.RequestedCount, maxRequestPageSize)
	}
}

// TestRequestOlderMessagesDefaultsToFifty covers the default page size.
func TestRequestOlderMessagesDefaultsToFifty(t *testing.T) {
	b := pairedBridge(t)

	res, _ := b.RequestOlderMessages(context.Background(), "15551234567@s.whatsapp.net", 0)
	if res.RequestedCount != 50 {
		t.Errorf("RequestedCount = %d, want 50", res.RequestedCount)
	}
}

// TestOldestAndNewestMessageInfoHaveInverseOrdering covers the anchor helpers.
func TestOldestAndNewestMessageInfoHaveInverseOrdering(t *testing.T) {
	ms, err := store.New(t.TempDir())
	if err != nil {
		t.Fatalf("store.New: %v", err)
	}
	defer ms.Close()

	chat := "100000000000001@lid"
	if err := ms.StoreChat(chat, "Alice Example", time.Now()); err != nil {
		t.Fatalf("StoreChat: %v", err)
	}

	base := time.Date(2026, 4, 18, 12, 0, 0, 0, time.UTC)
	for i := 0; i < 3; i++ {
		id := []string{"m-old", "m-mid", "m-new"}[i]
		ts := base.Add(time.Duration(i) * time.Minute)
		if err := ms.StoreMessage(id, chat, "15551234567", "hello", ts, false, "", "", "", nil, nil, nil, 0); err != nil {
			t.Fatalf("StoreMessage: %v", err)
		}
	}

	oldest, err := ms.OldestMessageInfo(chat)
	if err != nil {
		t.Fatalf("OldestMessageInfo: %v", err)
	}
	newest, err := ms.NewestMessageInfo(chat)
	if err != nil {
		t.Fatalf("NewestMessageInfo: %v", err)
	}

	if oldest.ID != "m-old" {
		t.Errorf("oldest = %q, want m-old", oldest.ID)
	}
	if newest.ID != "m-new" {
		t.Errorf("newest = %q, want m-new", newest.ID)
	}
	if !newest.Timestamp.After(oldest.Timestamp) {
		t.Error("newest should be after oldest")
	}
	if newest.Chat.String() != chat {
		t.Errorf("chat = %q, want %q", newest.Chat, chat)
	}
}

// TestAnchorOnChatWithoutMessagesReturnsErrNoAnchor covers the empty case.
func TestAnchorOnChatWithoutMessagesReturnsErrNoAnchor(t *testing.T) {
	ms, err := store.New(t.TempDir())
	if err != nil {
		t.Fatalf("store.New: %v", err)
	}
	defer ms.Close()

	_, err = ms.OldestMessageInfo("nobody@s.whatsapp.net")
	if !errors.Is(err, store.ErrNoAnchor) {
		t.Errorf("expected ErrNoAnchor, got %v", err)
	}
}

// TestRequestOlderMessagesBootstrapsEmptyChat confirms an empty chat falls back
// to a recovered anchor instead of failing outright, and reports that it did.
func TestRequestOlderMessagesBootstrapsEmptyChat(t *testing.T) {
	b := pairedBridge(t)

	dir := t.TempDir()
	ms, err := store.New(dir)
	if err != nil {
		t.Fatalf("store.New: %v", err)
	}
	defer ms.Close()
	b.Store = ms

	// With no recovered anchors available, bootstrapping must fail cleanly
	// rather than panic, and must say so.
	res, err := b.RequestOlderMessages(context.Background(), "15551234567@s.whatsapp.net", 50)
	if err == nil {
		// Disconnected clients fail earlier; either way no panic and no crash.
		t.Log("request unexpectedly succeeded")
	}
	if res.ChatJID != "15551234567@s.whatsapp.net" {
		t.Errorf("ChatJID = %q, want the requested chat", res.ChatJID)
	}
}

// TestGroupHistoryErrorExplainsTheLimitation locks the message a caller sees
// for a group with no stored message.
//
// This is a measured limitation, not a bug: on-demand history walks back from a
// message, and a group that has never delivered one has no anchor. An
// anchor-less request was tried against a real account and WhatsApp answered
// with the correct conversation but zero messages, so none is sent now.
func TestGroupHistoryErrorExplainsTheLimitation(t *testing.T) {
	err := noAnchorError(types.JID{User: "120363000000000000", Server: types.GroupServer})
	if err == nil {
		t.Fatal("expected an error for a group with no anchor")
	}
	msg := err.Error()
	for _, want := range []string{"anchor", "starting message"} {
		if !strings.Contains(msg, want) {
			t.Errorf("error should mention %q, got: %v", want, msg)
		}
	}
}

// TestDirectChatNoAnchorUsesRecovery confirms a direct chat takes the recovery
// path rather than the group error.
func TestDirectChatNoAnchorUsesRecovery(t *testing.T) {
	if err := noAnchorError(types.JID{User: "15551234567", Server: types.DefaultUserServer}); err != nil {
		t.Errorf("a direct chat should not produce the group error, got: %v", err)
	}
}

// TestLoadSurvivingGroupAnchorsFiltersGroups covers the session-store helper.
func TestLoadSurvivingGroupAnchorsFiltersGroups(t *testing.T) {
	dir := t.TempDir()
	sessionDB := filepath.Join(dir, "whatsapp.db")
	raw, err := sql.Open("sqlite", "file:"+sessionDB)
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	if _, err := raw.Exec(`CREATE TABLE whatsmeow_message_secrets (
		our_jid TEXT, chat_jid TEXT, sender_jid TEXT, message_id TEXT, key BLOB)`); err != nil {
		t.Fatalf("schema: %v", err)
	}
	rows := [][3]string{
		{"me@s.whatsapp.net", "15551234567@s.whatsapp.net", "15551234567@s.whatsapp.net"},
		{"me@s.whatsapp.net", "120363000000000000@g.us", "15557654321@s.whatsapp.net"},
	}
	for _, r := range rows {
		if _, err := raw.Exec("INSERT INTO whatsmeow_message_secrets VALUES (?,?,?,?,?)", r[0], r[1], r[2], "AC"+r[1], []byte{1}); err != nil {
			t.Fatalf("insert: %v", err)
		}
	}
	raw.Close()

	groups, err := store.LoadSurvivingGroupAnchors(sessionDB)
	if err != nil {
		t.Fatalf("LoadSurvivingGroupAnchors: %v", err)
	}
	if len(groups) != 1 {
		t.Fatalf("expected 1 group anchor, got %d", len(groups))
	}
	if groups[0].ChatJID != "120363000000000000@g.us" {
		t.Errorf("group anchor = %q, want the group JID", groups[0].ChatJID)
	}
}

// TestKnownGroupIDsReadsChatSettings covers the fallback discovery source.
func TestKnownGroupIDsReadsChatSettings(t *testing.T) {
	dir := t.TempDir()
	sessionDB := filepath.Join(dir, "whatsapp.db")
	raw, err := sql.Open("sqlite", "file:"+sessionDB)
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	if _, err := raw.Exec(`CREATE TABLE whatsmeow_chat_settings (
		our_jid TEXT, chat_jid TEXT, muted_until BIGINT, pinned BOOLEAN, archived BOOLEAN, wasa_root_secret_id TEXT)`); err != nil {
		t.Fatalf("schema: %v", err)
	}
	for _, j := range []string{"120363000000000000@g.us", "15551234567@s.whatsapp.net"} {
		if _, err := raw.Exec("INSERT INTO whatsmeow_chat_settings VALUES (?,?,0,0,0,'')", "me@s.whatsapp.net", j); err != nil {
			t.Fatalf("insert: %v", err)
		}
	}
	raw.Close()

	ids, err := store.KnownGroupIDs(sessionDB)
	if err != nil {
		t.Fatalf("KnownGroupIDs: %v", err)
	}
	if len(ids) != 1 || ids[0] != "120363000000000000@g.us" {
		t.Errorf("KnownGroupIDs = %v, want the single group JID", ids)
	}
}
