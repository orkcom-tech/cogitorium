# Writing a Cogitorium plugin in Python

One file, standard library only. Copy `cogitorium/` into your plugin directory
beside `plugin.py` — a plugin is a directory somebody reads before approving
it, so a dependency here would be a dependency in everybody's review.

```python
from cogitorium import Plugin

plugin = Plugin("myplugin")


@plugin.provider("home")
def home(request, host):
    return {"greeting": "Hello, " + request.viewer_name}


if __name__ == "__main__":
    plugin.run()
```

```yaml
# plugin.yaml
schema: 1
id: myplugin
name: My Plugin
version: 0.1.0
needs: python
host:
  contract: 1
pages:
  - path: /p/myplugin/
    template: myplugin.page.home
    provider: home
```

You declare `needs: python`. The host fetches an interpreter, shares it with
every other plugin that asked for one, and starts your file on its first call.
Which lane it picked is not your problem — that is the whole point of declaring
a technology instead of shipping a runtime.

## What `host` offers

Nine calls, identical on every tier, so a plugin rewritten in Rust calls the
same nine.

| | |
|---|---|
| `host.log(msg)` | the server's log, tagged with your id |
| `host.now()` · `host.rand(n)` | the host's clock and randomness, so a test can pin them |
| `host.config()` | what the operator set for you. Read-only |
| `host.render(name, data)` | one of your templates, through the layer stack |
| `host.http(url, ...)` | outbound, only to hosts listed under `hosts:` |
| `host.api(path, ...)` | this server's API, as your plugin, only subjects under `api:` |
| `host.enqueue(export, ...)` | run one of your exports later, durably |
| `host.get/set/delete/incr/keys/compare_and_set` | your own storage |

A refusal raises `HostError` with the host's own sentence in it — which names
both what you asked for and what you were granted.

## What it saves you

The protocol is small enough to implement by hand, and everybody who does gets
the same three things wrong: forgetting to flush after a write, assuming a pipe
read returns everything asked for, and answering without the frame wrapper. The
first two present as a plugin that hangs.
