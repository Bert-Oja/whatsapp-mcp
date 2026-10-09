// Command whatsapp-mcp runs the WhatsApp bridge and exposes it to MCP clients.
//
// Phase 1 wires up the bridge and the local stores. The MCP tool surface and
// the Streamable HTTP transport arrive in later phases.
package main

import (
	"context"
	"flag"
	"fmt"
	"os"
	"os/signal"
	"syscall"
	"time"

	waLog "go.mau.fi/whatsmeow/util/log"

	"github.com/Bert-Oja/whatsapp-mcp/internal/wabridge"
)

func main() {
	var (
		storeDir = flag.String("store-dir", envOr("STORE_DIR", "store"), "directory holding whatsapp.db, messages.db and downloaded media")
		pairPhone = flag.String("pair-phone", os.Getenv("WA_PAIR_PHONE"), "phone number in international format to pair with an 8-digit code instead of a QR code")
		logLevel  = flag.String("log-level", envOr("WA_LOG_LEVEL", "INFO"), "whatsmeow log level (DEBUG, INFO, WARN, ERROR)")
	)
	flag.Parse()

	logger := waLog.Stdout("Client", *logLevel, true)

	bridge, err := wabridge.Open(*storeDir, logger)
	if err != nil {
		logger.Errorf("Failed to open bridge: %v", err)
		os.Exit(1)
	}
	defer bridge.Close()

	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stop()

	if _, err := bridge.Connect(ctx, *pairPhone); err != nil {
		logger.Errorf("Failed to connect: %v", err)
		os.Exit(1)
	}

	if err := bridge.WaitConnected(30 * time.Second); err != nil {
		logger.Errorf("%v", err)
		os.Exit(1)
	}

	fmt.Println("\n✓ Connected to WhatsApp.")

	// Keep running until interrupted. The MCP server will be started here.
	<-ctx.Done()
	fmt.Println("\nDisconnecting...")
}
