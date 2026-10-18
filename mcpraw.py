#!/usr/bin/env python3
"""One MCP tools/call with exactly the given arguments (nothing added).

Usage: mcpraw.py TOOL 'JSON_ARGS' OUT.json
The server is `npx -y @nevzatcirak/review-mcp@next` unless RM_CMD is set; it
inherits this environment. Its stderr goes to $RMLOG (default mcpraw.log).
"""
import json, os, shlex, subprocess, sys

tool, args, out = sys.argv[1], json.loads(sys.argv[2]), sys.argv[3]
cmd = shlex.split(os.environ.get("RM_CMD", "npx -y @nevzatcirak/review-mcp@next"))
p = subprocess.Popen(cmd, stdin=subprocess.PIPE, stdout=subprocess.PIPE,
                     stderr=open(os.environ.get("RMLOG", "mcpraw.log"), "w"), text=True, bufsize=1)


def send(o):
    p.stdin.write(json.dumps(o) + "\n")
    p.stdin.flush()


def recv(want):
    for line in p.stdout:
        m = json.loads(line)
        if m.get("id") == want:
            return m
    sys.exit("server closed the connection")


send({"jsonrpc": "2.0", "id": 1, "method": "initialize",
      "params": {"protocolVersion": "2025-06-18", "capabilities": {}, "clientInfo": {"name": "mcpraw", "version": "1"}}})
recv(1)
send({"jsonrpc": "2.0", "method": "notifications/initialized"})
send({"jsonrpc": "2.0", "id": 2, "method": "tools/call", "params": {"name": tool, "arguments": args}})
res = recv(2)
p.stdin.close()
p.terminate()
json.dump(res, open(out, "w"), indent=1)
print("saved", out)
