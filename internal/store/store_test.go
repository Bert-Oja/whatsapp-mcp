package store

import (
	"testing"
	"time"
)

// TestSchemaAndRoundTrip covers the messages.db schema and the basic
// read/write paths the MCP tools depend on.
func TestSchemaAndRoundTrip(t *testing.T) {
	ms, err := New(t.TempDir())
	if err != nil {
		t.Fatalf("New failed: %v", err)
	}
	defer ms.Close()

	ts := time.Date(2026, 4, 18, 12, 0, 0, 0, time.UTC)

	if err := ms.StoreChat("123@s.whatsapp.net", "Alice", ts); err != nil {
		t.Fatalf("StoreChat: %v", err)
	}
	if err := ms.StoreMessage(
		"msg1", "123@s.whatsapp.net", "123", "hello world", ts, false,
		"", "", "", nil, nil, nil, 0,
	); err != nil {
		t.Fatalf("StoreMessage: %v", err)
	}
	// A message with neither content nor media must be skipped.
	if err := ms.StoreMessage(
		"skip", "123@s.whatsapp.net", "123", "", ts, false, "", "", "", nil, nil, nil, 0,
	); err != nil {
		t.Fatalf("StoreMessage (skip): %v", err)
	}

	messages, err := ms.GetMessages("123@s.whatsapp.net", 10)
	if err != nil {
		t.Fatalf("GetMessages: %v", err)
	}
	if len(messages) != 1 {
		t.Fatalf("expected 1 message, got %d", len(messages))
	}
	if messages[0].Content != "hello world" || messages[0].IsFromMe {
		t.Fatalf("unexpected message: %+v", messages[0])
	}

	chats, err := ms.GetChats()
	if err != nil {
		t.Fatalf("GetChats: %v", err)
	}
	if len(chats) != 1 {
		t.Fatalf("expected 1 chat, got %d", len(chats))
	}

	name, err := ms.ChatName("123@s.whatsapp.net")
	if err != nil || name != "Alice" {
		t.Fatalf("ChatName = %q, %v", name, err)
	}
}
