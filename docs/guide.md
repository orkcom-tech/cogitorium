---
layout: default
title: Guide
permalink: /guide/
description: A worked walkthrough of Cogitorium — from an empty install to agents with tools, with every command and every error message taken from a real run.
---

# Guide

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

There is **no add-agent button anywhere in the interface**. Worker agents are
created by the orchestrator, on your instruction, or through the API. That is
deliberate: the orchestrator is the operator's single point of contact.

Ask for one in the chat:

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

![A wired workspace](assets/02-workspace-wired.png)

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
   `variables_dir` and `secrets_dir`, which is the shape Kubernetes mounts a
   ConfigMap and a Secret in;
3. the workspace's own overrides, which is how one gear serves staging and
   production without being edited.

A name nothing supplies **stops the run and names it**, rather than handing the
gear an empty string that fails somewhere far away with a message about nothing.
The approval screen says so in advance, per name.

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

## 6. A door for the rest of your system

An **inlet** takes an HTTP POST from outside and hands it to an agent. It has an
address, its own key, and a list of **tasks**; a task says what it accepts,
which agent gets it, what to tell that agent, and what counts as success.

Any number of doors per workspace, any number of tasks per door. Add one from
the workspace page, or:

```bash
curl -X POST http://127.0.0.1:8688/api/v1/workspaces/1/inlets -H 'Content-Type: application/json' -d '{"address":"drop","description":"files from outside"}'
```

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

### What a door will not do

- **An unknown key is 401, an unknown task 404**, and a payload that does not
  match the task is **400 before any model is called** — a malformed request
  from somebody's cron costs nothing.
- **A delivery writes nothing into the operator's conversation.** Otherwise
  request two hundred would carry the previous hundred and ninety-nine.
- **The run is treated as third-party from the first byte**, so the agent
  behind a door cannot write to the instruction library, the gear catalog or the
  workspace graph — the same latch that stops text from the web doing it.
- **`web_search` is not offered**: it pauses the turn waiting for a person to
  approve the query, and there is nobody there.
- **One run per workspace.** A second delivery while one is running gets 429.

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

- No add-agent button; the orchestrator creates agents, or the API does.
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
- No inlet may target a gear directly; a task names an agent, and the agent calls the gear.
- No streaming from a door, no fan-out to several agents, and no inlets on a schedule.
- Gears do not run as Kubernetes Jobs, and there are no remote agents.
