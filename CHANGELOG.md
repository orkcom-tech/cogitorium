# Changelog

## v0.9.0

Stage 4 of the parity plan: a command line over the API the interface already
uses.

### `cogitorium` is a client for its own server

```
$ cogitorium gears run wordcount --args '{"text":"one two three four five"}'
5
```

Ten commands, over routes that already existed and are already in
`docs/openapi.yaml`: list workspaces and move one between installs, list and run
gears, list receivers and deliver to one, see the queue and stop a unit, read a
delivery back from the ledger.

What it adds over `curl` is the two things a terminal wants and a browser does
not. **An exit code that means something**: `gears run` exits with the gear's
own code, so a shell branches on what the gear said rather than on whether the
HTTP call worked; `run <id>` exits non-zero for anything that did not complete.
And **output narrow enough to pipe** — columns for a person, and for a script,
the server's own JSON on the routes that carry a record.

It is deliberately a wrapper. Creating agents, drawing wires, editing
prohibitions and approving gears are not here: those are decisions made while
looking at a canvas or a source listing, and a flag is a worse place to make
them than a screen that shows what is being decided.

`internal/client` is shared with the MCP server rather than written twice — two
copies would be two error-handling behaviours, and the one that mattered would
be whichever the reader was not looking at.

### Moving a workspace between installs, from a shell

```
$ cogitorium workspaces export 1 --gears -o court.json
wrote court.json (4828 bytes)

$ COGITORIUM_URL=http://the-other-install:8688 cogitorium workspaces import court.json --gears
workspace 1 "code court" — 8 agents, 15 wires, 0 context files
gears: wordcount (pending — approve them before anything can run them)
agent orchestrator wants anthropic/claude-opus-4-6, which this install does not have
```

That is a real transcript between two installs, and the second half is why the
report is printed rather than counted. A bundle whose gears were all skipped
imports "successfully" and leaves you a workspace that cannot do its work.
Skips and unresolved models go to stderr, the summary to stdout, so a pipeline
keeps the line it wants and a person still sees what did not come across.

### `--async` on a delivery

A delivery holds the connection until the work finishes, which is right at a
prompt and wrong in anything with a timeout of its own. `--async` sets the
`Prefer: respond-async` the receiver already understood and takes a run number
instead; `cogitorium run <id>` reads it back.

### Cancelling a unit that was not there answered 500

```
$ cogitorium queue cancel 99999
error: 500 Internal Server Error: work unit 99999: not found
```

Every other store aliases the catalog's not-found sentinel; `internal/work`
defines its own, and the status mapping had never been taught it. The
difference is not cosmetic — 5xx is what a client retries and what pages
somebody, and "you asked for something that is not here" is neither. It answers
404 now, and the test for it was confirmed by unmapping the sentinel again.

Found by driving the command line against a real server, which is what having
one is for.

### A test for the class of bug this could ship

The first draft of the run struct called its fields `task` and `address`; the
server calls them `task_name` and `inlet_address`. Nothing failed —
encoding/json ignores a key it was not asked for — so `cogitorium run 3` printed
`run 3  completed  /  agent ` and looked like a formatting problem rather than a
client reading the wrong document.

There is now a test that walks every json tag the client declares against
responses from the real handlers and fails on any key the server does not send.
Presence, not value: half these fields are legitimately empty, and what makes a
client wrong is asking for something that was never there. Both a top-level and
a nested field were renamed to confirm it fails.

## v0.8.0

Stage 3, first half: the API has a description that cannot drift from it.

### docs/openapi.yaml

An OpenAPI 3.1 document listing every endpoint this server has — 85 paths,
their methods, their path parameters, and which credential opens each.

It is **generated from the server's own route table**. Every route now
registers itself into that table on its way to the mux, so a route cannot exist
without appearing in the document and a deleted one cannot linger in it. A test
regenerates and compares; adding a route without updating the description fails
the build and names the line it noticed:

    docs/openapi.yaml no longer matches the routes this server registers.
    first difference at line 612:
      committed:   "/api/v1/users":
      current:     "/api/v1/undocumented":

