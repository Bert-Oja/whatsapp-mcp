package wabridge

import (
	"context"
	"testing"
	"time"

	waAdv "go.mau.fi/whatsmeow/proto/waAdv"
	"go.mau.fi/whatsmeow/types"
)

func contextBackground() context.Context { return context.Background() }

func timeNow() time.Time { return time.Now() }

// pairedBridge returns a Bridge whose device has been saved, which attaches the
// session-scoped stores (contacts, LID map) that only exist after pairing.
//
// Open alone leaves a device unpaired, so those stores are nil; tests that
// exercise name resolution need the paired shape.
func pairedBridge(t *testing.T) *Bridge {
	t.Helper()

	b, err := Open(t.TempDir(), testLogger())
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	t.Cleanup(b.Close)

	// Give the device an identity and persist it, mirroring a completed pairing.
	// Account details are required by PutDevice, and the schema enforces their
	// signature lengths (they arrive from the pairing handshake in real use).
	b.Client.Store.ID = &types.JID{User: "15550000000", Server: types.DefaultUserServer, Device: 17}
	b.Client.Store.Account = &waAdv.ADVSignedDeviceIdentity{
		Details:             []byte{0x01},
		AccountSignatureKey: make([]byte, 32),
		AccountSignature:    make([]byte, 64),
		DeviceSignature:     make([]byte, 64),
	}
	if err := b.Client.Store.Save(contextBackground()); err != nil {
		t.Fatalf("saving device: %v", err)
	}
	if b.Client.Store.LIDs == nil {
		t.Fatal("LID store still nil after saving the device")
	}
	if b.Client.Store.Contacts == nil {
		t.Fatal("contact store still nil after saving the device")
	}

	return b
}
