# Plan: whatsapp-mcp as a single-stack Go container

Status: accepted
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
on the host, and the goal is one efficient, self-contained deployment with the
smallest practical image.

## Decision

Collapse into **one Go binary in one container**:

- MCP tools live in the same process as the `whatsmeow` client, so tool calls
  that today traverse `http://localhost:8080/api` become in-process function
  calls. **The REST server is deleted** rather than containerized.
- The MCP layer is written in Go with the official
  `github.com/modelcontextprotocol/go-sdk` and served over **Streamable HTTP**
  (`mcp.NewStreamableHTTPHandler`), which Pi supports as a `url` MCP entry.
- **SQLite via `modernc.org/sqlite`** (pure Go) instead of `mattn/go-sqlite3`
  (CGO). No C compiler and no glibc at runtime: this yields a fully static
  binary and lets the final image be Alpine-based.
- **A minimal static `ffmpeg`** (libopus + ogg only) is built in a builder stage
  and copied into the final image. It is used **only for audio** — converting
  non-Opus audio to `.ogg` Opus in `send_audio_message`.
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

## Media handling (reference)

Media is passed through as raw bytes; nothing is transcoded except audio.

| Kind | Send | Receive/download | ffmpeg involved? |
|---|---|---|---|
| Image | `send_file` — jpeg, png, gif, webp | `download_media` | No |
| Video | `send_file` — mp4, avi, mov | `download_media` | No |
| Audio | `send_file` (raw), `send_audio_message` (voice note) | `download_media` | **Yes** — only for non-`.ogg` → Opus Ogg |
| Document | `send_file` — any other type | `download_media` | No |

Source of truth: `extractMediaInfo` and the `send_file` MIME mapping in
`whatsapp-bridge/main.go`, and `audio.py` in the Python server.

## Development without Go on the host

Docker **is** the toolchain. `pi` edits files on the host; every `go` invocation
runs in the pinned image:

```bash
# dev.sh — run any Go command in the pinned toolchain
docker run --rm \
  -v "$PWD:/src" -w /src \
  -v whatsapp-gomod:/go/pkg/mod \
  -v whatsapp-gobuild:/root/.cache/go-build \
  golang:1.24-alpine "$@"
```

```bash
./dev.sh go mod tidy
./dev.sh go vet ./...
./dev.sh go test ./...
./dev.sh go build -o /tmp/whatsapp-mcp ./cmd/whatsapp-mcp
```

Named cache volumes keep dependency downloads and build output between runs.
Host footprint is zero; the same image is used by CI and the Dockerfile, so
there is no toolchain drift. (Go is deliberately *not* installed on the host —
it would add ~250 MB plus a module cache for no benefit.)

## Tool parity contract

The 12 tools must keep their names, arguments, and output shape:

- Read: `search_contacts`, `list_messages`, `list_chats`, `get_chat`,
  `get_direct_chat_by_contact`, `get_contact_chats`, `get_last_interaction`,
  `get_message_context`.
- Write: `send_message`, `send_file`, `send_audio_message`, `download_media`.

The human-readable message formatting produced by `format_messages_list` in
`whatsapp.py` is part of the contract (the model reads it) and must be preserved.

## Architecture (target)

```
cmd/whatsapp-mcp/main.go        flags, wiring, signal handling
internal/store/                 SQLite open/schema/queries (modernc.org/sqlite)
internal/wabridge/              whatsmeow client, event handlers, history sync,
                                media download, opus conversion
internal/mcp/                   13 MCP tools bound with mcp.AddTool
Dockerfile                      multi-stage, static binary, non-root, minimal ffmpeg
compose.yaml                    single service, 127.0.0.1 port, named volume
dev.sh                          Docker-based Go toolchain wrapper
```

SQLite schema is unchanged (`chats`, `messages`), so existing `store/` data
migrates as-is.

## Phases and acceptance gates

| Phase | Work | Acceptance (must exit 0) |
|---|---|---|
| 0 | Branch, ADR | ADR committed on feature branch |
| 1 | Restructure into `cmd/` + `internal/`; delete REST server; store path configurable | `go vet ./...`, `go build ./...` via `dev.sh` |
| 2 | Port 12 tools to Go; port ffmpeg opus conversion | Table-driven tests vs. Python output on a fixture DB; in-memory MCP test lists 12 tools |
| 3 | Streamable HTTP transport on `127.0.0.1:8765`; QR in logs; optional `PairPhone` | `pi mcp list` connects, 12 tools; fresh volume prints scannable QR |
| 4 | Multi-stage Dockerfile + compose | `docker compose up -d` healthy; non-root; caps dropped |
| 5 | Pi `mcp.json` entry | read tool returns real data; send test lands |
| 6 | Smoke, persistence, parity diff, then remove Python | `restart` needs no re-auth; README documents Docker flow |

## Container shape

- **Builder stage**: `golang:1.24-alpine` — compiles the Go binary (static,
  `CGO_ENABLED=0`), and builds a minimal static `ffmpeg`/`ffprobe` with only
  libopus + ogg support.
- **Runtime stage**: `alpine:3` (≈13 MB) + the two copied binaries. No glibc, no
  X11, no VA-API, no SVT-AV1. Target total **well under 50 MB**.
- Non-root user; `STORE_DIR=/data`.
- `ports: ["127.0.0.1:8765:8765"]` — never exposed beyond loopback.
- Named volume `whatsapp-store:/data` holds `whatsapp.db` (WhatsApp session) and
  `messages.db`.
