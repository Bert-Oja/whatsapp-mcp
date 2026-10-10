package store

import (
	"testing"
	"time"
)

// TestMergeChatCombinesMessagesAndDropsDuplicates covers folding a LID chat
// into its phone-number chat.
func TestMergeChatCombinesMessagesAndDropsDuplicates(t *testing.T) {
	ms, err := New(t.TempDir())
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	defer ms.Close()

	lid := "100000000000001@lid"
	pn := "15551234567@s.whatsapp.net"
	base := time.Date(2026, 4, 18, 12, 0, 0, 0, time.UTC)

	if err := ms.StoreChat(lid, "Alice Example", base.Add(time.Hour)); err != nil {
		t.Fatalf("StoreChat(lid): %v", err)
	}
	if err := ms.StoreChat(pn, "Alice Example", base); err != nil {
		t.Fatalf("StoreChat(pn): %v", err)
	}

	// Two distinct messages in the LID chat, one of which already exists in PN.
	if err := ms.StoreMessage("shared", lid, "100000000000001", "dup", base.Add(10*time.Minute), false, "", "", "", nil, nil, nil, 0); err != nil {
		t.Fatalf("StoreMessage: %v", err)
	}
	if err := ms.StoreMessage("only-lid", lid, "100000000000001", "unique", base.Add(20*time.Minute), false, "", "", "", nil, nil, nil, 0); err != nil {
		t.Fatalf("StoreMessage: %v", err)
	}
	if err := ms.StoreMessage("shared", pn, "15551234567", "dup", base.Add(10*time.Minute), false, "", "", "", nil, nil, nil, 0); err != nil {
		t.Fatalf("StoreMessage: %v", err)
	}

	moved, err := ms.MergeChat(lid, pn)
	if err != nil {
		t.Fatalf("MergeChat: %v", err)
	}
	if moved != 1 {
		t.Errorf("moved = %d, want 1 (the duplicate should be dropped)", moved)
	}

	chats, err := ms.AllChats()
	if err != nil {
		t.Fatalf("AllChats: %v", err)
	}
	if len(chats) != 1 {
		t.Fatalf("expected 1 chat after merge, got %d", len(chats))
	}
	if chats[0].JID != pn {
		t.Errorf("surviving chat = %q, want %q", chats[0].JID, pn)
	}

	msgs, err := ms.GetMessages(pn, 100)
	if err != nil {
		t.Fatalf("GetMessages: %v", err)
	}
	if len(msgs) != 2 {
		t.Errorf("expected 2 messages after merge, got %d", len(msgs))
	}

	// The surviving chat's last-message time must reflect the moved message.
	if !chats[0].LastMessageTime.Equal(base.Add(20 * time.Minute)) {
		t.Errorf("last_message_time = %v, want %v", chats[0].LastMessageTime, base.Add(20*time.Minute))
	}
}

// TestMergeChatCreatesTargetWhenAbsent covers merging into a chat that does not
// exist yet.
func TestMergeChatCreatesTargetWhenAbsent(t *testing.T) {
	ms, err := New(t.TempDir())
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	defer ms.Close()

	lid := "100000000000001@lid"
	pn := "15551234567@s.whatsapp.net"
	ts := time.Date(2026, 4, 18, 12, 0, 0, 0, time.UTC)

	if err := ms.StoreChat(lid, "Alice Example", ts); err != nil {
		t.Fatalf("StoreChat: %v", err)
	}
	if err := ms.StoreMessage("m1", lid, "100000000000001", "hi", ts, false, "", "", "", nil, nil, nil, 0); err != nil {
		t.Fatalf("StoreMessage: %v", err)
	}

	if _, err := ms.MergeChat(lid, pn); err != nil {
		t.Fatalf("MergeChat: %v", err)
	}

	name, err := ms.ChatName(pn)
	if err != nil {
		t.Fatalf("ChatName: %v", err)
	}
	if name != "Alice Example" {
		t.Errorf("name = %q, want Alice Example (inherited from source)", name)
	}
}

// TestMergeChatSameJIDIsNoop covers the identity case.
func TestMergeChatSameJIDIsNoop(t *testing.T) {
	ms, err := New(t.TempDir())
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	defer ms.Close()

	moved, err := ms.MergeChat("a@s.whatsapp.net", "a@s.whatsapp.net")
	if err != nil {
		t.Fatalf("MergeChat: %v", err)
	}
	if moved != 0 {
		t.Errorf("moved = %d, want 0", moved)
	}
}
