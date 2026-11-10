#!/usr/bin/env python3
"""A tiny support bot that speaks Obatala's HTTP adapter protocol.

Standard library only. It shows what a bot in any language exposes so the
obatala binary can test it:

  POST /obatala/messages                  one customer message -> replies
  GET  /obatala/meta                      preflight (test mode only)
  PUT  /obatala/fixtures/{key}            seed test data
  DELETE /obatala/fixtures/{key}          remove it
  DELETE /obatala/identities/{id}         forget one synthetic user
  GET  /obatala/fixtures/{key}/system-prompt   (optional, for reconcile)

The "bot" is rule-based so the example runs without a model; a real bot
would call its LLM where reply() is.

Run:  python3 bot.py          (listens on 127.0.0.1:8080)
"""
import json
import os
import re
import threading
from http.server import BaseHTTPRequestHandler, ThreadingHTTPServer

LOCK = threading.Lock()
FIXTURES = {}      # key -> fixture document (seeded by obatala)
HISTORY = {}       # session_id -> list of (role, text)
TOKEN = os.environ.get("BOT_CONTROL_TOKEN", "")


def reply(fixture, history, text):
    """Answer one customer message from the seeded store data."""
    store = FIXTURES.get(fixture, {})
    name = store.get("name", "our store")
    plans = store.get("plans", [])
    lower = text.lower()
    if any(w in lower for w in ("includ", "feature", "come with", "what do i get", "what's in", "offer")):
        feats = " and ".join(store.get("features", [])) or "a shared inbox"
        return [f"Every plan includes {feats}."]
    if any(w in lower for w in ("price", "cost", "how much", "plan")) and plans:
        cheapest = min(plans, key=lambda p: p["price_per_seat"])
        return [f"{name} plans start at ${cheapest['price_per_seat']} per seat per month "
                f"on the {cheapest['name']} plan."]
    if len(history) <= 1:
        return [f"Hi! Welcome to {name}. How can I help?"]
    return ["Happy to help with pricing or features. What would you like to know?"]


def system_prompt(key):
    store = FIXTURES.get(key)
    if store is None:
        return None
    return f"You are the support assistant for {store.get('name')}. Plans: {json.dumps(store.get('plans', []))}."


class Handler(BaseHTTPRequestHandler):
    def _send(self, status, body=None):
        data = b"" if body is None else json.dumps(body).encode()
        self.send_response(status)
        if body is not None:
            self.send_header("Content-Type", "application/json")
        self.send_header("Content-Length", str(len(data)))
        self.end_headers()
        self.wfile.write(data)

    def _json(self):
        n = int(self.headers.get("Content-Length", 0))
        return json.loads(self.rfile.read(n) or b"{}")

    def _authorized(self):
        return not TOKEN or self.headers.get("Authorization") == f"Bearer {TOKEN}"

    def do_GET(self):
        if not self._authorized():
            return self._send(401)
        if self.path == "/obatala/meta":
            return self._send(200, {"test_mode": True, "environment": "local", "bot_model": "rules-v1"})
        m = re.fullmatch(r"/obatala/fixtures/([a-z0-9_-]+)/system-prompt", self.path)
        if m:
            with LOCK:
                prompt = system_prompt(m.group(1))
            return self._send(200, {"system_prompt": prompt}) if prompt else self._send(404)
        self._send(404)

    def do_POST(self):
        if self.path != "/obatala/messages":
            return self._send(404)
        req = self._json()
        with LOCK:
            hist = HISTORY.setdefault(req["session_id"], [])
            hist.append(("customer", req["text"]))
            out = reply(req.get("fixture", ""), hist, req["text"])
            hist.extend(("bot", t) for t in out)
        self._send(200, {"replies": [{"text": t} for t in out]})

    def do_PUT(self):
        if not self._authorized():
            return self._send(401)
        m = re.fullmatch(r"/obatala/fixtures/([a-z0-9_-]+)", self.path)
        if not m:
            return self._send(404)
        with LOCK:
            FIXTURES[m.group(1)] = self._json()
        self._send(200, {"meta": {}})

    def do_DELETE(self):
        if not self._authorized():
            return self._send(401)
        m = re.fullmatch(r"/obatala/(fixtures|identities)/([A-Za-z0-9_-]+)", self.path)
        if not m:
            return self._send(404)
        with LOCK:
            if m.group(1) == "fixtures":
                FIXTURES.pop(m.group(2), None)
            # Identities: this bot keys history by session_id, so there is
            # nothing per-identity to forget; a real bot deletes the user.
        self._send(204)

    def log_message(self, *args):
        pass


if __name__ == "__main__":
    port = int(os.environ.get("PORT", "8080"))
    print(f"bot listening on http://127.0.0.1:{port}")
    ThreadingHTTPServer(("127.0.0.1", port), Handler).serve_forever()
