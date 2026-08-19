<div align="center">

<img src="docs/assets/logo.png" alt="Cogitorium" width="200">

# Cogitorium

**Our magnum opus of running agents and the workflows they live in** — a harness
and platform ops in the same binary, built on graph engineering and context
engineering.

Memory and context, managed, indexed and versioned. A dependency graph in which
every edge is a permission the runtime checks on the call. And a thing that bends
at both levels: the toolkit inside it — gears, instructions, MCP servers — and
the platform around them, through plugins.

One Go binary, your own models, no telemetry, ever.

[Documentation](https://orkcom-tech.github.io/cogitorium/) ·
[Guide](https://orkcom-tech.github.io/cogitorium/guide/) ·
[Install](#install) ·
[Licence](#licence)

</div>

---

It is a sandbox you are meant to rebuild from the inside. Run it on a laptop as
the thing that remembers between sessions, or behind an API as a department's
infrastructure — the same product either way, with nothing bolted on for the
larger use. Shape the workflows however you want them. And when the platform
itself is what is in the way, change that too: a plugin can add a screen,
override one that shipped, or give the interface something it never had.

You describe a team: who thinks, who checks, who is allowed to run code, and
what each of them may reach. Cogitorium runs it and shows you what happened —
every wire a capability somebody granted, every gear a piece of code you read
before it was allowed to run, every token spent attributed to the agent that
spent it.

![The workspace: the stages on the rail, the drawers beside them](docs/assets/02-workspace-chat.png)

## Features

- 🕸 **Graph engineering** — agents are nodes, and every edge is a permission the
  runtime checks on the call rather than a convention written down: a **wire**
  grants delegation, a **gear binding** grants a tool, a **context binding** is
  what an agent is told, an **outward grant** lets an agent ask to search.
  Delete one and the agent is refused. The orchestrator can redraw a wire, a
  context binding or a gear grant on its next turn; the outward grant is yours
  alone.
- 🎛 **Two hands on the same controls** — build it by telling the orchestrator, or
  by drawing it yourself. Both write the same objects, so there is no conversion
  between them and no "advanced mode" holding the real controls. Drag a gear or
  an instruction out of its drawer onto an agent on the blueprint and it is
  granted there; drop it on empty canvas and every agent has it. One thing
  stays one-handed on purpose: only you grant the outward gate.
- 🧠 **A model per agent** — an expensive frontier model reasons while free local
  ones write docs and run checks, in one topology, with what each agent spent
  recorded against it. Two provider kinds: Anthropic, and anything
  OpenAI-compatible — which is how Ollama, LM Studio, vLLM and llama.cpp are
  reached.
- ⚙️ **Gears: tools that outlive the conversation** — an agent forges a script, it
  lands in a versioned catalogue, and nothing runs until you approve that exact
  version; a new version drops back to pending. The network allowlist is set
  beside the source, in the same act as the approval. Gears run in a container
  holding none of the server's files **when a sandbox backend is present** — with
  the default `sandbox: auto` and no Docker answering, they run as subprocesses
  with this server's own file access, and the log says so at startup.
  [→](https://orkcom-tech.github.io/cogitorium/#what-a-gear-may-hold-and-where-it-may-reach)
- 🔑 **Stand-in credentials, for a gear that also has the network** — name a
  secret and a granted gear receives a per-run stand-in, which the gate swaps for
  the real value at the edge; the gear never holds it. A gear **without** a
  network grant is handed the real value, because there is no edge to substitute
  at — which is the argument for granting the network to anything that carries a
  credential.
  [→](https://orkcom-tech.github.io/cogitorium/#named-values)
- 📊 **Watchable when it is somebody's job to watch it** — a Prometheus endpoint
  on its own port (off by default), JSON logs for whatever collects them, and a
  Helm chart that wires both up. No workspace, agent or model name is ever a
  label: a scrape has a different audience from a screen, and a label per
  workspace is how a metrics database runs out of memory.
  [→](https://orkcom-tech.github.io/cogitorium/#metrics--what-an-operator-can-alert-on)
- 📐 **A described API** — `docs/openapi.yaml` is generated from the server's own
  route table by a test that fails when the two disagree — **97 paths, 130
  operations** — so a route cannot exist without appearing in it.
  [→](https://orkcom-tech.github.io/cogitorium/#the-api-description)
- 🔌 **Speaks MCP** — `cogitorium mcp` serves your approved gears and receiver
  tasks to Claude Desktop, Cursor or anything else that speaks the Model Context
  Protocol. The approval gate holds through it: a gear you have not approved is
  not listed and will not run.
  [→](https://orkcom-tech.github.io/cogitorium/#mcp--this-install-as-a-tool-provider)
- 🔗 **And consumes it, when you say so** — an agent can be granted an external
  MCP server's tools the way it is granted a gear: install, probe, approve the
  server *and each tool*, then grant. **Pick one from the built-in library**
  rather than knowing that Jira's server is an npm package, drag it onto an
  agent on the blueprint, and see it there as a node. Off by default,
  admin-only, and the review screen states plainly what it costs — the child
  runs on the host, outside the sandbox, fetched fresh every time it starts.
  [→](https://orkcom-tech.github.io/cogitorium/#consuming-mcp--somebody-elses-tools-granted-to-an-agent)
- 🌐 **A browser, when you grant one** — a gear can be given an environment with a
  real browser in it, through the API: `PATCH /api/v1/gears/{id}` with
  `{"environment": "browser"}`. Screenshots and page text come back as ordinary
  run artifacts; there is no separate browser pipeline to learn, and no screen
  for it yet.
  [→](https://orkcom-tech.github.io/cogitorium/#the-environment)
- ☸️ **Gears run as Kubernetes Jobs in-cluster** — one Job per run, mounting the
  data claim at that run's own subPath, so the gear sees its payload and nothing
  else on the volume. No token in the gear's pod, every capability dropped, the
  timeout enforced by the cluster as well as by the server.
  [→](https://orkcom-tech.github.io/cogitorium/#install)
- ⌨️ **A command line over the same API** — `cogitorium gears run`,
  `receivers deliver`, `queue cancel`, `workspaces export | import`. It exits
  with the gear's own code, so a shell script branches on what the gear said.
  [→](https://orkcom-tech.github.io/cogitorium/#the-command-line)
- 🚪 **Receivers: a door for your own systems** — an address and a key; data
  arrives by HTTP, an agent works on it, the result comes back. The payload is
  checked against a JSON Schema **before any model is called**, so a malformed
  request costs nothing.
  [→](https://orkcom-tech.github.io/cogitorium/#receivers--a-door-from-the-rest-of-your-system)
- 📋 **Judged by the record, not the sentence** — every delivery carries what
  actually ran: which tools, which files appeared, what it cost. A task states
  its own success conditions and they are checked against that record, so a
  confident answer over an empty record fails.
- ⏱ **It can be left alone** — work queues instead of being dropped, starts on a
  cron line or an interval, can be handed off with `Prefer: respond-async` and
  called back when it finishes, and can be stopped mid-run — the work, not just
  the row.
  [→](https://orkcom-tech.github.io/cogitorium/#the-queue-and-work-that-waits)
- 🔒 **Prohibitions and an internet gate** — rules an agent must never break go
  last in its prompt and are inherited by agents it creates; reaching the web is
  a per-agent grant, and every search still stops for a human to approve that
  exact query.
  [→](https://orkcom-tech.github.io/cogitorium/#letting-agents-reach-the-web)
- 📦 **Portable and local-first** — a workspace exports as one JSON document you
  can hand to another install, from the interface or the command line. Everything
  runs on your machine.
  [→](#no-telemetry)

## How it compares

The field is young enough that most comparisons are between things that are not
really alternatives. These four are: an agent workspace you can host, an agent
harness where everything is a plugin, and the self-hosted workflow builders
people already run. Each row is a structural difference, not a feature tick.

| | Cogitorium | [Cloudflare OS](https://github.com/cloudflare/cloudflare-os) | [deepseek-harness](https://github.com/deepseek-ai/deepseek-harness) | Dify · n8n · Flowise |
|---|---|---|---|---|
| **What you install** | one Go binary with the interface inside it | a pnpm workspace of Workers | an npm package, Node 22+ | containers, with Postgres and Redis beside them |
| **Where it actually runs** | your laptop, your Docker, your cluster | Cloudflare Workers and Durable Objects; `workerd` locally | one Node process on your machine | your host, plus its datastores |
| **Who may change the interface** | anyone — a plugin installs into a running server | whoever owns the deployment repository | anyone — the UI is itself a swappable plugin row | node and component authors, through a review you do not control |
| **What isolates a third party's code** | tiered: WebAssembly today, container and native declared | Workers isolates; agent frames with outbound networking off | nothing — plugins mount in-process with full host rights | nothing to partial, depending on product and mode |
| **Before an extension runs** | approval bound to the sha256 of the bytes on disk; a new build drops back to pending | review of the deployment repository | no manifest, no prompt, no signature | install and it runs |
| **Code an agent wrote for itself** | will not execute until a person approves that exact version | gadgets run in sandboxed frames | registered by a plugin, runs unsandboxed | executes when saved |
| **Outbound: an allowlist *and* a record** | per-host at approval, and a row per connection — allowed and refused alike | Gatekeepers mediate per resource and operation, and every resource an agent observes is recorded | neither; the docs put network "outside this vocabulary" | Dify: Squid ACL with a log. n8n: off unless switched on. Flowise: a denylist, empty, no log |
| **Governance without paying** | accounts, teams, workspace sharing and per-host records, Apache-2.0, no licence key | Apache-2.0 | MIT | SSO, roles and audit behind a paid licence in five of six |
| **Maturity** | v2.0.0; the plugin system is still on a branch | 8.6k stars, run daily inside Cloudflare | developer preview at `rc.7`, warning of breaking changes | years in production |

### Where it loses

A table with no losses in it is an advertisement, so:

- **No SSO, and only one real role.** There is no SAML, OIDC or SCIM anywhere in
  this codebase, and every access check is admin-or-not plus team membership.
  The workflow builders ship all of it — behind a paid licence, but shipping. If
  SSO is a requirement, Cogitorium does not meet it at any price today.
- **The host allowlist is cooperative.** Whether a gear gets a network at all is
  enforced by the container runtime, and that part is real. *Which hosts* it may
  reach is enforced by proxy variables an obliging client honours — so a gear
  that opens its own socket reaches the network and leaves no row. Modal, E2B and
  Cloudflare enforce outside the process, where the code's cooperation does not
  matter. This is stated in the source rather than hidden.
- **A killed run does not restart itself.** Every turn and tool result is
  journaled and replayed, and nothing already spent is paid twice — but
  interrupted work is marked dead rather than requeued, on purpose, because
  re-running something that may already have sent an email is a second execution
  nobody asked for. If you want automatic resume, that is Temporal's job.
- **A plugin does not reach as far as the marketing word "plugin" suggests.** It
  adds a rail entry, a workspace panel and pages of its own, and its stylesheet
  and script load on every screen. It cannot yet contribute a tool to an agent or
  make an outbound request, and the product's own screens are not template
  surfaces yet.
- **Cloudflare OS is ahead on data flow.** Recording every resource an agent
  observed, and deciding policy on that record, is a stronger question than the
  one an egress allowlist answers. Cogitorium controls where an agent reaches,
  not what it has already read.

## You can use it like…

One binary, and nothing is bolted on for the larger uses — they are the same
workspaces, gears and receivers, addressed differently. Three that people
actually run, to show the range; they are examples, not a menu.

### …your own companion, in front of a model you already pay for

Run it on your laptop, point it at Anthropic or a model on your own machine, and
it becomes the thing that *remembers* between sessions. Context lives in
[Contextverse](https://github.com/orkcom-tech/contextverse) and is versioned;
each agent has memory you can read and delete; and code your assistant writes
becomes a **gear** — reviewed once, approved once, then reused instead of
rewritten from scratch every conversation.

It also works the other way round. `cogitorium mcp` serves this install's
approved gears and receivers to Claude Desktop, Cursor or anything else that
speaks MCP, so your existing assistant gains the tools you have already checked
— and nothing else. Creating agents, drawing wires and approving gears stay with
you.

```sh
cogitorium mcp --server http://127.0.0.1:8688 --token $COGITORIUM_TOKEN
```

![The gear catalogue](docs/assets/07-gears.png)

### …the engine under a service, in a cluster

A **receiver** is an HTTP door into one workspace: a caller posts a task with a
key, one agent runs it, and the answer comes back on the same response. Add a
schedule and it runs on its own clock. The Helm chart runs gears as Kubernetes
Jobs, each in its own pod with no network unless you granted one.

That is enough to put small, sharply-scoped agents behind an API — a classifier,
a summariser, a triage step — with the scenario prepared in advance rather than
improvised per request, and a queue that refuses rather than melting when the
work arrives faster than the models answer.

```sh
helm install cogitorium ./deploy/helm/cogitorium \
  --namespace cogitorium --create-namespace \
  --set auth.adminToken="$(openssl rand -hex 24)"
```

### …one install per department, talking to each other

Every install is a server. A receiver on one is an address another can post to,
and a completion callback tells the caller when the work finished — to hosts you
listed, and no others. Support's install files a ticket into Engineering's;
Engineering's release workspace tells Ops when a build is signed. Each keeps its
own models, its own context and its own audit trail; what crosses the boundary
is a task and an answer.

![The install map](docs/assets/09-map.png)

## A look at it

**A frame, and a hole in it.** Everything you operate lives on the frame — the
rail down its left edge — and the hole holds only the work. A panel does not
fly in over the work: the frame grows inward on that edge and the hole shrinks
to make room, so the whole thing stays one object.

![The frame: the rail on the bezel, the chat in the cavity, the agents crawled out](docs/assets/02-workspace-chat.png)

**The blueprint.** Drag between two agents to draw a wire; the wire IS the
permission, not a picture of one. Drag a gear or an instruction out of its
drawer and onto an agent to give it there — or onto empty canvas, for every
agent in the workspace.

![The blueprint](docs/assets/03-blueprint.png)

**Approving a gear**, with what it grants stated before you agree to it.

![What approving a gear grants](docs/assets/08-gear-review.png)

**And who approved it** — when, to which version, and with what. A gear
approved at v3 and edited since is not an approved gear, and the trail is where
that shows.

![The approval trail](docs/assets/20-gear-approvals.png)

**One workspace opened on the map** — its agents, and their memory.

![One workspace opened on the map](docs/assets/10-map-open.png)

**Files, an editor and a shell**, in the workspace the agents are working in.

![The Editor stage](docs/assets/05-editor.png)

**Who can reach what**, drawn rather than inferred from three settings screens.

![People and the access map](docs/assets/06-people.png)

**Search inside the memory**, rather than needing to know a path already.

![Searching the context space](docs/assets/21-context-search.png)

**Light or dark, in a colour that is yours.** Appearance is two choices and
nothing else. The colour is not just the accent: every neutral in the palette
is mixed towards it, so the ground and the surfaces carry a little of it too.

![Appearance](docs/assets/04-appearance.png)

![The same install, dark](docs/assets/14-dark.png)

## Install

Every route installs the same binary, and every route brings
[Contextverse](https://github.com/orkcom-tech/contextverse) with it — declared
as a dependency where a package manager can act on it, carried in the artifact
where nothing can. The context space is created on first start.

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

**Anywhere — the archive**, from [releases](https://github.com/orkcom-tech/cogitorium/releases).
It carries `contextd` beside `cogitorium`; unpack both into the same directory
and the server finds it there. Debian and RPM packages from the same page carry
it too.

Then open `http://127.0.0.1:8688`. The first run asks you to choose a password
for the `admin` account; after that, on your own machine, it remembers you.

## No telemetry

Nothing is reported about you or about this install. There is no analytics
endpoint and no crash reporter, and the interface fetches no fonts and no
scripts from the network.

Everything this binary does reach, in full:

- the **model providers you configured**, and nothing else in that class;
- the hosts you listed in **`callback_hosts`**, when a task is told to report
  that it finished;
- addresses you **granted a gear by name**, through the gate that enforces the
  allowlist;
- with egress switched on, the **two search services compiled into the binary** —
  `echo-page.com`, then `api.duckduckgo.com` as a fallback. They are constants
  fixed at build time rather than settings, so no agent can name where its words
  go and nobody can be talked into repointing them;
- the **cluster API**, in Kubernetes mode, to create the Job a gear runs as;
- **whatever scrapes `/metrics`**, if you switched it on — inbound rather than
  outbound, on its own port, carrying no name you chose;
- **`api.github.com`, only if you say yes** — see below.

### The one question this product asks

Cogitorium and Contextverse are binaries people install once and keep. Nothing
told anybody a newer one existed, so somebody installs this in March and runs a
year-old build without ever knowing.

The fix is a daily GET to GitHub's public releases API — and because that is the
first outbound request this server makes on its own behalf, **it does not happen
until you agree to it.** `update_check` defaults to `ask`: the interface puts
the question once, on the rail, and nothing leaves the machine until it is
answered. Set `update_check: off` and it is never asked and never checks,
including when somebody presses *check now* — and the interface cannot lift
that, because it is a decision made on the server's own disk.

The request carries **no identifier, no version, no count and no usage**. What
comes back is a tag and the release notes. Nothing is downloaded and **nothing
is ever replaced**: whoever installed the binary is who replaces it, so the
panel prints `brew upgrade cogitorium` on a Homebrew install and no command at
all in a container, where the next deploy owns the version anyway.

Context and memory go to a `contextd` process on the same machine, not to a
network service.

## Where to go next

- **[The guide](https://orkcom-tech.github.io/cogitorium/guide/)** — one section
  per screen, with worked examples: a panel of models judging each other's code,
  a receiver behind an API, a scheduled run.
- **[The reference](https://orkcom-tech.github.io/cogitorium/)** — every setting,
  every endpoint, and what each one refuses to do.
- **[Contextverse](https://github.com/orkcom-tech/contextverse)** — where the
  context and the memory actually live.

## Licence

Apache 2.0. See [LICENSE](LICENSE).