- Hardening: `cap_drop: [ALL]`, `no-new-privileges`, `read_only: true`,
  `tmpfs: /tmp` (ffmpeg scratch), memory limit, healthcheck.
- `restart: unless-stopped`.

## Why omitting glibc is safe here

glibc is the GNU C Library that non-static Linux programs link against. Alpine
uses musl instead, and the two are incompatible. The only component that would
have forced glibc was `mattn/go-sqlite3` (Go binding to the C SQLite). Choosing
pure-Go `modernc.org/sqlite` removes both the C compiler need and the runtime C
library need, so the binary is fully static and Alpine (or `scratch`) is
sufficient. The tradeoff is that pure-Go SQLite is somewhat slower on CPU-bound
queries, which is immaterial for personal message history.

## Security note

This server reads private messages and can send files — the "lethal trifecta"
warned about in the upstream README. The port stays bound to `127.0.0.1`, the
MCP entry stays at default `codemode` exposure in Pi, and the container is
unprivileged.

## Risks

1. **`modernc.org/sqlite` + `whatsmeow` `sqlstore` compatibility** — must confirm
   the dialect string whatsmeow expects (likely `sqlite` rather than `sqlite3`).
   If incompatible, fall back to CGO + a glibc runtime base.
2. **Static ffmpeg build** — must produce a working libopus encoder without
   pulling the full dependency tree; verified by running a real conversion in the
   Phase 4 image test.
3. **Schema drift** — if upstream evolves the DB schema, the Go port must track
   it; mitigated by the parity test.
4. **Fork divergence** — this is a substantial fork of `lharries/whatsapp-mcp`;
   the Docker support could later be offered upstream as a PR.

## Phase 3 notes

- The MCP endpoint is `/mcp` (Streamable HTTP); `/healthz` returns 200 when
  connected to WhatsApp and 503 otherwise, so a container healthcheck reflects
  real session state rather than just process liveness.
- Pairing supports both modes: QR printed to the log stream, or `-pair-phone`
  for an 8-digit code. In phone mode the code is requested only after the first
  QR event, because whatsmeow requires the websocket to be established first
  and pairing codes expire once the server stops issuing QR codes.
- Message-reading tools return a single plain-text content block. The SDK
  otherwise appends a JSON-encoded duplicate when the output type is not an
  object, which would have changed what the model sees.
- A `LoggedOut` event closes `Bridge.LoggedOut`, so a headless deployment can
  exit and be restarted to print a fresh pairing code instead of idling with an
  invalidated session.

## Read-only mode

Writes are opt-in and **disabled by default**. This is enforced in two
independent places so that a single mistake cannot expose sending:

1. **Tool registration.** In read-only mode the `send_message`, `send_file` and
   `send_audio_message` tools are never registered, so they do not appear in
   `tools/list` and a call to them fails with `unknown tool`.
2. **Sender guard.** The `tools.Sender` handed to the MCP layer is wrapped in a
   guard that refuses every `SendMessage` call when writes are disabled. This
   covers any future code path that bypasses the tool registry.

Every tool carries MCP annotations: the read tools advertise
`readOnlyHint: true`, and the write tools advertise `readOnlyHint: false` with
`destructiveHint`, so clients can make their own permission decisions.

Enable writes with `-allow-writes` or `ALLOW_WRITES=1` (Compose:
`ALLOW_WRITES=true`). An unparseable value falls back to the safe default.

Verified end to end against the running binary: read-only exposes 9 tools, all
annotated `readOnlyHint`, and `send_message` returns `unknown tool`; with
`-allow-writes` all 12 are exposed.

## Phase 4 notes

### ffmpeg is compiled from source

`send_audio_message` is the only consumer of ffmpeg. The available options were
measured rather than estimated:

| Option | Image size |
|---|---|
| `apk add ffmpeg` | 201 MB (123 packages, X11/VA-API/SVT-AV1 and friends) |
| prebuilt static ffmpeg | 414 MB (the `ffmpeg` binary alone is 144 MB) |
| **compiled encoder-only build** | **~17 MB** |

The Dockerfile compiles ffmpeg 7.1 with libopus and only the demuxers/decoders
needed to read common attachments (wav, mp3, m4a/aac, flac, ogg/opus, webm),
then strips it to ~2.7 MB. The exact invocation used by `audio.go` was verified
to produce a valid `OggS`/`OpusHead` stream.

### Runtime image

Final image is ~45 MB on `alpine:3.21`: a static CGO-free Go binary plus the
minimal ffmpeg. Runs as uid 10001 with a read-only root filesystem, a 64 MB
`/tmp` tmpfs for ffmpeg scratch space, all capabilities dropped, a 512 MB
memory limit, and the port published to loopback only.

### The pinned whatsmeow was too old to connect

The first container run failed with `err-client-outdated` (HTTP 405): WhatsApp
rejected the client version from the March 2025 whatsmeow build. The library
and the Go toolchain were both updated (whatsmeow to 2026-10, Go to 1.26,
required by both the library and the MCP SDK). The newer whatsmeow takes a
`context.Context` on most client calls; those call sites were updated.

This is the reason the Phase 3 event handling matters in practice: the failure
was reported clearly instead of hanging until the pairing timeout.

### Pairing precedes listening

The MCP endpoint starts only after WhatsApp connects. Before pairing, an MCP
client cannot connect and `/healthz` reports 503 for liveness purposes. This is
deliberate: exposing tools that cannot reach WhatsApp would be misleading.
