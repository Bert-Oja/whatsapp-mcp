package wabridge

import (
	"context"
	"fmt"
	"path/filepath"
	"sync"
	"time"

	"go.mau.fi/whatsmeow"
	"go.mau.fi/whatsmeow/types"

	"github.com/Bert-Oja/whatsapp-mcp/internal/store"
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

// BackfillHistory walks history backwards continuously while enabled.
//
// On-demand history is anchored on the oldest message we hold for a chat, so
// each request reaches further back than the last. A single page is small (the
// library recommends 50), so filling a conversation takes many requests: this
// keeps issuing them, pausing between passes, until every chat stops producing
// new messages or ctx is cancelled.
//
// A chat that returns no new messages is given a growing cooldown so it is not
// polled forever; it is retried occasionally in case the phone can supply more
// later.
func (b *Bridge) BackfillHistory(ctx context.Context, pageSize, limit int) {
	// Let the connection settle before sending peer requests.
	select {
	case <-ctx.Done():
		return
	case <-time.After(15 * time.Second):
	}

	if pageSize <= 0 {
		pageSize = 50 // whatsmeow recommends 50 per request
	}

	// Per-chat state: consecutive passes that produced nothing, and the oldest
	// message we have already anchored on (so an unanswered request is not
	// re-sent immediately).
	type chatState struct {
		barren     int
		lastAnchor string
	}
	states := map[string]*chatState{}

	const maxBarrenPasses = 4
	pause := 30 * time.Second

	for {
		select {
		case <-ctx.Done():
			return
		case <-time.After(pause):
		}

		if !b.Client.IsConnected() || b.Client.Store.ID == nil {
			continue // wait for reconnection
		}

		chats, err := b.Store.AllChats()
		if err != nil {
			b.Logger.Warnf("Failed to list chats for history backfill: %v", err)
			continue
		}

		requested := 0
		for _, c := range chats {
			select {
			case <-ctx.Done():
				return
			default:
			}

			st := states[c.JID]
			if st == nil {
				st = &chatState{}
				states[c.JID] = st
			}
			if st.barren >= maxBarrenPasses {
				continue // this chat has gone quiet
			}
			if eff := b.effectiveHistoryLimit(c.JID, limit); eff > 0 {
				if n, err := b.Store.MessageCount(c.JID); err == nil && n >= eff {
					continue // reached the configured cap for this chat
				}
			}

			anchor, err := b.Store.OldestMessageInfo(c.JID)
			if err != nil {
				continue // nothing to anchor on in this chat
			}
			if anchor.ID == st.lastAnchor {
				// Same anchor as last pass: the previous request has not been
				// answered yet, so do not send it again.
				st.barren++
				continue
			}

			if err := b.RequestHistoryBefore(ctx, anchor, pageSize); err != nil {
				b.Logger.Warnf("History request for %s failed: %v", c.JID, err)
				st.barren++
				continue
			}

			st.lastAnchor = anchor.ID
			requested++

			// Space requests out; they are peer messages to the primary device.
			select {
			case <-ctx.Done():
				return
			case <-time.After(2 * time.Second):
			}
		}

		if requested == 0 {
			b.Logger.Infof("History backfill idle: no chat has more history to fetch")
		}

		// Slow down as the account goes quiet, but never stop entirely.
		if pause < 10*time.Minute {
			pause += 30 * time.Second
		}
	}
}

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

// historyLimitOverrides holds per-chat caps raised by an explicit request.
var historyLimitOverrides sync.Map // chatJID -> int

// effectiveHistoryLimit returns the per-chat cap, preferring an explicit
// override from a manual request. Zero means unlimited.
func (b *Bridge) effectiveHistoryLimit(chatJID string, base int) int {
	if v, ok := historyLimitOverrides.Load(chatJID); ok {
		if n, ok := v.(int); ok && n > base {
			return n
		}
	}
	return base
}

// ExtendHistoryLimit raises how many messages may be backfilled for one chat
// and immediately requests a page further back.
//
// This is the manual escape hatch from the default cap: a deliberate,
// user-initiated request for one conversation, rather than an automatic sweep
// over every chat.
func (b *Bridge) ExtendHistoryLimit(ctx context.Context, chatJID string, wantMessages, pageSize int) (int, error) {
	anchor, err := b.Store.OldestMessageInfo(chatJID)
	if err != nil {
		return 0, err
	}
	if err := b.RequestHistoryBefore(ctx, anchor, pageSize); err != nil {
		return 0, err
	}
	if wantMessages > 0 {
		historyLimitOverrides.Store(chatJID, wantMessages)
	}
	return wantMessages, nil
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

// BootstrapHistory recovers history for a database with no anchors.
//
// On-demand history walks BACKWARDS from a message we already hold, so an empty
// messages.db cannot start backfill at all. Two approaches were tried:
//
//  1. An anchor-less request (the proto's anchor fields are optional). Measured
//     against a real account: 145 chats requested, zero messages returned.
//     WhatsApp silently ignores it, so it is not usable.
//  2. Anchoring on message IDs recovered from whatsmeow's own store. whatsmeow
//     keeps per-message secrets in the session database (whatsapp.db), which
//     survives a messages.db wipe, and each row carries a real message ID. This
//     is the path used here.
//
// With no anchors available at all, the only way to repopulate is for messages
// to arrive naturally (new traffic) or to re-pair the device.
// BootstrapHistory gives every known conversation a starting point.
//
// A freshly reset database has no messages, and on-demand history can only
// continue from a message already held, so backfill would immediately report
// "no more history to fetch". This enumerates the chats WhatsApp already knows
// about (contacts and joined groups), asks for the latest messages in each, and
// thereby creates the anchors that BackfillHistory needs.
func (b *Bridge) BootstrapHistory(ctx context.Context, count int) {
	if b.Client == nil || b.Client.Store.ID == nil {
		b.Logger.Warnf("Skipping history bootstrap: not logged in")
		return
	}
	if !b.Client.IsConnected() {
		b.Logger.Warnf("Skipping history bootstrap: not connected")
		return
	}
	if count <= 0 {
		count = 50
	}

	sessionDB := filepath.Join(b.Store.Dir(), "whatsapp.db")
	anchors, err := store.LoadSurvivingAnchors(sessionDB)
	if err != nil {
		b.Logger.Warnf("No surviving message anchors found (%v); history can only refill from new traffic", err)
		return
	}

	ownUser := ""
	if own := b.Client.Store.ID; own != nil {
		ownUser = own.User
	}

	seen := map[string]bool{}
	requested := 0
	for _, a := range anchors {
		if ctx.Err() != nil {
			return
		}

		chatJID := a.ChatJID
		// Prefer the canonical chat so bootstrap and live traffic agree.
		if jid, perr := types.ParseJID(chatJID); perr == nil && jid.Server != types.GroupServer {
			chatJID = b.CanonicalChatJID(jid).String()
		}
		if seen[chatJID] {
			continue
		}
		seen[chatJID] = true

		// Only bootstrap chats that are still empty.
		if n, err := b.Store.MessageCount(chatJID); err == nil && n > 0 {
			continue
		}

		chat, perr := types.ParseJID(chatJID)
		if perr != nil {
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
			b.Logger.Warnf("Bootstrap history for %s failed: %v", chatJID, err)
			continue
		}
		b.Logger.Infof("Requested history before recovered anchor in %s", chatJID)
		requested++

		select {
		case <-ctx.Done():
			return
		case <-time.After(2 * time.Second):
		}
	}

	if requested == 0 {
		b.Logger.Infof("No empty chat had a recoverable anchor; nothing to bootstrap")
	} else {
		b.Logger.Infof("History bootstrap requested %d chat(s) from surviving anchors", requested)
	}
}
