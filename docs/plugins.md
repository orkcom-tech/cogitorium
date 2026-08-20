---
layout: default
title: Writing a plugin
permalink: /plugins/
description: How to write a Cogitorium plugin — the manifest, the five tiers, the nine host calls, overriding a screen that shipped, the SDKs, and how to list yours in the shared catalog.
---

# Writing a plugin

A gear adds a tool an agent can call. A **plugin** adds a screen, hangs a panel
inside every workspace, takes over a screen that shipped, and runs code of its
own. It is how you change the product rather than what runs on it.

Everything below is written for somebody who has not read this repository. The
one command worth knowing before anything else is:

```bash
cogitorium plugins names
```

It prints every template name you may override, what model each renders against,
and what the host's own body is today — offline, from the binary you are
actually running. `docs/registry.json` is the same list as a file, and
`docs/plugin.schema.json` is the manifest schema, so an editor completes the
fields and underlines a typo.

## Your first fifteen minutes

A plugin is a directory. Nothing else is required.

```bash
cogitorium plugins new hello
```

That leaves you with:

```
hello/
  plugin.yaml
  templates/home.html
```

```yaml
# plugin.yaml
schema: 1
id: hello                 # lowercase, dashes, unique on the install
name: Hello
version: 1.0.0
license: Apache-2.0
host:
  contract: 1             # the ABI generation this plugin speaks
pages:
  - path: /p/hello/
    template: hello.page.home
    title: Hello
```

```html
<!-- templates/home.html -->
{% raw %}{{define "hello.page.home"}}
<section class="panel">
  <h2>Hello</h2>
  <p>Rendered by a plugin, inside the product.</p>
</section>
{{end}}{% endraw %}
```

Point a running server at the directory and it is live, with no build step:

```bash
cogitorium plugins dev .
```

A development directory is listed as one everywhere it appears — no version
directory, no digest, no signature, because there is nothing stable to take one
of. When you are done, package it:

```bash
cogitorium plugins build .
cogitorium plugins install hello.zip
cogitorium plugins approve hello
cogitorium plugins enable hello
```

**It is live on the next page.** The template stack is rebuilt from what is on
disk and swapped in whole, on enable, disable, reorder, revoke and remove, so a
plugin cannot half-apply and you do not have to restart anything to see it.
(Installing does not rebuild anything, because a plugin arrives switched off and
contributes nothing until you enable it.) That was not always true — the stack used to be composed once at boot,
and removing a plugin left its rail entry sitting there through a reload.

The one exception is a **backend**. A plugin that declares `needs:` has HTTP
routes attached at boot and there is no way to take a route back, so those still
ask for a restart, and the screen tells you which.

## The naming rule

Every template you ship is named `<your-id>.<area>.<name>` — `hello.page.home`,
`hello.row.item`. Ownership lives in the name, so two plugins cannot collide by
accident and nobody has to register anything.

The one exception is deliberate: to **override** a host template you use the
host's name, `cog.row.gear`, exactly. That is not a collision, it is the whole
mechanism, and the approval screen shows it as an override so an operator sees
what you are taking over before they agree.

`area` is a closed vocabulary — `shell`, `page`, `stage`, `drawer`, `list`,
`row`, `field`, `action`, `empty`, `badge`, `frag`, `slot` — and a name outside
it is refused at load with the list. It exists so that a name says what a thing
is before you read it.

## What a plugin can do

### A page of its own

```yaml
pages:
  - path: /p/hello/
    template: hello.page.home
    title: Hello
    auth: token          # token (default) | admin | none
    provider: home       # an export of your backend, if you have one
```

The page is served inside the product — the rail is there, the frame is there,
your template fills the cavity. `auth: none` is allowed and is **shown in red on
the approval screen**, because it is a page on somebody's server open to anybody
who can reach it.

### An entry in the rail

