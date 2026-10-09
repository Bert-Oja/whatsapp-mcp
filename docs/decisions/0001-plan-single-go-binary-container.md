# Plan: whatsapp-mcp as a single-stack Go container

Status: proposed
Date: 2026-04-18
Branch: `feature/docker-single-stack`

## Context

`whatsapp-mcp` today is two processes in two stacks:

- `whatsapp-bridge/` — Go + `whatsmeow`. Connects to WhatsApp, prints a QR code
  for first-time pairing, syncs chat history into SQLite under `store/`, and
  serves an unauthenticated REST API on `:8080` (`/api/send`, `/api/download`).
- `whatsapp-mcp-server/` — Python + FastMCP, run through `uv`. Exposes 13 MCP
  tools over stdio. It reads `whatsapp-bridge/store/messages.db` **directly** for
  all read/search/context tools and calls `http://localhost:8080/api` for sends
  and media download. `audio.py` shells out to `ffmpeg`.

We want to run this on a rootless-Docker VPS. Neither Go nor `uv` is installed
on the host, and the goal is one efficient, self-contained deployment.

## Decision

Collapse into **one Go binary in one container**:

- MCP tools live in the same process as the `whatsmeow` client, so tool calls
  that today traverse `http://localhost:8080/api` become in-process function
  calls. **The REST server is deleted** rather than containerized.
- The MCP layer is written in Go with the official
  `github.com/modelcontextprotocol/go-sdk` and served over **Streamable HTTP**
  (`mcp.NewStreamableHTTPHandler`), which Pi supports as a `url` MCP entry.
- **SQLite via `modernc.org/sqlite`** (pure Go) instead of `mattn/go-sqlite3`
  (CGO). This yields a static binary and a small final image with no glibc.
- **`ffmpeg` is bundled** so `send_audio_message` keeps full feature parity
  (mp3/wav/etc. converted to Opus Ogg).
- The **Python MCP server is removed** once Go parity is verified against a
  fixture database.

### Why Go over the Node/Baileys alternative

`whatsmeow` is the established, stable WhatsApp Web multidevice implementation;
community reports consistently rate it as more stable and lighter than the Node
`Baileys` alternative, particularly around session/auth persistence. Rewriting
the bridge in TypeScript would replace the one component that already works.

### Why in-process over "containerize both as-is"

Keeping the REST hop would mean shipping an unauthenticated network surface with
no benefit: both halves would still run in the same container/volume scope. An
in-process call removes ~150 lines of HTTP plumbing, a port, and an attack
surface.

## Architecture (target)

```
cmd/whatsapp-mcp/main.go        flags, wiring, signal handling
internal/store/                 SQLite open/schema/queries (modernc.org/sqlite)
internal/wabridge/              whatsmeow client, event handlers, history sync,
                                media download, opus conversion
internal/mcp/                   13 MCP tools bound with mcp.AddTool
Dockerfile                      multi-stage, static binary, non-root, ffmpeg
compose.yaml                    single service, 127.0.0.1 port, named volume
```

SQLite schema is unchanged (`chats`, `messages`), so existing `store/` data
migrates as-is.

## Tool parity contract

The 13 tools must keep their names, arguments, and output shape:

- Read: `search_contacts`, `list_messages`, `list_chats`, `get_chat`,
  `get_direct_chat_by_contact`, `get_contact_chats`, `get_last_interaction`,
  `get_message_context`.
- Write: `send_message`, `send_file`, `send_audio_message`, `download_media`.

The human-readable message formatting produced by `format_messages_list` in
`whatsapp.py` is part of the contract (the model reads it) and must be preserved.

## Phases and acceptance gates

| Phase | Work | Acceptance (must exit 0) |
|---|---|---|
| 0 | Branch, ADR | ADR committed on feature branch |
| 1 | Restructure into `cmd/` + `internal/`; delete REST server; store path configurable | `go vet ./...`, `go build ./...` (in `golang:1.24` container) |
| 2 | Port 13 tools to Go; port ffmpeg opus conversion | Table-driven tests vs. Python output on a fixture DB; in-memory MCP test lists 13 tools |
| 3 | Streamable HTTP transport on `127.0.0.1:8765`; QR in logs; optional `PairPhone` | `pi mcp list` connects, 13 tools; fresh volume prints scannable QR |
| 4 | Multi-stage Dockerfile + compose | `docker compose up -d` healthy; non-root; caps dropped |
| 5 | Pi `mcp.json` entry | read tool returns real data; send test lands |
| 6 | Smoke, persistence, parity diff, then remove Python | `restart` needs no re-auth; README documents Docker flow |

## Container shape

- Multi-stage: `golang:1.24-bookworm` build → `debian:bookworm-slim` (or
  `distroless/cc`; if the pure-Go driver makes it fully static, `static-debian`
  or `scratch` is enough) + `ffmpeg`, non-root user, `STORE_DIR=/data`.
- `ports: ["127.0.0.1:8765:8765"]` — never exposed beyond loopback.
- Named volume `whatsapp-store:/data` holds `whatsapp.db` (WhatsApp session) and
  `messages.db`.
- Hardening: `cap_drop: [ALL]`, `no-new-privileges`, `read_only: true`,
  `tmpfs: /tmp` (ffmpeg scratch), memory limit, healthcheck.
- `restart: unless-stopped`.

## Security note

This server reads private messages and can send files — the "lethal trifecta"
warned about in the upstream README. The port stays bound to `127.0.0.1`, the
MCP entry stays at default `codemode` exposure in Pi, and the container is
unprivileged.

## Risks

1. **`modernc.org/sqlite` + `whatsmeow` `sqlstore` compatibility** — must confirm
   the dialect string whatsmeow expects (likely `sqlite` rather than `sqlite3`).
   If incompatible, fall back to CGO + `distroless/cc`.
2. **Schema drift** — if upstream evolves the DB schema, the Go port must track
   it; mitigated by the parity test.
3. **Fork divergence** — this is a substantial fork of `lharries/whatsapp-mcp`;
   the Docker support could later be offered upstream as a PR.
4. **Image size** — `ffmpeg` adds roughly 70 MB; accepted for feature parity.

## Open items deferred to implementation

- Healthcheck endpoint for the Streamable HTTP server (a trivial `/healthz` on
  the same listener).
- Whether to default to QR-in-logs or `PairPhone` for first-time pairing.
