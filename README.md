<div align="center">

<img src="docs/assets/logo.png" alt="Cogitorium" width="200">

# Cogitorium

**A modular workbench for agentic development.**

Every part moves, and every part moves two ways — by your hand or by the
orchestrator. One binary, your models, your machine, and no telemetry, ever.

[Documentation](https://orkcom-tech.github.io/cogitorium/) ·
[Features](#features) ·
[Install](#install) ·
[Guide](https://orkcom-tech.github.io/cogitorium/guide/) ·
[Licence](#licence)

</div>

---

Cogitorium is a **workbench for graph engineering**: you build a graph of
agents, wire what each one may do to the others, and get a pipeline with models
inside it that behaves like a pipeline — deterministic where determinism is
available, the same from one run to the next, and reshapeable without being
rebuilt. One Go binary, your own models, no telemetry.

## Features

- 🕸 **Graph engineering** — agents are nodes, and every edge is a permission the
  runtime enforces: a **wire** grants delegation, a **gear link** grants a tool,
  a **binding** is what an agent knows, an **outward gate** grants the internet.
  Delete an edge and the thing it allowed is not discouraged, it is impossible.
  [→](#graph-engineering)
- 🎛 **Two hands on the same controls** — build it by telling the orchestrator, or
  by working the canvas and panels yourself. Both write to the same objects, so
  there is no conversion between them and no "advanced mode" holding the real
  controls. [→](#what-it-does)
- 🧠 **A model per agent** — an expensive frontier model reasons while free local
  ones write docs and run checks, in one topology, with the token spend shown per
  agent. Anthropic, OpenAI-compatible, Ollama, or all at once.
- ⚙️ **Gears: tools that outlive the conversation** — an agent forges a script,
  it lands in a versioned catalog, and nothing runs until you approve it. It
  executes in a container holding none of the server's files, with named
  credentials it never sees the values of and a network allowlist you set beside
  the source.
- 🚪 **Receivers: a door for your own systems** — an address and a key; data
  arrives by HTTP, an agent works on it, the result comes back. The payload is
  checked against a JSON Schema **before any model is called**, so a malformed
  request costs nothing.
- 📋 **Judged by the record, not the sentence** — every delivery carries what
  actually ran: which tools, which files appeared, what it cost. A task states
  its own success conditions and they are checked against that record, so a
  confident answer over an empty record fails.
- ⏱ **It can be left alone** — work queues instead of being dropped, starts on a
  cron line or an interval, can be handed off with `Prefer: respond-async` and
  called back when it finishes, and can be stopped mid-run — the work, not just
  the row.
- 🔒 **Prohibitions and an internet gate** — rules an agent must never break go
  last in its prompt and are inherited by agents it creates; reaching the web is
  a per-agent grant, and every search still stops for a human to approve that
  exact query. [→](#letting-agents-reach-the-web)
- 📦 **Portable and local-first** — a workspace exports as one JSON document you
  can hand to another install. Everything runs on your machine, and the binary
  talks to the model providers you configure and to nothing else.
  [→](#no-telemetry)

---

**The goal is a workflow with a model inside it that still behaves like a
workflow.** Deterministic where determinism is available, consistent from one
run to the next, and flexible enough to be reshaped without being rebuilt. A
model is the least predictable component anyone has ever put in a pipeline;
everything here exists to bound it — a payload checked against a schema before
a model is called at all, a delegation that is only possible along a wire you
drew, work that is judged by the record of what ran rather than by the sentence
the model wrote, an answer that can be taken from a gear's stdout instead of
from the model's retelling of it, and a ledger that keeps every run whether it
succeeded or not.

Most agent tooling asks you to accept a black box: one model, one vendor, a
conversation that wanders, and a bill you cannot attribute. Cogitorium is the
other shape. **Everything in it is a part you can take hold of** — the agents,
the wires between them, the context each one carries, the tools they forge, the
doors your own systems deliver through, the clock that starts work without you.

And each of those is configurable **twice over**: by hand on the bench, or by
telling the orchestrator to do it. Both hands reach the same objects, and
neither hides its work from the other — draw a wire yourself and the
orchestrator sees a capability it now has; ask it to hire an agent and the
agent appears on your canvas, wired, with a model, and yours to change. That is
where the range comes from: the same install is a two-agent draft, a panel of
eight models judging each other, and a stage inside somebody else's pipeline,
without becoming a different product each time.

It runs as a single Go binary with the interface embedded in it. Point it at a
frontier API, at Ollama on your laptop, at a box in your homelab, or at all
three at once — it does not care and it does not phone home.

![The workspace: an orchestrator conversation with the blueprint beside it](docs/assets/02-workspace-wired.png)

> **Status: early development.** The commands, the API and the on-disk layout
> are not stable yet.

---

## What it does

**A workspace is a group of agents behind one orchestrator chat.** You talk to
the orchestrator; it creates the other agents, gives them roles and models,
wires them together, and hands them work — and everything it does is a visible
step on the timeline rather than something that happened inside a black box.

**Or you do it yourself.** Nearly every one of those acts has a control on the
bench: change any agent's model, role and prohibitions; draw a wire or cut it;
hand an agent a gear or revoke it; write a gear from scratch and approve it;
put an instruction in the shared library; open a receiver and write the task
behind it; put that task on a clock; edit the workspace's files; stop anything
in the queue. Neither route is the "real" one and there is no import step
between them — the orchestrator and the panels write to the same objects, so a
workspace can be started by conversation and finished by hand, or the reverse,
in the middle of one session.

That includes hiring. **`+ agent` on the blueprint** puts a new node on the
canvas — a name, a model and a role — and it arrives with no capabilities at
all: nothing may delegate to it and it may delegate to nothing until you draw
an edge. Which is the honest shape of a new agent in a graph, and the reason
the button belongs on the canvas rather than in a list.

**Every agent gets its own model.** That is what makes the economics
expressible: an expensive frontier model does the reasoning while free local
models write the docs and run the checks, all inside one topology. The token
spend is shown per agent, so a cheap arrangement can be told apart from an
expensive one that merely feels cheap.

### Graph engineering

**The edge IS the capability, not a picture of one.** What
you build here is a graph — agents are the nodes, and every edge is a
permission the runtime enforces. Four kinds, on four layers of the same canvas:
a **wire** is permission to delegate, a **gear link** is permission to call
that tool, a **binding** is what an agent knows, and the **outward gate** is
permission to ask the internet anything at all. Draw an edge and a capability
exists; select it and press Delete and it stops being possible — not
discouraged, not documented against. Impossible.

That is the shift worth naming. Prompt engineering asks what to say to one
model. Graph engineering asks what the parts may do to each other — and the
answer is a structure you can look at, change, hand to somebody else, and
diff.

**Agents build tools that outlive the conversation.** When an agent needs a
capability that does not exist it can forge one — a script, a small program —
which is registered in a catalog, versioned, and callable afterwards by other
agents and by you. Nothing runs until you approve it, and it runs inside a
container holding none of the server's files. Approving is also where you say
what that code may hold — named credentials it reads from its environment, never
sees the values of in a prompt, and never prints — and where it may reach: the
network, and a list of hosts, or nothing at all. Both are on one screen, beside
the source, because a decision made half-blind is not a decision.

**Context is a managed resource.** It is stored and versioned by
[Contextverse](https://github.com/orkcom-tech/contextverse), with a branch per
workspace and per agent. You can read exactly what reaches an agent's prompt,
and edit or delete it — including the things it remembered that you would
rather it forgot.

**An agent can be told what it must never do.** Prohibitions are the last thing
in its prompt, and an agent the orchestrator creates inherits them — otherwise
a standing rule would be one tool call from being routed around by hiring
someone without it.

**It can be a stage in your own pipeline.** A receiver is a door with an address
and its own key: data arrives by HTTP, an agent works on it, a result comes
back. Any number of doors, any number of tasks behind each, and a task is
editable in place rather than deleted and rewritten. A file is written into the
workspace and handed to a gear as a path — so a zip arrives, a gear unpacks it,
and the files are there.

**And it can be left alone.** A workspace already busy makes the next job
**wait** rather than throwing it away. Work can start because a clock said so,
on a duration or a cron line in your own timezone. A caller can hand a job off
and be told when it is finished, read the run back with the key it already has,
and fetch the files it produced. An operator can see the queue and stop
anything in it — the row and the work, not just the row. And a budget, if you
set one, refuses before the next model call rather than reporting afterwards.

**And the caller is told what happened, not only what the agent says.** Every
delivery carries a record: which tools ran, which files exist afterwards, what
it cost. A task can state what success is — this gear must have run, this many
files must appear — and it is checked against that record rather than against
the model's sentence. A confident answer over an empty record fails.

**A workspace is portable.** It exports as one JSON document — the arrangement,
not a database dump — with its gears and its context as separate opt-ins.
Wires and models are referenced by name because ids from another install mean
nothing, nothing private is in the document, and an imported gear always
arrives unapproved: it is somebody else's executable code, and approval does
not travel.

---

## Install

Every route installs the same binary. Context and memory are stored by
[Contextverse](https://github.com/orkcom-tech/contextverse), so the channels
that can bring it do.

**macOS and Linux — Homebrew** (brings `contextd` with it):

```sh
brew install orkcom-tech/tap/cogitorium
cogitorium serve
```

**Windows — Scoop** (brings `contextd` with it):

```sh
scoop bucket add contextverse https://github.com/orkcom-tech/scoop-bucket
scoop install cogitorium
```

**Docker** — the image carries `contextd` and sets up its space on first start:

```sh
docker compose up --build
```

**Linux packages** — `.deb` and `.rpm` on the
[releases page](https://github.com/orkcom-tech/cogitorium/releases), with a
systemd unit. They recommend `contextd` rather than requiring it, because it
ships from GitHub rather than a distribution repository; the postinstall says
what to run.

**Desktop application** — attached to each release for macOS, Windows and
Linux. The same server and interface in a native window; it picks a free port
rather than 8688, so it and `cogitorium serve` can run at the same time. Not
signed with a platform identity, so the first launch needs one extra click —
[the documentation says exactly which](https://orkcom-tech.github.io/cogitorium/#install).

**From source** — Go ≥1.25 and Node ≥22; Docker if you want gears and the
terminal sandboxed:

```sh
git clone https://github.com/orkcom-tech/cogitorium
cd cogitorium
make build
./bin/cogitorium serve
```

Archives on the releases page are signed. `checksums.txt` carries a cosign
signature and certificate, so a download can be traced to the workflow that
built it rather than merely proven uncorrupted — the verification command is in
the release notes.

Then open <http://127.0.0.1:8688>. On a local install you are the admin and
there is no login screen; the same binary asks for credentials the moment it
listens on anything but loopback.

Add a model under **Models**, then press **+ New workspace**.

![The workspaces list, with a workspace shared across two teams](docs/assets/01-workspaces.png)

---

## The interface

Panels, not tabs. Chat, blueprint, files, editor, terminal and the agent
inspector are panels on a grid: put one at any edge, slide it out over the
centre, float it, or hide it. Arrangements can be saved, and five are
ready-made.

![The tree, the conversation, a file open in the editor and a shell underneath](docs/assets/03-build-layout.png)

Clicking a file in the tree hands the room to the editor: the blueprint and the
rosters step aside, the conversation and the shell stay. The editor highlights
the dozen languages a workspace actually contains, and shows unsaved edits as a
diff against what is on disk. A gear past its first version diffs against the
previous one — an approval covers exact content, so what you want before
approving is what changed. All of it is written in the product rather than
installed: no highlighter library, no diff library, no editor component.

![Unsaved edits, shown as a diff against what is on disk — in Instrument](docs/assets/12-diff.png)

Nothing has to be docked at all. Any panel can be pulled off the grid into a
window you move, resize, roll up or close.

![The conversation and the file tree as floating windows](docs/assets/11-floats.png)

An agent's card shows what it is, what it knows, what it may call, and what it
has spent.

![The agent inspector beside the conversation](docs/assets/05-agent.png)

### Three looks, and a palette under them

Pick the room you want to work in. **Sketch** is the default and everything
above this line is drawn in it: paper ground, ink outlines whose corners
disagree, handwriting on the chrome — and code, logs, schemas and figures left
in the type they were already in, because a workbench you cannot read a stack
trace in is not one. It opens light, and switching to dark gives the same
drawing in chalk on slate.

**Instrument** is a bench instrument — no light at all, hairlines instead of
cards, nothing rounded, every figure in monospace so a column of spends lines
up, and the conversation in the centre.

![Instrument](docs/assets/13-instrument.png)

**Canvas-first** turns it inside out: the wiring graph becomes the application,
the menu shrinks to a rail of glyphs, and the panels float over a drafting grid.
It contradicts the rule that the chat is the way in, which is why it is a choice
and never the default. Choosing a look brings its arrangement with it.

![Canvas-first: the graph takes the room](docs/assets/08-canvas-first.png)

The palette is independent of all three. Up to three colours make the
gradient — the third is the accent — with dials for grain, tint, and where the
light falls, plus a drift that walks it slowly around the screen. Panels are
frosted glass or plain solid fill, with the blur and the darkness on sliders.
Five palettes ship ready-made; the picture below is Sketch wearing one of them.

![The same workspace in a warmer palette](docs/assets/09-palette.png)

![The appearance dialog](docs/assets/04-appearance.png)

Or put your own picture or looping clip behind the whole thing, with a scrim
dial that buys back as much legibility as it needs.

![An operator's own backdrop behind the panels](docs/assets/10-backdrop.png)

Light and dark both work, and either follows the system by default. Nothing here
is fetched: the grain, the mark and your backdrop are all carried in the page.

Admins get a map of who can reach what — the same relationships the permission
checks use, drawn rather than pieced together from three tables.

![The access map of users, teams and workspaces](docs/assets/06-people-map.png)

---

## Letting agents reach the web

Off by default, and switching it on is deliberately awkward: a config key plus
a restart. There is no route and no database row behind it, so nothing running
inside Cogitorium — no agent, no tool, no gear — has a code path to enable it.

That switch alone grants nothing. Each agent additionally needs a grant you
draw yourself on the blueprint, and **every single search stops the turn and
asks you** to approve that exact query before it leaves the machine. There is
no "allow for this turn" and no "remember this agent".

Agents never choose a destination — they supply words, nothing else. Searches
go first to [echopage](https://echo-page.com), our own engine built so crawlers
can find what they need faster, more accurately and far more cheaply than
grinding through pages built for human eyes; when it has nothing, the search
falls through to a general engine so the agent is not left stuck.

Worth being straight about: this bounds and records outbound traffic, it does
not prevent exfiltration. Any egress at all is a channel. What you get is a
hard cap, a full record, and a human in the loop for every request — a leak
someone approved rather than a silent one.

---

## Known issues

- **A shell does not survive a reload** — restoring a layout brings the panel
  back, not the session. Deliberate, and listed so it does not look like a
  fault.
- **The shell works on a copy** of the workspace and nothing is carried back
  out. Read and write freely; use the file tree for changes meant to last.

---

## No telemetry

Not "telemetry you can disable". There is no analytics code, no crash
reporting, no update ping, no phone-home of any kind. The binary talks to the
model providers you configured and to nothing else. Your conversations, your
context and your agents stay on your machine.

---

## Documentation

**<https://orkcom-tech.github.io/cogitorium/>** — the reference: every screen,
every setting, the security model, and what is deliberately absent.

**<https://orkcom-tech.github.io/cogitorium/guide/>** — the guide: a walkthrough
from an empty install to agents with tools. Every command in it was run, and
every error message quoted in it is the exact text the software emits.

## Testing end to end

`scripts/e2e.sh` exercises the pipeline against real components — a real local
model, a real `contextd` space, the real binary. There are no mocks or stubs
anywhere in this repository; when something cannot be verified the script says
so and skips, rather than passing on a stand-in.

## Licence

[Apache License 2.0](LICENSE). Use it as a tool, build a product on it, put it
inside a service you sell — none of that needs permission and none of it needs
to be discussed with anyone. The hosted-use restriction that was here under the
Business Source Licence is gone: this was always due to become Apache 2.0 in
2030, and that date has been brought forward to now.

What the licence does ask is that you not present the work as originating with
you. Keep the copyright and licence notices, keep the [NOTICE](NOTICE) file
with any copy you redistribute, say in a modified copy that you changed it, and
leave the names alone — "Cogitorium" and "ORKCOM" are marks, not part of the
grant, so a fork does not get to wear them. Build on it, ship it, charge for
it. Say where it came from.

Questions: `licensing@orkcom.tech`.
