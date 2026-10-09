package store

import (
	"fmt"
	"strings"
	"sync"
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

// TestDSNHasRequiredPragmas locks the connection settings that prevent
// SQLITE_BUSY. modernc.org/sqlite defaults busy_timeout to 0 (unlike
// mattn/go-sqlite3, which set 5000ms itself), so losing this pragma causes
// failures under the concurrent access whatsmeow performs.
func TestDSNHasRequiredPragmas(t *testing.T) {
	got := DSN("/data/messages.db")
	for _, want := range []string{
		"busy_timeout(5000)",
		"journal_mode(WAL)",
		"foreign_keys(1)",
	} {
		if !strings.Contains(got, want) {
			t.Errorf("DSN missing %q: %s", want, got)
		}
	}
}

// TestConcurrentWritesDoNotFail reproduces the contention that produced
// SQLITE_BUSY in the live container: many goroutines writing at once.
func TestConcurrentWritesDoNotFail(t *testing.T) {
	ms, err := New(t.TempDir())
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	defer ms.Close()

	ts := time.Date(2026, 4, 18, 12, 0, 0, 0, time.UTC)
	if err := ms.StoreChat("c@g.us", "Chat", ts); err != nil {
		t.Fatalf("StoreChat: %v", err)
	}

	const writers, perWriter = 16, 25
	errs := make(chan error, writers)
	var wg sync.WaitGroup
	for w := 0; w < writers; w++ {
		wg.Add(1)
		go func(n int) {
			defer wg.Done()
			for i := 0; i < perWriter; i++ {
				id := fmt.Sprintf("m-%d-%d", n, i)
				if err := ms.StoreMessage(id, "c@g.us", "s", "hi", ts, false, "", "", "", nil, nil, nil, 0); err != nil {
					errs <- err
					return
				}
			}
		}(w)
	}
	wg.Wait()
	close(errs)

	for err := range errs {
		t.Errorf("concurrent write failed: %v", err)
	}

	messages, err := ms.GetMessages("c@g.us", writers*perWriter+10)
	if err != nil {
		t.Fatalf("GetMessages: %v", err)
	}
	if len(messages) != writers*perWriter {
		t.Errorf("stored %d messages, want %d", len(messages), writers*perWriter)
	}
}
