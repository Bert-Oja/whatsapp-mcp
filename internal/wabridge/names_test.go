package wabridge

import (
	"testing"

	"go.mau.fi/whatsmeow/types"
)

// TestIsPlaceholderName locks the rule that decides whether a stored chat name
// is worth replacing.
func TestIsPlaceholderName(t *testing.T) {
	lid := types.JID{User: "100000000000001", Server: types.HiddenUserServer}
	pn := types.JID{User: "15551234567", Server: types.DefaultUserServer}
	group := types.JID{User: "120363000000000000", Server: "g.us"}

	cases := []struct {
		name   string
		stored string
		jid    types.JID
		want   bool
	}{
		{"empty is a placeholder", "", lid, true},
		{"bare LID user is a placeholder", "100000000000001", lid, true},
		{"full LID string is a placeholder", "100000000000001@lid", lid, true},
		{"real contact name is not", "Alice Example", lid, false},
		{"bare number for a PN chat is a placeholder", "15551234567", pn, true},
		{"number with server is a placeholder", "15551234567@s.whatsapp.net", pn, true},
		{"group fallback is a placeholder", "Group 120363000000000000", group, true},
		{"real group name is not", "Weekend Plans", group, false},
		{"a name equal to another jid's user is not treated as placeholder for this jid", "Alice Example", pn, false},
	}

	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if got := isPlaceholderName(c.stored, c.jid); got != c.want {
				t.Errorf("isPlaceholderName(%q, %s) = %v, want %v", c.stored, c.jid, got, c.want)
			}
		})
	}
}

// TestResolveContactNameForLID covers the bug this fixes: a LID chat must be
// translated to its phone-number JID before the contact table is consulted.
func TestResolveContactNameForLID(t *testing.T) {
	b := pairedBridge(t)

	ctx := contextBackground()
	lid := types.JID{User: "100000000000001", Server: types.HiddenUserServer}
	pn := types.JID{User: "15551234567", Server: types.DefaultUserServer}

	// Register the LID -> phone number mapping, as WhatsApp does.
	if err := b.Client.Store.LIDs.PutLIDMapping(ctx, lid, pn); err != nil {
		t.Fatalf("PutLIDMapping: %v", err)
	}
	// Register the contact under its phone number.
	if err := b.Client.Store.Contacts.PutContactName(ctx, pn, "", "Alice Example"); err != nil {
		t.Fatalf("PutContactName: %v", err)
	}

	got, ok := b.resolveContactName(ctx, lid)
	if !ok {
		t.Fatal("resolveContactName returned no name for a mapped LID")
	}
	if got != "Alice Example" {
		t.Errorf("resolveContactName(lid) = %q, want %q", got, "Alice Example")
	}

	// Resolving directly by phone number must still work.
	gotPN, ok := b.resolveContactName(ctx, pn)
	if !ok || gotPN != "Alice Example" {
		t.Errorf("resolveContactName(pn) = %q, %v", gotPN, ok)
	}
}

// TestResolveContactNameUnmappedLID confirms an unknown LID fails cleanly
// rather than inventing a name.
func TestResolveContactNameUnmappedLID(t *testing.T) {
	b := pairedBridge(t)

	lid := types.JID{User: "999999999999999", Server: types.HiddenUserServer}
	if name, ok := b.resolveContactName(contextBackground(), lid); ok {
		t.Errorf("expected no name for an unmapped LID, got %q", name)
	}
}

// TestChatNameUsesLIDContactName is the end-to-end shape: a chat addressed by
// LID should be labelled with the contact's name, not the numeric ID.
func TestChatNameUsesLIDContactName(t *testing.T) {
	b := pairedBridge(t)

	ctx := contextBackground()
	lid := types.JID{User: "100000000000001", Server: types.HiddenUserServer}
	pn := types.JID{User: "15551234567", Server: types.DefaultUserServer}

	if err := b.Client.Store.LIDs.PutLIDMapping(ctx, lid, pn); err != nil {
		t.Fatalf("PutLIDMapping: %v", err)
	}
	if err := b.Client.Store.Contacts.PutContactName(ctx, pn, "", "Alice Example"); err != nil {
		t.Fatalf("PutContactName: %v", err)
	}

	got := b.ChatName(lid, lid.String(), nil, "")
	if got != "Alice Example" {
		t.Errorf("ChatName(lid) = %q, want %q", got, "Alice Example")
	}
}

// TestChatNameUpgradesStoredPlaceholder ensures an existing chat row holding a
// placeholder is re-resolved rather than returned as-is.
func TestChatNameUpgradesStoredPlaceholder(t *testing.T) {
	b := pairedBridge(t)

	ctx := contextBackground()
	lid := types.JID{User: "100000000000001", Server: types.HiddenUserServer}
	pn := types.JID{User: "15551234567", Server: types.DefaultUserServer}

	// The chat was stored earlier under its raw LID.
	if err := b.Store.StoreChat(lid.String(), lid.User, timeNow()); err != nil {
		t.Fatalf("StoreChat: %v", err)
	}
	if err := b.Client.Store.LIDs.PutLIDMapping(ctx, lid, pn); err != nil {
		t.Fatalf("PutLIDMapping: %v", err)
	}
	if err := b.Client.Store.Contacts.PutContactName(ctx, pn, "", "Alice Example"); err != nil {
		t.Fatalf("PutContactName: %v", err)
	}

	got := b.ChatName(lid, lid.String(), nil, "")
	if got != "Alice Example" {
		t.Errorf("ChatName did not upgrade the placeholder: got %q", got)
	}
}

// TestCommunitySuffixLabelsParentsAndSubGroups covers Community labelling.
func TestCommunitySuffixLabelsParentsAndSubGroups(t *testing.T) {
	// A Community itself.
	parent := &types.GroupInfo{JID: types.JID{User: "120363", Server: "g.us"}}
	parent.IsParent = true
	if got := communitySuffix(contextBackground(), nil, parent); got != " [Community]" {
		t.Errorf("parent suffix = %q, want %q", got, " [Community]")
	}

	// An ordinary group has no suffix.
	ordinary := &types.GroupInfo{}
	if got := communitySuffix(contextBackground(), nil, ordinary); got != "" {
		t.Errorf("ordinary group suffix = %q, want empty", got)
	}

	// A sub-group falls back to a generic label when the client is unavailable.
	sub := &types.GroupInfo{}
	sub.LinkedParentJID = types.JID{User: "120363", Server: "g.us"}
	got := communitySuffix(contextBackground(), nil, sub)
	if got == "" || got == " [Community]" {
		t.Errorf("sub-group suffix = %q, want a sub-group label", got)
	}

	// nil info must not panic.
	if got := communitySuffix(contextBackground(), nil, nil); got != "" {
		t.Errorf("nil info suffix = %q, want empty", got)
	}
}
