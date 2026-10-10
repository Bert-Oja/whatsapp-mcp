# Running whatsapp-mcp in Docker

A single Go container running the WhatsApp bridge and the MCP server. No Go,
Python or `uv` is needed on the host.

## Quick start

```bash
docker compose build
docker compose up -d

# Scan the QR code shown in the logs with
# WhatsApp > Settings > Linked devices > Link a device
docker compose logs -f
```

Once paired, the MCP endpoint is at `http://127.0.0.1:8765/mcp`.

**The server starts listening only after pairing succeeds.** Before that, an
MCP client will fail to connect — that is expected. Watch the logs until you
see the QR code scanned and the "Connected to WhatsApp" line.

Pairing is also possible without a scannable terminal:

```bash
WA_PAIR_PHONE=15551234567 docker compose up -d
docker compose logs -f   # shows an 8-digit code to type into WhatsApp
```

## Writes are off by default

The server is **read-only** unless you opt in. In read-only mode the
`send_message`, `send_file` and `send_audio_message` tools are not exposed at
all — they are absent from `tools/list`, and a call to them fails.

```bash
ALLOW_WRITES=true docker compose up -d
```

| | Tools exposed |
|---|---|
| Default (`ALLOW_WRITES=false`) | 10 read-only tools |
| `ALLOW_WRITES=true` | the 10 read tools plus the 3 send tools |

Every tool carries MCP annotations (`readOnlyHint`), so clients can also make
their own permission decisions.

## Connecting pi

Add the server to `~/.pi/agent/mcp.json`:

```json
{
  "mcpServers": {
    "whatsapp": {
      "url": "http://127.0.0.1:8765/mcp",
      "description": "Read and send personal WhatsApp messages"
    }
  }
}
```

Then check the connection:

```bash
pi mcp list
```

## Data and re-pairing

The `whatsapp-store` volume holds:

- `whatsapp.db` — the linked-device session
- `messages.db` — the message history

`docker compose restart` needs no re-scan. If you delete the volume, or
WhatsApp invalidates the device (roughly every 20 days), the container exits so
that it can be restarted and print a fresh QR code.

## Hardening

The container runs as an unprivileged user with:

- `read_only: true` root filesystem, with `/tmp` on a small tmpfs for ffmpeg
- all capabilities dropped, and `no-new-privileges`
- a 512 MB memory limit
- the port published to **loopback only** (`127.0.0.1:8765`)

This server reads private messages and can send them. Do not expose the port
beyond `127.0.0.1`.

Check status and health:

```bash
docker compose ps
docker inspect whatsapp-mcp --format '{{.State.Health.Status}}'
curl -s http://127.0.0.1:8765/healthz   # 200 when connected, 503 otherwise
```

## Image layout

| Component | Size |
|---|---|
| Base `alpine:3.21` | ~12 MB |
| Go binary (static, CGO-free) | ~18 MB |
| Minimal ffmpeg | ~2.7 MB |
| **Total image** | **~45 MB** |

ffmpeg is compiled from source with only the codecs used to turn an attached
audio file into an Opus/Ogg voice note. The distribution build would add
~188 MB, and a prebuilt static ffmpeg is a 144 MB binary on its own.

## Troubleshooting

- **`err-client-outdated`** — the WhatsApp client version is rejected. Update
  `go.mau.fi/whatsmeow` in `go.mod` and rebuild; the pinned version matters.
- **Device limit reached** — remove a linked device in WhatsApp, then retry.
- **Messages not appearing** — the initial history sync can take several
  minutes for large accounts.
- **Older messages** — history is not fetched automatically. An agent asks for
  it with `get_older_messages` (up to 50 messages per call) after locating the
  conversation with `list_chats`. Each result reports the local message count
  and the time range it covers (`stored_from` / `stored_to`), so an agent can
  see whether it needs to reach further back.
- **Out of sync** — remove the volume (`docker compose down -v`) and pair again.
