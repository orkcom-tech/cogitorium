# Writing a Cogitorium plugin in Go

One package, two tiers. The same source compiles to a WebAssembly module the
server runs inside itself, and to a native binary the server supervises as a
child process. Nothing in your plugin changes between them — the tier is the
operator's decision, made when they approve the install, and a decision the
operator makes must not be a decision you had to write code for.

```go
package main

import cogitorium "github.com/orkcom-tech/cogitorium/sdk/go"

// At package level. See "Where exports are registered" below — this is the one
// thing about Go plugins that is not obvious, and it is not a style choice.
var plugin = cogitorium.New("myplugin").Provider("home", home)

func home(r *cogitorium.Request, h *cogitorium.Host) (any, error) {
	return map[string]any{"Greeting": "Hello, " + r.Ctx.Viewer.Name}, nil
}

func main() { plugin.Run() }
```

```yaml
# plugin.yaml
schema: 1
id: myplugin
name: My Plugin
version: 1.0.0
license: Apache-2.0
needs: go
host:
  contract: 1
pages:
  - path: /p/myplugin/
    template: myplugin.page.home
    title: My Plugin
    provider: home
```

```html
<!-- templates/home.html -->
{{define "myplugin.page.home"}}
<section class="panel"><h2>{{.Data.Greeting}}</h2></section>
{{end}}
```

## Building

```bash
GOOS=wasip1 GOARCH=wasm go build -buildmode=c-shared -ldflags="-s -w" -o plugin.wasm .
```

or, for a module about a third the size:

```bash
tinygo build -target=wasip1 -buildmode=c-shared -o plugin.wasm .
```

Both toolchains take the same source and the same SDK. `needs: go` and
`needs: tinygo` are the same tier and the same artifact; declare whichever you
built with.

`-buildmode=c-shared` in both, and this is the part that bites. The default
builds a command: it runs, it ends, and a module that has ended has no exports
left to call. c-shared builds something the host can call into repeatedly,
which is what a plugin is.

Then package and install it:

```bash
cogitorium plugins build .
cogitorium plugins install myplugin.zip
cogitorium plugins approve myplugin
cogitorium plugins enable myplugin
```

For the native tier instead — an ordinary binary, no WebAssembly, full access
to whatever the machine has — build with plain `go build` and declare
`needs: native` with a `native:` row per os/arch. The same source; the SDK
picks the transport from the build tags.

## Where exports are registered

Package level, in a `var`, not inside `main`.

On the WebAssembly tier the host loads your module and calls into it. It never
runs `main` — a `main` that ran would also end. Package-level initialisation is
the one thing that happens on both tiers, so it is where registration goes.

Registering inside `main` works when you test the native build and silently
registers nothing once the same code is a module. The SDK answers that case by
name rather than with "no such export", but the fix is to not write it.

## What a plugin may ask the host for

Nine calls, identical in every SDK and on every tier. A plugin that outgrows Go
and is rewritten in Rust calls the same nine things.

| call | what it is for |
| --- | --- |
| `h.Log(msg)` | the server's log, tagged with your plugin |
| `h.Now()` | the host's clock — pinnable, so your output is reproducible in a test |
| `h.Rand(max)` | randomness, pinned the same way |
| `h.Config(&into)` | what the operator set for your plugin |
| `h.Render(name, data)` | one of your templates, through the layer stack |
| `h.HTTP(method, url, headers, body)` | outbound, only to hosts listed under `hosts:` |
| `h.API(method, path, body)` | this server's API, as your plugin, only the subjects under `api:` |
| `h.Enqueue(export, args, after, key)` | your own export, later, on the durable queue |
| `h.Get/Set/Delete/Incr/Keys/CompareAndSet` | your own storage |

`Incr` and `CompareAndSet` exist because two instances of your plugin **will**
run at once. Read-then-write loses increments, and you find out from a wrong
number rather than from an error.

An error from a host call is the host refusing — a host you were not granted, a
scope the operator did not approve. It arrives as `*cogitorium.Error` carrying
the host's own sentence, which already names both what you asked for and what
you have.

## Returning something other than a model

Return any JSON-marshalable value and it becomes `.Data` in your template.
Return a `*cogitorium.Response` to say more:

```go
return &cogitorium.Response{Template: "myplugin.row.item", Model: item}, nil
```

Rendering through a template name rather than emitting HTML is what lets another
plugin override what yours produced. A backend that returned finished HTML would
be outside the mechanism the whole system runs on.

## A working example

[`examples/plugins/gosample`](../../examples/plugins/gosample) — a page, the
host's clock, and a counter that survives the instance. Built and run on both
toolchains.
