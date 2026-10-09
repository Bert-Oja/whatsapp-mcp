package wabridge

import (
	"context"
	"fmt"
	"io"
	"os"
	"time"

	"github.com/mdp/qrterminal"
	"go.mau.fi/whatsmeow"
)

// PairingOptions controls how a first-time login is performed.
type PairingOptions struct {
	// Phone, when set, requests an 8-digit pairing code to type into WhatsApp
	// instead of scanning a QR code. Must be international format (no leading 0).
	Phone string
	// Out receives the QR code or pairing code. Defaults to os.Stdout.
	Out io.Writer
}

// ConnectResult reports how the client ended up connected.
type ConnectResult struct {
	Paired   bool   // true when this call completed a fresh pairing
	PairCode string // the 8-digit code, when phone pairing was used
}

// Connect establishes the WhatsApp connection.
//
// With an existing session it simply reconnects. Otherwise it pairs: either by
// printing a QR code to scan, or, when opts.Phone is set, by requesting an
// 8-digit pairing code.
func (b *Bridge) Connect(ctx context.Context, opts PairingOptions) (ConnectResult, error) {
	if opts.Out == nil {
		opts.Out = os.Stdout
	}

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

	requestCode := func() (string, error) {
		return b.Client.PairPhone(opts.Phone, true, whatsmeow.PairClientChrome, "Chrome (Linux)")
	}

	return b.runPairing(ctx, qrChan, opts, requestCode)
}

// runPairing drives the pairing exchange to completion.
//
// requestCode is only called in phone mode, and only once the first QR code has
// arrived: whatsmeow requires the websocket to be established before PairPhone
// is called, and pairing codes expire when the server stops sending QR codes
// (about 160 seconds), so the request happens as early as is safe.
func (b *Bridge) runPairing(ctx context.Context, qrChan <-chan whatsmeow.QRChannelItem, opts PairingOptions, requestCode func() (string, error)) (ConnectResult, error) {
	phoneMode := opts.Phone != ""
	requested := false

	if phoneMode {
		fmt.Fprintln(opts.Out, "\nWaiting for the pairing socket...")
	} else {
		fmt.Fprintln(opts.Out, "\nScan this QR code with your WhatsApp app (Settings > Linked devices):")
	}

	for {
		select {
		case <-ctx.Done():
			return ConnectResult{}, ctx.Err()
		case evt, ok := <-qrChan:
			if !ok {
				// The emitter closed without a success or error event.
				return ConnectResult{}, fmt.Errorf("pairing channel closed before pairing completed")
			}

			switch {
			case evt.Event == whatsmeow.QRChannelEventError:
				if evt.Error != nil {
					return ConnectResult{}, fmt.Errorf("pairing error: %w", evt.Error)
				}
				return ConnectResult{}, fmt.Errorf("pairing error")

			case evt.Event == "code":
				if !phoneMode {
					qrterminal.GenerateHalfBlock(evt.Code, qrterminal.L, opts.Out)
					continue
				}
				// Phone mode: the socket is ready, so request the code now.
				if requested {
					continue
				}
				requested = true
				code, err := requestCode()
				if err != nil {
					return ConnectResult{}, fmt.Errorf("failed to request pairing code: %w", err)
				}
				fmt.Fprintf(opts.Out, "\nEnter this code in WhatsApp > Linked devices > Link with phone number:\n\n    %s\n\n", code)
				result := ConnectResult{PairCode: code}
				paired, err := continuePairing(ctx, qrChan)
				if err != nil {
					return result, err
				}
				result.Paired = paired
				return result, nil

			case evt.Event == "success":
				fmt.Fprintln(opts.Out, "\nSuccessfully connected and authenticated!")
				return ConnectResult{Paired: true}, nil

			case evt.Event == "timeout":
				return ConnectResult{}, fmt.Errorf("timeout waiting for pairing")

			default:
				// Any other terminal event (err-unexpected-state, err-client-outdated,
				// err-scanned-without-multidevice, ...) is a failure.
				return ConnectResult{}, fmt.Errorf("pairing failed: %s", evt.Event)
			}
		}
	}
}

// continuePairing waits out the remainder of a phone-mode pairing. Further QR
// codes are ignored once the pairing code has been issued.
func continuePairing(ctx context.Context, qrChan <-chan whatsmeow.QRChannelItem) (bool, error) {
	for {
		select {
		case <-ctx.Done():
			return false, ctx.Err()
		case evt, ok := <-qrChan:
			if !ok {
				return false, fmt.Errorf("pairing channel closed before pairing completed")
			}
			switch {
			case evt.Event == whatsmeow.QRChannelEventError:
				if evt.Error != nil {
					return false, fmt.Errorf("pairing error: %w", evt.Error)
				}
				return false, fmt.Errorf("pairing error")
			case evt.Event == "success":
				return true, nil
			case evt.Event == "timeout":
				return false, fmt.Errorf("timeout waiting for pairing code to be entered")
			case evt.Event == "code":
				// Ignore; the user has the pairing code.
				continue
			default:
				return false, fmt.Errorf("pairing failed: %s", evt.Event)
			}
		}
	}
}

// WaitConnected blocks until the client reports a connection, or times out.
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