That was verified by making exactly that mistake, not by trusting the code.

Two things it deliberately gets right by omission. `/api/` is a catch-all so a
typo'd API call answers JSON instead of falling through to the single-page app
— it is a fallback rather than an endpoint, and describing it would invent a
route that answers nothing but 404. And the version in the document is the API
surface, `1`, not the build's version: the latter would report a new API on
every release, and would fail the generation test on any build that stamps a
version in — checked with `-ldflags` rather than assumed.

**Nine of the forty-two mutating routes now describe their request body**,
generated by reflecting over the struct the handler decodes into — walked the
way encoding/json walks it, embedded fields flattened and tags honoured, so the
schema is the parser's own definition rather than a second account of it. They
are the routes an integrator actually calls: open a receiver, write and edit
its task, forge a gear, dry-run one, invoke an approved one, approve one, create
a workspace, hire an agent.

A test holds that count so it can only fall. Describing another asks you to
raise a number; un-naming one fails the build and prints what is still
undescribed.

That generator got the nesting wrong first, and there is now a test for exactly
that: the schema was indented to sit beside `content` instead of under
`schema`, which is valid YAML — the document parsed, and a check for "does this
route have a requestBody" passed — while every schema read back as null. Well
formed and meaningless is the failure mode a generator has, so it is the one
being watched.

**Response bodies are still not described**, and it says so in its own
description rather than leaving somebody to discover that the schemas are
missing. Every path, method, parameter and credential in it is exact. Naming
the request types the handlers currently declare inline is the other half of
this stage; a document that under-describes silently would be worse than one
that states its own edge.

## v0.7.0

Stage 2 of the parity plan: this install can be handed to an MCP client.

### Cogitorium speaks MCP

`cogitorium mcp` serves the Model Context Protocol over stdio, so Claude
Desktop, Cursor or anything else that spawns an MCP server can use what this
install holds. One command line is the whole integration.

Two kinds of thing become tools and nothing else does. An **approved gear**
becomes `gear_<name>` — a gear is already a name, a description and an argument
schema, which is the whole of an MCP tool definition. A **receiver task** that
accepts JSON becomes `receive_<address>_<task>`, which adds a transport to a
door that already had a key, a schema checked before any model is called, a
queue and a row in the ledger.

The management API is not exposed, on purpose. Creating agents, drawing wires,
editing prohibitions and approving gears are the operator's acts; an MCP client
is a guest with a tool list, and a guest that can approve its own tools is not
one.

The two credentials stay separate for the same reason. `--token` decides what
can be listed; a receiver's own key decides what may be delivered to it, and
there is no default — a door's credential is the door's, and lending it the
admin's would put the wrong caller in the ledger.

The process holds no database. It talks to a running server over HTTP, so a
client may start and kill it as often as it likes without ever contending for
the SQLite file this product pins its Helm chart to one replica over.

### A route that runs an approved gear, separate from the one that does not

`POST /api/v1/gears/{id}/invoke` runs an approved gear and answers 403 for
anything else. It is deliberately not `/run`, which is the dry run and bypasses
the approval gate so an operator can see what code does before trusting it.

Two routes because they are two different promises, and collapsing them into
one route with a flag is how the safe one becomes optional. Proven by making
exactly that mistake in a mutation: setting `DryRun` on the invoke path makes
both refusal tests fail immediately.

The gate is also checked where a stale tool list would otherwise bite. A client
that listed a gear a minute ago and calls it after it was disabled gets a
sentence — "The gear X is disabled, not approved, so it cannot be run" — as a
tool result rather than a protocol error, so the model is told what happened
instead of the client reporting a broken server.

### And a real bug the CI found on the way