```yaml
nav:
  - label: Hello
    href: /p/hello/
    icon: grid           # a shape the product already draws; unknown names show the label
    order: 650           # host entries are spaced by hundreds
```

### A panel inside every workspace

```yaml
mounts:
  - point: workspace.drawer
    title: Hello
    page: hello.page.panel
```

A page is a destination somebody navigates to. A **mount** is inside a
workspace, and there are two points:

| Point | What you get |
|---|---|
| `workspace.drawer` | a panel that crawls out over the work without leaving it |
| `workspace.stage` | a **view of its own** on the workspace's track, beside Chat, the Blueprint and the Editor |

The set is closed, and an unknown point is refused at install rather than
ignored.

`workspace.stage` exists because four screens are not templates and cannot
honestly become ones: the blueprint and the map are drawn canvases, the editor
is live text, the terminal is a socket, and a template renders a thing that
exists at a moment. A stage does not try to override them — it stands beside
them, on the same track, with the same guarantee that every view stays mounted
for its whole life.

### Taking over a screen that shipped

Define the host's own name and yours wins, because later layers render instead
of earlier ones:

```html
{% raw %}{{define "cog.row.gear"}}
<article class="card">
  <h3>{{.Name}} <span class="hint">v{{.Version}}</span></h3>
  <p>{{.Description}}</p>
</article>
{{end}}{% endraw %}
```

Two things make this safe to rely on. **Late binding**: the stack is composed by
name — at boot and again on every plugin change — so a plugin layered above
yours overrides what you produced, and
your backend returning a template name rather than finished HTML is what keeps
you inside that mechanism. And **validation**: every template is rendered
against a zero-value model at load, so one that would panic on an empty list is
refused with your plugin named, rather than discovered by a visitor.

If your template renders nothing at all, the approval screen says so — a region
that silently became blank is the failure this catches.

### The cheapest override there is

```html
{% raw %}{{define "cog.shell.tokens"}}
<style>
  :root { --accent: #0e7490; }
  :root[data-theme='dark'] { --accent: #22d3ee; }
</style>
{{end}}{% endraw %}
```

One name is the palette. Overriding it restyles the whole product, in both
appearances, with no code at all — which is why the interface's look is as
configurable as everything else here.

### Adding to a place instead of replacing it

Names in the `slot` area **concatenate** rather than replace, so two plugins
contributing to the same place both survive and neither had to know the other
existed:

```html
{% raw %}{{define "cog.slot.head"}}<meta name="hello" content="1">{{end}}{% endraw %}
```

**`cog.slot.stagehead`** is the one that reaches the screens a template cannot
render. It is a strip above whichever view is on screen — `chat`, `blueprint`,
`workbench`, `terminal` or `map` — rendered through the same composed stack as
everything else, and told which screen is asking and which workspace it belongs
to (`.Workspace` is zero where there is none):

```html
{% raw %}{{define "cog.slot.stagehead"}}
  <span>{{.Screen}} in workspace {{.Workspace}}</span>
{{end}}{% endraw %}
```

It renders nothing at all until somebody overrides it, so an install with no
plugins is unchanged by its existence.

### Stylesheets, scripts and files

```yaml
styles:
  - assets/hello.css
scripts:
  - src: assets/hello.js
media:                     # optional: what you want somebody to see before deciding
  - file: docs/screen.png
    caption: The list, with two feeds watched
```

Only files you declare are served, under `/p/<id>/assets/…`. Everything else in
your bundle stays private — a plugin ships whatever its author zipped, and
serving a directory would publish all of it because one file in it was
referenced.

Scripts load on **every** screen, so guard yours:

```js
if (!document.querySelector('.hello-panel')) return
```

## Backends: five tiers, and you pick none of them

You declare a technology. The host decides how it runs, tells the operator
which lane it picked, and refuses before anything is downloaded if this install
cannot run it.

