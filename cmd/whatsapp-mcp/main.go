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
		storeDir       = flag.String("store-dir", envOr("STORE_DIR", "store"), "directory holding whatsapp.db, messages.db and downloaded media")
		pairPhone      = flag.String("pair-phone", os.Getenv("WA_PAIR_PHONE"), "phone number in international format to pair with an 8-digit code instead of a QR code")
		logLevel       = flag.String("log-level", envOr("WA_LOG_LEVEL", "INFO"), "whatsmeow log level (DEBUG, INFO, WARN, ERROR)")
		httpAddr       = flag.String("http", envOr("MCP_HTTP_ADDR", "127.0.0.1:8765"), "address for the MCP Streamable HTTP endpoint")
		mcpOnly        = flag.Bool("mcp-only", os.Getenv("MCP_ONLY") == "1", "serve MCP without connecting to WhatsApp (for inspecting the tool surface)")
		allowWrite     = flag.Bool("allow-writes", envBool("ALLOW_WRITES", false), "expose the send_message, send_file and send_audio_message tools (default: read-only)")
		requestHistory = flag.Bool("request-history", envBool("REQUEST_HISTORY", false), "walk history backwards for known chats on connect (uses an unofficial endpoint)")
		historyLimit   = flag.Int("history-limit", envInt("HISTORY_LIMIT", 200), "max messages to backfill per chat (0 = unlimited); use extend_history to go deeper in one chat")
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

		// The same conversation can be stored twice: live messages arrive
		// addressed by LID while history uses the phone number. Merge those so a
		// whole-account view sees each conversation once.
		go func() {
			// Wait for the LID mappings that arrive with app state.
			select {
			case <-ctx.Done():
				return
			case <-time.After(45 * time.Second):
			}
			bridge.CanonicalizeChats(ctx)
		}()

		// Contact details often arrive after a chat is first seen, so chats can
		// initially be stored under a placeholder name (a LID or bare number).
		// Refresh those periodically once contacts have synced.
		go bridge.RefreshPlaceholderNames(ctx)

		// On-demand history can only continue from a message we already hold, so
		// a freshly reset database has nothing to anchor on. Bootstrap asks for
		// the latest messages in every known chat first, which creates those
		// anchors; backfill then walks each chat further back.
		if *requestHistory {
			go func() {
				bridge.BootstrapHistory(ctx, 50)
				bridge.BackfillHistory(ctx, 50, *historyLimit)
			}()
		}
	}

	// Build the MCP server over the local message history and the bridge.
	// Writes are opt-in: without -allow-writes the sending tools are neither
	// registered nor reachable.
	reader := tools.NewReader(bridge.Store.DB())
	mcpServer := mcpserver.New(reader, bridge, mcpserver.Options{AllowWrites: *allowWrite})

	if *allowWrite {
		logger.Warnf("Writes are ENABLED: the send_message, send_file and send_audio_message tools are exposed")
	} else {
		logger.Infof("Read-only mode: sending tools are not exposed (start with -allow-writes to enable)")
	}

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
