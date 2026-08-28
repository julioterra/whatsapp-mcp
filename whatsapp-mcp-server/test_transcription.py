"""Tests for transcription.py.

Run directly: `uv run python test_transcription.py`

Speech is synthesised with macOS `say` rather than checked in as a fixture,
because the only real audio available here is personal messages and those must
not enter the repo.
"""
import os
import shutil
import subprocess
import sys
import tempfile

TMP = tempfile.mkdtemp(prefix="whatsapp-transcription-test-")
os.environ["WHATSAPP_TRANSCRIPTS_DB"] = os.path.join(TMP, "transcriptions.db")

sys.path.insert(0, os.path.dirname(os.path.abspath(__file__)))
import transcription  # noqa: E402 - must follow the env var

SPOKEN = "The quick brown fox jumps over the lazy dog."
MESSAGE_ID, CHAT_JID = "TESTMSG001", "test@s.whatsapp.net"

failures = []


def check(label, condition, detail=""):
    print(f"  {'PASS' if condition else 'FAIL'}  {label}" + (f"  ({detail})" if detail else ""))
    if not condition:
        failures.append(label)


def make_voice_note(path):
    """Produce an Ogg Opus file shaped like a WhatsApp voice note."""
    aiff = os.path.join(TMP, "speech.aiff")
    subprocess.run(["say", "-o", aiff, SPOKEN], check=True)
    subprocess.run(
        ["ffmpeg", "-nostdin", "-loglevel", "error", "-i", aiff,
         "-c:a", "libopus", "-ar", "48000", "-ac", "1", path],
        check=True,
    )


def main():
    for tool in ("say", "ffmpeg"):
        if shutil.which(tool) is None:
            print(f"SKIP: {tool} not available")
            return 0

    audio = os.path.join(TMP, "voice.ogg")
    make_voice_note(audio)
    print(f"synthesised {os.path.getsize(audio)} byte voice note\n")

    print("transcribe_file")
    result = transcription.transcribe_file(audio)
    spoken_words = result.text.lower()
    check("recognises the spoken words", "brown fox" in spoken_words, result.text.strip())
    check("detects English", result.language == "en", result.language)
    check("reports a duration", result.duration_seconds > 0, f"{result.duration_seconds:.1f}s")
    check("is not marked cached", result.cached is False)

    print("\ntranscribe_message caching")
    first = transcription.transcribe_message(MESSAGE_ID, CHAT_JID, audio)
    second = transcription.transcribe_message(MESSAGE_ID, CHAT_JID, audio)
    check("first call is a miss", first.cached is False)
    check("second call is a hit", second.cached is True)
    check("cached text is identical", first.text == second.text)

    print("\ncache keys on the model")
    original = transcription.MODEL_REPO
    transcription.MODEL_REPO = "mlx-community/whisper-tiny"
    check("different model misses", transcription.get_cached(MESSAGE_ID, CHAT_JID) is None)
    transcription.MODEL_REPO = original
    check("original model still hits", transcription.get_cached(MESSAGE_ID, CHAT_JID) is not None)

    print("\nunknown message")
    check("no transcript for an unseen id",
          transcription.get_cached("NOPE", CHAT_JID) is None)

    print()
    if failures:
        print(f"FAILED: {len(failures)} check(s): {', '.join(failures)}")
        return 1
    print("all checks passed")
    return 0


try:
    sys.exit(main())
finally:
    shutil.rmtree(TMP, ignore_errors=True)
