package wabridge

import (
	"testing"
	"time"

	"go.mau.fi/whatsmeow/types/events"
)

// TestLoggedOutChannelSignals verifies a LoggedOut event closes the channel
// exactly once, so a supervisor can react to an invalidated session.
func TestLoggedOutChannelSignals(t *testing.T) {
	b, err := Open(t.TempDir(), testLogger())
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	defer b.Close()

	select {
	case <-b.LoggedOut:
		t.Fatal("LoggedOut should not be closed before any event")
	default:
	}

	// Simulate the event twice; closing a channel twice would panic, so this
	// also covers the sync.Once guard.
	b.handleEvent(&events.LoggedOut{})
	b.handleEvent(&events.LoggedOut{})

	select {
	case <-b.LoggedOut:
		// expected
	case <-time.After(time.Second):
		t.Fatal("LoggedOut was not signalled")
	}
}
