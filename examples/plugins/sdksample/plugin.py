"""The same plugin as before, written against the SDK instead of the wire."""

from cogitorium import Plugin

plugin = Plugin("sdksample")


@plugin.provider("home")
def home(request, host):
    visits = host.incr("visits")
    reply = host.http("https://api.github.com/zen")
    return {
        "who": request.viewer_name or "nobody",
        "visits": visits,
        "zen": reply["body"].decode().strip(),
        "clock": host.now(),
    }


if __name__ == "__main__":
    plugin.run()
