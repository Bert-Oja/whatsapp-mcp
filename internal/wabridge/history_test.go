package wabridge

import (
	"context"
	"errors"
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

// TestBackfillHistoryHandlesChatsWithoutAnchor confirms the loop skips chats
// that have no message rather than panicking.
func TestBackfillHistoryHandlesChatsWithoutAnchor(t *testing.T) {
	b := pairedBridge(t)

	// A chat exists but has no messages.
	if err := b.Store.StoreChat("100000000000001@lid", "Alice Example", time.Now()); err != nil {
		t.Fatalf("StoreChat: %v", err)
	}

	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()

	done := make(chan struct{})
	go func() {
		b.BackfillHistory(ctx, 50, 0)
		close(done)
	}()

	select {
	case <-done:
		// Returned without panicking: the desired behaviour.
	case <-time.After(25 * time.Second):
		t.Fatal("BackfillHistory did not return")
	}
}

// TestOldestAndNewestMessageInfoHaveInverseOrdering covers the anchor helpers.
func TestOldestAndNewestMessageInfoHaveInverseOrdering(t *testing.T) {
	dir := t.TempDir()
	ms, err := store.New(dir)
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
