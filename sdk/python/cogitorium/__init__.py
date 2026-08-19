"""Writing a Cogitorium plugin in Python.

The protocol is small enough to implement by hand — four bytes of length, then
JSON — and every author implementing it by hand is every author getting the
same three things wrong: forgetting to flush, reading the body in one call that
can return short, and answering without the frame wrapper.

Standard library only, and one file. A plugin is a directory somebody reads
before approving it, so a dependency here would be a dependency in everybody's
review.

    from cogitorium import Plugin

    plugin = Plugin("myplugin")

    @plugin.provider("home")
    def home(request, host):
        return {"greeting": "Hello, " + request.viewer_name}

    plugin.run()
"""

import base64
import json
import struct
import sys

__all__ = ["Plugin", "Request", "Host", "CONTRACT"]

# The ABI generation this SDK speaks. The host refuses a mismatch at the
# handshake rather than discovering it mid-call, which is why it is sent first.
CONTRACT = 1

_MAX_FRAME = 8 << 20


class Request:
    """One call from the host."""

    def __init__(self, raw):
        self.raw = raw
        self.export = raw.get("export") or ""
        self.role = raw.get("role") or ""
        self.input = raw.get("input")
        ctx = raw.get("ctx") or {}
        viewer = ctx.get("viewer") or {}
        self.viewer_name = viewer.get("name") or ""
        self.viewer_is_admin = bool(viewer.get("is_admin"))
        self.signed_in = bool(viewer.get("signed_in"))
        self.workspace = ctx.get("workspace")
        self.http = raw.get("http") or {}

    def args(self):
        """The JSON body of a task, decoded. Empty dict when there is none."""
        if self.input in (None, b"", ""):
            return {}
        if isinstance(self.input, (bytes, bytearray)):
            return json.loads(self.input)
        if isinstance(self.input, str):
            # Bytes arrive base64-encoded, because that is how the host's JSON
            # carries them.
            return json.loads(base64.b64decode(self.input))
        return self.input


class HostError(Exception):
    """The host refusing, in its own words.

    An exception rather than a returned pair, because a refusal is nearly
    always something the plugin cannot continue past — and an author who wants
    to continue writes a try, which is visible, instead of an ignored second
    return value, which is not.
    """


class Host:
    """What a plugin may ask the host for.

    Every method is one cog.* call. They exist on every tier with identical
    behaviour, so a plugin that outgrows Python and is rewritten in Rust calls
    the same nine things.
    """

    def __init__(self, channel):
        self._channel = channel

    def _call(self, name, payload=None):
        out, err = self._channel(name, payload if payload is not None else {})
        if err:
            raise HostError(err)
        return out

    # ── the nine ─────────────────────────────────────────────────────────
    def log(self, message):
        """Write to the server's log, tagged with this plugin."""
        self._call("log", message if isinstance(message, str) else json.dumps(message))

    def now(self):
        """The host's clock, as an RFC3339 string.

        The host's rather than this process's so that `plugins invoke` can pin
        it — a plugin reading its own clock cannot be reproduced in a test.
        """
        return self._call("now")["rfc3339"]

    def rand(self, max_exclusive):
        """A random integer in [0, max_exclusive). Pinnable, like now()."""
        return self._call("rand", {"max": max_exclusive})["n"]

    def config(self):
        """What the operator set for this plugin. Read-only, and often empty."""
        return self._call("config")

    def render(self, template, data=None):
        """Render one of this plugin's templates through the layer stack.

        Through the stack, so another plugin's override of the same name is
        what you get — which is the point, and rendering your own file in
        isolation would be quietly wrong.
        """
        return self._call("render", {"template": template, "data": data})["html"]

    def http(self, url, method="GET", headers=None, body=b""):
        """One outbound request, through the host's gate.

        Only hosts listed under `hosts:` in plugin.yaml. The refusal names both
        what you asked for and what you were granted.
        """
        out = self._call("http", {
            "url": url, "method": method, "headers": headers or {},
            "body": base64.b64encode(_as_bytes(body)).decode(),
        })
        return {
            "status": out["status"],
            "headers": out.get("headers") or {},
            "body": base64.b64decode(out.get("body") or ""),
        }

    def api(self, path, method="GET", body=None):
        """Call this server's own API, as this plugin rather than as anybody.

        Only subjects listed under `api:` in plugin.yaml. A write grant implies
        the matching read.
        """
        out = self._call("api", {"method": method, "path": path, "body": body})
        return {"status": out["status"], "body": base64.b64decode(out.get("body") or "")}

    def enqueue(self, export, args=None, after=0, key=""):
        """Run one of your own exports later, on the host's durable queue.

        `key` makes it idempotent, so enqueuing on every start does not
        accumulate a task per restart.
        """
        return self._call("enqueue", {
            "export": export, "args": args, "after": after, "key": key,
        })

    # ── storage ──────────────────────────────────────────────────────────
    def get(self, key):
        """The stored bytes, or None. Absent is a value, not an error."""
        out = self._call("kv", {"op": "get", "key": key})
        if not out.get("found"):
            return None
        return base64.b64decode(out["value"])

    def set(self, key, value):
        self._call("kv", {"op": "set", "key": key,
                          "value": base64.b64encode(_as_bytes(value)).decode()})

    def delete(self, key):
        self._call("kv", {"op": "delete", "key": key})

    def incr(self, key, by=1):
        """Increment a counter in one statement, so two callers cannot lose one
        of their increments to a read-modify-write race."""
        return int(self._call("kv", {"op": "incr", "key": key, "by": by})["value"])

    def keys(self, prefix=""):
        out = self._call("kv", {"op": "list", "prefix": prefix})
        return [row["key"] for row in out.get("keys") or []]

    def compare_and_set(self, key, value, version):
        """Write only if the stored version is still what you read.

        Version rather than value: two writers who happen to write identical
        bytes are two writers, and a value comparison cannot tell them apart.
        Pass version=0 to mean "only if it does not exist yet".
        """
        return bool(self._call("kv", {
            "op": "cas", "key": key, "version": version,
            "value": base64.b64encode(_as_bytes(value)).decode(),
        }).get("swapped"))


