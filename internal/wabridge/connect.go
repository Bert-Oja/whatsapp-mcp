package wabridge

import (
	"context"
	"fmt"
	"os"
	"time"

	"github.com/mdp/qrterminal"
	"go.mau.fi/whatsmeow"
)

// ConnectResult reports how the client ended up connected.
type ConnectResult struct {
	Paired   bool // true when this call completed a fresh pairing
	PairCode string
}

// Connect establishes the WhatsApp connection.
//
// With no stored session it either prints a QR code for scanning (default) or,
// when phone is set, requests an 8-digit pairing code that the user types into
// WhatsApp on their phone (Settings > Linked devices > Link with phone number).
func (b *Bridge) Connect(ctx context.Context, phone string) (ConnectResult, error) {
	if b.Client.Store.ID != nil {
		// Existing session: just reconnect.
		if err := b.Client.Connect(); err != nil {
			return ConnectResult{}, fmt.Errorf("failed to connect: %w", err)
		}
		return ConnectResult{}, nil
	}

	qrChan, err := b.Client.GetQRChannel(ctx)
	if err != nil {
		return ConnectResult{}, fmt.Errorf("failed to get QR channel: %w", err)
	}

	if err := b.Client.Connect(); err != nil {
		return ConnectResult{}, fmt.Errorf("failed to connect: %w", err)
	}

	if phone != "" {
		// Must be called after Connect; the QR channel stays idle meanwhile.
		code, err := b.Client.PairPhone(phone, true, whatsmeow.PairClientChrome, "Chrome (Linux)")
		if err != nil {
			return ConnectResult{}, fmt.Errorf("failed to request pairing code: %w", err)
		}
		fmt.Printf("\nEnter this code in WhatsApp > Linked devices > Link with phone number:\n\n    %s\n\n", code)
		return ConnectResult{Paired: true, PairCode: code}, b.waitForPairing(ctx, qrChan)
	}

	fmt.Println("\nScan this QR code with your WhatsApp app (Settings > Linked devices):")

	for {
		select {
		case <-ctx.Done():
			return ConnectResult{}, ctx.Err()
		case evt, ok := <-qrChan:
			if !ok {
				return ConnectResult{Paired: true}, nil
			}
			switch evt.Event {
			case "code":
				qrterminal.GenerateHalfBlock(evt.Code, qrterminal.L, os.Stdout)
			case "success":
				fmt.Println("\nSuccessfully connected and authenticated!")
				return ConnectResult{Paired: true}, nil
			case "timeout":
				return ConnectResult{}, fmt.Errorf("timeout waiting for QR scan")
			}
		}
	}
}

// waitForPairing waits for the pairing-code flow to complete.
func (b *Bridge) waitForPairing(ctx context.Context, qrChan <-chan whatsmeow.QRChannelItem) error {
	for {
		select {
		case <-ctx.Done():
			return ctx.Err()
		case evt, ok := <-qrChan:
			if !ok {
				return nil
			}
			switch evt.Event {
			case "success":
				fmt.Println("\nSuccessfully paired!")
				return nil
			case "timeout":
				return fmt.Errorf("timeout waiting for pairing code to be entered")
			}
		}
	}
}

// WaitConnected blocks briefly for the connection to stabilise.
func (b *Bridge) WaitConnected(timeout time.Duration) error {
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		if b.Client.IsConnected() {
			return nil
		}
		time.Sleep(250 * time.Millisecond)
	}
	return fmt.Errorf("failed to establish stable connection")
}