`TestGateTunnelsAndRecordsIt` failed on Linux while passing on macOS, and the
cause was not the test. Once a CONNECT tunnel is established, either side may
tear the socket down with a reset rather than a clean close — Go's own
`http.Transport` does it on `CloseIdleConnections` — and the gate was recording
`ECONNRESET` as a failed connection.

The connection log is an audit trail an operator reads to decide whether a
granted gear misbehaved. Filling it with failures that did not happen is how a
real one becomes invisible. A reset and a broken pipe are ordinary ends of a
tunnel now; a refused connection and a timeout are still failures, and there is
a test that holds both halves of that line.

### Verified by driving it

The protocol has its own tests — initialize, notifications going unanswered,
schemas surviving as objects rather than strings, an empty list marshalling as
`[]` and not `null`, a failing tool reported as a result rather than an RPC
error. Beyond those, the real binary was driven against a real server: a gear
forged, listed as absent while pending, approved, listed, called, and returning
`5` for the five words it was given. Then disabled, and refused.

One test in this batch initially passed for the wrong reason — the fixture it
used approves the gear it forges, so a test about a *pending* gear was
asserting about an approved one. It now forges directly and fails if the status
is not what it claims.

## v0.6.0

Stage 1 of the parity plan: an operator who has installed a hardened container
runtime can now tell Cogitorium to use it.

### The OCI runtime is selectable

`sandbox_runtime` in the config, `COGITORIUM_SANDBOX_RUNTIME` in the
environment, carried into `docker create --runtime`. Set it to `runsc` for
gVisor or `kata-runtime` for Kata Containers.

**Cogitorium does not install or configure those.** It names one and checks the
daemon has it — the isolation is the runtime's work, and claiming otherwise
would be claiming somebody else's. What this adds is the check: a name your
daemon does not have is refused at startup, with the names it does have in the
message, instead of surfacing on the first gear run days later as
`create container: exit status 125`.

Two more refusals, both for configurations that read as the opposite of what
they are. `sandbox_runtime` beside `sandbox: subprocess` is an error rather
than an ignored line: that combination looks like hardened isolation and is in
fact no isolation at all, gears running with the server's own file access.
`sandbox_runtime` with no daemon answering is an error for the plainer reason
that there is nothing to select a runtime on.

Everything else about the container is untouched, and there is a test that says
so: naming a runtime does not quietly return `--cap-drop=ALL`, the pid ceiling,
the memory limit, the unprivileged user or `--network none`. A container that
runs perfectly well under gVisor with its capabilities back is the regression
that would matter most and show least.

The tests run against a real Docker daemon rather than a mocked one, because
the whole value here is that Docker accepts the flag — a mocked `docker info`
proves a string survived a function call. One of them creates a container and
runs work in it under an explicitly named runtime; the rest check arguments,
and an argument is not a container.

### The sandbox image is fetched at startup

Once, in the background, best-effort. Before this the first gear on a fresh
install paid for a full image pull inside its own timeout, which is how a
sixty-second gear fails for reasons that have nothing to do with the gear.

## v0.5.0

The licence opens, the interface is drawn, and the thing this project is for is
finally written down.

### Apache 2.0

Cogitorium was under the Business Source Licence, which allowed production use
but not offering it to third parties on a hosted basis. That restriction is
gone. Use it as a tool, build a product on it, run it inside a service you
sell — none of it needs permission or a conversation.

This is not a reversal so much as an early arrival: the BUSL already named
Apache 2.0 as its Change Licence, dated 2030-08-08. The date has been brought
forward to now.

What the licence asks in return is attribution, not restraint. Keep the
copyright and licence notices; carry the new `NOTICE` file with anything you
redistribute; say in a modified copy that you changed it; and leave the names
alone, because "Cogitorium" and "ORKCOM" are marks and section 6 does not grant
them. Build on it, ship it, charge for it — say where it came from.

