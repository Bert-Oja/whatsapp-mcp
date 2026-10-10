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

## Phase 5 notes: SQLITE_BUSY regression

After pairing, the live container logged repeated failures:

```
[Client WARN] Failed to remove expired tctokens from DB: database is locked (5) (SQLITE_BUSY)
[Client WARN] Error decrypting message AC49... from 138...@lid: failed to decrypt prekey
              message: failed to load session with 138..._1:0: database is locked (5)
[Client ERROR] Failed to sync app state after notification: ... mismatching LTHash
```

These were caused by the Phase 1 driver swap. `mattn/go-sqlite3` sets
`PRAGMA busy_timeout = 5000` by default and opens with its own defaults;
`modernc.org/sqlite` defaults `busy_timeout` to 0, so any contention fails
immediately. whatsmeow accesses the session database from many goroutines, so
this fired constantly: failed session lookups produce message decryption
errors, and the failed writes are what produced the downstream
`mismatching LTHash` app-state errors.

Measured directly (20 goroutines writing):

| DSN | busy_timeout | concurrent write failures |
|---|---|---|
| `_pragma=foreign_keys(1)` only | 0 | **19 of 20** |
| `_pragma=busy_timeout(5000)&_pragma=journal_mode(WAL)&_pragma=foreign_keys(1)` | 5000 | **0** |

Both the session and message databases now use the full DSN. After rebuilding
and recreating the container, the log is clean: no `SQLITE_BUSY` and no
`mismatching LTHash`, with the existing session restored without re-pairing.

A regression test (`TestConcurrentWritesDoNotFail`) reproduces the contention
in-process, and `TestDSNHasRequiredPragmas` locks the required pragmas.

## Phase 6 notes: LID names and on-demand history

### Chats were labelled with raw LIDs

WhatsApp increasingly addresses users by LID (`100000000000001@lid`). The
contact table is keyed by phone-number JIDs, so looking a LID up directly
always missed and chats were stored under their numeric ID. Verified against
the live database: `whatsmeow_contacts` held `15551234567@s.whatsapp.net ->
Alice Example` and `whatsmeow_lid_map` held `100000000000001 -> 15551234567`, but
the chat was stored as `100000000000001`.

`ChatName` now translates LID to phone number through `Store.LIDs.GetPNForLID`
before consulting contacts. Names already stored as placeholders (a bare LID,
number, JID, or `Group <id>`) are re-resolved by a periodic refresh, because
contact details often arrive after a chat is first seen.

Confirmed live: `Resolved chat name 100000000000001@lid -> Alice Example`.

### On-demand history requires an anchor

The upstream `requestHistorySync` was never called, and wiring it up as written
panicked: `BuildHistorySyncRequest(nil, 100)` nil-dereferences because the API
needs a real `MessageInfo` to anchor on. It also must be sent with
`SendPeerMessage`, not `SendMessage`.

The real semantics are narrower than the name suggests: it fetches `count`
messages *before* a given message, one chat at a time, and the response arrives
as `events.HistorySync` with type `ON_DEMAND`. Implemented as `RequestHistoryBefore`
(anchor-based) and `RequestHistoryForKnownChats` (walks the oldest stored
message per chat), behind `-request-history` / `REQUEST_HISTORY`, default off.

Confirmed live: WhatsApp returned a history sync event and 46 messages were
stored for a chat that previously had one.

### Known issue: LID and phone-number chats are not merged

Enabling history surfaced a problem. The same conversation now exists twice:
`100000000000001@lid` (the LID-addressed chat) and
`15551234567@s.whatsapp.net` (the phone-number-addressed chat introduced by the
history response). Both resolve to the name "Alice Example" but hold different
messages (0 shared message IDs), so a search over the account sees the
conversation split across two rows.

This is not yet fixed. It is likely to need either canonicalising chats onto
one identifier, or merging on read (for example resolving every chat JID to a
canonical PN where a mapping exists), and should be resolved before the
read tools are considered trustworthy for a whole-account view.

## Phase 7: history backfill, rate limits and ToS risk

### How much history is reachable

Two mechanisms exist, and both come from the linked-device protocol, so they
carry the same limits WhatsApp Web has:

- The initial sync after pairing returns only a recent window (roughly the
  most-recently-active chats, ~1000 messages on a large account).
- On-demand history walks BACKWARDS from a message already held, one page at a
  time (50 is the recommended page size). Confirmed working here: repeated
  requests extended one chat from 46 to 418+ messages, reaching back weeks.

Practical consequence: a database reset cannot restart backfill, because there
is no message to anchor on. Measured against a real account:

