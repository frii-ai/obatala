#!/usr/bin/env python3
"""Reference bot for the conformance suite (HTTP adapter protocol, sync).

ECHO_MODE selects the behavior:
  echo    reply "You said: <text>"            (default)
  silent  reply with an empty replies array   (every turn is an error)
  slow    echo after 300 ms                   (latency budget case)
  broken  answer HTTP 500                     (transport error case)

Run: ECHO_MODE=echo PORT=8090 python3 echo_bot.py
"""
import json
import os
import time
from http.server import BaseHTTPRequestHandler, ThreadingHTTPServer

MODE = os.environ.get("ECHO_MODE", "echo")


class Handler(BaseHTTPRequestHandler):
    def do_POST(self):
        body = json.loads(self.rfile.read(int(self.headers.get("Content-Length", 0))) or b"{}")
        if MODE == "broken":
            self.send_response(500)
            self.end_headers()
            return
        if MODE == "slow":
            time.sleep(0.3)
        replies = [] if MODE == "silent" else [{"text": "You said: " + body.get("text", "")}]
        data = json.dumps({"replies": replies}).encode()
        self.send_response(200)
        self.send_header("Content-Type", "application/json")
        self.send_header("Content-Length", str(len(data)))
        self.end_headers()
        self.wfile.write(data)

    def log_message(self, *args):
        pass


if __name__ == "__main__":
    ThreadingHTTPServer(("127.0.0.1", int(os.environ.get("PORT", "8090"))), Handler).serve_forever()
