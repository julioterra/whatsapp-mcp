"""Per-instance configuration.

An instance is one WhatsApp account: a bridge and an MCP server sharing a
name, a port and a store directory. Settings come from three places, in
order of precedence:

  1. Environment variables, for one-off overrides.
  2. whatsapp-bridge/instances.conf, the file you normally edit.
  3. Defaults that reproduce the original single-account layout, so an
     install that configures nothing keeps working.

Naming a `personal` instance gives you store-personal/, a server Claude sees
as "whatsapp-personal", and whatever port and description the conf line says.
"""
import json
import os

_BRIDGE_DIR = os.path.join(
    os.path.dirname(os.path.abspath(__file__)), "..", "whatsapp-bridge"
)
_INSTANCES_FILE = os.environ.get(
    "WHATSAPP_INSTANCES_FILE", os.path.join(_BRIDGE_DIR, "instances.conf")
)


# Shortest run of digits still treated as a phone number rather than the first
# word of a description. Real numbers with a country code are longer.
_MIN_PHONE_DIGITS = 7


def _digits(text: str) -> str:
    return "".join(c for c in text if c.isdigit())


def _unquote(field: str) -> str:
    """Trim whitespace and a matching pair of double quotes."""
    field = field.strip()
    if len(field) >= 2 and field.startswith('"') and field.endswith('"'):
        field = field[1:-1]
    return field.strip()


def _read_instance(name):
    """Return the instances.conf entry for `name`, or an empty dict.

    Lines are: name, port, number, "description". The description is quoted
    so its own commas are fine; an empty number or "-" means don't check.
    """
    try:
        with open(_INSTANCES_FILE) as fh:
            lines = fh.readlines()
    except OSError:
        return {}

    for line in lines:
        line = line.strip()
        if not line or line.startswith("#"):
            continue

        # At most four parts, so commas inside the description survive.
        parts = line.split(",", 3)
        if _unquote(parts[0]) != name or len(parts) < 2:
            continue
        entry = {"port": _unquote(parts[1])}

        # The number is optional. Treat it as one only if it looks like one,
        # so `personal, 8080, "My US number"` keeps its description.
        rest = parts[2:]
        if rest:
            third = _unquote(rest[0])
            if third in ("", "-"):
                rest = rest[1:]
            elif len(_digits(third)) >= _MIN_PHONE_DIGITS:
                entry["number"] = third
                rest = rest[1:]
        if rest:
            entry["description"] = _unquote(",".join(rest))
        return entry
    return {}


INSTANCE_NAME = os.environ.get("WHATSAPP_INSTANCE_NAME", "whatsapp")

_entry = _read_instance(INSTANCE_NAME)

# What Claude sees in its list of servers. Prefixed so a bare "work" is not
# ambiguous among other MCP servers.
SERVER_NAME = (
    INSTANCE_NAME if INSTANCE_NAME.startswith("whatsapp")
    else f"whatsapp-{INSTANCE_NAME}"
)

INSTANCE_DESCRIPTION = os.environ.get(
    "WHATSAPP_INSTANCE_DESCRIPTION", _entry.get("description", "")
)


def _resolve_store_dir() -> str:
    """Locate the store, matching how the bridge resolves the same setting.

    The bridge opens its store relative to its own working directory, which is
    whatsapp-bridge/. Resolving relative values the same way here means one
    setting works for both processes.
    """
    configured = os.environ.get("WHATSAPP_STORE_DIR")
    if configured is None:
        # An instance from the conf file gets store-<name>; the unnamed
        # default keeps the original store/.
        configured = f"store-{INSTANCE_NAME}" if _entry else "store"
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


def _resolve_bridge_url() -> str:
    explicit = os.environ.get("WHATSAPP_BRIDGE_URL")
    if explicit:
        return explicit.rstrip("/")
    port = _entry.get("port", "8080")
    return f"http://localhost:{port}"


BRIDGE_URL = _resolve_bridge_url()

WHISPER_MODEL = os.environ.get(
    "WHATSAPP_WHISPER_MODEL", "mlx-community/whisper-large-v3-turbo"
)


def linked_account() -> dict:
    """The account this store is actually linked to, as recorded at login.

    Written by the bridge once it knows which phone answered the QR code, so
    this reports the real number rather than trusting the label.
    """
    try:
        with open(os.path.join(STORE_DIR, "instance.json")) as fh:
            return json.load(fh)
    except (OSError, ValueError):
        return {}


def server_instructions() -> str:
    """Tell the model which account this is and how it relates to the others."""
    account = linked_account()

    identity = f"This server reads the '{INSTANCE_NAME}' WhatsApp account."
    if INSTANCE_DESCRIPTION:
        identity += f" {INSTANCE_DESCRIPTION}."
    if account.get("number"):
        identity += f" It is linked to the phone number +{account['number']}."

    return " ".join([
        identity,
        "It is read-only: it cannot send, reply, react, or otherwise write to "
        "WhatsApp.",

        "Other WhatsApp servers may also be connected, each for a different "
        "account. They hold entirely separate messages, chats and contacts.",

        # The overlap is the part that is easy to get wrong: the accounts are
        # separate, the people are not.
        "The same person can appear on more than one account — a colleague who "
        "is also a friend, or someone who has both of your numbers. So a "
        "conversation with somebody may be split across accounts, and what you "
        "see here can be only part of it.",

        "Before concluding that a person, chat or message does not exist, or "
        "that you have someone's full history, check the other WhatsApp "
        "servers too. When you report what you found, say which account it "
        "came from.",
    ])