| `needs:` | You ship | Runs |
|---|---|---|
| *(omitted)* | templates, CSS, JS | everywhere; nothing executes |
| `js` | `plugin.js` | on the JavaScript engine compiled into the server — nothing fetched |
| `wasm`, `rust`, `go`, `tinygo`, `zig`, `c` | `plugin.wasm` | everywhere; sandboxed by the runtime |
| `python`, `node`, `bun`, `deno` | source | wherever the data directory is executable; the interpreter is fetched once and shared |
| *(an OCI image)* | an image reference | where this install has a sandbox that starts containers |
| `native` | a binary per `{os, arch}` | where one matches. **No isolation**: your machine code as this server's user |

Two honest limits, both on the approval screen rather than in a footnote. A
provisioned or native backend is a child process running as the server's own
user — it is not isolated from the server's files. The **container** tier is the
isolated option and costs a container start per call. Warm and unisolated
against cold and isolated is a trade the operator reads and chooses.

### The nine host calls

Identical on every tier, with the same names and the same behaviour, so a plugin
that outgrows one language and is rewritten in another calls the same nine
things.

| Call | What it is for |
|---|---|
| `log` | the server's log, tagged with your plugin |
| `now` | the host's clock — pinnable, so your output is reproducible in a test |
| `rand` | randomness, pinned the same way |
| `config` | what the operator set for you; read-only, often empty |
| `render` | one of your templates, through the layer stack |
| `http` | outbound, **only** to hosts listed under `hosts:` |
| `api` | this server's API, as `plugin:<id>`, only the subjects under `api:` |
| `enqueue` | one of your own exports, later, on the durable queue |
| `kv` | your own storage: get, set, delete, list, incr, compare-and-set |

`incr` and `compare-and-set` exist because two instances of your plugin **will**
run at once. Read-then-write loses increments, and you find out from a wrong
number rather than from an error.

A refusal is a value, not a crash: the host answers "you may not reach that"
with a sentence naming both what you asked for and what you were granted.

### Roles, and where they are not

An export is a named function your plugin registers. **There is no `exports:`
block in the manifest** — a page names the export that supplies it, and that is
the only declaration there is:

```yaml
pages:
  - path: /p/hello/
    template: hello.page.home
    provider: home          # the export that supplies this page's model
```

The **role** is what the host says when it calls, on the request rather than in
the manifest, so one export can be reached more than one way without being
declared twice. The ABI names seven — `route`, `provider`, `filter`, `event`,
`tool`, `schedule`, `command` — and this build issues two of them: `provider`
when a page is rendered, and `event` when something you put on the queue with
`cog.enqueue` comes back to you. The rest are named because the contract is
versioned and adding a role later must not be a breaking change; a host that
does not call them yet is not a promise that it never will.

### The SDKs

Three, all offering the same nine calls under the same names:

