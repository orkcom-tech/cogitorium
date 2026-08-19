"""Calls the subject it was granted, one it was not, and a write it was not."""

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
    send({"contract": 1, "plugin": "lister"})
    while True:
        if receive() is None:
            return

        ok, ok_err = host("api", {"method": "GET", "path": "/api/v1/workspaces"})
        if ok_err:
            granted = "REFUSED: " + ok_err
        else:
            body = base64.b64decode(ok.get("body") or "").decode(errors="replace")
            granted = "%s — %s" % (ok.get("status"), body.strip()[:70])

        _, no_subject = host("api", {"method": "GET", "path": "/api/v1/providers"})
        _, no_write = host("api", {"method": "POST", "path": "/api/v1/workspaces",
                                   "body": {"name": "should not happen"}})

        send({"response": {"data": {
            "granted": granted,
            "denied": no_subject or "IT WENT THROUGH — that is a hole",
            "write": no_write or "IT WROTE — that is a hole",
        }}})


if __name__ == "__main__":
    main()
