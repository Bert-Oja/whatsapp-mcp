package wabridge

import (
	"context"
	"errors"
	"fmt"
	"path/filepath"
	"time"

	"go.mau.fi/whatsmeow"
	"go.mau.fi/whatsmeow/types"

	"github.com/Bert-Oja/whatsapp-mcp/internal/store"
	"github.com/Bert-Oja/whatsapp-mcp/internal/tools"
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

// RequestOlderMessages asks for one small block of messages older than the
// oldest message held for a chat.
//
// This is the on-demand primitive: nothing runs automatically. An agent that has
// identified a conversation calls it, and each call walks one page further back.
// Page size is kept small (default 50, the size whatsmeow recommends) so a
// single request is a modest, bounded operation rather than an account-wide
// sweep.
//
// It returns the anchor the request was based on, so the caller can tell whether
// a later call actually advanced (a repeated anchor means the previous request
// produced nothing, usually because no more history is available).
func (b *Bridge) RequestOlderMessages(ctx context.Context, chatJID string, count int) (tools.HistoryRequestResult, error) {
	res := tools.HistoryRequestResult{ChatJID: chatJID}

	// Normalise the page size first: this is input validation, and callers
	// should see the effective size even when the request cannot be sent.
	if count <= 0 {
		count = 50
	}
	if count > maxRequestPageSize {
		count = maxRequestPageSize
	}
	res.RequestedCount = count

	if b.Client == nil {
		return res, fmt.Errorf("client is not initialized")
	}
	if !b.Client.IsConnected() {
		return res, fmt.Errorf("client is not connected")
	}
	if b.Client.Store.ID == nil {
		return res, fmt.Errorf("client is not logged in")
	}

	before, err := b.Store.MessageCount(chatJID)
	if err != nil {
		return res, err
	}

	anchor, err := b.Store.OldestMessageInfo(chatJID)
	if err != nil {
		if errors.Is(err, store.ErrNoAnchor) {
			// No message to anchor on. Fall back to recovering a real message
			// ID from whatsmeow's own store, which survives a local reset.
			if err := b.bootstrapChat(ctx, chatJID, count); err != nil {
				return res, err
			}
			res.Bootstrapped = true
			res.AnchorID = "(recovered)"
			return res, nil
		}
		return res, err
	}

	if err := b.RequestHistoryBefore(ctx, anchor, count); err != nil {
		return res, err
	}
	res.AnchorID = anchor.ID
	res.MessagesBefore = before
	return res, nil
}

// maxRequestPageSize bounds a single on-demand page.
const maxRequestPageSize = 50

// CanonicalizeChats merges chats that address the same conversation under
// different identifiers.
//
// A conversation can be stored twice: live messages arrive addressed by LID
// (100000000000001@lid) while history responses use the phone number
// (15551234567@s.whatsapp.net). Both resolve to the same contact and hold
// different messages, so a search over the account would only see half the
// conversation in each row.
//
// Where a LID has a known phone number, the phone-number chat wins: history is
// the larger, more complete record and is addressed by number. The LID chat is
// folded into it, keeping the resolved contact name.
func (b *Bridge) CanonicalizeChats(ctx context.Context) {
	if b.Client == nil || b.Client.Store.LIDs == nil {
		return
	}

	chats, err := b.Store.AllChats()
	if err != nil {
		b.Logger.Warnf("Failed to list chats for canonicalisation: %v", err)
		return
	}

	for _, c := range chats {
		select {
		case <-ctx.Done():
			return
		default:
		}

		jid, err := types.ParseJID(c.JID)
		if err != nil || jid.Server != types.HiddenUserServer {
			continue // only LID chats need re-addressing
		}
		if jid.User == "" {
			continue
		}

		pn, err := b.Client.Store.LIDs.GetPNForLID(ctx, jid)
		if err != nil || pn.IsEmpty() {
			continue // no known phone number for this LID
		}
		target := pn.ToNonAD()
		if target.Server != types.DefaultUserServer {
			continue
		}

		moved, err := b.Store.MergeChat(c.JID, target.String())
		if err != nil {
			b.Logger.Warnf("Failed to merge %s into %s: %v", c.JID, target, err)
			continue
		}
		// Make sure the surviving chat carries a real name.
		if name := b.ChatName(target, target.String(), nil, ""); name != "" {
			if err := b.Store.StoreChat(target.String(), name, c.LastMessageTime); err != nil {
				b.Logger.Warnf("Failed to update merged chat name for %s: %v", target, err)
			}
		}
		b.Logger.Infof("Merged chat %s into %s (%d message(s) moved)", c.JID, target, moved)
	}
}

// CanonicalChatJID returns a single stable identifier for a conversation, so
// live messages (addressed by LID) and history (addressed by phone number) land
// on the same chat row.
//
// Only peer and own-account chats are rewritten: groups are returned unchanged.
// When a LID has a known phone number, the phone-number form is canonical,
// matching the history responses that carry the bulk of the data. An unknown
// LID is left as-is so nothing is lost. Our own LID is rewritten too, so the
// note-to-self chat does not exist under two identifiers.
func (b *Bridge) CanonicalChatJID(jid types.JID) types.JID {
	if b.Client == nil || b.Client.Store.LIDs == nil {
		return jid
	}
	if jid.Server != types.HiddenUserServer || jid.User == "" {
		return jid // groups and PN chats are already canonical
	}

	pn, err := b.Client.Store.LIDs.GetPNForLID(context.Background(), jid)
	if err != nil || pn.IsEmpty() {
		return jid // unmapped: keep the LID rather than guessing
	}
	if pn.Server != types.DefaultUserServer {
		return jid
	}
	return pn.ToNonAD()
}

// communitySuffix describes a group's position in a Community, so a sub-group
// is not mistaken for a standalone group and a parent is recognisable.
//
// Returns "" when the group is an ordinary group. The parent's name is looked
// up so a sub-group reads as "<sub> · <parent>", which is what a reader needs
// to tell the two apart in a flat chat list.
func communitySuffix(ctx context.Context, client *whatsmeow.Client, info *types.GroupInfo) string {
	if info == nil {
		return ""
	}

	// The Community itself.
	if info.IsParent {
		return " [Community]"
	}

	// A sub-group linked to a Community.
	parent := info.LinkedParentJID
	if parent.IsEmpty() {
		return ""
	}

	label := " [Community sub-group]"
	if parentInfo, err := client.GetGroupInfo(ctx, parent); err == nil && parentInfo.Name != "" {
		label = fmt.Sprintf(" [Community: %s]", parentInfo.Name)
	}
	return label
}

// bootstrapChat recovers a starting point for a single chat that has no stored
// messages to anchor on.
//
// On-demand history walks BACKWARDS from a message we already hold, so an empty
// messages.db cannot make a request at all. Two approaches were tried:
//
//  1. An anchor-less request (the proto's anchor fields are optional). Measured
//     against a real account: 145 chats requested, zero messages returned.
//     WhatsApp silently ignores it, so it is not usable.
//  2. Anchoring on message IDs recovered from whatsmeow's own store. whatsmeow
//     keeps per-message secrets in the session database (whatsapp.db), which
//     survives a messages.db wipe, and each row carries a real message ID. This
//     is the path used here.
//
// It is scoped to one chat on purpose: recovering message IDs account-wide would
// mean messaging every contact, which is exactly the sweep this design avoids.
// With no recovered anchor for the chat, history can only refill from new
// traffic.
func (b *Bridge) bootstrapChat(ctx context.Context, chatJID string, count int) error {
	sessionDB := filepath.Join(b.Store.Dir(), "whatsapp.db")
	anchors, err := store.LoadSurvivingAnchors(sessionDB)
	if err != nil {
		return fmt.Errorf("no recoverable message anchor for %s: %w", chatJID, err)
	}

	chat, err := types.ParseJID(chatJID)
	if err != nil {
		return fmt.Errorf("parsing chat jid %q: %w", chatJID, err)
	}
	canonical := b.CanonicalChatJID(chat).String()

	ownUser := ""
	if own := b.Client.Store.ID; own != nil {
		ownUser = own.User
	}

	var lastErr error
	for _, a := range anchors {
		if ctx.Err() != nil {
			return ctx.Err()
		}
		if a.ChatJID != chatJID && a.ChatJID != canonical {
			continue
		}

		sender, serr := types.ParseJID(a.SenderJID)
		if serr != nil {
			sender = types.JID{User: a.SenderJID, Server: types.DefaultUserServer}
		}
		if sender.User == ownUser {
			sender = types.EmptyJID
		}

		anchor := types.MessageInfo{
			ID:        a.MessageID,
			Timestamp: time.Now(), // unknown; the server keys off the ID
			MessageSource: types.MessageSource{
				Chat:   chat,
				Sender: sender,
			},
		}
		if err := b.RequestHistoryBefore(ctx, anchor, count); err != nil {
			lastErr = err
			continue
		}
		b.Logger.Infof("Bootstrapped %s from a recovered message anchor", chatJID)
		return nil
	}

	if lastErr != nil {
		return lastErr
	}
	return fmt.Errorf("no recoverable message anchor for %s", chatJID)
}
