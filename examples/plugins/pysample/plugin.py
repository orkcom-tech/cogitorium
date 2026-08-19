"""A plugin whose page model comes from Python.

The protocol is four bytes of big-endian length, then that many bytes of JSON,
in both directions. A hello frame first, naming the contract this was written
against — the host refuses a mismatch rather than discovering it mid-call.

An answer is wrapped: {"response": {...}}. The same channel also carries
{"host": {...}} — a plugin asking the host for the clock, its own storage, a
rendered template — and the host answers on the same pipe, so the wrapper is
what tells the two apart.

Standard library only, deliberately: this file is the proof that the tier needs
nothing installed beyond the interpreter the host fetched.
"""

import json
import struct
import sys
import platform

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
    body = sys.stdin.buffer.read(size)
    if len(body) < size:
        return None
    return json.loads(body)


def main():
    send({"contract": CONTRACT, "plugin": "pysample"})

    while True:
        request = receive()
        if request is None:
            return

        viewer = (request.get("ctx") or {}).get("viewer") or {}
        send({"response": {
            "data": {
                "answer": "This sentence was written by Python, on the server.",
                "viewer": viewer.get("name") or "nobody",
                "runtime": "CPython " + platform.python_version(),
            }
        }})


if __name__ == "__main__":
    main()