- **Anchor-less request** (the proto's anchor fields are optional): 145 chats
  requested, **zero messages returned**. WhatsApp silently ignores it.
- **Recovered anchors**: whatsmeow keeps per-message secrets in the session
  database (`whatsapp.db`), which survives a `messages.db` wipe, and each row
  carries a real message ID. Anchoring on those returned history, recovering
  185+ messages into an empty database. This is the bootstrap path used.

Even with anchors, WhatsApp Web itself stops for old chats and tells the user
to check their phone, so "full history" is bounded by that behaviour, not by
our loop.

### Rate limits and ToS risk

This is the honest risk assessment, from upstream issues and the mature
implementations:

- Using an unofficial client is itself a ToS violation: WhatsApp's terms
  forbid reverse-engineered clients and bulk/automated messaging. Action can
  range from a warning to a ban.
- There are real reports of the "Your account may be at risk" warning and of
  bans across whatsmeow, Baileys and whatsapp-web.js. Upstream closed these as
  not-planned, with maintainers arguing it is behavioural rather than a protocol
  defect, and the main correlation reported is automated/bulk sending.
- Crucially, the reported triggers are SENDING messages, not reading history.
  No source attributes bans to history sync or to a read-only client. Our
  deployment is read-only by default for exactly this reason.

Mitigations adopted:

- Writes are opt-in (`-allow-writes`), so the default deployment never sends.
- Backfill is capped per chat (`-history-limit`, default 200) so a connect does
  not sweep the whole account.
- Requests are paced: one page per chat per pass, 2s between requests, increasing
  pauses (30s up to 10 min), and a chat that stops producing new messages gets a
  cooldown after 4 barren passes.
- Deeper history for one conversation is deliberate, via the `extend_history`
  tool, rather than an automatic account-wide sweep.

Residual risk is not zero: this remains an unofficial client on a personal
account. The design keeps the account as close to passive as the protocol allows.

## Phase 8: on-demand history instead of automatic backfill

The automatic sweep was removed. History is now purely agent-driven, which
matches how the account is actually used: an agent is asked to check or build
something from a past conversation, it locates the chat or group, and requests
the messages it needs.

### Thread awareness

`list_chats`, `get_chat` and `get_contact_chats` now report:

- `kind`: `direct`, `group`, `community` or `broadcast`, derived from the JID
  and the resolved name (Community labels are added at name-resolution time).
- `message_count`: how many messages are stored locally right now.
- `stored_from` / `stored_to`: the time range those local messages cover.

This is what lets an agent decide whether it needs more history: if the answer
to a question lies before `stored_from`, it calls `get_older_messages`, rather
than guessing from the JID or reading everything.

### One tool, small pages

`get_older_messages(chat_jid, count)` replaces the previous `extend_history`:

- `count` is 1-50, default 50, and a larger value is rejected rather than
  clamped silently. The page size is deliberately small so each request is a
  modest, bounded operation.
- Each call walks one page further back from the oldest stored message, so an
  agent iterates when it needs more.
- It reports the anchor used and the message count beforehand, so a repeated
  anchor signals that no older history is available.
- For a chat with no local messages, it bootstraps from a recovered anchor (see
  Phase 7) instead of failing.

The `-history-limit` / `-request-history` flags and `REQUEST_HISTORY` /
`HISTORY_LIMIT` environment variables were removed: there is no automatic
sweep left to configure. Nothing is fetched at connect time, so a connection
makes no outbound history requests at all.

## Phase 9: pi wiring, and three bugs it surfaced

`~/.pi/agent/mcp.json`:

```json
{
  "mcpServers": {
    "whatsapp": {
      "url": "http://127.0.0.1:8765/mcp",
      "exposure": "codemode"
    }
  }
}
```

`exposure: "codemode"` keeps ten read-only tools out of every prompt; an agent
reaches them from a codemode script. Writes remain off.

`pi mcp list` reports `connected, 10 tools (codemode, global)`.

### Port publishing was broken

The app listened on `127.0.0.1:8765` *inside* the container. Docker forwards to
the container interface, not its loopback, so the port was unreachable from the
host. The symptom was misleading: the in-container healthcheck passed, so the
container reported `healthy`, while a host request got an empty reply.

`MCP_HTTP_ADDR` is now `0.0.0.0:8765` inside the container, and exposure is
controlled where it belongs, by publishing on the host loopback
(`127.0.0.1:8765:8765`). Verified: `127.0.0.1` answers, while the public IP and
the VPN IP both refuse the connection.

Binding `0.0.0.0` inside a container is safe here because the published address
is loopback-only; the container's own network is not routed to the internet.

### structuredContent must be an object

Three tools returned Go slices, so `structuredContent` was a JSON array. The MCP
spec requires an object, and Pi rejects the result outright ("Invalid MCP
tools/call structured content") even though `content[0].text` held valid data.
This broke `search_contacts`, `get_contact_chats`, and — critically —
`list_chats`, the entry point for locating a conversation. Lists now travel in a
named field (`{"chats": [...]}`), with a test asserting every tool's
structuredContent root is an object.

### include_last_message=false was broken

Omitting the last message also omitted the JOIN, but the projection still
referenced `messages.*`, so the query failed with `no such column:
messages.content`. Both projections now share a column order, so the shape fields
(`kind`, `message_count`, `stored_from`, `stored_to`) are returned either way.
`last_is_from_me` became a pointer so it is omitted rather than reported as a
misleading `false` when there is no last message.

### Verified workflow

The intended scenario works end to end through pi:

1. `list_chats` locates the conversation and reports `kind`, `message_count` and
   the stored range.
2. The agent judges that the answer lies earlier than `stored_from` and calls
   `get_older_messages`.
3. History arrives asynchronously and the next read shows a larger range.

Measured: 276 -> 326 messages and `stored_from` moving 2026-08-27 -> 2026-08-23
from a single 50-message request.
