#!/usr/bin/env python3
"""A local LLM endpoint that refuses tool calls and forwards everything else.

Usage: UPSTREAM=<real base URL> python3 notoolsproxy.py [port]
Then point REVIEW_MCP_LLM_BASE_URL at http://127.0.0.1:<port> for one run.
A chat request carrying "tools" gets HTTP 400 "tools are not supported";
any other request is forwarded unchanged (with its own Authorization header)
to UPSTREAM + path. Each refusal and each forward is counted on stderr; no
body, header or key is printed.
"""
import json, os, sys, urllib.error, urllib.request
from http.server import BaseHTTPRequestHandler, ThreadingHTTPServer

UP = os.environ["UPSTREAM"].rstrip("/")
count = {"refused": 0, "forwarded": 0}


class H(BaseHTTPRequestHandler):
    def log_message(self, *a):
        pass

    def _send(self, code, body, ctype="application/json"):
        self.send_response(code)
        self.send_header("Content-Type", ctype)
        self.send_header("Content-Length", str(len(body)))
        self.end_headers()
        self.wfile.write(body)

    def _forward(self, method, body=None):
        hdr = {k: v for k, v in self.headers.items() if k.lower() in ("authorization", "content-type", "accept")}
        req = urllib.request.Request(UP + self.path, data=body, method=method, headers=hdr)
        try:
            with urllib.request.urlopen(req, timeout=900) as r:
                self._send(r.status, r.read(), r.headers.get("Content-Type", "application/json"))
        except urllib.error.HTTPError as e:
            self._send(e.code, e.read(), e.headers.get("Content-Type", "application/json"))
        count["forwarded"] += 1
        print(f"forwarded {method} (total {count['forwarded']})", file=sys.stderr, flush=True)

    def do_GET(self):
        self._forward("GET")

    def do_POST(self):
        body = self.rfile.read(int(self.headers.get("Content-Length", "0")))
        try:
            has_tools = bool(json.loads(body).get("tools"))
        except ValueError:
            has_tools = False
        if has_tools:
            count["refused"] += 1
            print(f"refused a request with tools (total {count['refused']})", file=sys.stderr, flush=True)
            self._send(400, json.dumps({"error": {"message": "tools are not supported by this model",
                                                  "type": "invalid_request_error"}}).encode())
            return
        self._forward("POST", body)


port = int(sys.argv[1]) if len(sys.argv) > 1 else 8099
print(f"listening on 127.0.0.1:{port}", file=sys.stderr, flush=True)
ThreadingHTTPServer(("127.0.0.1", port), H).serve_forever()
