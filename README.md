# WhatsApp MCP Server (read-only fork)

A Model Context Protocol (MCP) server that lets Claude **read** a personal WhatsApp account: search and read messages, look up contacts and chats, download attachments, and transcribe voice messages to text.

This is a fork of [lharries/whatsapp-mcp](https://github.com/lharries/whatsapp-mcp). It differs from upstream in four ways:

1. **Sending is deliberately disabled.** See [Why this fork is read-only](#why-this-fork-is-read-only).
2. **Voice messages can be transcribed**, locally, with no audio leaving the machine.
3. **Media downloads work.** Upstream drops the CDN signature when reconstructing a media path, so every download returns HTTP 403.
4. **One copy can serve several accounts** — personal and work, say — each labelled, kept separate, and set up by editing one text file rather than any code.

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

### How messages arrive

Two paths, and they are not equally trustworthy:

- **Live** — anything that arrives while the bridge is running, including messages WhatsApp buffered while your machine was asleep and delivers on reconnect. This path is reliable. A closed laptop is not a problem: macOS wakes roughly every 15 minutes to keep the connection alive, and messages sent during those gaps arrive on the next wake.
- **History sync** — a bulk backfill that your *phone* generates, sent only when you pair a new device. It is best-effort and incomplete. Measured on a real account, a fresh sync omitted about 3% of a month's messages, including ones sent the same day.

Both paths go through whatsmeow's `ParseWebMessage`, so a message means the same thing whichever way it arrives. They used to diverge, and history sync silently dropped **every edited message** — an edit arrives as a bare `protocolMessage` carrying the new text under the original message's ID, and the old code found no text in it and moved on without logging.

**Restarting a bridge never backfills.** Reconnecting reuses the existing device session: you get new and buffered messages, but no history sync. Only pairing produces one.

**Never re-link to move machines.** Copy the whole `store-<name>/` directory instead. `whatsapp.db` carries the device identity and Signal session, so the new host *is* the same linked device — no QR, no history sync, nothing lost, and no device slot burned. The one rule is that the two copies must never run at the same time; one session driven from two places corrupts it.

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

## Running more than one WhatsApp account

You can connect more than one account — your personal number and your work number, for example. Claude sees them as two clearly labelled sources and knows which is which.

**You never edit any code.** You fill in one small text file, then type one word to start each account.

### 1. Create the accounts file

You need a plain text file named **`instances.conf`**, saved in the **`whatsapp-bridge`** folder of this project.

Sitting next to it is **`instances.conf.example`** — a filled-in example showing exactly the format. Open that to see what a finished file looks like, and use it as your starting point.

Each line describes one account: four things separated by commas, with the description in double quotes.

```
personal, 8080, 15551234567,   "My US number, family and friends"
work,     8081, 5511988887777, "Work number in Brazil, colleagues and clients"
```

| | What to put |
|---|---|
| **Name** | A short word you choose: `personal`, `work`, `us`, `brasil`. You type this to start the account, and Claude shows it as `whatsapp-personal`. |
| **Port** | Just a number, and each account needs a different one. Use `8080` and `8081` unless something else on your computer is already using them. |
| **Phone number** | The number for this account: country code first, digits only, no `+`, spaces or dashes. It's a safety check — the account refuses to start if a different phone scans the code. Leave it empty or write `-` to skip the check. |
| **Description** | Plain English, in double quotes. Claude reads this to decide which account to use, so "Work number in Brazil, colleagues and clients" helps it far more than "work". |

Commas inside the quotes are fine. Spaces around the commas don't matter — the example lines them up only because it's easier to read. Lines starting with `#` are ignored.

**The quick way, in Terminal.** This copies the example to the right place under the right name and opens it for editing:

```bash
cd whatsapp-bridge
cp instances.conf.example instances.conf
open -e instances.conf
```

Replace the two example lines with your own accounts, then save and close.

**If you'd rather create it by hand** in TextEdit: choose *Format → Make Plain Text* before saving, or it saves as rich text and the program can't read it. Save it as `instances.conf` — not `instances.conf.txt` — inside the `whatsapp-bridge` folder.

Your phone numbers stay on your computer. This file is never uploaded or committed.

### Where downloaded files are saved

Photos, PDFs and voice notes aren't downloaded until you ask for them. By default they're written inside this project folder — fine for the program, a bad place to go looking for a PDF later.

Add a line to `instances.conf` to send them somewhere useful:

```
media_dir = /Users/you/Documents/WhatsApp downloads
```

**What you can write as the path:**

| | |
|---|---|
| A full path | `/Users/you/Documents/WhatsApp downloads` |
| Spaces in folder names | Fine, and no quotes needed: `/Users/you/Documents/5. Claude/Downloads` |
| Commas in folder names | Also fine: `/Users/you/Photos, scans/WhatsApp` |
| Quotes around it | Optional. `"/Users/you/My Folder"` works too |
| `~/` for your home folder | Works: `~/Claude/whatsapp` is your home folder's `Claude/whatsapp` |
| `~someone-else/` | Not understood. It stops and asks for the full path. |

`~/Claude` is the folder Claude Desktop already uses for files, so `media_dir = ~/Claude/whatsapp` is usually the convenient choice. Any folder works.

Files land in `media_dir/<account name>/<chat>/`, so `personal` and `work` never overwrite each other's copy of the same document from the same person.

The bridge prints where downloads are going when it starts, so you can check at a glance:

```
Downloads: /Users/you/Documents/WhatsApp downloads/personal
```

Leave the line out and nothing changes from before.

### 2. Start each account

One Terminal window per account. To start the first:

```bash
cd whatsapp-bridge
go run main.go personal
```

That last word is the name from your file. You'll see it confirm what it's doing:

```
Instance: personal   store: store-personal   port: 8080
Expecting account: +15551234567
```

Then a QR code appears. On the phone for **that** account, open WhatsApp → Settings → Linked Devices → Link a Device, and scan it.

If you scan with the wrong phone, it stops immediately and tells you:

```
wrong account: "personal" expects +15551234567 but the QR code was
scanned by +5511988887777. Nothing has been synced.
```

Nothing gets mixed up, and nothing is downloaded. Scan again with the right phone.

Once it connects it prints the number it linked to, so you can see it's the right one:

```
Instance "personal" is linked to +15551234567

✓ Connected to WhatsApp!
```

**Leave this window open.** Closing it stops that account from receiving new messages.

Now open a **second** Terminal window and do the same for the other account:

```bash
cd whatsapp-bridge
go run main.go work
```

Scan with the other phone. Two windows, two accounts, both running.

The first time each account connects it downloads your message history. That can take fifteen minutes or more on a busy account.

### 3. Tell Claude about them

Open Claude Desktop's settings file:

```bash
open -e ~/Library/Application\ Support/Claude/claude_desktop_config.json
```

Add one block per account inside `"mcpServers"`. **Only the name changes between them** — everything else is identical:

```json
{
  "mcpServers": {
    "whatsapp-personal": {
      "command": "/path/to/uv",
      "args": ["--directory", "/path/to/whatsapp-mcp/whatsapp-mcp-server", "run", "main.py"],
      "env": { "WHATSAPP_INSTANCE_NAME": "personal" }
    },
    "whatsapp-work": {
      "command": "/path/to/uv",
      "args": ["--directory", "/path/to/whatsapp-mcp/whatsapp-mcp-server", "run", "main.py"],
      "env": { "WHATSAPP_INSTANCE_NAME": "work" }
    }
  }
}
```

The port, the message storage and the description all come from `instances.conf`, so you don't repeat them here.

For the two paths: run `which uv` in Terminal to get the first one. The second is wherever you put this project, with `/whatsapp-mcp-server` on the end.

If the file already has other things in it, add these alongside them rather than replacing everything.

### 4. Restart Claude Desktop

Quit it properly with ⌘Q — closing the window isn't enough — then open it again.

Ask it *"which WhatsApp accounts can you see?"* It should name both.

### What Claude understands about your accounts

Each account tells Claude its name, your description of it, and the actual phone number it's linked to. It also knows three things that prevent common mistakes:

- It **cannot send messages** from any account.
- The accounts hold **completely separate** messages and chats.
- **The same person can appear on both** — a colleague who's also a friend, or someone who has both your numbers. So a conversation with somebody may be split across the two, and one account may only show you half of it. Claude is told to check the other account before concluding it has someone's full history, and to say which account anything came from.

### If something goes wrong

| What you see | What it means |
|---|---|
| `no instance named "..."` | The name you typed isn't in `instances.conf`. The message lists the names that are. |
| `wrong account: ... expects ...` | You scanned with the other phone. Nothing was saved; just run it again and scan with the right one. |
| `address already in use` | Two accounts have the same port. Give them different numbers in `instances.conf`. |
| Both accounts show the same messages | They're sharing storage. Check each line in `instances.conf` has a different name. |
| A QR code appears again later | Normal. WhatsApp expires each link about every 20 days, separately per account. Scan with that account's phone. |

### Adding a third account

Add another line to `instances.conf` with a new name and an unused port, run `go run main.go thatname`, scan, and add one more block to Claude's settings. There's no limit built in. What you'll actually run into: each account needs its own port, each keeps its own copy of that history on disk, and transcribing voice messages uses about 1.7GB of memory per account while it's working — around 3.4GB if both transcribe at the same time.

## Configuration reference

For one account, nothing needs configuring — the defaults work. For several, `whatsapp-bridge/instances.conf` is the file to edit; see [Running more than one WhatsApp account](#running-more-than-one-whatsapp-account).

Everything can also be overridden with environment variables, which take precedence over the file. This is mostly useful for one-off runs and testing.

| Variable | Default | Used by |
|---|---|---|
| `WHATSAPP_INSTANCE_NAME` | `whatsapp` | both |
| `WHATSAPP_STORE_DIR` | `store`, or `store-<name>` for a named account | both |
| `WHATSAPP_BRIDGE_PORT` | `8080`, or the port from `instances.conf` | bridge |
| `WHATSAPP_ACCOUNT_NUMBER` | the number from `instances.conf` | bridge |
| `WHATSAPP_BRIDGE_URL` | `http://localhost:<port>` | server |
| `WHATSAPP_INSTANCE_DESCRIPTION` | the description from `instances.conf` | both |
| `WHATSAPP_INSTANCES_FILE` | `whatsapp-bridge/instances.conf` | both |
| `WHATSAPP_MEDIA_DIR` | unset — downloads stay in `<store>/` | bridge |
| `WHATSAPP_MESSAGES_DB` | `<store>/messages.db` | server |
| `WHATSAPP_TRANSCRIPTS_DB` | `<store>/transcriptions.db` | server |
| `WHATSAPP_WHISPER_MODEL` | `mlx-community/whisper-large-v3-turbo` | server |

A relative `WHATSAPP_STORE_DIR` is resolved against `whatsapp-bridge/` by both processes, so one value configures the pair. A bad port falls back to the default rather than refusing to start, and an empty value counts as unset.

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
| A message you can see on your phone is missing | It only ever arrived through history sync, which is lossy. See [How messages arrive](#how-messages-arrive) |

To reset a broken sync, delete `whatsapp-bridge/store/messages.db` and `whatsapp-bridge/store/whatsapp.db` and restart the bridge to re-authenticate. This discards local history, and WhatsApp only replays a bounded window, so older messages may not return.

Treat that as a last resort rather than routine maintenance. Re-authenticating is the lossy path — the replacement history comes from the phone's best-effort backfill, so you can end up with *fewer* messages than you started with. Copy the store directory somewhere safe first; it is the only complete record you have, and the gaps a re-sync leaves can be filled back in from it afterwards.

## License

MIT, as upstream.
