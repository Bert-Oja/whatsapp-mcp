# WhatsApp MCP Server

A Model Context Protocol (MCP) server for WhatsApp, as a **single Go binary**.

Search and read your personal WhatsApp messages (including images, videos,
documents and audio), search contacts, and — if you opt in — send messages to
individuals or groups. It connects to your personal WhatsApp account through the
WhatsApp web multi-device API using [whatsmeow](https://github.com/tulir/whatsmeow).

Everything is stored locally in SQLite. Messages only leave the machine when an
agent reads them through a tool you control.

> **Caution:** as with many MCP servers, this is subject to
> [the lethal trifecta](https://simonwillison.net/2025/Jun/16/the-lethal-trifecta/).
> Project injection could lead to private data exfiltration. Bind it to
> loopback and keep writes off unless you need them.

This is a fork of [lharries/whatsapp-mcp](https://github.com/lharries/whatsapp-mcp)
that replaces the original Go-bridge + Python-server pair with one binary and a
container-friendly deployment.

## What changed from the original

| | Original | This fork |
|---|---|---|
| Process model | Go bridge + Python MCP server, run separately | One Go binary |
| Transport | bridge REST API + stdio | MCP over Streamable HTTP at `/mcp` |
| SQLite driver | `mattn/go-sqlite3` (needs CGO, glibc) | `modernc.org/sqlite` (pure Go, static) |
| Deployment | two processes, host toolchain | one hardened container |
| Writes | always available | **off by default** |
| History | whatever the initial sync gave | on-demand, per conversation |

The Python server and its parity oracle are gone. Its exact output is frozen in
`internal/tools/testdata/python_oracle.json` and still enforced by
`TestParityAgainstPythonServer`, so the tool contract did not change.

## Architecture

```
cmd/whatsapp-mcp/       flags, wiring, signal handling
internal/store/         messages.db: schema and queries
internal/wabridge/      whatsmeow client, pairing, history, media
internal/tools/         query and send logic behind the tools
internal/mcpserver/     MCP tool registration and write guard
```

One process does everything: it holds the WhatsApp session, writes messages to
SQLite, and serves MCP over HTTP. There is no bridge API and no second service.

### Data

Two SQLite databases under `STORE_DIR`:

- `whatsapp.db` — the linked-device session and WhatsApp's own state
  (contacts, LID map, app state). **Losing this means re-scanning the QR code.**
- `messages.db` — chats and messages, used by the read tools.

Both run in WAL mode with `busy_timeout`, which matters: whatsmeow touches the
session database from many goroutines, and the pure-Go driver defaults
`busy_timeout` to 0, where contention fails immediately and surfaces as message
decryption errors.

## Deploy

See [README.docker.md](README.docker.md) for the container setup, hardening, and
how to connect `pi`.

```bash
docker compose up -d          # first run prints a QR code to scan
docker compose logs -f
```

To pair with a code instead of a QR image:

```bash
WA_PAIR_PHONE=46700000000 docker compose up -d
```

Ports are published on the host loopback only (`127.0.0.1:8765`). This server
reads private messages, so it must never be reachable from the network.

## Writes are off by default

In read-only mode the sending tools are not registered at all — they are absent
from `tools/list`, and calling one fails. A second guard wraps the sender, so
even a future code path that bypassed registration could not send.

```bash
ALLOW_WRITES=true docker compose up -d
```

| | Tools exposed |
|---|---|
| Default | 10 read-only tools |
| `ALLOW_WRITES=true` | the 10 read tools plus the 3 send tools |

Every tool carries MCP annotations (`readOnlyHint`), so clients can make their
own permission decisions.

## MCP tools

Read (always available):

- `search_contacts` — find contacts by name or phone number
- `list_messages` — messages with optional filters and context
- `list_chats` — conversations, including groups and communities
- `get_chat` — one chat's details
- `get_direct_chat_by_contact` — the direct chat for a phone number
- `get_contact_chats` — chats involving a contact
- `get_last_interaction` — the most recent message with a contact
- `get_message_context` — messages around a specific one
- `download_media` — fetch media and return a local file path
- `get_older_messages` — request older messages for one chat

Write (`ALLOW_WRITES=true` only):

- `send_message`, `send_file`, `send_audio_message`

### Threads, groups and communities

`list_chats`, `get_chat` and `get_contact_chats` report:

- `kind` — `direct`, `group`, `community` or `broadcast`
- `message_count` — how many messages are stored locally
- `stored_from` / `stored_to` — the time range those messages cover

Communities are WhatsApp groups containing other groups; a sub-group is labelled
with its parent, so it is not mistaken for a standalone group.

### History is on demand

History is **never fetched automatically**. Connecting makes no history requests
at all.

Only the recent window arrives at pairing, so an agent that needs older context
should:

1. Find the conversation with `list_chats` and read `stored_from`.
2. Call `get_older_messages` with its `chat_jid`, up to 50 messages per call.
3. Re-read the chat: history arrives asynchronously.

Each call walks one page further back and reports the anchor it used, so a
repeated anchor means no older history is available.

**Groups need a starting message.** On-demand history walks backwards from a
message already held, and a group that has never delivered one has no anchor, so
its history cannot be requested. Group history accumulates from new messages
onward; sending one message to a group is enough to seed it.

### Media

Only media metadata is stored by default. `download_media` takes the
`message_id` and `chat_jid` shown alongside a media message, downloads it and
returns the file path.

`send_audio_message` sends a playable voice note. It needs `.ogg` Opus input;
with ffmpeg available other formats are converted automatically. The image
compiles a minimal ffmpeg (~17 MB total) rather than installing the distribution
build, which would add ~188 MB.

## Development

Go is not required on the host — Docker is the toolchain:

```bash
./dev.sh go build ./...
./dev.sh go test ./...
./dev.sh go vet ./...
```

The wrapper runs every command in the same pinned Go image the Dockerfile uses,
with cached module and build volumes.

## Troubleshooting

- **`err-client-outdated`** — the WhatsApp client version is rejected. Update
  `go.mau.fi/whatsmeow` in `go.mod` and rebuild; the pinned version matters.
- **Port unreachable but the container is healthy** — the app must listen on
  `0.0.0.0` *inside* the container (`MCP_HTTP_ADDR`). Docker forwards to the
  container interface, not its loopback.
- **Device limit reached** — remove a linked device in WhatsApp
  (Settings → Linked Devices), then retry.
- **Older messages missing** — expected. See "History is on demand" above; use
  `get_older_messages`.
- **Everything out of sync** — remove the volume (`docker compose down -v`) and
  pair again. Note that clearing only `messages.db` also loses every history
  anchor, so backfill cannot restart until messages arrive again.

## Licence and credit

Based on [lharries/whatsapp-mcp](https://github.com/lharries/whatsapp-mcp); see
[LICENSE](LICENSE). WhatsApp integration via
[whatsmeow](https://github.com/tulir/whatsmeow).