The archives, the container image and the deb/rpm all carry `LICENSE` and
`NOTICE` now. Shipping the licence without the notice it requires would have
put this project in breach of its own terms on the first download.

### Graph engineering

The product's subject is a graph and the documentation never said the word. It
described a canvas, wires and grants — the parts — without the thing they add up
to: agents are nodes, and four kinds of edge on four layers of one canvas are
each a permission the runtime checks. Delegation is may-hand-work-to, tools is
may-call, memory is what-it-knows-going-in, outward is may-reach-the-internet.
Remove an edge and the thing it allowed stops being possible.

Prompt engineering asks what to say to one model. Graph engineering asks what
the parts may do to each other, and the answer is a structure you can look at,
change, export and hand to somebody else.

### Sketch, and it is the default

A third look: paper ground, ink outlines whose corners disagree, handwriting on
the chrome — and code, logs, schemas and figures left in the type they were
already in, because a workbench you cannot read a stack trace in is not one. It
opens light; switching to dark gives the same drawing in chalk on slate. A
fresh install lands here now, and an upgrade never repaints a stored theme.

### And the light-mode bugs it walked into

Both existing looks were dark, so the light half of this product had never been
looked at. Twenty-two problems were found and each verified in the file before
being touched. The terminal rendered white glyphs on paper and had never set a
foreground at all; xterm's own stylesheet pins `.xterm-viewport` to black,
which is why `allowTransparency` had never done anything; and seven semantic
colours — the amber that means "warning" worst of all — were single hexes
picked against near-black, landing between 2:1 and 3.2:1 on a light ground.

### Screenshots are taken by a script

`scripts/shots.mjs` shoots all eleven from one running install, driving the real
menus. The graph shots measure themselves and fail the run if any node is
outside the pane — which is how a fit that parked the graph off-screen got
caught, having built and uploaded perfectly cleanly.

### Also

`goreleaser check` runs on every push. It only ever ran at release time before,
and a description with a colon in it — invalid YAML — would have been found on
the tag, with the release already half-published.

## v0.4.1

Four things v0.4.0 shipped without, one it shipped that should not have existed,
and a word that was never going to be understood.

### Inlets are called receivers

On screen only. "Inlet" is a word this product invented and nobody arrives
already knowing what it means; "receiver" says what the thing does. The URLs,
the API paths, the config keys and the tables keep the old name, because callers
outside your install hold those strings and breaking them to improve a label is
the wrong trade.

### A task can be fixed

There was no editor. A wrong schema meant deleting the task and writing it
again — the address answering 404 to every caller in between, and the task
coming back with a new id, so the schedules and the runs on record pointed at
nothing. `PUT /api/v1/inlet-tasks/{id}` keeps the id, and the row has an **edit**
button that opens the same form it was written in, filled in.

The body is the whole task rather than the fields that changed: an absent
`schema` would otherwise have to mean *accept anything*, and a door does not
widen because a field was left out of a request. Both routes go through one
validator, so a task cannot be edited into a state it could not have been
created in — proven by breaking it, with the edit route accepting an agent that
does not exist the moment the shared check is skipped.

### Callbacks could not be turned on

A task's `callback_url` was read by the runner, stored in the table, documented
in the reference — and written by no route on this server. The whole feature was
reachable only from a test. It is a field on the create and edit routes now, and
a line on the form.

### The form stopped hiding

The schema example was the field's placeholder, so it vanished the instant
anybody typed — at exactly the moment it was needed, and it could not be copied,
edited or got back. It is a button that fills the field with a real schema now,
in a box tall enough to read, checked as you type. And the task form opens by
itself on a receiver that has none: a receiver with no task answers 404 to
everything, so the one state with exactly one thing to do should not hide the
control for it.

### The daily budget is gone

`budget_workspace_day_tokens` read from the config and went nowhere — a knob an
operator could set and believe in. It has been removed rather than implemented,
because its only real use was a hosted service where the person paying is not
the person asking for the work, and that is not what this is. Nothing drives a
workspace's daily total except your own schedules and your own typing, so a cap
on it would only ever stop your own work.

