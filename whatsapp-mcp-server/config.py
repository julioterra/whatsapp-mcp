"""Per-instance configuration, read from the environment.

One checkout can serve several WhatsApp accounts by running a bridge and an
MCP server per account, each with its own name, port and store directory.
Every default here reproduces the original single-account layout, so an
install that sets nothing keeps working unchanged.

The matching bridge settings are WHATSAPP_INSTANCE_NAME, WHATSAPP_STORE_DIR
and WHATSAPP_BRIDGE_PORT; give both processes of one instance the same store
directory and the same port.
"""
import os

# Shown to the model as the MCP server name, so two accounts are
# distinguishable rather than appearing as two identical "whatsapp" servers.
INSTANCE_NAME = os.environ.get("WHATSAPP_INSTANCE_NAME", "whatsapp")

# Free text telling the model whose account this is ("Brazilian number,
# family and building contractors"). Worth setting when more than one
# instance is connected.
INSTANCE_DESCRIPTION = os.environ.get("WHATSAPP_INSTANCE_DESCRIPTION", "")

_BRIDGE_DIR = os.path.join(
    os.path.dirname(os.path.abspath(__file__)), "..", "whatsapp-bridge"
)


def _resolve_store_dir() -> str:
    """Locate the store, matching how the bridge resolves the same setting.

    The bridge opens its store relative to its own working directory, which is
    whatsapp-bridge/. Resolving a relative value the same way here means one
    WHATSAPP_STORE_DIR works for both processes.
    """
    configured = os.environ.get("WHATSAPP_STORE_DIR", "store")
    if os.path.isabs(configured):
        return configured
    return os.path.normpath(os.path.join(_BRIDGE_DIR, configured))


STORE_DIR = _resolve_store_dir()

MESSAGES_DB_PATH = os.environ.get(
    "WHATSAPP_MESSAGES_DB", os.path.join(STORE_DIR, "messages.db")
)

# Kept in its own database rather than in messages.db: the message store is
# disposable and gets deleted when a broken sync is reset, while transcription
# is the expensive part to redo.
TRANSCRIPTS_DB_PATH = os.environ.get(
    "WHATSAPP_TRANSCRIPTS_DB", os.path.join(STORE_DIR, "transcriptions.db")
)

BRIDGE_URL = os.environ.get("WHATSAPP_BRIDGE_URL", "http://localhost:8080").rstrip("/")

WHISPER_MODEL = os.environ.get(
    "WHATSAPP_WHISPER_MODEL", "mlx-community/whisper-large-v3-turbo"
)


def server_instructions() -> str:
    """Explain to the model which account it is talking to."""
    lines = [
        f"This server reads the '{INSTANCE_NAME}' WhatsApp account. "
        "It is read-only: it cannot send, reply, react, or otherwise write "
        "to WhatsApp."
    ]
    if INSTANCE_DESCRIPTION:
        lines.append(f"About this account: {INSTANCE_DESCRIPTION}")
    lines.append(
        "If several WhatsApp servers are connected, each covers a different "
        "account and they do not share messages or contacts. Check that you "
        "are querying the right one before concluding a person or chat does "
        "not exist."
    )
    return " ".join(lines)
