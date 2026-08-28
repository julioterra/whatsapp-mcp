"""Tests for config.py.

Run directly: `uv run python test_config.py`

Configuration resolves at import time, so each case runs in its own
subprocess with its own environment — which is also how the servers start.
"""
import json
import os
import shutil
import subprocess
import sys
import tempfile

HERE = os.path.dirname(os.path.abspath(__file__))
BRIDGE_DIR = os.path.normpath(os.path.join(HERE, "..", "whatsapp-bridge"))
TMP = tempfile.mkdtemp(prefix="whatsapp-config-test-")

CONF = os.path.join(TMP, "instances.conf")
with open(CONF, "w") as fh:
    fh.write(
        '# name, port, number, "description"\n'
        'personal, 8080, 15555550134, "Personal US number, family and friends"\n'
        'work,     8081, -,           "Work number, colleagues and clients"\n'
        'nonumber, 8082, "My US number, family"\n'
        'loose,8083,15555550199,Unquoted description\n' 
    )

failures = []


def check(label, condition, detail=""):
    print(f"  {'PASS' if condition else 'FAIL'}  {label}" + (f"  ({detail})" if detail else ""))
    if not condition:
        failures.append(label)


def resolve(**env):
    """Import config in a clean subprocess and return what it resolved to."""
    child = {k: v for k, v in os.environ.items() if not k.startswith("WHATSAPP_")}
    child.update(env)

    out = subprocess.run(
        [sys.executable, "-c",
         "import json, config; print(json.dumps({"
         "'name': config.INSTANCE_NAME,"
         "'server': config.SERVER_NAME,"
         "'description': config.INSTANCE_DESCRIPTION,"
         "'store': config.STORE_DIR,"
         "'messages': config.MESSAGES_DB_PATH,"
         "'transcripts': config.TRANSCRIPTS_DB_PATH,"
         "'bridge': config.BRIDGE_URL,"
         "'account': config.linked_account(),"
         "'instructions': config.server_instructions()}))"],
        cwd=HERE, env=child, capture_output=True, text=True, check=True,
    )
    return json.loads(out.stdout)


print("an install that configures nothing is unaffected")
d = resolve()
check("instance name", d["name"] == "whatsapp", d["name"])
check("server name", d["server"] == "whatsapp", d["server"])
check("original store path",
      d["messages"] == os.path.join(BRIDGE_DIR, "store", "messages.db"), d["messages"])
check("default bridge", d["bridge"] == "http://localhost:8080", d["bridge"])

print("\nnaming an account in instances.conf")
p = resolve(WHATSAPP_INSTANCES_FILE=CONF, WHATSAPP_INSTANCE_NAME="personal")
check("store derived from the name",
      p["store"] == os.path.join(BRIDGE_DIR, "store-personal"), p["store"])
check("port comes from the conf file", p["bridge"] == "http://localhost:8080", p["bridge"])
check("description comes from the conf file",
      p["description"] == "Personal US number, family and friends", p["description"])
check("Claude sees a prefixed name", p["server"] == "whatsapp-personal", p["server"])

w = resolve(WHATSAPP_INSTANCES_FILE=CONF, WHATSAPP_INSTANCE_NAME="work")
check("second account gets its own port", w["bridge"] == "http://localhost:8081", w["bridge"])
check("second account gets its own store", w["store"] != p["store"])
check("second account gets its own transcripts", w["transcripts"] != p["transcripts"])

print("\nthe file forgives how you write it")
check("commas inside a quoted description survive",
      p["description"] == "Personal US number, family and friends", p["description"])

n = resolve(WHATSAPP_INSTANCES_FILE=CONF, WHATSAPP_INSTANCE_NAME="nonumber")
check("a skipped number is not eaten from the description",
      n["description"] == "My US number, family", n["description"])
check("no number claimed for it", "number" not in n["account"])

lo = resolve(WHATSAPP_INSTANCES_FILE=CONF, WHATSAPP_INSTANCE_NAME="loose")
check("no spaces around commas is fine", lo["bridge"] == "http://localhost:8083", lo["bridge"])
check("quotes are optional", lo["description"] == "Unquoted description", lo["description"])

print("\nthe label is reported, but so is the real number")
store = os.path.join(BRIDGE_DIR, "store-linked")
os.makedirs(store, exist_ok=True)
try:
    with open(os.path.join(store, "instance.json"), "w") as fh:
        json.dump({"instance": "personal", "number": "15555550134",
                   "push_name": "Test"}, fh)

    linked = resolve(WHATSAPP_INSTANCES_FILE=CONF, WHATSAPP_INSTANCE_NAME="personal",
                     WHATSAPP_STORE_DIR="store-linked")
    check("linked number is read back", linked["account"].get("number") == "15555550134")
    check("instructions state the real number", "+15555550134" in linked["instructions"])
finally:
    shutil.rmtree(store, ignore_errors=True)

unlinked = resolve(WHATSAPP_INSTANCES_FILE=CONF, WHATSAPP_INSTANCE_NAME="personal")
check("no number claimed before linking", "+" not in unlinked["instructions"].split(".")[0])

print("\nwhat the model is told")
i = p["instructions"]
check("names the account", "personal" in i)
check("includes the description", "family and friends" in i)
check("states it is read-only", "read-only" in i)
check("warns other accounts exist", "Other WhatsApp servers" in i)
check("warns the same person can appear on both", "same person can appear" in i)
check("tells it to check the others before concluding absence", "does not exist" in i)
check("asks it to attribute findings", "which account" in i)

print("\nenvironment overrides the conf file")
o = resolve(WHATSAPP_INSTANCES_FILE=CONF, WHATSAPP_INSTANCE_NAME="personal",
            WHATSAPP_BRIDGE_URL="http://localhost:9999/",
            WHATSAPP_STORE_DIR="/tmp/elsewhere")
check("bridge url overridden and de-slashed", o["bridge"] == "http://localhost:9999", o["bridge"])
check("store overridden", o["messages"] == "/tmp/elsewhere/messages.db", o["messages"])

print("\nan unknown name falls back rather than crashing")
u = resolve(WHATSAPP_INSTANCES_FILE=CONF, WHATSAPP_INSTANCE_NAME="nosuch")
check("still starts", u["name"] == "nosuch")
check("uses the default store", u["store"] == os.path.join(BRIDGE_DIR, "store"), u["store"])
check("uses the default port", u["bridge"] == "http://localhost:8080")

shutil.rmtree(TMP, ignore_errors=True)

print()
if failures:
    print(f"FAILED: {len(failures)} check(s): {', '.join(failures)}")
    sys.exit(1)
print("all checks passed")
