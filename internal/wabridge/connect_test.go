package wabridge

import (
	"bytes"
	"context"
	"strings"
	"testing"
	"time"

	"go.mau.fi/whatsmeow"
)

// feed returns a channel preloaded with items, then closed. The channel is big
// enough that no goroutine is needed.
func feed(items ...whatsmeow.QRChannelItem) <-chan whatsmeow.QRChannelItem {
	ch := make(chan whatsmeow.QRChannelItem, len(items)+1)
	for _, it := range items {
		ch <- it
	}
	return ch
}

// TestPairingQRSuccess prints QR codes and finishes on success.
func TestPairingQRSuccess(t *testing.T) {
	var out bytes.Buffer
	b := &Bridge{Logger: testLogger()}
	requested := false

	res, err := b.runPairing(context.Background(),
		feed(
			whatsmeow.QRChannelItem{Event: "code", Code: "qr-code-1"},
			whatsmeow.QRChannelItem{Event: "success"},
		),
		PairingOptions{Out: &out},
		func() (string, error) { requested = true; return "", nil },
	)
	if err != nil {
		t.Fatalf("runPairing: %v", err)
	}
	if !res.Paired {
		t.Fatal("expected Paired=true")
	}
	if requested {
		t.Fatal("requestCode must not be called in QR mode")
	}
	if !strings.Contains(out.String(), "Scan this QR code") {
		t.Fatalf("QR instructions missing from output: %q", out.String())
	}
}

// TestPairingPhoneModeRequestsCodeAfterFirstQR is the important sequencing
// contract: whatsmeow requires the websocket to be established before
// PairPhone, which is signalled by the first QR event.
func TestPairingPhoneModeRequestsCodeAfterFirstQR(t *testing.T) {
	var out bytes.Buffer
	b := &Bridge{Logger: testLogger()}
	called := 0

	res, err := b.runPairing(context.Background(),
		feed(
			whatsmeow.QRChannelItem{Event: "code", Code: "qr-code-1"},
			whatsmeow.QRChannelItem{Event: "success"},
		),
		PairingOptions{Out: &out, Phone: "15551234567"},
		func() (string, error) { called++; return "ABCD-1234", nil },
	)
	if err != nil {
		t.Fatalf("runPairing: %v", err)
	}
	if called != 1 {
		t.Fatalf("requestCode called %d times, want 1", called)
	}
	if res.PairCode != "ABCD-1234" {
		t.Fatalf("PairCode = %q", res.PairCode)
	}
	if !res.Paired {
		t.Fatal("expected Paired=true")
	}
	if !strings.Contains(out.String(), "ABCD-1234") {
		t.Fatalf("pairing code missing from output: %q", out.String())
	}
	// The QR itself should not be drawn in phone mode.
	if strings.Contains(out.String(), "Scan this QR code") {
		t.Fatalf("QR instructions should not appear in phone mode: %q", out.String())
	}
}

// TestPairingPhoneModeIgnoresExtraQRCodes ensures subsequent QR codes do not
// trigger repeated PairPhone calls.
func TestPairingPhoneModeIgnoresExtraQRCodes(t *testing.T) {
	var out bytes.Buffer
	b := &Bridge{Logger: testLogger()}
	called := 0

	_, err := b.runPairing(context.Background(),
		feed(
			whatsmeow.QRChannelItem{Event: "code", Code: "qr-1"},
			whatsmeow.QRChannelItem{Event: "code", Code: "qr-2"},
			whatsmeow.QRChannelItem{Event: "code", Code: "qr-3"},
			whatsmeow.QRChannelItem{Event: "success"},
		),
		PairingOptions{Out: &out, Phone: "15551234567"},
		func() (string, error) { called++; return "WXYZ-9999", nil },
	)
	if err != nil {
		t.Fatalf("runPairing: %v", err)
	}
	if called != 1 {
		t.Fatalf("requestCode called %d times, want exactly 1", called)
	}
}

// TestPairingErrorEvent checks an error event is surfaced instead of hanging.
func TestPairingErrorEvent(t *testing.T) {
	var out bytes.Buffer
	b := &Bridge{Logger: testLogger()}

	_, err := b.runPairing(context.Background(),
		feed(whatsmeow.QRChannelItem{Event: whatsmeow.QRChannelEventError, Error: context.DeadlineExceeded}),
		PairingOptions{Out: &out},
		nil,
	)
	if err == nil {
		t.Fatal("expected an error")
	}
	if !strings.Contains(err.Error(), "pairing error") {
		t.Fatalf("unexpected error: %v", err)
	}
}

// TestPairingTerminalErrEvents covers the err-* terminal events.
func TestPairingTerminalErrEvents(t *testing.T) {
	for _, event := range []string{
		"err-unexpected-state",
		"err-client-outdated",
		"err-scanned-without-multidevice",
	} {
		t.Run(event, func(t *testing.T) {
			b := &Bridge{Logger: testLogger()}
			_, err := b.runPairing(context.Background(),
				feed(whatsmeow.QRChannelItem{Event: event}),
				PairingOptions{Out: &bytes.Buffer{}},
				nil,
			)
			if err == nil {
				t.Fatalf("%s: expected an error", event)
			}
			if !strings.Contains(err.Error(), event) {
				t.Fatalf("%s: error did not name the event: %v", event, err)
			}
		})
	}
}

// TestPairingTimeout checks the timeout event is reported.
func TestPairingTimeout(t *testing.T) {
	b := &Bridge{Logger: testLogger()}
	_, err := b.runPairing(context.Background(),
		feed(whatsmeow.QRChannelItem{Event: "timeout"}),
		PairingOptions{Out: &bytes.Buffer{}},
		nil,
	)
	if err == nil || !strings.Contains(err.Error(), "timeout") {
		t.Fatalf("expected timeout error, got %v", err)
	}
}

// TestPairingChannelClosed checks a silently-closed channel is not treated as
// success.
func TestPairingChannelClosed(t *testing.T) {
	b := &Bridge{Logger: testLogger()}
	ch := make(chan whatsmeow.QRChannelItem)
	close(ch)

	_, err := b.runPairing(context.Background(), ch, PairingOptions{Out: &bytes.Buffer{}}, nil)
	if err == nil {
		t.Fatal("expected an error for a channel closed without success")
	}
}

// TestPairingContextCancelled checks cancellation unblocks the loop.
func TestPairingContextCancelled(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	b := &Bridge{Logger: testLogger()}
	ch := make(chan whatsmeow.QRChannelItem) // never sends

	done := make(chan error, 1)
	go func() {
		_, err := b.runPairing(ctx, ch, PairingOptions{Out: &bytes.Buffer{}}, nil)
		done <- err
	}()

	select {
	case err := <-done:
		if err == nil {
			t.Fatal("expected context error")
		}
	case <-time.After(2 * time.Second):
		t.Fatal("runPairing did not return after context cancellation")
	}
}