`budget_run_tokens` stays, with the reason stated properly this time: an inlet
is a door for somebody ELSE's system, and whoever holds the key can drive
deliveries. It bounds what a third party can cost, which is a different thing
from limiting yourself.

### A run stopped by that ceiling says so

It settled as `failed` with the reason in the error text, so a caller could only
tell "your job hit the ceiling" from "we broke" by reading prose — and retrying
a job that was deliberately stopped is how a ceiling turns into a bill. It is
`refused_budget` now, answered with 413, and the state was already in the
ledger's CHECK waiting for a writer.

### The queue and schedules have an interface

They existed only as routes. A queue nobody can see is discovered by being
refused by it, and a schedule nobody can pause is one you disable by deleting
it. The panel shows what is running and what is waiting, with a Stop on each
that ends the work rather than only the row, and lists schedules with pause,
run-now and delete. It polls only while it is on the bench and the tab is in
front.

### And a plan item withdrawn

Refusing to create a schedule for a task whose agent holds a web-search grant
was in the plan and is wrong: an agent may hold a grant and never use it on that
job, so the refusal would block legitimate schedules. What is true — that a
scheduled run never gets web search, because every search waits for a person to
approve that exact query and on a schedule there is nobody to ask — is now in
the documentation and beside the form.

## v0.4.0

Cogitorium can now be left alone. Work waits its turn instead of being thrown
away, it can start because a clock said so, a caller can hand a job off and be
told when it is done, and a run can be stopped — by a person or by a ceiling.

### A busy workspace queues instead of destroying

A delivery that met a running turn used to be settled `failed` with the engine's
busy error and answered 429 — the same terminal state a genuinely broken job
gets. A burst of two hundred tickets was one job done and a hundred and
ninety-nine losses a caller could only tell from real failures by
string-matching an error message.

Now it is written `queued`, waits, and runs. One durable table carries the lane
rule, the queue and the scheduler, because they are one mechanism seen from
three angles and building them separately means three protocols that have to
keep agreeing forever.

**The lane rule is a partial unique index**, not a `NOT EXISTS` subquery. The
subquery is correct under SQLite's single writer, so a queue built on it alone
passes every test here and is silently wrong the moment a second writer exists —
two workers claiming two rows of one lane both see an empty subquery and both
succeed.

An operator's chat turn takes that same lane. Two latches that could not see
each other would let a turn and a delivery run at once in one workspace, and
they share an egress budget, two anti-worm latches and one run record. The
difference is what happens when the lane is busy: a delivery queues; a chat turn
is refused, because a person is holding a stream and cannot be parked in a line
they cannot see.

**`max_attempts` is 1.** At-least-once, not exactly-once: a re-run can repeat an
agent that already spent tokens, wrote files and sent something outward. A unit
found running at startup is marked dead rather than requeued, for the same
reason.

`Idempotency-Key` is read before the body and answered with the job it already
produced — never a second one, never a refusal.

### Seeing it, and stopping it

`GET /api/v1/workspaces/{id}/queue` shows depth, position and what is running.
`DELETE /api/v1/queue/{id}` stops a unit waiting or running: it marks the row
**and** interrupts the work, because a cancel that only relabelled the row would
leave the model answering for a job somebody stopped.

### Schedules

`every 15m` or a five-field cron subset, with an IANA timezone and tzdata
embedded so a zone means the same thing in a container that has none. A schedule
points at an inlet task rather than carrying its own agent and instruction —
there is one definition of a job, and a firing is that job with nobody on the
other end.

Everything checkable is checked when the schedule is saved: the spec, the zone,
the payload against the task's own schema, that the agent exists. A firing whose
previous run has not finished is skipped and recorded as a skip.

### Handing a job off

