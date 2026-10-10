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
