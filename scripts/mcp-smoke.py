#!/usr/bin/env python3
"""Minimal MCP stdio smoke test for `agentchat mcp`."""
import argparse
import json
import subprocess
import sys
import threading
import time

TOOLS = {
    "chat_send",
    "chat_inbox",
    "chat_wait",
    "chat_rooms",
    "chat_upload",
    "chat_download",
    "chat_who",
}


def main() -> int:
    parser = argparse.ArgumentParser()
    parser.add_argument("--bin", default="./bin/agentchat")
    parser.add_argument("--config", required=True)
    args = parser.parse_args()

    proc = subprocess.Popen(
        [args.bin, "mcp", "--config", args.config],
        stdin=subprocess.PIPE,
        stdout=subprocess.PIPE,
        stderr=subprocess.PIPE,
        text=True,
        bufsize=1,
    )

    next_id = 0

    def request(method: str, params: dict | None = None, timeout: float = 10.0) -> dict:
        nonlocal next_id
        next_id += 1
        msg = {"jsonrpc": "2.0", "id": next_id, "method": method}
        if params is not None:
            msg["params"] = params
        proc.stdin.write(json.dumps(msg) + "\n")
        proc.stdin.flush()
        deadline = time.time() + timeout
        while time.time() < deadline:
            line = proc.stdout.readline()
            if not line:
                break
            line = line.strip()
            if not line:
                continue
            try:
                reply = json.loads(line)
            except json.JSONDecodeError:
                continue
            if reply.get("id") == next_id:
                return reply
        raise TimeoutError(f"no response for {method}")

    try:
        init = request(
            "initialize",
            {
                "protocolVersion": "2025-06-18",
                "capabilities": {},
                "clientInfo": {"name": "agentchat-smoke", "version": "0"},
            },
        )
        assert "result" in init, init
        proc.stdin.write(json.dumps({"jsonrpc": "2.0", "method": "notifications/initialized"}) + "\n")
        proc.stdin.flush()

        tools = request("tools/list")
        names = {t["name"] for t in tools.get("result", {}).get("tools", [])}
        missing = TOOLS - names
        assert not missing, f"missing tools: {sorted(missing)}"

        rooms = request("tools/call", {"name": "chat_rooms", "arguments": {}})
        assert "result" in rooms, rooms

        inbox = request(
            "tools/call",
            {"name": "chat_inbox", "arguments": {"limit": 5}},
            timeout=15,
        )
        assert "result" in inbox, inbox

        print(json.dumps({"tools": sorted(names), "rooms": rooms["result"], "inbox": inbox["result"]}, ensure_ascii=False, indent=2))
        return 0
    finally:
        try:
            proc.stdin.close()
        except Exception:
            pass
        try:
            proc.wait(timeout=5)
        except subprocess.TimeoutExpired:
            proc.kill()
        stderr = proc.stderr.read()
        if stderr:
            print(stderr, file=sys.stderr)


if __name__ == "__main__":
    raise SystemExit(main())
