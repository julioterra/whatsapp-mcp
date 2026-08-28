"""Local speech-to-text for WhatsApp voice messages.

Transcription runs entirely on this machine. Voice notes are personal
messages, so sending them to a hosted API would contradict the rule the rest
of this install is built around; MLX also happens to be faster than the
CPU alternatives on Apple Silicon.

Results are cached in a database of their own rather than in messages.db,
because the message store is disposable — it gets deleted and re-synced when
a session breaks — and transcription is the expensive part to redo.
"""
import sqlite3
import time
from dataclasses import dataclass
from typing import Optional

import config

# Whisper large-v3-turbo: measured at ~24x realtime and 1.7GB peak RSS on
# Apple Silicon. The smaller models are quicker to load but mis-hear proper
# names badly enough to make messages misleading rather than merely rough.
MODEL_REPO = config.WHISPER_MODEL
TRANSCRIPTS_DB_PATH = config.TRANSCRIPTS_DB_PATH


@dataclass
class Transcript:
    text: str
    language: str
    duration_seconds: float
    model: str
    cached: bool


def _connect() -> sqlite3.Connection:
    conn = sqlite3.connect(TRANSCRIPTS_DB_PATH)
    conn.execute("""
        CREATE TABLE IF NOT EXISTS transcriptions (
            message_id TEXT,
            chat_jid TEXT,
            text TEXT,
            language TEXT,
            duration_seconds REAL,
            model TEXT,
            transcribed_at TIMESTAMP,
            PRIMARY KEY (message_id, chat_jid)
        )
    """)
    return conn


def get_cached(message_id: str, chat_jid: str) -> Optional[Transcript]:
    """Return a previously stored transcript, or None.

    A transcript produced by a different model is treated as a miss, so
    changing WHATSAPP_WHISPER_MODEL re-transcribes rather than silently
    serving output from the old one.
    """
    conn = _connect()
    try:
        row = conn.execute(
            """SELECT text, language, duration_seconds, model
               FROM transcriptions
               WHERE message_id = ? AND chat_jid = ? AND model = ?""",
            (message_id, chat_jid, MODEL_REPO),
        ).fetchone()
    finally:
        conn.close()

    if row is None:
        return None
    return Transcript(text=row[0], language=row[1], duration_seconds=row[2],
                      model=row[3], cached=True)


def _store(message_id: str, chat_jid: str, transcript: Transcript) -> None:
    conn = _connect()
    try:
        conn.execute(
            """INSERT OR REPLACE INTO transcriptions
               (message_id, chat_jid, text, language, duration_seconds, model,
                transcribed_at)
               VALUES (?, ?, ?, ?, ?, ?, ?)""",
            (message_id, chat_jid, transcript.text, transcript.language,
             transcript.duration_seconds, transcript.model,
             time.strftime("%Y-%m-%d %H:%M:%S")),
        )
        conn.commit()
    finally:
        conn.close()


def transcribe_file(path: str, language: Optional[str] = None) -> Transcript:
    """Transcribe an audio file. Loads the model on first use.

    mlx_whisper caches the loaded model internally, so the multi-second load
    cost is paid once per process and idle servers never pay it at all.
    """
    import mlx_whisper  # imported lazily so the server starts without the model

    kwargs = {"path_or_hf_repo": MODEL_REPO, "verbose": False}
    if language:
        kwargs["language"] = language

    result = mlx_whisper.transcribe(path, **kwargs)
    segments = result.get("segments") or []

    return Transcript(
        text=result["text"].strip(),
        language=result.get("language", "unknown"),
        duration_seconds=segments[-1]["end"] if segments else 0.0,
        model=MODEL_REPO,
        cached=False,
    )


def transcribe_message(message_id: str, chat_jid: str, media_path: str,
                       language: Optional[str] = None) -> Transcript:
    """Transcribe a voice message, reusing a cached result when there is one."""
    cached = get_cached(message_id, chat_jid)
    if cached is not None:
        return cached

    transcript = transcribe_file(media_path, language=language)
    _store(message_id, chat_jid, transcript)
    return transcript
