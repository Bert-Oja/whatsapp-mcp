package wabridge

import (
	"bytes"
	"strings"
	"testing"

	"github.com/mdp/qrterminal"
)

// TestQRRendersInLogs verifies a QR code sent to a plain writer (as it reaches
// `docker compose logs`) produces a scanable half-block rendering rather than
// empty or unreadable output.
func TestQRRendersInLogs(t *testing.T) {
	var out bytes.Buffer
	// A realistic WhatsApp QR payload length.
	qrterminal.GenerateHalfBlock("2@abcDEF1234567890xyz/+/=longpayloaddigits0123456789", qrterminal.L, &out)

	rendered := out.String()
	if rendered == "" {
		t.Fatal("QR rendering produced no output")
	}

	// Half-block rendering uses these characters; without them the code is not
	// scannable and the log output would be useless.
	if !strings.ContainsAny(rendered, "▀▄█") {
		t.Fatalf("QR output lacks block characters, not scannable:\n%s", rendered)
	}

	// It must be multi-line: a single line would indicate it collapsed.
	if strings.Count(rendered, "\n") < 10 {
		t.Fatalf("QR output has only %d lines, too small to scan", strings.Count(rendered, "\n"))
	}

	t.Logf("QR rendering: %d bytes, %d lines", len(rendered), strings.Count(rendered, "\n"))
}
