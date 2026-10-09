"""Parity oracle: run the REAL Python server's formatting against a fixture DB.

This imports whatsapp-mcp-server/whatsapp.py directly (no uv, no MCP runtime
needed) and points MESSAGES_DB_PATH at the database given on the command line,
then prints a JSON blob of the outputs the Go port must reproduce.

    python3 dev/parity_oracle.py <path-to-messages.db>

Used only as a test oracle; not part of the shipped program.
"""
import importlib.util
import json
import sys
import os

if len(sys.argv) < 2:
    print("usage: parity_oracle.py <messages.db>", file=sys.stderr)
    sys.exit(2)

DB_PATH = os.path.abspath(sys.argv[1])
REPO_ROOT = os.path.dirname(os.path.dirname(os.path.abspath(__file__)))
SERVER_DIR = os.path.join(REPO_ROOT, "whatsapp-mcp-server")

# whatsapp.py imports its sibling audio.py, so the server directory must be
# importable before the module is loaded.
sys.path.insert(0, SERVER_DIR)

# Load the real module from the repository rather than reimplementing it.
spec = importlib.util.spec_from_file_location(
    "whatsapp", os.path.join(SERVER_DIR, "whatsapp.py")
)
if spec is None or spec.loader is None:
    print("could not locate whatsapp-mcp-server/whatsapp.py", file=sys.stderr)
    sys.exit(1)

whatsapp = importlib.util.module_from_spec(spec)
spec.loader.exec_module(whatsapp)

# Redirect the module at the fixture database.
whatsapp.MESSAGES_DB_PATH = DB_PATH

ALICE = "15551234567@s.whatsapp.net"

def capture():
    """Call the real functions and collect their output."""
    out = {}

    # format_message on a media message, via get_message_context.
    ctx = whatsapp.get_message_context("m3", 1, 1)
    out["format_image"] = whatsapp.format_message(ctx.message)

    ctx2 = whatsapp.get_message_context("m2", 1, 1)
    out["format_own"] = whatsapp.format_message(ctx2.message)

    out["list_context"] = whatsapp.list_messages(
        chat_jid=ALICE, query="photo attached", limit=20,
        include_context=True, context_before=1, context_after=1,
    )
    out["empty"] = whatsapp.list_messages(
        chat_jid=ALICE, query="zzz-no-such-message", limit=20,
    )
    out["last_interaction"] = whatsapp.get_last_interaction(ALICE)

    # Structured results, serialised the way the Go tools return them.
    out["search_contacts"] = [
        {"phone_number": c.phone_number, "name": c.name, "jid": c.jid}
        for c in whatsapp.search_contacts("alice")
    ]

    chats = whatsapp.list_chats(query=None, limit=20, page=0,
                                include_last_message=True, sort_by="last_active")
    out["list_chats_last_active"] = [
        {"jid": c.jid, "name": c.name, "last_message": c.last_message}
        for c in chats
    ]

    chats_by_name = whatsapp.list_chats(query=None, limit=20, page=0,
                                        include_last_message=True, sort_by="name")
    out["list_chats_by_name"] = [
        {"jid": c.jid, "name": c.name} for c in chats_by_name
    ]

    chat = whatsapp.get_chat(ALICE, include_last_message=True)
    out["get_chat"] = {"jid": chat.jid, "name": chat.name, "last_message": chat.last_message}

    direct = whatsapp.get_direct_chat_by_contact("15551234567")
    out["direct_chat"] = {"jid": direct.jid, "name": direct.name}

    return out

print(json.dumps(capture()))
