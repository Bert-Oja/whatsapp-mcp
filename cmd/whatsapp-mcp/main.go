// Command whatsapp-mcp runs the WhatsApp bridge and exposes it to MCP clients.
//
// The bridge (whatsmeow) and the MCP tool surface live in a single process, so
// tool calls reach WhatsApp directly instead of over the old REST API.
package main

import (
	"context"
	"flag"
	"fmt"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/modelcontextprotocol/go-sdk/mcp"
	waLog "go.mau.fi/whatsmeow/util/log"

	"github.com/Bert-Oja/whatsapp-mcp/internal/mcpserver"
	"github.com/Bert-Oja/whatsapp-mcp/internal/tools"
	"github.com/Bert-Oja/whatsapp-mcp/internal/wabridge"
)

func main() {
	var (
		storeDir  = flag.String("store-dir", envOr("STORE_DIR", "store"), "directory holding whatsapp.db, messages.db and downloaded media")
		pairPhone = flag.String("pair-phone", os.Getenv("WA_PAIR_PHONE"), "phone number in international format to pair with an 8-digit code instead of a QR code")
		logLevel  = flag.String("log-level", envOr("WA_LOG_LEVEL", "INFO"), "whatsmeow log level (DEBUG, INFO, WARN, ERROR)")
		httpAddr  = flag.String("http", envOr("MCP_HTTP_ADDR", "127.0.0.1:8765"), "address for the MCP Streamable HTTP endpoint")
		mcpOnly   = flag.Bool("mcp-only", os.Getenv("MCP_ONLY") == "1", "serve MCP without connecting to WhatsApp (for inspecting the tool surface)")
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

	if !*mcpOnly {
		if _, err := bridge.Connect(ctx, wabridge.PairingOptions{Phone: *pairPhone}); err != nil {
			logger.Errorf("Failed to connect: %v", err)
			os.Exit(1)
		}
		if err := bridge.WaitConnected(30 * time.Second); err != nil {
			logger.Errorf("%v", err)
			os.Exit(1)
		}
		fmt.Println("\n✓ Connected to WhatsApp.")
	}

	// Build the MCP server over the local message history and the bridge.
	reader := tools.NewReader(bridge.Store.DB())
	mcpServer := mcpserver.New(reader, bridge)

	mux := http.NewServeMux()
	mux.Handle("/mcp", mcp.NewStreamableHTTPHandler(func(*http.Request) *mcp.Server {
		return mcpServer
	}, nil))
	mux.HandleFunc("/healthz", func(w http.ResponseWriter, r *http.Request) {
		if bridge.Client.IsConnected() {
			w.WriteHeader(http.StatusOK)
			fmt.Fprintln(w, "ok")
			return
		}
		w.WriteHeader(http.StatusServiceUnavailable)
		fmt.Fprintln(w, "not connected to WhatsApp")
	})

	httpServer := &http.Server{Addr: *httpAddr, Handler: mux}
	go func() {
		fmt.Printf("MCP server listening on http://%s/mcp\n", *httpAddr)
		if err := httpServer.ListenAndServe(); err != nil && err != http.ErrServerClosed {
			logger.Errorf("HTTP server error: %v", err)
			stop()
		}
	}()

	// A logged-out session cannot recover on its own: the linked device was
	// removed or expired, so a new QR scan is required. Exit non-zero so the
	// container restarts and prints a fresh pairing code instead of idling
	// with a dead session.
	if !*mcpOnly {
		select {
		case <-ctx.Done():
		case <-bridge.LoggedOut:
			logger.Warnf("Session ended; exiting so a new pairing can start")
		}
	} else {
		<-ctx.Done()
	}

	fmt.Println("\nShutting down...")
	shutdownCtx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	_ = httpServer.Shutdown(shutdownCtx)
}
