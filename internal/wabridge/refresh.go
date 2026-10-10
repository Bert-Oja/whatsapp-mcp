package wabridge

import (
	"context"
	"time"

	"go.mau.fi/whatsmeow/types"
)

// RefreshPlaceholderNames periodically replaces placeholder chat names (a bare
// LID, phone number, or "Group <id>") with real ones once the corresponding
// contact details have synced.
//
// Contacts, groups and LID mappings arrive asynchronously after connecting, so
// a chat first seen early can be stored under a name derived from its JID and
// would otherwise stay that way until it next receives a message. This walks
// the stored chats and upgrades any that are still showing a placeholder.
//
// It returns when ctx is cancelled.
func (b *Bridge) RefreshPlaceholderNames(ctx context.Context) {
	// Initial delay: give the contact list a chance to download first.
	select {
	case <-ctx.Done():
		return
	case <-time.After(30 * time.Second):
	}

	b.refreshPlaceholderNamesOnce(ctx)

	ticker := time.NewTicker(10 * time.Minute)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			b.refreshPlaceholderNamesOnce(ctx)
		}
	}
}

// refreshPlaceholderNamesOnce performs a single pass over the stored chats.
func (b *Bridge) refreshPlaceholderNamesOnce(ctx context.Context) {
	chats, err := b.Store.AllChats()
	if err != nil {
		b.Logger.Warnf("Failed to list chats for name refresh: %v", err)
		return
	}

	updated := 0
	for _, c := range chats {
		jid, err := types.ParseJID(c.JID)
		if err != nil {
			continue
		}
		if !isPlaceholderName(c.Name, jid) {
			continue
		}

		resolved := b.ChatName(jid, c.JID, nil, "")
		if resolved == "" || isPlaceholderName(resolved, jid) {
			continue
		}

		if err := b.Store.StoreChat(c.JID, resolved, c.LastMessageTime); err != nil {
			b.Logger.Warnf("Failed to update chat name for %s: %v", c.JID, err)
			continue
		}
		b.Logger.Infof("Resolved chat name %s -> %s", c.JID, resolved)
		updated++
	}

	if updated > 0 {
		b.Logger.Infof("Updated %d chat name(s)", updated)
	}
}

// RequestHistoryForKnownChats asks for older history in every chat that has a
// stored message to anchor the request on, then stops.
//
// The request is per-chat and anchored on the oldest message we hold, so
// repeated runs extend backwards a page at a time. It returns early if the
// client is not connected, and never panics on a chat without an anchor.
func (b *Bridge) RequestHistoryForKnownChats(ctx context.Context, count int) {
	// Let the connection settle before sending peer requests.
	select {
	case <-ctx.Done():
		return
	case <-time.After(15 * time.Second):
	}

	if !b.Client.IsConnected() || b.Client.Store.ID == nil {
		b.Logger.Warnf("Skipping history request: not connected")
		return
	}

	chats, err := b.Store.AllChats()
	if err != nil {
		b.Logger.Warnf("Failed to list chats for history request: %v", err)
		return
	}

	requested := 0
	for _, c := range chats {
		select {
		case <-ctx.Done():
			return
		default:
		}

		// Anchor on the OLDEST stored message so each request reaches further
		// back than the last.
		anchor, err := b.Store.OldestMessageInfo(c.JID)
		if err != nil {
			continue // no anchor for this chat; nothing to request
		}

		if err := b.RequestHistoryBefore(ctx, anchor, count); err != nil {
			b.Logger.Warnf("History request for %s failed: %v", c.JID, err)
			continue
		}
		b.Logger.Infof("Requested %d older messages for %s", count, c.JID)
		requested++

		// Space requests out; they are peer messages to the primary device.
		select {
		case <-ctx.Done():
			return
		case <-time.After(2 * time.Second):
		}
	}

	if requested == 0 {
		b.Logger.Infof("No chat had a message to anchor a history request on")
	}
}