class Plugin:
    """A plugin, and the loop that serves it."""

    def __init__(self, plugin_id):
        self.id = plugin_id
        self._exports = {}

    def provider(self, name):
        """Register an export that supplies a page's model.

        Return a dict. It becomes `.Data` in the template.
        """
        def register(fn):
            self._exports[name] = fn
            return fn
        return register

    # A task is the same shape and a different word, because an author reading
    # their own file should be able to tell which of their functions answer a
    # person and which do background work.
    task = provider

    def run(self, stdin=None, stdout=None):
        """Serve until the host closes the pipe."""
        stdin = stdin or sys.stdin.buffer
        stdout = stdout or sys.stdout.buffer

        _write(stdout, {"contract": CONTRACT, "plugin": self.id})

        def channel(call, payload):
            _write(stdout, {"host": {"call": call, "input": payload}})
            reply = _read(stdin)
            if reply is None:
                raise SystemExit(0)
            return reply.get("output") or {}, reply.get("error") or ""

        host = Host(channel)

        while True:
            raw = _read(stdin)
            if raw is None:
                return
            request = Request(raw)

            fn = self._exports.get(request.export)
            if fn is None:
                # Named, with what does exist. An author whose export is not
                # called has otherwise no way to tell a typo from a host that
                # never asked.
                _write(stdout, {"response": {"error": "%s has no export %r; it has: %s" % (
                    self.id, request.export, ", ".join(sorted(self._exports)) or "none")}})
                continue

            try:
                data = fn(request, host)
            except HostError as e:
                _write(stdout, {"response": {"error": str(e)}})
                continue
            except Exception as e:  # noqa: BLE001 — an author's bug, reported not swallowed
                _write(stdout, {"response": {"error": "%s: %s" % (type(e).__name__, e)}})
                continue

            _write(stdout, {"response": {"data": data if data is not None else {}}})


def _as_bytes(v):
    if isinstance(v, (bytes, bytearray)):
        return bytes(v)
    return str(v).encode("utf-8")


def _write(out, obj):
    body = json.dumps(obj).encode("utf-8")
    if len(body) > _MAX_FRAME:
        raise ValueError("frame is %d bytes, past the %d byte limit" % (len(body), _MAX_FRAME))
    out.write(struct.pack(">I", len(body)))
    out.write(body)
    # The flush every hand-written guest forgets. Without it the host waits on
    # a reply sitting in this process's buffer, and the symptom is a plugin
    # that "hangs" with nothing wrong in it.
    out.flush()


def _read(inp):
    header = _exactly(inp, 4)
    if header is None:
        return None
    (size,) = struct.unpack(">I", header)
    if size > _MAX_FRAME:
        raise ValueError("the host announced a %d byte frame" % size)
    body = _exactly(inp, size)
    if body is None:
        return None
    return json.loads(body)


def _exactly(inp, n):
    """Read exactly n bytes, or None at end of pipe.

    A pipe read returns what is available, not what was asked for. Assuming
    otherwise works on every small message and fails on the first large one,
    which is the worst possible distribution of failures.
    """
    buf = b""
    while len(buf) < n:
        chunk = inp.read(n - len(buf))
        if not chunk:
            return None
        buf += chunk
    return buf
