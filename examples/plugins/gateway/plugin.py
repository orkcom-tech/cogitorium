"""Calls every cog.* verb the host answers, and reports what came back."""

import base64
import json
import struct
import sys

CONTRACT = 1


def send(obj):
    body = json.dumps(obj).encode("utf-8")
    sys.stdout.buffer.write(struct.pack(">I", len(body)))
    sys.stdout.buffer.write(body)
    sys.stdout.buffer.flush()


def receive():
    header = sys.stdin.buffer.read(4)
    if len(header) < 4:
        return None
    (size,) = struct.unpack(">I", header)
    return json.loads(sys.stdin.buffer.read(size))


def host(call, payload=None):
    """One host call. The reply comes back on the same pipe, tagged."""
    send({"host": {"call": call, "input": payload if payload is not None else {}}})
    reply = receive()
    if reply is None:
        raise SystemExit(0)
    return reply.get("output") or {}, reply.get("error") or ""


def main():
    send({"contract": CONTRACT, "plugin": "gateway"})
    while True:
        request = receive()
        if request is None:
            return

        now, _ = host("now")
        rnd, _ = host("rand", {"max": 1000})
        cfg, _ = host("config")

        # A counter that survives a restart, which is the whole point of kv.
        visits, _ = host("kv", {"op": "incr", "key": "visits", "by": 1})
        # Bytes travel as base64: the host stores a value as bytes and does not
        # parse it, so its JSON dialect never becomes part of a plugin's data format.
        host("kv", {"op": "set", "key": "greeting",
                    "value": base64.b64encode(b"kept").decode()})
        got, _ = host("kv", {"op": "get", "key": "greeting"})

        html, render_err = host("render", {"template": "gateway.frag.hello"})

        send({"response": {"data": {
            "now": now.get("rfc3339", "?"),
            "rand": rnd.get("n", "?"),
            "config": json.dumps(cfg),
            "visits": visits.get("value", "?"),
            "stored": base64.b64decode(got["value"]).decode() if got.get("found") else "(absent)",
            "rendered": html.get("html") or render_err,
        }}})


if __name__ == "__main__":
    main()
