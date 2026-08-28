# CLAUDE.md

This file provides guidance to Claude Code (claude.ai/code) when working with code in this repository.

## What this is

A fork of [lharries/whatsapp-mcp](https://github.com/lharries/whatsapp-mcp) that gives Claude **read-only** access to a personal WhatsApp account. Two processes:

- `whatsapp-bridge/` — Go. Connects to WhatsApp via the multi-device protocol (whatsmeow), mirrors messages into SQLite, and serves a small REST API on `localhost:8080`. Must be running for anything to work.
- `whatsapp-mcp-server/` — Python MCP server (FastMCP, stdio transport). Exposes the database to Claude Desktop as tools.

The fork exists to run **one instance per WhatsApp account** rather than one install total. See "Running multiple accounts" below.

## Critical invariant: this install is READ-ONLY

The send tools are **deliberately disabled**. In `whatsapp-mcp-server/main.py`, the `@mcp.tool()` decorator is commented out above:

- `send_message`
- `send_file`
- `send_audio_message`

**Do not re-enable these.** Do not add new tools that send, reply, react, or otherwise write to WhatsApp. If a task seems to need sending, say so and stop — the repo owner will decide.

### Why

This server reads group chats containing people outside the account owner's control. Every message is untrusted input that reaches a model's context. Combining that with the ability to send messages from the owner's real number creates a working prompt-injection path: a crafted message could induce Claude to reply, forward, or leak conversation contents. Removing the send tools breaks the chain structurally rather than relying on anyone remembering to be careful.

This is a deliberate security decision, not an oversight or an incomplete install.

The underlying plumbing is still live: `whatsapp.py` keeps working `send_*` functions, and the bridge still serves `POST /api/send`. Only the MCP tool surface is closed. That is intentional — but it means restoring send capability is a one-character edit, which is why the check below matters.

### Verify it still holds

```bash
grep -n -B3 "^def send_" whatsapp-mcp-server/main.py
```

Every `send_*` function should have a **commented-out** decorator above it. If any shows a live `@mcp.tool()`, the invariant is broken — flag it immediately.

## Relationship to upstream

`origin` is this fork; `upstream` is `lharries/whatsapp-mcp`. Two commits diverge from upstream and both must survive any merge:

1. **The send-tool disabling** described above.
2. **A whatsmeow upgrade.** `go.mod`/`go.sum` are bumped well past upstream's pin, and `main.go` is adapted to the newer API, which threads `context.Context` through `client.Download`, `sqlstore.New`, `container.GetFirstDevice`, `client.GetGroupInfo`, and `Store.Contacts.GetContact`.

Merging upstream can revert either one. After any `git merge upstream/main`:

1. Re-run the grep above. If upstream's `main.py` won the merge, the decorators are live again.
2. Rebuild the bridge. Upstream's `main.go` will not compile against this whatsmeow (missing ctx arguments), and upstream's `go.mod` will not compile against this `main.go`. The two halves travel together.

## Architecture

Reads and writes take different paths, which is why a stale-looking bug is usually a bridge problem, not an MCP problem:

- **Reads** (`list_messages`, `list_chats`, `search_contacts`, `get_message_context`, …) never touch the bridge. `whatsapp.py` opens `whatsapp-bridge/store/messages.db` directly with `sqlite3`. They still return data when the bridge is down — just increasingly stale data.
- **`download_media`** and the disabled `send_*` functions POST to `http://localhost:8080/api/{download,send}` on the bridge. These are the only calls that need the Go process alive.

The bridge writes to SQLite from two sources: live `events.Message` (`handleMessage`) and WhatsApp's bulk `events.HistorySync` (`handleHistorySync`), which arrives in bursts after authentication.

### Data model

`store/messages.db` has exactly two tables, created in `NewMessageStore`: `chats(jid, name, last_message_time)` and `messages(id, chat_jid, sender, content, timestamp, is_from_me, media_type, filename, url, media_key, file_sha256, file_enc_sha256, file_length)`, keyed on `(id, chat_jid)`.

Consequences worth knowing before writing a query:

- **There is no contacts table.** `search_contacts` derives contacts from `chats` rows whose JID is not `%@g.us`, so a person with no direct chat is not findable, and display names come from whatever `GetChatName` resolved at sync time.
- **Media is metadata-only at rest.** The message row stores the URL and decryption keys; bytes are fetched on demand by `download_media` and written to `whatsapp-bridge/store/<chat_jid>/<filename>`.
- **Group senders** are bare JIDs in `messages.sender`; `get_sender_name` resolves them back through `chats`.
- `store/whatsapp.db` is separate and belongs to whatsmeow — device identity and Signal session state. Do not query or migrate it.
- `store/transcriptions.db` is a **sidecar** written by the MCP server, not the bridge. It is deliberately not a table inside `messages.db`, because the message store is disposable — resetting a broken sync deletes it — and transcription is the expensive part to redo.

### Working directory matters

The bridge opens `store/...` by **relative** path. Run it from inside `whatsapp-bridge/` or it will silently create a second, empty database elsewhere. The MCP server resolves the same file by absolute path relative to its own source file, so a bridge started from the wrong directory shows up as "tools work, but no new messages ever arrive."

## Voice message transcription

`transcribe_audio(message_id, chat_jid)` turns a voice note into text. It downloads the audio if needed, transcribes it, and caches the result.

Transcription runs **locally**, on this machine, via `mlx-whisper` on the GPU. Voice notes are personal messages; sending them to a hosted API would contradict the rule the rest of this install is built around. That constraint drove the engine choice, but it cost nothing — measured on real voice notes, MLX beat CPU `faster-whisper` on both axes:

| Engine | Speed | Peak RSS |
|---|---|---|
| `faster-whisper`, large-v3-turbo, int8 CPU | 2.5x realtime | 2.03 GB |
| **`mlx-whisper`, large-v3-turbo, GPU** | **24x realtime** | **1.72 GB** |

Notes for anyone changing this:

- **Do not drop to a smaller model to save memory.** `small` runs faster but mis-hears proper names and produces plausible-looking wrong sentences, which is worse than a slow transcript — a wrong name in a household or contractor message is actively misleading.
- The model is loaded lazily on first use and cached by `mlx_whisper` for the process lifetime, so an idle server costs nothing. First use downloads ~1.6GB.
- `mlx-whisper` is Apple-Silicon-only, declared in `pyproject.toml` with a platform marker so the rest of the server still installs elsewhere.
- The cache is keyed on the model, so changing `WHATSAPP_WHISPER_MODEL` re-transcribes rather than silently serving output from the previous one.

Both env vars have working defaults: `WHATSAPP_WHISPER_MODEL` and `WHATSAPP_TRANSCRIPTS_DB`.

## Running multiple accounts

The goal is one instance per account — two of them, `whatsapp-us` and `whatsapp-brasil` — each with its own port, store, and device session. Five hardcoded values block that today:

| Location | Value |
|---|---|
| `whatsapp-bridge/main.go` | REST port literal `8080` |
| `whatsapp-bridge/main.go` | `store/messages.db`, relative |
| `whatsapp-bridge/main.go` | `store/whatsapp.db`, relative |
| `whatsapp-mcp-server/whatsapp.py` | `MESSAGES_DB_PATH`, resolved against its own file |
| `whatsapp-mcp-server/whatsapp.py` | `WHATSAPP_API_BASE_URL`, pinned to `localhost:8080` |

Until these are configurable, each account needs a full duplicate of the repo — which is itself hazardous, because **copying a folder copies `store/whatsapp.db` and therefore the WhatsApp device identity**. Two bridges sharing one Signal session corrupt it and can get the device unlinked, and both will fight over port 8080. Each instance must be linked by its own QR scan, never by copying a store.

## Commands

There is no CI. Two test suites exist and both run in seconds:

```bash
cd whatsapp-bridge && go test ./...                          # media direct-path handling
uv run --directory whatsapp-mcp-server python test_transcription.py   # transcription + caching
```

The Python test synthesises its own speech with macOS `say` rather than checking in an audio fixture, because the only real audio here is personal messages.

**Start the bridge** (must stay running; blocks until Ctrl+C):

```bash
cd whatsapp-bridge && go run main.go
```

**Compile-check without running** — the fast way to validate a `main.go` or whatsmeow change:

```bash
cd whatsapp-bridge && go build -o /dev/null .
```

**Run the MCP server standalone** (it speaks stdio, so this only proves it imports and starts; Claude Desktop is the real driver):

```bash
cd whatsapp-mcp-server && uv run main.py
```

**Sync Python deps** after touching `pyproject.toml`:

```bash
cd whatsapp-mcp-server && uv sync
```

**Check the bridge is alive:**

```bash
curl -s -o /dev/null -w '%{http_code}\n' -X POST http://localhost:8080/api/download   # 400 = up
```

**Inspect the database** — counts and schema, not message text (see Data sensitivity):

```bash
sqlite3 whatsapp-bridge/store/messages.db '.schema' 'select count(*) from messages;'
```

`ffmpeg` is **required**: `mlx_whisper` shells out to it to decode Opus, so transcription fails without it. (`audio.py` also uses it, but that is part of the disabled send path.)

## Data sensitivity

`whatsapp-bridge/store/` holds a complete personal message history plus downloaded media. It is gitignored, and that must stay true.

- Never commit it. Never copy its contents into logs, issues, pastebins, or commit messages.
- Never move the repo into iCloud Drive, Dropbox, or any synced folder. Besides the privacy problem, concurrent sync corrupts SQLite mid-write.
- When debugging, quote the minimum necessary. Prefer row counts and schema over message text.

## Operating it

**Re-authentication:** the WhatsApp device link expires roughly every 20 days. The bridge prints a QR code to be scanned from the phone. This step is always human — never attempt to automate it. The QR wait times out after 3 minutes.

**Reset a broken sync:**

```bash
rm whatsapp-bridge/store/messages.db whatsapp-bridge/store/whatsapp.db
cd whatsapp-bridge && go run main.go   # then rescan
```

This discards local history and re-syncs from WhatsApp — and WhatsApp only replays a bounded window, so old messages may not come back. Confirm with the owner first.

## Troubleshooting

| Symptom | Cause |
|---|---|
| No WhatsApp tools in Claude | Config path wrong, or Claude Desktop not fully quit (⌘Q) |
| Read tools work, `download_media` errors | Go bridge isn't running |
| All tools error | MCP server failed to start — run it standalone to see the traceback |
| Results stop at an old date | Bridge running from the wrong directory, or disconnected |
| Empty results after fresh auth | History sync still in progress — can take 15+ min |
| Send tools visible | Invariant broken. Stop and flag. |
| Sudden disconnect | Device unlinked, or the 4-device cap was hit |
| `CGO_ENABLED=0` build error | `go-sqlite3` needs cgo; a C toolchain must be on PATH |
| Every media download returns 403 | Bridge is running a binary from before the direct-path fix. Restart it. |
| Transcription fails on every file | `ffmpeg` missing, or not Apple Silicon (`mlx-whisper` will not have installed) |

## Editing the Claude Desktop config

`~/Library/Application Support/Claude/claude_desktop_config.json` usually contains **other MCP servers**. When touching it: back it up, edit in place, never regenerate the file wholesale, and diff before finishing. Losing the other entries is the standard failure here.
