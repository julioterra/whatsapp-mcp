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

`WHATSAPP_WHISPER_MODEL` and `WHATSAPP_TRANSCRIPTS_DB` are listed with the other settings under [Running multiple accounts](#settings).

## Running multiple accounts

One checkout serves several accounts. An account is a bridge plus an MCP server sharing a name, a port and a store directory.

**The configuration surface is `whatsapp-bridge/instances.conf`, not environment variables.** Env vars still work and take precedence, but they are the override, not the interface — the file is what a user edits. It is gitignored (it holds phone numbers); `instances.conf.example` is the committed template.

```
# name, port, number, "description"
personal, 8080, 15551234567, "Personal US number, family and friends"
work,     8081, -,           "Work number, colleagues and clients"
```

Comma-separated, description quoted so its own commas survive. Parsing is deliberately forgiving: quotes optional, spacing ignored, and the number is only taken as a number if it has at least `minPhoneDigits` digits — otherwise `personal, 8080, "My US number"` would silently turn the description into a phone number and then refuse to connect. Empty or `-` skips the check. From `whatsapp-bridge/`:

```bash
go run main.go personal      # name selects the entry
```

`store-<name>` and the port are derived, so the only thing typed is the name. An unknown name errors and lists the names that do exist. The MCP server needs one variable, `WHATSAPP_INSTANCE_NAME`, and reads the same file for everything else.

### Downloaded media

Attachments default to `<store>/<chat_jid>/`, which keeps personal files inside the repo — gitignored, but also invisible to anything that isn't this program. `media_dir` in `instances.conf`, or `WHATSAPP_MEDIA_DIR`, moves them; `~/Claude/whatsapp` is the useful answer since that is Claude Desktop's own files folder.

When set, the path is `<media_dir>/<instance>/<chat_jid>/`. The account level is not decoration: a direct chat is named after the *other* party, so the same JID appears on every account, and two accounts sharing a media directory would otherwise overwrite each other's copy of `Document.pdf` from the same sender. Keep it if you touch `mediaPath`.

Path handling, all covered by tests: full paths, spaces and commas inside folder names, and optional surrounding quotes. `~/` is expanded in Go, because a value from `instances.conf` or a JSON config never passes through a shell and would otherwise create a directory literally named `~`. `~otheruser` is deliberately *not* imitated — `checkMediaDir` rejects it at startup rather than half-supporting shell syntax.

Transcription follows the media directory automatically: `transcribe_audio` uses the absolute path the bridge returns from `/api/download` and never reconstructs one, so the Python side has no knowledge of the layout. Keep it that way. The transcript cache is keyed on `(message_id, chat_jid, model)` rather than the path, so moving `media_dir` does not invalidate existing transcripts. There is a test covering transcription from a path containing spaces and commas, since `mlx_whisper` shells out to ffmpeg.

`settingLine` distinguishes `key = value` from an account by looking for a comma *before* the `=`, not anywhere in the line, so a folder name containing a comma does not silently turn the setting into a malformed account entry. `loadInstance` skips settings lines so they never appear in the "no account named" suggestion list.

### The label must not be able to lie

Which account a bridge serves is decided by **whichever phone scans the QR code**. The name is only a label, so nothing structurally prevented scanning the Brazilian phone into `store-us` and having Claude confidently report Brazilian messages as US ones.

`confirmAccount` closes that: after login it reads `client.Store.ID.User` — the real linked number — prints it, and if `instances.conf` names a different number, exits before syncing anything. It then writes `instance.json` into the store, which `config.linked_account()` reads so the model is told the verified number rather than the label alone.

If you touch this area, keep that property: a label that can silently disagree with reality is worse than no label, because it is trusted.

### What the model is told

The name becomes the MCP server name (`whatsapp-personal`), and `server_instructions()` states the account name, its description, and the verified phone number. It also carries two warnings that exist because they are easy to get wrong:

- Other WhatsApp servers may be connected, holding entirely separate data.
- **The same person can appear on more than one account** — a colleague who is also a friend, someone who has both numbers. A conversation may be split across accounts, so one account can show only half of it. The model is told to check the others before concluding something is absent or complete, and to attribute what it reports to an account.

### Settings

Relative `WHATSAPP_STORE_DIR` resolves against `whatsapp-bridge/` in both processes, so one value configures the pair.

| Variable | Default |
|---|---|
| `WHATSAPP_INSTANCE_NAME` | `whatsapp` |
| `WHATSAPP_STORE_DIR` | `store`, or `store-<name>` when named |
| `WHATSAPP_BRIDGE_PORT` | `8080`, or the conf file's port |
| `WHATSAPP_ACCOUNT_NUMBER` | the conf file's number |
| `WHATSAPP_BRIDGE_URL` | `http://localhost:<port>` |
| `WHATSAPP_INSTANCE_DESCRIPTION` | the conf file's description |
| `WHATSAPP_INSTANCES_FILE` | `instances.conf` |
| `WHATSAPP_MEDIA_DIR` | unset — attachments stay in `<store>/` |
| `WHATSAPP_MESSAGES_DB` | `<store>/messages.db` |
| `WHATSAPP_TRANSCRIPTS_DB` | `<store>/transcriptions.db` |
| `WHATSAPP_WHISPER_MODEL` | `mlx-community/whisper-large-v3-turbo` |

Defaults reproduce the original single-account layout. A bad port falls back rather than refusing to start; an empty value counts as unset so a blank store dir cannot point at the filesystem root.

## Commands

There is no CI. Two test suites exist and both run in seconds:

```bash
cd whatsapp-bridge && go test ./...                                   # config + media direct-path handling
uv run --directory whatsapp-mcp-server python test_config.py          # per-instance configuration
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