`Prefer: respond-async` answers 202 with a run number; the default stays
synchronous. An inlet key can now read the runs that arrived through its own
door, which is what makes 202 mean anything — before this a key was write-only
and the only way to let a pipeline poll was to hand it a user token that can also
delete workspaces and approve agent-authored code.

`GET /i/{address}/runs/{id}/file?path=` finally makes the record's own claim
true. It has always named files and said the path was one a caller could fetch;
the only file route was user-scoped, capped at 2 MiB, refused non-UTF-8 and
returned content as a JSON string. This one streams anything, authorised by the
run's own record.

Callbacks tell a listener when a run finishes, in the same shape reading the run
back gives. `callback_hosts` is **empty by default and empty means off** — a
callback URL arrives in a task, and a task is editable by anyone who can reach
the workspace.

### What a run cost, and a ceiling that refuses

Model calls, gear runs and granted-gear connections now name the queued unit
they belonged to. The only link before was a timestamp, which works today
because runs are serialised per workspace and stops the moment anything else is
true.

`GET /api/v1/workspaces/{id}/spend` answers what a workspace used over a window;
the aggregates that existed were lifetime sums, so "what did last week cost"
could not be asked.

`budget_run_tokens` and `budget_workspace_day_tokens` are off by default and
REFUSE when set — checked before the model call, not after. Tokens, not money:
there is no price data in this schema and there is not going to be.

### Fixes

- **Two runs of one gear deleted each other's code mid-execution.** A call
  carrying no files ran in the shared gear directory, and every run begins by
  clearing it. Every run gets its own now.
- **The record did not say who did anything.** `ToolRun` was a name and a
  duration; the dispatcher had the agent's name and the delegation depth in hand
  and dropped both. And an operator's own turn produced no record at all, so the
  chat — which is how every workspace is built — left no evidence.
- **Deleting a workspace deleted nothing on disk**, and left rows in three
  tables whose `workspace_id` carries no foreign key on purpose.
- **A rate limit ended the run.** Every non-200 from every provider was one
  error with no branch on the status, so a 429 at delegation depth three
  discarded twelve minutes of work. 429 and 5xx are retried with backoff now,
  `Retry-After` honoured and bounded; a wrong key still is not.
- **A delivery that never ran left its file on the volume.**
- **`Settle` did not know about the `queued` state** it had just been given, so
  cancelling a waiting delivery left its ledger row saying `queued` forever
  while whoever polled it waited for an answer nobody would write.
- **The cron parser expanded `*` into every value**, which made it
  indistinguishable from an explicit list — and day-of-month and day-of-week are
  OR'd — so `0 7 * * 1-5` fired on a Saturday and 30 February resolved to a real
  date.
- **The budget guarded one of the two places that call a model.** The delivery
  path went straight past it.
- **An inlet key test failed about one run in three**, replacing a key's last
  character with a zero when one key in sixteen already ends in one. The same
  defect had been found and fixed in another copy of that test months earlier.

### Not built

Gears still do not run as Kubernetes Jobs, and there are no remote agents.
Several people sharing one workspace at once is designed and not built — see the
planning notes.

## v0.3.0

A gear can now hold a credential and reach a named host — the last step of
"unpack this and put it in a bucket", and the pair that was listed as not built
in v0.2.0. Both are granted at approval, beside the source.

### Named values

A gear declares the **names** it needs; the values are put into its environment
when it runs and never enter a prompt. That is the point rather than a detail: an
agent's answer leaves the building — in an inlet response, in the chat — so a
credential a model can see is a credential that can be published.

A **variable** is shown wherever it appears; a **secret** is shown once, when it
is set, and never again. The kind is sticky, because turning a secret back into a
variable would un-redact everything already stored under that name.

Values resolve from three places, later winning: this install's store, sealed
with AES-256-GCM under `COGITORIUM_SECRET_KEY`; the directories named by
`variables_dir` and `secrets_dir`, one file per name; and the workspace's own
override.

