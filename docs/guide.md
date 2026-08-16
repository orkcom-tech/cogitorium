---
layout: default
title: Guide
permalink: /guide/
description: A worked walkthrough of Cogitorium — from an empty install to a pipeline with models in it that behaves the same way twice, with every command and every error message taken from a real run.
---

# Guide

This walks from an empty install to a workflow you can hand to another system:
agents with their own models, tools they keep, a door your own services deliver
through, and a clock that starts work without you. The thread running through
it is the one thing the whole product is for — **a pipeline with a model in it
that behaves the same way twice**, which is why so much of what follows is
about what gets checked rather than what gets said.

Every command below was run against a real install, and every error message is
the exact text the software produces. Where something does not exist, it says
so rather than describing what it might look like.

The examples use `http://127.0.0.1:8688`, the default. On a loopback address you
are the admin without signing in, so no `Authorization` header appears in them —
add `-H "Authorization: Bearer $TOKEN"` to every call if your server listens
anywhere else.

---

## 1. From nothing to an answer

### Install and start

```bash
brew install orkcom-tech/tap/cogitorium && cogitorium serve
```

Other routes are in [Install](./#install). The server listens on
`127.0.0.1:8688` and keeps everything in `~/.cogitorium`. Open
<http://127.0.0.1:8688>.

`serve` takes exactly four flags — `--config`, `--data`, `--listen`,
`--log-level` — and the binary has exactly two subcommands, `serve` and
`version`. There is no `--port`, no `--debug`, and no `cogitorium init`.

### The token, and when you need it

On first start the log carries one line you cannot get back:

```
admin token created; local requests are admin automatically  token=cg-admin-b1e87f6…
```

You do not need it on your own machine — a request from loopback *is* the
admin. You need it the moment the server listens anywhere else. Only the SHA-256
hash is stored, so a lost token cannot be recovered; there is no reset command.

Two things people try here that do not work:

- **Pasting the token into the login form.** The login card has three fields —
  server, user, password — and none of them is for a token. The seeded admin has
  no password at all, so it answers `invalid username or password`.
- **Reaching a Docker install and expecting to be the admin.** Inside the
  container the server listens on `0.0.0.0`, so your request is not loopback:

  ```
  {"error":{"message":"authentication required: send Authorization: Bearer <token>"}}
  ```

To give the admin a password — there is no screen for this, only the route:

```bash
curl -X PUT http://127.0.0.1:8688/api/v1/users/1/password -H 'Content-Type: application/json' -d '{"password":"correct-horse-battery"}'
```

### A provider and a model

A workspace cannot exist without a model, because its orchestrator needs
something to think with. Until the catalog has one, **+ New workspace** is
disabled and the page says so:

> A workspace needs a model for its orchestrator, and the catalog is empty. Add one under Models first.

Go to **Models** → fill in the provider row → **add provider**, then add a model
from the provider's card. Or:

```bash
curl -X POST http://127.0.0.1:8688/api/v1/providers -H 'Content-Type: application/json' -d '{"name":"local","type":"openai-compatible","base_url":"http://127.0.0.1:11434/v1","api_key":""}'
```

```json
{"id":1,"name":"local","type":"openai-compatible","base_url":"http://127.0.0.1:11434/v1","has_key":false}
```

`has_key` is the only thing ever said about the key. The key itself is not in
that response, nor in any other — the field holding it is unexported, so the
JSON encoder cannot see it.

Two provider types exist: `anthropic` and `openai-compatible`. Anthropic has a
default address; an openai-compatible provider does not, and leaving it empty is
refused with *"base_url is required for openai-compatible providers"*. There is
no environment variable and no config file key for a provider key — the catalog
is the only place keys live.

```bash
curl -X POST http://127.0.0.1:8688/api/v1/models -H 'Content-Type: application/json' -d '{"provider_id":1,"model_name":"qwen2.5:0.5b","label":"local / tiny"}'
```

```json
{"id":1,"provider_id":1,"provider_name":"local","provider_type":"openai-compatible","model_name":"qwen2.5:0.5b","label":"local / tiny"}
```

### A workspace

**Workspaces** → **+ New workspace** → name it, pick the orchestrator model →
**create workspace**. Or:

```bash
curl -X POST http://127.0.0.1:8688/api/v1/workspaces -H 'Content-Type: application/json' -d '{"name":"research","description":"release notes","orchestrator_model_id":1}'
```

```json
{"id":1,"name":"research","description":"release notes","branch":"workspaces/research-1","shared_branch":"workspaces/research-1/shared","owner_id":1,"team_ids":[]}
```

Name a model id that is not in the catalog and you get `404 {"error":{"message":"model 1: not found"}}` — the workspace is not half-created.

The workspace opens with one agent already in it, called `orchestrator`. Type
into **tell the orchestrator what you need…** and press **send**. That is the
whole first loop.

![The workspaces list](assets/01-workspaces.png)

---

## 2. A second agent

Two ways, and they land in the same place.

**By hand:** open **Blueprint** and press **`+ agent`**. Give it a name, a model
and a role. It appears on the canvas with no capabilities at all — nothing may
delegate to it and it may delegate to nothing — until you draw an edge, which
is what a new node in a graph honestly is.

**Or ask the orchestrator**, which is often faster when you know the shape of
the team but not the names:

> Create a worker agent named researcher with the role "You summarize sources accurately and cite them." and delegate to it: summarise what a blueprint wire does in this product.

The orchestrator calls `agent_create` and then `delegate`. You see a ⚙ chip per
tool call, a collapsible ✓/✗ result row, and the worker's answer in its own
labelled bubble.

Through the API instead:

```bash
curl -X POST http://127.0.0.1:8688/api/v1/workspaces/1/agents -H 'Content-Type: application/json' -d '{"name":"researcher","role":"You summarize sources accurately and cite them.","model_id":1}'
```

### The wire is the capability

Open **Blueprint**. Drag from one agent to another to connect them. The hint on
the canvas says it exactly:

> Drag between nodes to connect — a wire IS the capability, not a picture of one.

An agent can delegate to another **only** if a wire runs to it. Delete the edge
and the capability is gone on the next turn — click the edge and press Delete.
Wires are created and deleted, never edited; to change one, remove it and draw
the new one.

### It arranges itself

You do not place agents by hand unless you want to. The canvas lays them out by
the wires between them — rank by distance from the orchestrator, ordered within
each rank so the wires cross as little as possible:

```
                 orchestrator
                  /        \
             lead-a        lead-b
             /    \        /    \
      worker-1  worker-2  worker-3  worker-4
             \    |        |    /
                 reporter
```

**⤢ tidy** re-lays out everything and stores it, so the arrangement is what
everybody sees next time rather than a view your screen is holding. Drag any
node afterwards and that wins — a dragged position is kept and the layout leaves
it alone. Tidy again to take it back.

![A wired workspace](assets/02-workspace-wired.png)

This is the part worth slowing down on, because it is **graph engineering** and
not decoration. The canvas holds four layers of the same graph, and the buttons
above it show one at a time:

| Layer | An edge means |
|---|---|
| **delegation** | this agent may hand work to that one |
| **tools** | this agent may call that gear |
| **memory** | this is what the agent knows going into a turn |
| **outward** | this agent may ask to reach the internet |

Every one of them is a permission the runtime checks, not a note about
intentions. So the arrangement you are looking at is the program: to change
what the system can do, you change the graph — and to know what it can do, you
look at it, rather than reading four prompts and hoping.

### What an agent carries into a turn

Open **Agents**, click an agent, and read the inspector. Everything the model
sees is listed there in the order it is assembled, and each part can be changed
or dropped:

1. its **role** — the system prompt it always carries
2. any **instructions** bound to it from the library
3. the **context** bound to the workspace or to that agent
4. the **conversation**, replayed in full

Click **show what this agent sees** for the exact assembled prompt. Hover a chat
entry and click **forget** to stop that entry being replayed — the confirmation
reads *"Forget this from the conversation? It stops being replayed to the model."*

The inspector also carries the running token count per agent, and the **Agents**
panel shows each agent's share of the workspace total.

![An agent inspector](assets/05-agent.png)

---

## 3. Gears — tools your agents keep

A gear is a small program an agent can call. It survives the conversation, it
runs in a throwaway container with none of the server's files and no network
unless you grant it one, and **nothing an agent calls runs until you approve
it**.

### Write one yourself

**Gears** → **write a gear**. Or:

```bash
curl -X POST http://127.0.0.1:8688/api/v1/gears -H 'Content-Type: application/json' -d '{"name":"word_count","description":"count words in a string","tags":["text"],"runtime":"python","code":"import sys, json\nargs = json.load(sys.stdin)\nprint(len(args[\"text\"].split()))\n","args_schema":"{\"type\":\"object\",\"properties\":{\"text\":{\"type\":\"string\"}},\"required\":[\"text\"]}"}'
```

```json
{"id":1,"name":"word_count","tags":["text"],"version":1,"runtime":"python",
 "entrypoint":"main.py","status":"pending","timeout_seconds":60}
```

Note what came back: `status` is `pending` and `entrypoint` was filled in for
you. Runtimes are `python` (`main.py`), `node` (`main.js`), `bash` (`main.sh`)
and `binary`. The name must match `^[a-z][a-z0-9_]{1,48}$` — it becomes a tool
name every model has to be able to call.

Arguments arrive as JSON on stdin. Whatever the gear prints on stdout is the
result.

### Try it before you trust it

The dry run works on an unapproved gear — that is its entire purpose:

```bash
curl -X POST 'http://127.0.0.1:8688/api/v1/gears/1/run?dry=1' -H 'Content-Type: application/json' -d '{"args":{"text":"one two three"}}'
```

```json
{"exit_code":0,"stderr":"","stdout":"3\n","timed_out":false}
```

In the interface this is **review & run** on the gear card: read the source,
run it with arguments you choose, and only then press **approve**. Output
streams while it runs rather than appearing at the end.

Call it without approving and without `dry=1` and it is refused — an unapproved
gear does not execute, whoever asks.

### Versions

Saving new code makes a new version and returns the gear to `pending`. Approval
covers exact content, never a moving target. There is no per-version approval
and no rollback: `status` is one column on the gear.

![The gears list](assets/07-gears.png)

### What the sandbox does, and does not

With Docker: a read-only copy of only the files the gear was given, 512 MB, one
CPU, 256 processes, no network unless you granted it one, and the whole
container is removed afterwards. Those limits are fixed — there is no per-gear
setting for them.

Without Docker the gear runs as a subprocess of the server, **with the server's
own file access** — including the database, and the provider keys in it. The
server says so at startup:

```
gears will run as unsandboxed subprocesses with this server's file access — an approved gear can read the database, including provider API keys; install Docker or set sandbox: docker to isolate them
```

In that configuration the approval gate is the only control there is. `sandbox: docker`
refuses to start when the daemon does not answer; `auto` warns and continues.

### Harder isolation, if you have installed it

A container is a process with a restricted view, not a machine. If that is not
enough for what your gears run, point Docker at a stronger runtime and name it:

```yaml
sandbox: docker
sandbox_runtime: runsc        # gVisor. Or kata-runtime for Kata Containers.
```

**Cogitorium does not install or configure these.** You install gVisor or Kata,
register it with your Docker daemon, and this names it — the isolation is the
runtime's work, and claiming otherwise would be claiming somebody else's. What
this adds is that a name your daemon does not have is refused **at startup**,
with the names it does have in the message:

```
sandbox_runtime "runsc" is not one this Docker daemon has: it offers io.containerd.runc.v2, runc. The runtime has to be installed and registered with the daemon first — Cogitorium selects one, it does not install one
```

Without that check the mistake surfaces on the first gear run, possibly days
later, as `create container: exit status 125`.

Two refusals worth knowing. `sandbox_runtime` with `sandbox: subprocess` is an
error rather than an ignored setting — it reads as hardened isolation and is in
fact no isolation at all. And every other restriction stays exactly as it was:
naming a runtime does not quietly return capabilities, the network or the
memory ceiling.

The sandbox image is also fetched once at startup now, so the first gear does
not spend its own timeout pulling a distribution.

### Names a gear is given

A gear that needs a key does not receive one in its arguments. It declares a
**name** — `API_KEY` — and reads the value from its own environment when it
runs. You say what the name means; the model never sees the value. That is not
caution for its own sake: an agent's answer leaves the building, in the chat and
in an inlet response, so a secret in a prompt is a secret published.

Set them under **Variables** (install-wide, administrators) or in a workspace's
own **Variables** panel. A **variable**'s value is shown afterwards. A
**secret**'s is shown once, when you set it, and never again — not behind a
reveal button; the server has nowhere to put it in a response, and it is removed
from anything the gear prints, from the recorded run, from the log line and from
the live output you are watching.

Three sources, later winning:

1. this install's own store, encrypted with `COGITORIUM_SECRET_KEY` from the
   environment (without that key, variables still work and a secret cannot be
   stored — it would have to go to disk in plaintext, and it will not);
2. a directory on disk, one file per name, contents being the value —
   `variables_dir` and `secrets_dir`. That is the shape Kubernetes mounts a
   ConfigMap and a Secret in, and the chart wires exactly that up:
   `config.variablesConfigMap` and `config.secretsSecret` name the objects,
   the server reads the mounted files, and rotation is the cluster's own;
3. the workspace's own overrides, which is how one gear serves staging and
   production without being edited.

A name nothing supplies **stops the run and names it**, rather than handing the
gear an empty string that fails somewhere far away with a message about nothing.
The approval screen says so in advance, per name.

### And the gear does not get the secret

A gear granted the network is not handed its secrets at all. What it finds in
its environment is a stand-in, and the gate puts the real value in on the way
out. This is a real run, and the gear is printing what it was given:

```
HELD=cogitorium-secret-jMDrxMgeHate9cewY3k73-vfWcfPIJIF
STATUS=200 BODY=the-origin-answered
```

The destination received `Bearer sk-live-…`, the real credential. The container
never had it. A stand-in is random, minted for that one run, known only to this
install's gate, and worthless the moment the run ends — so a gear that sends its
environment somewhere has sent a string that opens nothing.

Two consequences worth knowing before you rely on it.

**The gate reads inside those runs' TLS.** It cannot substitute into bytes it
cannot see, so for a run holding stand-ins — and only that kind of run — it
terminates TLS with its own certificate, which the run is given, and opens its
own verified connection onward. A granted gear with no secrets is tunnelled as
before, and the gate sees nothing but hosts and byte counts.

**A gear that was not granted the network gets the real value.** There is no
edge to substitute at, so a stand-in would simply be a credential that cannot
work. If your gear uses a key locally rather than in a request, that is the case
you are in, and nothing changes for you.

### Giving a gear a browser

A gear runs in the ordinary sandbox image, which has no browser in it. On the
approval screen you can give it the **browser** environment instead — the same
place and the same act as the network grant, because it is the same kind of
decision.

```bash
curl -X PATCH http://127.0.0.1:8688/api/v1/gears/1 -H 'Content-Type: application/json' \
  -d '{"environment":"browser","network":{"granted":true,"hosts":["example.com"]},"status":"approved"}'
```

The gear then finds a real browser in its container and drives it however it
likes. A few lines of shell is enough:

```bash
CHROME=$(ls -d /ms-playwright/chromium-*/chrome-linux/chrome | head -1)
mkdir -p out
"$CHROME" --headless --no-sandbox --screenshot=out/shot.png https://example.com
"$CHROME" --headless --no-sandbox --dump-dom https://example.com > out/page.txt
```

That is a real run:

```
BROWSER=/ms-playwright/chromium-1194/chrome-linux/chrome
SHOT=7012 TEXT=97
```

`out/` is collected the way it always is, so the screenshot and the page text
come back as run artifacts an agent can hand on and you can open. Nothing about
the record is new.

Three things worth knowing. **`--no-sandbox` is required**: a gear already runs
as an unprivileged user with every capability dropped, which is exactly where a
browser's own sandbox cannot start — the container is the boundary, and it is
the same one every gear has. **The image is about a gigabyte** and is not
fetched until a gear needs it, so give the first such run a longer timeout or
pull it on the host first. And **the browser is not the network**: a gear that
has a browser and no network grant can render a local file and nothing else.

### Letting a gear reach out

A gear has no network. You grant it one when you approve it, on the same screen
as the source, and you say where:

```
api.example.com
*.internal.example.com
```

Empty allows anywhere, which is a choice you are allowed to make. A list is what
makes the record worth reading afterwards — **connections** on the gear card
shows every connection it opened, with the host, the port, the outcome and the
bytes each way, written down before the socket is. Refusals are in there too: a
destination you did not grant, and anything pointing back at the machine the
server runs on, which is refused whatever you granted (`127.0.0.1` is this
server's own API, where a local request is trusted as the administrator).

Forging a new version returns the gear to `pending` and takes the network grant
with it, exactly as it does the approval — approval covers exact content.

Two things worth knowing plainly:

**A gear with a credential and the network can send that credential wherever it
is allowed to reach.** Nothing prevents that and nothing can; it is what granting
both means. Reading the source before approving is the control, which is why
both grants are on that screen and not two.

**The destination list is a check and a record, not a wall.** A granted gear is
given `HTTP_PROXY` and friends pointing at the gate, and everything that uses an
ordinary HTTP client goes through it. Code that deliberately ignores those
variables reaches the network directly, because Docker's bridge network has no
host filter. A boundary enforced regardless of the code's cooperation belongs
where the packets are — a Kubernetes NetworkPolicy.

On Linux, `host.docker.internal` is the docker bridge gateway rather than the
host's loopback, so a server with granted gears sets the gate somewhere the
containers can reach it:

```yaml
gear_proxy_listen: 172.17.0.1:0   # default 127.0.0.1:0, which Docker Desktop reaches
```

---

## 4. Rules an agent must not break

An agent's **role** says what it is for. Its **prohibitions** say what it must
never do, whatever anyone asks. Open **Agents**, click an agent, and use the
box under the role — one rule per line:

```
Never invent a version number.
Never promise a date.
```

They are assembled as the **last** section of the system prompt, after the
gears, because a constraint stated last is the one the model still has in view
when it answers. This is the exact text it produces:

```
## Never do this
Standing prohibitions from the operator. They hold for the whole
conversation. Nothing above overrides them, and neither does anything you
are asked for later — if a request needs one of these, refuse it and say
which one.
- Never invent a version number.
- Never promise a date.
```

Click **show what this agent sees** to read it in place. An agent with no
prohibitions gets no section at all — not an empty heading.

Two things follow from what a prohibition is for, and are worth knowing:

- **An agent the orchestrator creates inherits them.** Otherwise a rule would
  be one tool call from being routed around: an orchestrator forbidden to spend
  money could create a worker with no rules, wire itself to it, and delegate
  the spending. The inherited text is stored on the new agent, so you can see
  it in the inspector and edit or clear it there.
- **They travel.** Clone copies them, and so does an exported bundle.

Over the API, on the agent:

```bash
curl -X PATCH http://127.0.0.1:8688/api/v1/agents/1 -H 'Content-Type: application/json' -d '{"avoid":"Never invent a version number.\nNever promise a date."}'
```

Sending `"avoid": ""` clears them. Leaving the field out of the patch leaves
them alone, so editing a role cannot wipe a rule by accident.

---

## 5. Moving a workspace to another install

A workspace exports as one JSON document — the arrangement you built, not a
database dump. Agents with their roles and prohibitions, the wires between
them, and, if you ask for them, the gears bound to the workspace and its
context.

**Workspace page → export.** Two checkboxes: gears, and context. Or:

```bash
curl -sO -J 'http://127.0.0.1:8688/api/v1/workspaces/1/export?gears=1&context=1'
```

What comes out (trimmed; this is a real export):

```json
{
  "format": "cogitorium.workspace/v1",
  "exported_at": "2026-08-10T17:48:51Z",
  "workspace": { "name": "release notes", "description": "drafts the changelog" },
  "agents": [
    {
      "name": "orchestrator",
      "role": "You are the orchestrator of this workspace…",
      "avoid": "Never invent a version number.\nNever promise a date.",
      "is_orchestrator": true,
      "model": { "provider_type": "openai-compatible", "model_name": "qwen2.5:0.5b" }
    },
    { "name": "writer", "role": "You write plainly.", "avoid": "", "is_orchestrator": false,
      "model": { "provider_type": "openai-compatible", "model_name": "qwen2.5:0.5b" } }
  ],
  "wires": [ { "from": "orchestrator", "to": "writer", "label": "drafts" } ],
  "gears": [ { "name": "word_count", "runtime": "python", "entrypoint": "main.py",
               "bound_to": "workspace", "files": [ … ] } ]
}
```

Read the shape of it, because it is the whole design. **Wires name agents, not
ids** — ids from another install mean nothing. **Models are named, not
referenced** — a bundle cannot carry a provider key, so it can only say which
model the agent used and let the other install look for it. And there is
nowhere in the document for a key, a token, a user, an owner, a team or a chat
message: a bundle is handed to someone else, so it carries nothing private.

### Importing it

**Workspaces page → import**, pick the file, tick what you want, name it. Or:

```bash
curl -X POST http://127.0.0.1:8688/api/v1/workspaces/import -H 'Content-Type: application/json' \
  -d '{"name":"release notes (from A)","bundle":'"$(cat release-notes.cogitorium.json)"',"include_gears":true,"include_context":false}'
```

The reply is a report, and the report is the point. This one is from importing
the bundle above into a **different** install — one whose catalog has Anthropic
rather than a local model, and which already has a gear called `word_count`:

```json
{
  "workspace": { "id": 1, "name": "release notes (from A)",
                 "branch": "workspaces/release-notes-from-a-1" },
  "agents": 2,
  "wires": 1,
  "gears_imported": [],
  "gears_skipped": [
    { "name": "word_count",
      "why": "a gear with this name already exists in this install; it was left untouched and not bound to the imported workspace" }
  ],
  "context_files": 0,
  "unresolved_models": [
    { "agent": "orchestrator", "provider_type": "openai-compatible", "model_name": "qwen2.5:0.5b" },
    { "agent": "writer", "provider_type": "openai-compatible", "model_name": "qwen2.5:0.5b" }
  ]
}
```

Both agents came across with their roles, prohibitions and the wire between
them. Neither has a model, because this install has never heard of
`qwen2.5:0.5b` — and it says so instead of quietly binding them to whatever it
had. Add the model under **Models**, or point each agent at a local one.

### What import will not do

- **An imported gear is always `pending`.** A bundle is somebody else's
  executable code, and approval does not travel. Read it, dry-run it, then
  approve it — the same gate as a gear an agent forged for you.
- **A gear name already in use is skipped, not overwritten.** Other workspaces
  may depend on that gear; a bundle does not get to replace it, unapprove it,
  or bind itself to it.
- **A bundle does not choose a gear's timeout.** Raising one is an
  administrator's decision on the gear itself.
- **Context lands under the new workspace's own branch.** Paths in a bundle are
  relative, and anything trying to climb out of the branch is refused before
  the import creates anything at all.

The last one is why a refused bundle leaves nothing behind: the whole document
is checked first, so you never have to work out which half of an import
happened.

---

## 6. A receiver — a door for the rest of your system

A **receiver** takes an HTTP POST from outside and hands it to one agent. It has
an address, its own key, and a list of **tasks**; a task says what it accepts,
which agent gets it, what to tell that agent, and what counts as success.

It is called a receiver on screen and an **inlet** everywhere else — the URLs,
the API paths, the tables and the config keys. The word on screen changed
because "inlet" is a word this product invented and nobody arrives already
knowing; the strings did not, because callers outside your install hold them.

Any number of receivers per workspace, any number of tasks per receiver.

### Making one, in the interface

Open a workspace and turn on the **Receivers** panel from the row of panel
buttons at the top right.

1. **Give it an address.** One word, lowercase — `sites`, `tickets`, `drop`. It
   becomes part of every URL under this door: `POST /i/sites/…`. The second
   field is a note to yourself, shown only here.
2. **Press "add a receiver".** The key appears **once**, in full, right then.
   Copy it now: only its hash is stored, and nothing can show it to you again.
   Lost it, or leaked it? **new key** issues a fresh one and the previous string
   stops working the same instant — the receiver keeps its tasks and its
   address.
3. **Write its first task.** A receiver with no task answers 404 to everything,
   so the form is already open on a new one. Once a task exists it goes behind
   **add a task**.
4. **Press "add task".** The row that appears carries the full URL a caller
   posts to, and an **edit** button for when you got something wrong.

The fields on that form, and what each one decides:

| field | decides |
|---|---|
| task name | the last segment of the URL — `POST /i/sites/<name>` |
| accepts JSON / a file | whether the body is a payload or a file written into the workspace |
| which agent | who does the work. Only agents in this workspace, and it is checked as you save |
| schema | what a caller's JSON body must look like. **Empty accepts any JSON.** Press *start from an example* to get a real one to edit |
| instruction | the sentence the agent is given with the payload — the whole brief, since the run carries nothing else |
| tell somebody when it finishes | a URL the finished run is posted to. Empty is normal: the answer goes back on the caller's own connection |
| what has to have happened | the checks under **expect** — a gear that must have run, files that must exist, the shape of the answer |

### A worked receiver: a worker hands a page to the workspace

Something in your system already knows a URL needs looking at — a crawler, a
queue worker, a webhook. It should not have to know which agent, which model, or
what the prompt says. It posts the URL and gets an answer.

The schema, which is what makes this a contract rather than a hope:

```json
{
  "type": "object",
  "required": ["url"],
  "additionalProperties": false,
  "properties": {
    "url":   { "type": "string", "maxLength": 2000 },
    "depth": { "type": "integer", "minimum": 1, "maximum": 3 }
  }
}
```

The instruction, which is the whole brief the agent gets:

> Read the page at `url` and return what it is about, who publishes it, and
> anything that reads as a claim about a product. Nothing else.

And the call, from whatever you already have:

```bash
curl -X POST http://127.0.0.1:8688/i/sites/ingest-page \
  -H "Authorization: Bearer $INLET_KEY" \
  -H 'Content-Type: application/json' \
  -d '{"url":"https://example.com/pricing","depth":1}'
```

**The schema is enforced before any model is called**, so a caller that sends
the wrong thing costs nothing at all. Every one of these is the exact response
from a running install:

```json
{"run":1,"state":"refused_schema","error":"url is required but was not sent",
 "did":{"tools":[],"files":[],"model_calls":0,"tokens":{"in":0,"out":0}}}
```

```json
{"run":2,"state":"refused_schema","error":"depth: must be at most 3, got 9", …}
```

```json
{"run":3,"state":"refused_schema",
 "error":"the payload: \"priority\" is not a field this task accepts (it allows only: depth, url)", …}
```

`model_calls: 0` is the part worth noticing. That last one is `additionalProperties: false`
doing its job: a caller that invents a field is told so, instead of having it
silently ignored for a year.

The other two refusals, in full:

```json
{"error":{"message":"this inlet's key is required: send Authorization: Bearer <inlet key>. A key is issued from the workspace's inlet settings and shown once"}}
```

```json
{"error":{"message":"this inlet has no task called \"nope\""}}
```

401 and 404. An unknown address answers exactly as an unknown task does, so
somebody probing for names learns nothing you did not tell them.

### When the work takes longer than a request should

Reading a page and thinking about it can take a minute, and a worker that has to
hold a connection open for it is a worker you cannot restart. Hand the job off
instead:

```bash
curl -X POST http://127.0.0.1:8688/i/sites/ingest-page \
  -H "Authorization: Bearer $INLET_KEY" -H 'Content-Type: application/json' \
  -H 'Prefer: respond-async' \
  -H 'Idempotency-Key: page-8814' \
  -d '{"url":"https://example.com/pricing"}'
```

```
HTTP/1.1 202 Accepted
Location: /i/sites/runs/4
Preference-Applied: respond-async

{"run":4,"state":"queued","did":{"tools":[],"files":[],"model_calls":0,"tokens":{"in":0,"out":0}}}
```

Then either poll `GET /i/sites/runs/4` with the same key, or put a URL in **tell
somebody when it finishes** and be posted to instead. `Idempotency-Key` is what
makes the worker safe to retry: the same key twice is the same run, not two.

Without `Prefer: respond-async` nothing changes — the answer comes back on the
same response, exactly as it always has.

A task does not need a caller at all: the **Queue** panel can put it on a
schedule — `every 15m`, or five cron fields — and the payload is fixed when you
write the schedule. Same task, same instruction, same checks; nobody posting.

### Fixing a task

Press **edit** on the task row. Everything is there as you left it, and saving
keeps the task's id — so the deliveries already on record and any schedule
pointing at it stay pointed at it.

Renaming is allowed and it **moves the address**; the form says so, with the new
URL, before you save. The old one starts answering 404 and nothing here can tell
the callers holding it.

Over the API it is a `PUT`, and the body is the whole task:

```bash
curl -X PUT http://127.0.0.1:8688/api/v1/inlet-tasks/1 -H 'Content-Type: application/json' -d '{
  "name": "ingest-page",
  "accepts": "json",
  "schema": { "type": "object", "required": ["link"] },
  "agent": "orchestrator",
  "instruction": "Read the page at link and say what it is about."
}'
```

The whole task, not just what changed: a request with no `schema` in it would
otherwise mean *accept anything*, and widening a door is not something that may
happen because a field was left out. Everything that refuses a bad task on the
way in refuses it here too — one validator, both routes.

### The same, over the API

```bash
curl -X POST http://127.0.0.1:8688/api/v1/workspaces/1/inlets -H 'Content-Type: application/json' -d '{"address":"drop","description":"files from outside"}'
```

The response carries the first key. `POST /api/v1/inlets/1/key` issues a new one.

### A task that unpacks an archive

Everything below is a real capture from a running install, not an illustration.

```bash
curl -X POST http://127.0.0.1:8688/api/v1/inlets/1/tasks -H 'Content-Type: application/json' -d '{
  "name": "archive",
  "accepts": "file",
  "content_type": "application/zip",
  "agent": "filer",
  "instruction": "An archive arrived. Call gear_unpack with into=\"unpacked\", passing its path in _files.",
  "expect": { "runs_gear": "unpack", "produces_files": 1 }
}'
```

Then a key, which is shown once:

```bash
curl -X POST http://127.0.0.1:8688/api/v1/inlets/1/key
```

And the delivery — a plain POST from anything you already have:

```bash
curl -X POST http://127.0.0.1:8688/i/drop/archive \
  -H "Authorization: Bearer $INLET_KEY" \
  -H 'Content-Type: application/zip' \
  --data-binary @bundle.zip
```

```json
{
  "run": 2,
  "state": "completed",
  "result": "The archive has been unpacked into the folder named \"unpacked\"…",
  "did": {
    "tools": [ { "name": "gear_unpack", "ok": true, "ms": 47 } ],
    "files": [
      { "path": "gears/unpack/20260812-031936-08a6341e/unpacked/meta.json",   "bytes": 41 },
      { "path": "gears/unpack/20260812-031936-08a6341e/unpacked/note.txt",    "bytes": 45 },
      { "path": "gears/unpack/20260812-031936-08a6341e/unpacked/numbers.csv", "bytes": 29 }
    ],
    "model_calls": 2,
    "tokens": { "in": 3936, "out": 139 }
  }
}
```

The file was written into the workspace and the agent was given its **path**,
never its bytes — which is what lets a gear open it, and what stops a megabyte
of base64 landing in a prompt.

### Read `did`, not the sentence

`did` is the record of what happened: which tools ran and whether they
succeeded, which files exist afterwards, what it cost. It is on every response
and every run, on success and on failure alike.

It exists because the sentence cannot be trusted. A model asked to call a gear
answered *"The comma-separated text file was aligned and formatted using
gear_format … into folder formatted"* having made **zero tool calls**. The
delivery said 200 and completed. Nothing had happened. With `did`, that run
reads:

```json
"did": { "tools": [], "files": [], "model_calls": 1, "tokens": { "in": 11, "out": 7 } }
```

An empty tool list is the answer.

### `expect` — say what success is, and have it checked

Every field is optional; a task without the block behaves exactly as it did
before.

| field | means |
|---|---|
| `runs_gear` | this gear must have run and succeeded |
| `produces_files` | at least this many files must exist afterwards |
| `schema` | the answer must fit this shape |
| `answer_from` | `"agent"` (default) or `"gear"` — where the result comes from |

`runs_gear` and `produces_files` are checked against the **record**, never
against the agent's text. A run with a confident answer and an empty record
fails, and says both halves:

```
this task requires the gear "unpack" to have run and succeeded, and the record
holds no successful call to it. What this run did: 1 tool call: gear_unpack
(failed, 72ms); no files appeared; 2 model calls, 3936 tokens in and 139 out.
```

That is a real refusal, from a run where the gear was called and died. Note it
says *no successful call* rather than guessing that it never ran — the record
knows the difference, and so does whoever is paged.

### `answer_from: "gear"` — take the model out of the answer

For a deterministic job the agent is a router and its narration is not
evidence. A task that formats a file:

```json
"expect": { "runs_gear": "format", "produces_files": 1, "answer_from": "gear" }
```

```bash
curl -X POST http://127.0.0.1:8688/i/drop/table \
  -H "Authorization: Bearer $INLET_KEY" -H 'Content-Type: text/plain' \
  --data-binary @people.txt
```

```json
{
  "run": 3,
  "state": "completed",
  "result": "{\"wrote\": \"formatted/3-payload.aligned.txt\", \"rows\": 3}\n",
  "did": { "tools": [ { "name": "gear_format", "ok": true, "ms": 32 } ],
           "files": [ { "path": "gears/format/…/formatted/3-payload.aligned.txt", "bytes": 78 } ] }
}
```

The result is the gear's own stdout. The file is in the workspace:

```
name   role      city
ada    engineer  london
grace  admiral   new york
```

### What a receiver will not do

- **An unknown key is 401, an unknown task 404**, and a payload that does not
  match the task is **400 before any model is called** — a malformed request
  from somebody's cron costs nothing.
- **A delivery writes nothing into the operator's conversation.** Otherwise
  request two hundred would carry the previous hundred and ninety-nine.
- **The run is treated as third-party from the first byte**, so the agent
  behind a receiver cannot write to the instruction library, the gear catalog or
  the workspace graph — the same latch that stops text from the web doing it.
- **`web_search` is not offered**: it pauses the turn waiting for a person to
  approve the query, and there is nobody there.
- **One run at a time per workspace.** A delivery that arrives while one is
  running **waits** — it is `queued`, which is a state in the ledger and a row
  in the Queue panel, not a failure. What is refused is a queue that has stopped
  being one: past `queue_max_per_workspace` waiting deliveries the next gets 429
  and says how many are ahead of it.

---

## 6b. Handing this install to Claude Desktop or Cursor

Everything you just built can be a tool in somebody else's client. Cogitorium
speaks MCP over stdio:

```bash
cogitorium mcp --server http://127.0.0.1:8688 --token $COGITORIUM_TOKEN \
  --inlet-key sites=$INLET_KEY
```

That is the whole integration — in a client's configuration, that command line
goes where it keeps its servers. Approved gears and JSON receiver tasks appear
as tools:

```
gear_wordcount              → runs the gear in its sandbox
receive_sites_ingest-page   → delivers to the receiver, schema checked first
```

Real output from the exchange, with a gear called `wordcount` approved and the
`sites` receiver open:

```json
{"jsonrpc":"2.0","id":2,"method":"tools/list"}
→ tools: ["gear_wordcount", "receive_sites_ingest-page"]

{"jsonrpc":"2.0","id":3,"method":"tools/call",
 "params":{"name":"gear_wordcount","arguments":{"text":"one two three four five"}}}
→ isError: false, text: "5"
```

**The approval gate still holds.** Disable that gear and call it again with the
same tool list a client already fetched:

```
isError: true — The gear "wordcount" is disabled, not approved, so it cannot be run.
```

Not a crash and not a silent run: the model is told, in a sentence, and can
say so. The route behind it answers 403 to anything unapproved.

**What is deliberately absent.** No management: an MCP client cannot create an
agent, draw a wire, edit a prohibition or approve a gear. It is a guest with a
tool list. And the two credentials are separate on purpose — `--token` decides
what can be listed, a receiver's own key decides what may be delivered to it,
and there is no default for the second.

### The other direction: somebody else's MCP server as an agent's tools

An agent can also be granted an external MCP server's tools, the way it is
granted a gear. It is off unless you switch it on:

```yaml
mcp_clients: true
```

**Why it is off.** A gear is source this install holds — you read it, you
approved it, and it runs in a container that cannot see the server's files. An
external MCP server is a command. Cogitorium never sees its source, and the
child runs on this host as the server's own user, so approving one means it can
read the database and the provider keys in it. What bounds that is who may do
it, not what it can reach.

So it is three deliberate acts, all administrator-only, and no agent can reach
any of them:

```bash
# 1. Install it. It exists, and it is pending.
curl -X POST http://127.0.0.1:8688/api/v1/mcp-servers -H 'Content-Type: application/json' \
  -d '{"name":"files","command":"npx","args":["-y","@modelcontextprotocol/server-filesystem","/srv/shared"]}'

# 2. Probe it: started once, given nothing at all, and asked what it offers.
curl -X POST http://127.0.0.1:8688/api/v1/mcp-servers/1/probe

# 3. Approve the server, then each tool you actually want.
curl -X PATCH http://127.0.0.1:8688/api/v1/mcp-servers/1 -H 'Content-Type: application/json' -d '{"status":"approved"}'
curl -X PATCH http://127.0.0.1:8688/api/v1/mcp-tools/3  -H 'Content-Type: application/json' -d '{"approved":true}'

# Then grant it — to a workspace, or to one agent in it.
curl -X POST http://127.0.0.1:8688/api/v1/workspaces/1/mcp-bindings \
  -H 'Content-Type: application/json' -d '{"server_id":1}'
```

The tools then appear to that agent as `mcp_files__read_file` and so on, beside
its gears, and a call is dispatched to the server you installed.

**Per tool, not per server**, and that is the part worth keeping: a server that
grows a `run_shell` tool after you approved it has grown one nobody agreed to,
and it stays inert until you look. Editing the command puts the server back to
pending, and the command is re-checked at every spawn — though that check covers
the command line, not the bytes at the end of it, so `@latest` refetches and
nothing notices.

---

## 6c. From a terminal, and from a script

The same binary that serves the interface is also a client for it. Nothing in
this section can do anything the browser cannot; what it adds is an exit code a
shell can branch on and output narrow enough to pipe.

It reads two environment variables, so the address and the token are said once:

```bash
export COGITORIUM_URL=http://127.0.0.1:8688
export COGITORIUM_TOKEN=…
```

`--server` and `--token` override them per command. On a loopback address a
local call is already the admin, so on your own machine the token is optional —
change the listen address and it stops being.

**Look around.**

```
$ cogitorium workspaces
ID  NAME        DESCRIPTION
1   code court  several models write the same program, review each other, and the measured winner is returned

$ cogitorium gears list
ID  NAME       STATUS    RUNTIME  V  DESCRIPTION
1   wordcount  approved  python   1  Count the words in a piece of text.

$ cogitorium receivers list --workspace 1
ADDRESS  KEY     TASK                  ACCEPTS  AGENT
sites    issued  ingest-page           json     orchestrator
tickets  issued  (none — answers 404)
```

A receiver with no tasks is shown as one, because a door that answers 404 to
everything looks identical to a missing door from the outside, and the
difference matters when you are working out why a delivery bounced.

**Run a gear.** Its output goes to stdout, its exit code is the gear's own:

```
$ cogitorium gears run wordcount --args '{"text":"one two three four five"}'
5
```

The approval gate is the same gate. This is a gear that arrived in a bundle and
has not been approved on this install yet:

```
$ cogitorium gears run wordcount --args '{"text":"one two three"}'
error: 403 Forbidden: gear "wordcount" (status pending): gear is not approved
for execution — the operator must approve it in the gear catalog first
$ echo $?
1
```

**Deliver to a receiver**, with that receiver's own key rather than your token —
a door's credential opens that door and nothing else, and it is the one the
ledger records:

```bash
cogitorium receivers deliver sites/ingest-page \
  --key "$COGITORIUM_INLET_KEY" \
  --data '{"url":"https://example.com","depth":2}'
```

The call is held open until the work finishes, which is what you want at a
prompt and wrong in anything with a timeout of its own. `--async` takes a run
number instead and lets the work carry on:

```
$ cogitorium receivers deliver slowdoor/think --async --data '{"q":"…"}'
{"run":5,"state":"queued","did":{"tools":[],"files":[],"model_calls":0,"tokens":{"in":0,"out":0}}}
```

The key is checked at the door either way:

```
$ cogitorium receivers deliver slowdoor/think --key not-the-key --data '{}'
error: 401: this inlet's key is required: send Authorization: Bearer <inlet key>.
A key is issued from the workspace's inlet settings and shown once
```

**Read a delivery back.** This is a real run from a real install, and it is the
reason the command exists in this form:

```
$ cogitorium run 3
run 3  refused_schema  sites/ingest-page  agent orchestrator

error: the payload: "priority" is not a field this task accepts (it allows only: depth, url)

did: {"tools":[],"files":[],"model_calls":0,"tokens":{"in":0,"out":0}}
$ echo $?
1
```

Anything that did not complete exits non-zero, so a script can stop on it
without reading the text. And `did` is the record's own account — no tools, no
files, no model calls — which is how you tell a refusal at the door from an
agent that ran and produced nothing.

**See and stop what is running.** Three deliveries into a workspace whose
provider had stopped answering — one claimed, two waiting behind it:

```
$ cogitorium queue list --workspace 1
1 running, 2 waiting
UNIT  STATE    KIND      RUN  SINCE
1     claimed  delivery  1    2026-08-16T02:28:37Z
2     queued   delivery  2    2026-08-16T02:28:47Z
3     queued   delivery  3    2026-08-16T02:28:47Z

$ cogitorium queue cancel 1
stopped 1
$ cogitorium queue cancel 3
stopped 3

$ cogitorium queue list --workspace 1
1 running, 0 waiting
UNIT  STATE    KIND      RUN  SINCE
2     claimed  delivery  2    2026-08-16T02:28:47Z
```

`cancel` stops the work, not just the row — the same route the interface's stop
button uses, and it takes a unit that is already running as readily as one that
is still waiting. Unit 2 moved up on its own once the workspace was free again.
The ledger keeps what happened rather than losing the run:

```
$ cogitorium run 1
run 1  interrupted  slowdoor/think  agent orchestrator

error: stopped by admin
```

**Move a workspace between installs.** The export is the same document as the
one in section 5; these are the two commands that carry it:

```
$ cogitorium workspaces export 1 --gears -o court.json
wrote court.json (4828 bytes)

$ COGITORIUM_URL=http://the-other-install:8688 cogitorium workspaces import court.json --gears
workspace 1 "code court" — 8 agents, 15 wires, 0 context files
gears: wordcount (pending — approve them before anything can run them)
agent orchestrator wants anthropic/claude-opus-4-6, which this install does not have
agent author-gpt wants openai-compatible/gpt-5.2, which this install does not have
…
```

That is a real transcript between two installs. The report goes to stderr and
the summary to stdout, so a pipeline keeps the line it wants and a person still
sees what did not come across. Skips and unresolved models are printed rather
than counted, because a bundle whose gears were all skipped imports
"successfully" and leaves you a workspace that cannot do its work.

**What is deliberately not here.** No creating agents, drawing wires or
approving gears. Those are decisions made while looking at a canvas or a source
listing, and a flag is a worse place to make them than a screen that shows what
is being decided. Everything the command line does, it does over the same HTTP
API described in [openapi.yaml](openapi.yaml) — so anything missing here is one
`curl` away, not blocked.

---

## 7. A worked arrangement: a panel that judges code

Everything so far has been one agent doing one job. This is what the wiring is
for: eight agents, four models, and a decision made on evidence.

The operator states a requirement. Four authors, each on a different model,
write a program for it and then read one another's submissions. Two critics look
at every submission from one angle each. A referee reads all of it and picks the
winner.

![The code court on the blueprint](assets/13-code-court.png)

Read the edges, because they are the arrangement:

| edge | from → to | what it makes possible |
|---|---|---|
| `writes` | orchestrator → four authors | the orchestrator may hand the requirement out |
| `submits` | each author → both critics | a critic may be asked about any submission |
| `reports` | each critic → referee | the referee may ask a critic what it found |
| `decides` | orchestrator → referee | the verdict comes back to the operator |

Nobody's role says "you may review the others". The wire says it. Delete the
edge from `author-mini` to `critic-speed` and that submission stops being
measurable, on the next turn, with no prompt edited.

### The part that is not an opinion

"The best program" is a judgement. "The fastest program" is a measurement, and
the two must not be confused, so `critic-speed` does not read code and guess —
its role says to call the bench gear on every submission with the same input and
report what it measured. The referee carries prohibitions to match:

```
Never pick a program the bench did not run.
Never call a program fastest without a measurement.
```

Those are the last thing in its prompt and they are not overridable by anything
the authors write in their own defence. That is the whole reason prohibitions
exist as a separate field rather than a paragraph in a role.

### Take this arrangement

The workspace above is a download. It carries the agents with their roles and
prohibitions, the wires, and the canvas positions — and no key, no token, no
conversation, because a bundle has nowhere to put one.

**[code-court.cogitorium.json](assets/code-court.cogitorium.json)** — 4 KB.

```bash
curl -X POST http://127.0.0.1:8688/api/v1/workspaces/import \
  -H 'Content-Type: application/json' \
  -d "{\"name\":\"code court\",\"bundle\":$(cat code-court.cogitorium.json)}"
```

```json
{ "agents": 8, "wires": 15, "gears_imported": [], "gears_skipped": [],
  "context_files": 0, "unresolved_models": [] }
```

That report is from importing this exact file. `unresolved_models` is empty
because the install it landed on had those four models in its catalog; on one
that does not, each agent still arrives — with its role, its prohibitions and
its wires — and the models it could not find are named there for you to bind.
Nothing is silently substituted.

The bench gear is not in the file. Gears travel only when you ask for them, and
this arrangement is more useful with a bench you wrote for your own language and
your own definition of fast.

---

## 8. Files, the editor and diffs

Each workspace has its own directory. **Files** shows the tree; clicking a file
opens it in **Editor**, which is a real editor with syntax highlighting, not a
preview. Save writes the file; the diff view shows what changed, computed
locally — there is no git involved anywhere in this.

Files up to 2 MiB are editable; larger ones open read-only, and that limit is
not configurable. There is no upload, no download, no rename and no delete — a
directory comes into existence when you save a file into it.

![A diff](assets/12-diff.png)

---

## 9. Memory

Context and memory are stored and versioned by Contextverse's `contextd`.
Without it the server starts, says so at `GET /api/v1/context/status`, and
memory does nothing:

```json
{"available":false,"error":"no context space initialized — run: contextd init solo"}
```

Homebrew, Scoop and the container image bring `contextd` along. The container
initialises the space on first start, which fetches a template from GitHub — so
a first `docker compose up` on a machine with no outbound network comes up with
memory unavailable and says why in the log.

Each workspace gets its own branch plus a shared one, so one workspace's memory
does not leak into another's. **Context** (admin only) lists the space and lets
you read and edit files in it. Binding context to a workspace or to a single
agent is what puts it in front of the model — see the agent inspector for the
order it lands in.

---

## 10. Letting an agent search the web

Off by default, and there are three locks. All three must be open, in order:

1. **The install.** `egress: true` in the config file, or `COGITORIUM_EGRESS=1`.
   There is no route, no setting and no database row that turns this on — an
   agent cannot reach it, so no tool call can flip it. It also requires a
   sandbox, and the server refuses to start otherwise:

   ```
   egress is enabled but gears are not sandboxed. An unsandboxed gear runs with this server's file access and can rewrite the configuration and the grants table, so the gate would be decorative. Install Docker, or set egress: false.
   ```

   A credential for the search destination is required too, and a corporate
   proxy is refused rather than ignored — `HTTPS_PROXY` set means every address
   check would inspect the proxy instead of the real destination.

2. **The grant.** An operator draws it on the blueprint, per agent.

3. **The query.** Every individual search stops the turn and waits for a person
   to approve that exact query. The audit records which kind of authentication
   each decision had, so a row is never mistaken for stronger evidence than it is.

Only `1` and a case-insensitive `true` count as on. `COGITORIUM_EGRESS=yes`
evaluates to false — and overrides a config file that said true.

---

## 11. The terminal

Off by default: it is interactive code execution over HTTP. Turning it on takes
`terminal: true`, and it **also** requires a sandbox — without one the request
is refused rather than served with the server's own file access. In Kubernetes
the chart refuses to enable it at all, because there is no Docker inside a pod.

While a gear runs, its output streams here live.

---

## 12. More than one person

**People** (admin only) → **add user**. A token is shown once:

> — shown once, only its hash is stored. Copy it now.

Three roles exist: `admin`, `team-lead`, `member`. A member sees the workspaces
they own plus those shared with a team they belong to, and nothing else. Sharing
is per team, not per person. **clone** copies a workspace to you — blueprint and
all — so someone can build on your arrangement without touching it.

Admin-only: Context, Terminal, People, the model catalog's destructive actions,
and the internet gate.

![The access map](assets/06-people-map.png)

---

## 13. When it refuses

Every message below is the exact text.

| What you did | What you get |
|---|---|
| Named a model id that is not in the catalog | `model 1: not found` (404) |
| Left `base_url` empty on an openai-compatible provider | `base_url is required for openai-compatible providers` |
| Seeded a short admin token | `COGITORIUM_ADMIN_TOKEN is 5 characters; it seeds the admin's credential, so at least 24 are required` |
| Set `COGITORIUM_TERMINAL=yes` | Nothing. Only `1`/`true` count; the terminal stays off |
| Pointed `--config` at a file that is not there | `read config /path/nope.yaml: open /path/nope.yaml: no such file or directory` |
| Turned on egress without a sandbox | `egress is enabled but gears are not sandboxed…` |
| Asked for `sandbox: docker` with the daemon down | `sandbox: docker was requested but the daemon does not answer` |
| Built with `go build` and skipped the UI build | `web UI is not built into this binary — build with make build` (503) |
| Ran a gear that was never approved | the run is refused; the gear is not executed |
| Used `--port` | `Error: unknown flag: --port` |

`config.yaml` is only ever read, never written — a fresh data directory contains
the database and nothing else.

---

## 14. What is not here

So that you do not go looking:

- No way to duplicate an agent; make a new one and copy the role across.
- No way to chat with a worker agent directly — every turn goes through the orchestrator.
- No editing a workspace's name or description after it is created.
- No editing a gear's source in the interface, no per-version approval, no rollback.
- No per-gear network, memory or CPU setting; the sandbox limits are fixed.
- No upload, download, rename or delete for workspace files.
- No token management: tokens cannot be listed, named, rotated or expired individually.
- No self-service signup, and no password-change screen.
- No screen for the search audit log, though the route exists.
- No workspace-wide prohibitions; they are per agent, and a created agent inherits its creator's.
- A bundle carries the conversation nowhere — it is a template, not a transcript.
- No receiver may target a gear directly; a task names an agent, and the agent calls the gear.
- No streaming from a receiver, and no fan-out of one delivery to several agents.
- Consuming MCP is off unless you switch it on, and an external MCP server runs
  on the host outside the sandbox — see the reference before you do.
- No remote agents: every agent's turn is taken by this server's own process.
- No warm containers unless you ask: `sandbox_pool` is 0, so every gear gets a
  machine with no history. Turning it on is faster and is a real trade — the
  reference says exactly what it costs.
- No browser an agent drives directly: a gear granted the browser environment
  drives one, and the agent calls the gear. There is no live page an agent
  clicks around in, and no session that survives a run.
- No terminal in-cluster. A terminal is an interactive attachment and a gear Job
  is run-to-completion; the Kubernetes backend runs a gear rather than attaching
  to one.
- No private thread inside a shared workspace: two people in one workspace share its
  conversation. Separate workspaces are how two people stay out of each other's way.