- **[Python](https://github.com/orkcom-tech/cogitorium/tree/main/sdk/python)** —
  one file, standard library only, copied in beside your code.
- **[Go](https://github.com/orkcom-tech/cogitorium/tree/main/sdk/go)** — one
  package, two transports. The same source builds to WebAssembly or to a native
  binary, and TinyGo takes it unchanged for a module about a third the size.
- **[Rust](https://github.com/orkcom-tech/cogitorium/tree/main/sdk/rust)** — a
  macro over the same nine calls; the smallest module of the three.

`needs: js` needs no SDK at all: the engine is compiled into the server, and a
`cog` object is already in scope.

```python
from cogitorium import Plugin

plugin = Plugin("hello")

@plugin.provider("home")
def home(request, host):
    return {"Greeting": f"Hello, {request.viewer_name}.", "Now": host.now()}

plugin.run()
```

Building a Go or Rust plugin has one trap worth stating: use
`-buildmode=c-shared` (Go, TinyGo) or `crate-type = ["cdylib"]` (Rust). The
default builds a command — it runs once, it ends, and a module that has ended
has no exports left to call.

## What the operator sees before saying yes

Worth knowing, because it decides how you should write the manifest.

- **What you override**, with a **picture of each one** rendered through the
  composed stack against an example. "It overrides `cog.row.nav`" is not
  something anybody can evaluate.
- **Which hosts you want**, by name. You reach those and nothing else.
- **Which named values you ask for**, by name. You never see a value the
  operator did not name.
- **Which API subjects you want.** A write grant implies the matching read.
- **How it runs**, with native shown in red.
- **Whatever you shipped to show them** — screenshots or a clip, from your
  bundle, so what they look at is covered by the digest they are approving.

Declaring your overrides in `overrides:` earns nothing and is not required — the
host computes what you actually override from the templates you ship, because a
manifest can lie and parsed bytes cannot. It buys one quiet line saying the
manifest matched.

## Listing it in the shared catalog

[**orkcom-tech/cogitorium-plugins**](https://github.com/orkcom-tech/cogitorium-plugins)
is an index and nothing else: five fields saying where a plugin lives. Your
bundle stays in your own repository, in your own releases, so listing costs one
small file no matter how many plugins there are.

### 1. Publish a release

Tag your repository and attach the bundle `cogitorium plugins build` produced.
The catalog builds the download URL by convention from `repo` and the version,
so nothing else needs configuring.

### 2. Add five fields

Open a pull request against `plugins.json`:

```json
{
  "id": "release-radar",
  "name": "Release Radar",
  "author": "your-handle",
  "description": "Watches releases and files them into a workspace.",
  "repo": "your-handle/cogitorium-release-radar",
  "cover": "https://raw.githubusercontent.com/your-handle/cogitorium-release-radar/main/docs/cover.png"
}
```

`cover` is optional and **pinned to your own repository** — an address anywhere
else is refused, because a picture on somebody's own host would tell them who is
browsing the library before anybody installed anything.

### 3. It merges itself

**Adding an entry merges on green CI.** You are not waiting for anybody. CI runs
the same checks a client runs:

```bash
cogitorium plugins check-catalog plugins.json --base main
cogitorium plugins check-bundle your-plugin.zip
```

**Editing or removing an entry that is already listed does not merge itself**,
and that is the one rule worth explaining: an edit can point an id people have
already installed at a *different repository*, and nothing in a public JSON file
can establish who owns an id. Those wait for a person.

### About the verified mark

Verification shows as three states rather than a badge: the team read the
version somebody has, they read a different one, or nobody has looked. **The
last is the ordinary state and not an accusation.** Only the maintainers can set
it, and it means somebody read that version — not that it is safe, and not a
substitute for the approval step on the install where it will run.

## Rules that will refuse you

Stated so you meet them in this document rather than in an error.

- **A name outside the area vocabulary** is refused at load, with the list.
- **A template that renders nothing** is named on the approval screen.
- **A template that panics on a zero-value model** is refused at load; every
  one is rendered against an empty model before the server serves anything.
- **A plugin that fails to load is dropped and the rest are composed without
  it**, by name and with the reason. One broken plugin does not take the
  product down.
- **An id is not a claim.** `plugins.json` cannot establish who owns one, which
  is why edits are held.
- **`http` to a host you did not list** is refused, with both what you asked for
  and what you have in the message. Loopback and link-local are refused
  regardless.
- **`api` to a subject you were not granted** is refused the same way.
- **Nothing you ship runs until somebody approves that exact build.** Rebuild
  and it is pending again.

## Where to look next

- [The plugin manifest schema](https://github.com/orkcom-tech/cogitorium/blob/main/docs/plugin.schema.json)
- [Every overridable name, as a file](https://github.com/orkcom-tech/cogitorium/blob/main/docs/registry.json)
- [Worked examples](https://github.com/orkcom-tech/cogitorium/tree/main/examples/plugins) —
  a page, an override, a gear-forging backend, and one sample per SDK
- [The design, and its honest limits](https://github.com/orkcom-tech/cogitorium/blob/main/docs/planning/plugin-system.md)