The directories are how this works on Kubernetes. The chart takes
`config.variablesConfigMap` and `config.secretsSecret`, mounts them as
directories, and points the server at them, so rotation is whatever the cluster
already does. It deliberately does not call the cluster API — that needs a
service account token in the pod where agent-authored code runs, and the chart
mounts none.

Redaction happens at one boundary rather than at each caller, so no path can
forget it: the tool result, the stored run, the live output an operator is
watching, the log, the error, and the names of files the gear itself wrote.

Two things it does not do. A value a gear **sends** somewhere is not redacted and
cannot be — granting a key and a network is granting the ability to carry it out,
and the approval screen is the whole of the control. And an install without
`COGITORIUM_SECRET_KEY` still works: only writing a secret into the database is
refused, and it says why.

### The network, granted where the source is read

A gear reaches nothing unless it is granted the network at approval, with the
hosts it may use. Traffic goes through a gate in the server's own process that
checks the destination and records every connection, so what a gear reached is
in the record beside what it printed.

Both grants are on one approval screen, because a decision made half-blind is
not a decision. A new version returns to pending and keeps neither: an approval
is of exact content.

### A worked arrangement in the guide

The guide gains a panel of models that judge each other's code — four authors on
four different models, two critics, a referee — with the blueprint photographed
from the running interface and the whole arrangement downloadable as a bundle to
import. The capture is a script rather than a hand-taken picture, so a visual
change either updates it or shows up as a diff.

### Fixes

- **A dry run of unapproved code was handed this install's secrets.** The dry run
  is the one path that executes code nobody has agreed to yet — it exists so an
  operator can look before approving. An agent could forge a gear declaring a
  name, print the value, and have the operator press the safest-looking button to
  hand it over; redaction cannot help, because the gear may encode it however it
  likes. A dry run now gets the names with empty values and says so, which
  answers the question it is actually for.
- **Wire labels on the blueprint piled up on each other and on the agent cards.**
  Where four authors each submitted to two critics, eight labels shared one point
  and the row read "s submits decide submits". They are placed along the curve by
  the fan leaving an agent and across the bundle by the fan arriving at one —
  two crowds, two axes, because tuning one for both is whack-a-mole. A label also
  steps aside rather than landing on a card it does not belong to, and the
  capture script now reports any collision instead of shipping a mess quietly.
- **A granted gear could not reach anything on Linux.** The gate a gear's
  traffic goes through lives in the server's own process, and it bound the
  loopback. Docker Desktop forwards `host.docker.internal` to the host's
  loopback, so this worked on every laptop; on Linux that name is the docker
  bridge gateway, a different address, and every granted gear got "connection
  refused" — the feature was broken on the platform servers run on and fine on
  the one it was written on. The server now asks Docker which address a
  container reaches it at and binds there. The configuration reference used to
  tell operators to set this by hand, which was documentation standing in for a
  default that works.
- **A key with one character changed opened the door, once every sixteen runs.**
  The inlet was fine; the test that guards it replaced the last character with a
  zero, and one key in sixteen already ends in one. A security test that cries
  wolf periodically is a security test that gets ignored.

### Not built

Gears still do not run as Kubernetes Jobs, and there are no remote agents.

## v0.2.0

Cogitorium can now be part of somebody else's system rather than only a place a
person sits. Data arrives at a door, an agent works on it with real files, and
the caller is told what actually happened.

### Inlets — a door from outside

An inlet has an address, its own key, and a list of tasks. A task says what it
accepts — JSON against a schema, or a file of a given content type — which agent
receives it, what to tell that agent, and what counts as success. Any number of
doors per workspace, any number of tasks per door.

```
POST /i/{address}/{task}
```

A payload that does not match is refused with 400 **before any model is called**,
so a malformed request from somebody's cron costs nothing. A wrong key is 401, an
unknown task 404.

