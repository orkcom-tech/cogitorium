"""Schedules its own work, and reports what that work left behind."""

import base64
import json
import struct
import sys


def send(o):
    b = json.dumps(o).encode()
    sys.stdout.buffer.write(struct.pack(">I", len(b)))
    sys.stdout.buffer.write(b)
    sys.stdout.buffer.flush()


def receive():
    h = sys.stdin.buffer.read(4)
    if len(h) < 4:
        return None
    (n,) = struct.unpack(">I", h)
    return json.loads(sys.stdin.buffer.read(n))


def host(call, payload):
    send({"host": {"call": call, "input": payload}})
    r = receive()
    if r is None:
        raise SystemExit(0)
    return r.get("output") or {}, r.get("error") or ""


def main():
    send({"contract": 1, "plugin": "ticker"})
    while True:
        req = receive()
        if req is None:
            return

        # A task the queue runs later calls back in with role "event".
        if req.get("role") == "event":
            host("kv", {"op": "incr", "key": "ran", "by": 1})
            send({"response": {"data": {}}})
            continue

        out, err = host("enqueue", {"export": "sweep", "after": 0})
        ran, _ = host("kv", {"op": "get", "key": "ran"})

        send({"response": {"data": {
            "queued": err or ("unit %s" % out.get("id")),
            "ran": base64.b64decode(ran["value"]).decode() if ran.get("found") else "not yet",
        }}})


if __name__ == "__main__":
    main()
