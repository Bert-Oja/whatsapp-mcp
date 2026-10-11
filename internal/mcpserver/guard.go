package mcpserver

import (
	"context"
	"errors"

	"github.com/Bert-Oja/whatsapp-mcp/internal/tools"
)

// ErrWritesDisabled is returned when a send is attempted while the server is
// running in read-only mode.
var ErrWritesDisabled = errors.New("writes are disabled: start the server with writes enabled to send messages")

// guard wraps a tools.Sender and refuses every outbound operation unless writes
// are explicitly enabled.
//
// This is the second line of defence. The write tools are not registered in
// read-only mode, so a well-behaved client cannot even see them; this guard
// ensures that a mis-registered tool, or any future code path that bypasses the
// tool registry, still cannot send anything.
type guard struct {
	inner       tools.Sender
	allowWrites bool
}

func newGuard(inner tools.Sender, allowWrites bool) tools.Sender {
	if allowWrites {
		return inner
	}
	return &guard{inner: inner, allowWrites: false}
}

// SendMessage always fails in read-only mode, regardless of arguments.
func (g *guard) SendMessage(ctx context.Context, recipient, message, mediaPath string) (bool, string) {
	if !g.allowWrites {
		return false, ErrWritesDisabled.Error()
	}
	return g.inner.SendMessage(ctx, recipient, message, mediaPath)
}

// DownloadMedia is read-only and passes through.
func (g *guard) DownloadMedia(messageID, chatJID string) (bool, string, string, string, error) {
	return g.inner.DownloadMedia(messageID, chatJID)
}
