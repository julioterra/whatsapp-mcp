# WhatsApp MCP Server (read-only fork)

A Model Context Protocol (MCP) server that lets Claude **read** a personal WhatsApp account: search and read messages, look up contacts and chats, download attachments, and transcribe voice messages to text.

This is a fork of [lharries/whatsapp-mcp](https://github.com/lharries/whatsapp-mcp). It differs from upstream in four ways:

1. **Sending is deliberately disabled.** See [Why this fork is read-only](#why-this-fork-is-read-only).
2. **Voice messages can be transcribed**, locally, with no audio leaving the machine.
3. **Media downloads work.** Upstream drops the CDN signature when reconstructing a media path, so every download returns HTTP 403.
4. **One checkout can serve several accounts**, each with its own name, port and message store, configured through the environment.

It connects to your personal WhatsApp account through the WhatsApp Web multi-device API (via [whatsmeow](https://github.com/tulir/whatsmeow)). Messages are stored locally in SQLite and are only ever sent to a model when a tool you invoked reads them.

> **Caution:** like most MCP servers over personal data, this is subject to [the lethal trifecta](https://simonwillison.net/2025/Jun/16/the-lethal-trifecta/) — untrusted content, private data, and the ability to act. This fork removes the third leg on purpose; see below.

## Why this fork is read-only

The `send_message`, `send_file` and `send_audio_message` tools exist in the code but their `@mcp.tool()` decorators are commented out, so the model never sees them.

The reasoning: this server reads group chats containing people outside your control. Every message is untrusted input that reaches a model's context. Give that same model the ability to send from your real number and you have a working prompt-injection path — a crafted message could induce a reply, a forward, or exfiltration of a conversation. Removing the write surface breaks the chain structurally instead of relying on prompt-level caution.

If you fork this and want sending back, uncomment the decorators — but do it knowingly.

To check the protection is still in place:

```bash
grep -n -B3 "^def send_" whatsapp-mcp-server/main.py
```

Every `send_*` function should show a commented-out decorator.

## Architecture

Two processes:

- **`whatsapp-bridge/`** (Go) — connects to WhatsApp, handles QR authentication, mirrors messages into SQLite, and serves a small REST API on `localhost:8080`.
- **`whatsapp-mcp-server/`** (Python) — the MCP server Claude talks to.

Reads and writes take different paths, which is worth knowing when something looks stale:

- **Reads** go straight to SQLite. They keep working when the bridge is down — just with increasingly old data.
- **`download_media` and `transcribe_audio`** call the bridge over HTTP. These need the Go process alive.

Media is stored as **metadata only**. Incoming messages record the URL and decryption keys; nothing is fetched until a tool asks for it. Nothing downloads in the background.

## Requirements

- Go
- Python 3.11+ and [uv](https://docs.astral.sh/uv/)
- Claude Desktop (or Cursor)
- **ffmpeg** — required for transcription, which shells out to it to decode Opus
- **Apple Silicon** for transcription, which runs on the GPU via `mlx-whisper`. Everything else works anywhere; the dependency carries a platform marker so installs elsewhere simply skip it.

## Setup

**1. Run the bridge** (must stay running, and must be started from its own directory — it opens `store/` by relative path):

```bash
cd whatsapp-bridge && go run main.go
```

The first run prints a QR code to scan from your phone. The device link expires roughly every 20 days, after which it prints a new one.

**2. Point Claude Desktop at the server.** Add to `~/Library/Application Support/Claude/claude_desktop_config.json` (for Cursor, `~/.cursor/mcp.json`):

```json
{
  "mcpServers": {
    "whatsapp": {
      "command": "/path/to/uv",
      "args": ["--directory", "/path/to/whatsapp-mcp/whatsapp-mcp-server", "run", "main.py"]
    }
  }
}
```

Run `which uv` for the first path. If the file already contains other MCP servers, edit it in place rather than replacing it.

**3. Restart Claude Desktop** — fully quit it (⌘Q), don't just close the window.

### Windows

`go-sqlite3` needs cgo, which is off by default on Windows. Install a C compiler (via [MSYS2](https://www.msys2.org/), adding `ucrt64\bin` to `PATH`), then:

```bash
cd whatsapp-bridge
go env -w CGO_ENABLED=1
go run main.go
```

Transcription is unavailable on Windows.

## Tools

| Tool | Purpose |
|---|---|
| `search_contacts` | Find contacts by name or phone number |
| `list_messages` | Retrieve messages with filters and context |
| `list_chats` | List chats with metadata |
| `get_chat` | Details of one chat |
| `get_direct_chat_by_contact` | Find the direct chat with a contact |
| `get_contact_chats` | All chats involving a contact |
| `get_last_interaction` | Most recent message with a contact |
| `get_message_context` | Messages surrounding a given message |
| `download_media` | Download an attachment, returns a local path |
| `transcribe_audio` | Transcribe a voice message to text |

Contacts are derived from chats, so someone you have never had a direct conversation with will not be findable.

## Voice message transcription

`transcribe_audio(message_id, chat_jid)` downloads the audio if needed, transcribes it, and caches the result. Repeat requests return instantly.

Transcription runs **entirely on your machine** via `mlx-whisper`. Voice notes are personal messages, and sending them to a hosted API would defeat the point of a local-first setup. That constraint turned out to be free — measured on real voice notes on an M4 Pro, MLX on the GPU beat CPU `faster-whisper` on both speed and memory:

| Engine | Speed | Peak RSS |
|---|---|---|
| `faster-whisper` large-v3-turbo, int8 CPU | 2.5x realtime | 2.03 GB |
| **`mlx-whisper` large-v3-turbo, GPU** | **24x realtime** | **1.72 GB** |

In practice a 30-second voice note transcribes in about 1.5 seconds. The model loads lazily on first use (a ~1.6GB download) and is cached for the process lifetime, so an idle server costs nothing.

Language is detected automatically; pass `language` ("en", "pt") to skip detection.

**On model size:** the default is deliberately a large model. Smaller ones are faster but mis-hear proper names and emit fluent, wrong sentences — worse than a slow transcript when the content matters. Override with `WHATSAPP_WHISPER_MODEL` if you disagree; the cache is keyed on the model, so switching re-transcribes rather than serving stale text.

Transcripts are cached in `whatsapp-bridge/store/transcriptions.db` — deliberately separate from `messages.db`, which is disposable and gets deleted when a broken sync is reset.

## Running more than one account

An **instance** is one bridge plus one MCP server, sharing a name, a port and a store directory. Everything is set through the environment, so several accounts run from a single checkout with no code changes and no duplicated repos.

> **Never copy a store directory to create an instance.** `whatsapp.db` holds the device identity and Signal session. Two bridges driving the same session corrupt it and can get the device unlinked. Every instance is linked by its own QR scan.

The walkthrough below sets up two accounts. Decide the three values per account first — everything else follows from them:

| | Name | Store directory | Port |
|---|---|---|---|
| US number | `whatsapp-us` | `store-us` | 8080 |
| Brazilian number | `whatsapp-brasil` | `store-brasil` | 8081 |

Any names, directories and free ports work. Keep the store directories starting with `store-`, which is already gitignored.

### Step 1 — Start the first bridge

From `whatsapp-bridge/`, in its own terminal. It stays running:

```bash
cd whatsapp-bridge
WHATSAPP_INSTANCE_NAME=whatsapp-us \
WHATSAPP_STORE_DIR=store-us \
WHATSAPP_BRIDGE_PORT=8080 \
go run main.go
```

It prints its identity first, so you can confirm you launched the one you meant:

```
Instance: whatsapp-us   store: store-us   port: 8080
```

Then it prints a QR code. Scan it from the **US** phone: WhatsApp → Settings → Linked Devices → Link a Device. Wait for `✓ Connected to WhatsApp!`.

First sync takes a while — 15 minutes or more on a busy account. It's finished when the message count stops climbing.

### Step 2 — Start the second bridge

A **second terminal**, different name, different store, different port:

```bash
cd whatsapp-bridge
WHATSAPP_INSTANCE_NAME=whatsapp-brasil \
WHATSAPP_STORE_DIR=store-brasil \
WHATSAPP_BRIDGE_PORT=8081 \
go run main.go
```

Scan this QR from the **Brazilian** phone. Two different phones, two different codes — this is the step that must not be shortcut by copying files.

### Step 3 — Confirm both bridges are up

```bash
for port in 8080 8081; do
  printf '%s -> ' "$port"
  curl -s -o /dev/null -w '%{http_code}\n' --max-time 3 -X POST "http://localhost:$port/api/download"
done
```

`400` on both means both are listening (the request is deliberately empty, so a bridge that's up rejects it). `000` means that bridge isn't running.

Sanity-check that they really are separate accounts:

```bash
for s in store-us store-brasil; do
  printf '%-14s %s messages\n' "$s" "$(sqlite3 "whatsapp-bridge/$s/messages.db" 'select count(*) from messages;')"
done
```

Two different numbers, and two directories that both exist, mean the stores aren't shared.

### Step 4 — Tell Claude Desktop about both

Add both to `~/Library/Application Support/Claude/claude_desktop_config.json`. If it already contains other MCP servers, add these keys alongside them rather than replacing the file:

```json
{
  "mcpServers": {
    "whatsapp-us": {
      "command": "/path/to/uv",
      "args": ["--directory", "/path/to/whatsapp-mcp/whatsapp-mcp-server", "run", "main.py"],
      "env": {
        "WHATSAPP_INSTANCE_NAME": "whatsapp-us",
        "WHATSAPP_INSTANCE_DESCRIPTION": "US number",
        "WHATSAPP_STORE_DIR": "store-us",
        "WHATSAPP_BRIDGE_URL": "http://localhost:8080"
      }
    },
    "whatsapp-brasil": {
      "command": "/path/to/uv",
      "args": ["--directory", "/path/to/whatsapp-mcp/whatsapp-mcp-server", "run", "main.py"],
      "env": {
        "WHATSAPP_INSTANCE_NAME": "whatsapp-brasil",
        "WHATSAPP_INSTANCE_DESCRIPTION": "Brazilian number",
        "WHATSAPP_STORE_DIR": "store-brasil",
        "WHATSAPP_BRIDGE_URL": "http://localhost:8081"
      }
    }
  }
}
```

The two things that must differ per entry are `WHATSAPP_STORE_DIR` and `WHATSAPP_BRIDGE_URL`. Pointing both at the same store is the mistake to watch for: the servers would then report the same messages under two names.

Run `which uv` for the command path. The `--directory` argument is the same for both — one checkout serves both accounts.

### Step 5 — Restart Claude Desktop and check

Fully quit with ⌘Q, not just closing the window, then reopen. You should see both servers listed. Ask it something like *"which WhatsApp accounts can you see?"* — it should name both and be able to list chats from each separately.

### Keeping them running

Both bridges must stay running for downloads and transcription; reads keep working from the local database even when a bridge is down, just with increasingly stale data. If you close a terminal, that instance stops syncing.

Each device link expires roughly every 20 days **independently**, so the two will drift out of sync with each other. When one prints a QR again, scan it with that account's phone — nothing else is affected.

### More than two accounts

Nothing caps it at two. A third is the same three decisions again:

```bash
WHATSAPP_INSTANCE_NAME=whatsapp-work \
WHATSAPP_STORE_DIR=store-work \
WHATSAPP_BRIDGE_PORT=8082 \
go run main.go
```

The real limits:

- **One free port per instance.** Ports are the only thing instances contend over.
- **Memory during transcription.** Roughly 1.7GB per instance that is actively transcribing; the model loads lazily, so idle instances cost nothing. Two at once peak around 3.4GB.
- **Disk.** Each store holds that account's full history plus any downloaded attachments.
- **WhatsApp's four-device limit is per account**, not per machine, so separate accounts don't compete. It only bites if you link the same number to more than four devices, counting your other computers and browsers.

### Telling the model which account is which

The instance name becomes the MCP server name, so tools appear under `whatsapp-us` and `whatsapp-brasil` instead of two identical `whatsapp` servers.

Each server also sends the model instructions naming its account, restating that it is read-only, and warning that other WhatsApp servers may be connected which share no messages or contacts. That last part matters: without it, a model that fails to find a contact on one account may conclude the person doesn't exist, rather than checking the other. `WHATSAPP_INSTANCE_DESCRIPTION` is free text appended to those instructions — "Brazilian number, family and building contractors" is more useful to a model than a bare name.

## Configuration

`WHATSAPP_STORE_DIR` is resolved identically by both processes — absolute paths as given, relative ones against `whatsapp-bridge/` — so a single value configures the pair.

| Variable | Default | Used by |
|---|---|---|
| `WHATSAPP_INSTANCE_NAME` | `whatsapp` | both |
| `WHATSAPP_STORE_DIR` | `store` | both |
| `WHATSAPP_BRIDGE_PORT` | `8080` | bridge |
| `WHATSAPP_BRIDGE_URL` | `http://localhost:8080` | server |
| `WHATSAPP_INSTANCE_DESCRIPTION` | empty | server |
| `WHATSAPP_MESSAGES_DB` | `<store>/messages.db` | server |
| `WHATSAPP_TRANSCRIPTS_DB` | `<store>/transcriptions.db` | server |
| `WHATSAPP_WHISPER_MODEL` | `mlx-community/whisper-large-v3-turbo` | server |

Every default reproduces the original single-account layout, so setting nothing changes nothing. An unparseable port falls back to the default rather than refusing to start, and an empty value counts as unset so a blank `WHATSAPP_STORE_DIR` can't point the bridge at the filesystem root.

## Tests

```bash
cd whatsapp-bridge && go test ./...
uv run --directory whatsapp-mcp-server python test_config.py
uv run --directory whatsapp-mcp-server python test_transcription.py
```

The Python test synthesises its own speech with macOS `say` rather than shipping an audio fixture.

## Data and privacy

`whatsapp-bridge/store/` holds your complete message history, downloaded attachments, and transcripts. It is gitignored and should stay that way. Don't put this repo in iCloud Drive, Dropbox or any synced folder — besides the privacy problem, concurrent sync corrupts SQLite mid-write.

## Troubleshooting

| Symptom | Cause |
|---|---|
| No WhatsApp tools in Claude | Wrong config path, or Claude Desktop not fully quit (⌘Q) |
| Reads work, `download_media` fails | The Go bridge isn't running |
| All tools error | The MCP server failed to start — run it standalone to see the traceback |
| Every media download returns 403 | Bridge predates the direct-path fix; rebuild and restart it |
| Transcription always fails | `ffmpeg` missing, or not on Apple Silicon |
| Results stop at an old date | Bridge started from the wrong directory, or disconnected |
| Empty results after fresh auth | History sync still running — can take 15+ minutes |
| Sudden disconnect | Device unlinked, or WhatsApp's 4-device limit hit |
| Second bridge won't start | Port already taken — give it its own `WHATSAPP_BRIDGE_PORT` |
| Two instances show the same messages | They share a `WHATSAPP_STORE_DIR`. Give each its own and re-link |

To reset a broken sync, delete `whatsapp-bridge/store/messages.db` and `whatsapp-bridge/store/whatsapp.db` and restart the bridge to re-authenticate. This discards local history, and WhatsApp only replays a bounded window, so older messages may not return.

## License

MIT, as upstream.