Three properties are not options. A delivery writes nothing into the operator's
conversation — that timeline is replayed into every turn, so a pipeline on the
chat endpoint would make request two hundred carry the previous hundred and
ninety-nine. The run is treated as third-party from the first byte, so the agent
behind a door cannot write to the instruction library, the gear catalog or the
workspace graph. And `web_search` is not offered, because it waits for a person
to approve a query and there is nobody there.

The delivery route is the only path exempt from normal authentication; inlet
management stays behind it with the workspace's own access rule.

### Files reach the tools that need them

A gear run given files executes in a directory holding `in/` — the files,
read-only — and an empty `out/`; whatever it leaves in `out/` is copied back into
the workspace and reported. It opens `in/photo.jpg` and writes `out/result.json`
the way any program does. A gear given no files sees neither directory and its
input is byte-identical to before.

Read-only is ownership rather than a flag: the sandbox user owns `out/` and
nothing else, and a directory you do not own is one you cannot add to or delete
from.

Agents gained `list_files`, `read_file` and `write_file` over their own
workspace. `read_file` refuses a binary rather than base64-ing it into a prompt.

### Models can be shown images and PDFs

`llm.Turn` carries content parts, encoded as base64 blocks for Anthropic and
`data:` URLs for OpenAI, so an image can finally be sent to a model that would
take one. A plain text turn is byte-identical on the wire — proven by recording
both providers' request bodies before and after.

Anything no model can look inside — a zip, a spreadsheet, a video — is refused in
the model layer with a message naming the gear route; the file is in the
workspace either way. Whether a model accepts images is declared on it in the
catalog, never probed and never guessed from its name.

The operator can attach files to a chat message, and they land where an inlet's
do.

### `did` and `expect`

Every delivery and every run carries `did`: which tools ran and whether they
succeeded, which files exist afterwards with their sizes, how many model calls
and how many tokens. On success and failure alike, never behind a flag.

It exists because a model asked to call a gear answered *"The … file was aligned
and formatted using gear_format"* having made no tool calls at all, and the
delivery returned 200 with that sentence. A better model lowers the rate and does
not change the property.

A task may state what success is: `runs_gear`, `produces_files`, a `schema` on
the answer, and `answer_from: "gear"` to make the last successful gear's stdout
the result and return no prose at all. The first two are checked against the
record and never against the text, so a confident answer over an empty record
fails — with both halves in the message. `refused_expectation` and
`refused_output_schema` keep "the work did not happen" apart from "the answer was
malformed".

### Workspace bundles

A workspace exports as one JSON document: agents with their roles and
prohibitions, the wires between them, and — as separate opt-ins — the gears bound
to it and its context. Wires and models are referenced by name, because ids from
another install mean nothing. Nothing private is in the document. An imported
gear always arrives unapproved, and a name already taken is skipped and reported
rather than replaced.

### Prohibitions

An agent can be told what it must never do. The rules are the last section of its
prompt, and an agent the orchestrator creates inherits them — otherwise a
standing rule was one tool call away from being routed around by hiring someone
without it.

### Fixes

- The `.deb` and `.rpm` created no service account and no data directory, so the
  systemd unit they ship could not start. Both are created now, idempotently.
- The release publishes the container image the Helm chart has always pointed at,
  for amd64 and arm64.
- Desktop and server archives collided by name — GitHub compares release assets
  without regard to case — so one workflow's upload failed the other's release.
- A wedged `docker create` held a workspace's one-run latch forever, and every
  later delivery answered 429 with nothing running. Every docker call now has a
  deadline and a `WaitDelay`.
- A raw upload of `text/plain` landed as `payload.conf`.
- `produces_files` counted writes rather than files.

### Not built

Gear network access and secrets — the last step of "unpack this and put it in a
bucket" — remain undone, and are deliberately paired: a gear with credentials and
no network reaches nothing, and one with both is an outbound channel authored by
an agent. Gears do not run as Kubernetes Jobs, and there are no remote agents.
