"""Reaches one host it was granted, and one it was not."""

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


def host(call, payload):
    send({"host": {"call": call, "input": payload}})
    reply = receive()
    if reply is None:
        raise SystemExit(0)
    return reply.get("output") or {}, reply.get("error") or ""


def main():
    send({"contract": CONTRACT, "plugin": "fetcher"})
    while True:
        if receive() is None:
            return

        ok, ok_err = host("http", {"url": "https://api.github.com/zen"})
        if ok_err:
            granted = "REFUSED: " + ok_err
        else:
            body = base64.b64decode(ok.get("body") or "").decode(errors="replace")
            granted = "%s — %s" % (ok.get("status"), body.strip()[:60])

        # Not in hosts:. The operator never granted it, so this must not go out.
        _, bad_err = host("http", {"url": "https://example.com/"})
        denied = bad_err or "IT WENT OUT — that is a hole"

        send({"response": {"data": {"granted": granted, "denied": denied}}})


if __name__ == "__main__":
    main()
