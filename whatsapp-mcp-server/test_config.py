"""Tests for config.py.

Run directly: `uv run python test_config.py`

Configuration resolves at import time, so each case runs in its own
subprocess with its own environment. That also matches how the servers
actually start.
"""
import json
import os
import subprocess
import sys

HERE = os.path.dirname(os.path.abspath(__file__))
BRIDGE_DIR = os.path.normpath(os.path.join(HERE, "..", "whatsapp-bridge"))

failures = []


def check(label, condition, detail=""):
    print(f"  {'PASS' if condition else 'FAIL'}  {label}" + (f"  ({detail})" if detail else ""))
    if not condition:
        failures.append(label)


def resolve(**env):
    """Import config in a clean subprocess and return what it resolved to."""
    child = dict(os.environ)
    for key in list(child):
        if key.startswith("WHATSAPP_"):
            del child[key]
    child.update(env)

    out = subprocess.run(
        [sys.executable, "-c",
         "import json, config; print(json.dumps({"
         "'name': config.INSTANCE_NAME,"
         "'store': config.STORE_DIR,"
         "'messages': config.MESSAGES_DB_PATH,"
         "'transcripts': config.TRANSCRIPTS_DB_PATH,"
         "'bridge': config.BRIDGE_URL,"
         "'model': config.WHISPER_MODEL,"
         "'instructions': config.server_instructions()}))"],
        cwd=HERE, env=child, capture_output=True, text=True, check=True,
    )
    return json.loads(out.stdout)


print("defaults reproduce the original single-account layout")
d = resolve()
check("instance name", d["name"] == "whatsapp", d["name"])
check("messages.db under whatsapp-bridge/store",
      d["messages"] == os.path.join(BRIDGE_DIR, "store", "messages.db"), d["messages"])
check("transcripts alongside it",
      d["transcripts"] == os.path.join(BRIDGE_DIR, "store", "transcriptions.db"))
check("bridge url", d["bridge"] == "http://localhost:8080", d["bridge"])

print("\nnaming an instance")
us = resolve(WHATSAPP_INSTANCE_NAME="whatsapp-us")
check("name is used", us["name"] == "whatsapp-us", us["name"])
check("instructions name the account", "whatsapp-us" in us["instructions"])
check("instructions state it is read-only", "read-only" in us["instructions"])

described = resolve(WHATSAPP_INSTANCE_NAME="whatsapp-brasil",
                    WHATSAPP_INSTANCE_DESCRIPTION="Brazilian number, family and contractors")
check("description reaches the model", "family and contractors" in described["instructions"])
check("description omitted when unset", "About this account" not in us["instructions"])

print("\nstore directory")
rel = resolve(WHATSAPP_STORE_DIR="store-brasil")
check("relative resolves against whatsapp-bridge/",
      rel["messages"] == os.path.join(BRIDGE_DIR, "store-brasil", "messages.db"), rel["messages"])

absolute = resolve(WHATSAPP_STORE_DIR="/tmp/whatsapp-us")
check("absolute used as given",
      absolute["messages"] == "/tmp/whatsapp-us/messages.db", absolute["messages"])

explicit = resolve(WHATSAPP_STORE_DIR="/tmp/ignored",
                   WHATSAPP_MESSAGES_DB="/tmp/elsewhere/messages.db")
check("explicit messages db wins over store dir",
      explicit["messages"] == "/tmp/elsewhere/messages.db", explicit["messages"])

print("\ntwo instances stay separate")
a = resolve(WHATSAPP_INSTANCE_NAME="whatsapp-us", WHATSAPP_STORE_DIR="store-us",
            WHATSAPP_BRIDGE_URL="http://localhost:8080")
b = resolve(WHATSAPP_INSTANCE_NAME="whatsapp-brasil", WHATSAPP_STORE_DIR="store-brasil",
            WHATSAPP_BRIDGE_URL="http://localhost:8081")
check("different message stores", a["messages"] != b["messages"])
check("different transcript caches", a["transcripts"] != b["transcripts"])
check("different bridges", a["bridge"] != b["bridge"])
check("neither points at the default store",
      os.path.join(BRIDGE_DIR, "store", "messages.db") not in (a["messages"], b["messages"]))

print("\nbridge url handling")
slashed = resolve(WHATSAPP_BRIDGE_URL="http://localhost:8081/")
check("trailing slash stripped", slashed["bridge"] == "http://localhost:8081", slashed["bridge"])

print("\nmodel override")
tiny = resolve(WHATSAPP_WHISPER_MODEL="mlx-community/whisper-tiny")
check("model is configurable", tiny["model"] == "mlx-community/whisper-tiny")

print()
if failures:
    print(f"FAILED: {len(failures)} check(s): {', '.join(failures)}")
    sys.exit(1)
print("all checks passed")
