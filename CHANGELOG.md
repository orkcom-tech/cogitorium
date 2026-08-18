# Changelog

## Unreleased

### Signing in to a hosted MCP server, rather than pasting a token

Static headers by name covered a token somebody could copy. Most hosted servers
want OAuth, so most of the registry installed and could not authenticate.

The whole flow: ask the server, read the `WWW-Authenticate` refusal, fetch the
protected resource metadata, discover the authorization server through **both**
RFC 8414 and OpenID Connect (a client must support each), register with RFC 7591
if this install has never met it, and send the operator's browser there.

**Four checks, none optional.** PKCE with S256 — OAuth 2.1 removed `plain`, and
offering it would let a server choose the weaker one. The `resource` parameter
on the authorization request *and* the token request *and* every refresh, naming
what the token is for; without it a token minted for one server is replayable
against another. The callback's `iss` validated against the issuer recorded
**before** the redirect and taken from the validated metadata document — with no
normalisation of any kind, because case folding, a default port or a trailing
slash would each let a lookalike compare equal. And a `state` generated with a
CSPRNG and consumed exactly once, which is the only thing tying a callback to
the request that began it.

**This is the first live credential this schema holds**, and it says so: every
other one is a NAME resolved at the moment of use, which works because the
operator has the value. A token minted by somebody else, arriving through a
redirect and refreshed by this server on its own, has no name to resolve. So
access tokens, refresh tokens, client secrets and the PKCE verifier of a flow in
progress are all sealed with the same AEAD the secrets table uses — and **an
install with no `COGITORIUM_SECRET_KEY` is refused the flow** rather than
handed a worse version of it.

A step-up asks for the **union** of what is held and what the challenge demands:
a server naming only the scopes one operation needs would otherwise have the
client re-authorize into a token that lost everything else. A refresh that omits
a new refresh token keeps the old one, because a rotating server issues one and
a non-rotating server omits the field — and overwriting with empty would throw
the grant away.

### Documents and prompts, which were offered to nothing

Only `tools/list` and `tools/call` were implemented, so a server holding a wiki,
a drive or a codebase looked empty against this install — and a good share of
what people publish is exactly that.

**Two tools rather than one per item.** A server's documents reach an agent
through `mcp_documents` and its templates through `mcp_prompts`: call either
with nothing to list, or with a uri or a name to fetch one. The obvious
alternative — a synthetic `read_x` per document — would put four hundred tool
definitions into every request for a four-hundred-page wiki, and they are not
four hundred capabilities but one capability with an argument.

A server that does neither answers `method not found`, which is treated as the
ANSWER rather than a failure, so a tools-only server does not read as broken. A
prompt template arrives as a conversation and is flattened with the role as a
LABEL: splicing somebody else's "assistant" turns into an agent's history would
let a server write words into the transcript as though the model had said them.
Binary content is named rather than decoded, the same rule the tool path
already follows.

### It says when the pair no longer fits, instead of letting a save fail

The last open question from the update-checking plan, and the thing that
prompted the whole document: Cogitorium needs `contextd` 1.0.0 or newer for the
compare-and-set a context save uses, and on an older one the save is refused
with a message about an unknown flag — the right failure, arriving at the worst
moment, saying nothing about why.

"There is a newer one" and "the one you have is too old for the one you are
running" are different questions, and only the second describes something that
is already broken. It is reported separately, in red rather than orange, with
the version that would fix it — and a development build of `contextd` is never
called too old, because telling somebody to reinstall a binary they built
deliberately is a notice they cannot act on.

### A bundle carries MCP, and a connection outlives one call

**Workspace bundles carry external MCP servers**, answering the open question
the plan left: the SHAPE travels — the command or the URL, the arguments, the
NAMES of the values it wants — and the approval and the credentials never do.
An imported server arrives pending with no fingerprint, and there is no field in
the format that could say otherwise.

That rule matters more here than it does for a gear, and the reason is worth
stating: a gear's complete source is in the bundle, so the receiving operator
can read what they are approving. An MCP server is a command line or a hostname
and they cannot. That is an argument for carrying the shape — they need it in
order to decide at all — and a far stronger one for never carrying the approval.
A bundle that arrived pre-approved would be a way to hand somebody a process on
their own host by email. A name that already exists locally is skipped rather
than granted: it may be a different thing wearing the same name.

**Connections are pooled.** Every tool call used to dial afresh — a process per
call on stdio, and on a hosted server a TCP connection, a TLS handshake and an
`initialize` round trip to somebody else's host before the call anybody wanted.
An agent using four tools paid it four times.

The lifetime rules are the whole of it. A pooled connection is a process that
outlives a turn, so it is capped by an idle sweep that closes it whether or not
anybody asks again. It is keyed by the server's FINGERPRINT as well as its name,
so a server edited since gets a fresh connection rather than one opened under
what it used to be — a pool keyed only by id would route straight past the check
that refuses an unapproved edit. A dead connection is never handed out, and the
sweeper stops with the server rather than leaving children nobody owns.

### It can be watched now, which it could not be at all

An install on a server or in a cluster had **no metrics of any kind** — no
`/metrics`, no Prometheus, no OpenTelemetry — and wrote logs only as prose to
stderr. Every operation, error and state change was already logged, and none of
it was machine-readable, so Loki, Elastic and the vendor agents were handed
paragraphs to regex. This product runs agents that spend money, gears that
execute code and schedules that fire unattended: it is exactly the class of
thing an operator wants an alert on, and it could not be alerted on at all.

**A Prometheus endpoint, on its own port, off by default.** Its own listener
and never a route on the API — the API is authenticated and a scrape is not, so
an unauthenticated path on the authenticated surface would be a way in. Off by
default in the binary because starting a listener on somebody's laptop is not
our decision; **on** by default in the Helm chart, because somebody applying a
chart is running a cluster that has a scraper in it.

**No name anybody chose is ever a label** — not a workspace, agent, model, tool
or user. A scrape reaches a monitoring system that is usually less guarded than
the product, and a label whose values are unbounded turns a time series database
into a memory leak. The route label is the TEMPLATE, `/api/v1/workspaces/{id}`,
never the id — which was got wrong first: reading `r.Pattern` in middleware that
wraps the mux from outside gets an empty string every time, and every route was
silently labelled `other`, which looks exactly like a working metric.

**What is measured is what somebody would be paged on**: the schedule that has
been failing every night, the tokens (with providers that report no usage
counted separately, so a zero is never mistaken for free), the queue depth, gear
outcomes split into refused / failed / timed-out / non-zero exit because they
page different people, MCP calls by transport, and the outward gate. A metric
nobody has touched is not published, because a zero cannot be told apart from
"this is not wired up".

**Written by hand, no dependency.** `client_golang` pulls a tree for counters in
a map and a few hundred bytes of text, and it carries a global registry that
collects the runtime whether or not anybody asked. The precedent is `openapi.go`
— same reasoning, already in this repository. Its histogram bug was caught by
its own test: buckets are cumulative, and an implementation that tallies into
every matching bucket AND cumulates on write produces a histogram where every
quantile is the maximum.

**`log_format: json`**, and nothing more. Cogitorium ships logs nowhere; that is
what Vector, Fluent Bit, Alloy and the agents are for. What it owed them was a
format.

**Helm**: the port on the Service, a guarded `ServiceMonitor` (applying a
manifest whose kind does not exist fails the whole release), and a NetworkPolicy
rule of its own — which was found by reading the existing policy, where an
enabled policy would have silently blocked every scrape and looked precisely
like a broken exporter.

### Three protocol and interface debts, paid

**`Mcp-Name` was not being sent.** The spec requires it on `tools/call`,
`resources/read` and `prompts/get`, and a conformant server answers 400 with
`-32020 HeaderMismatch` without it. Now sent, with the base64 sentinel encoding
for a name that is not header-safe, and sent on those three alone — there is no
body field for anything else to be validated against.

**`x-mcp-header` is implemented**, which the spec says a client MUST support: a
tool may ask for annotated parameters to be mirrored into `Mcp-Param-*` headers,
and a tool whose annotation breaks the rules is excluded from the list rather
than called — one malformed definition must not take a server's other tools with
it.

**The registry was read one page deep.** `nextCursor` was parsed and never
followed, so a search saw the first fifty and called it the library — and most
of a page collapses, because every published version of a server comes back
separately. Now followed, bounded, and a later page that fails keeps what the
earlier ones found.

**A schedule's spec could not be edited.** `PATCH` took `enabled` and nothing
else, so changing a clock meant deleting and redrawing it, losing its counters —
and a schedule you cannot correct is one people replace, which is how a job ends
up with no history. `PUT` is the fuller edit, beside the pause rather than
replacing it: turning a job off in a hurry at night must stay the shortest
route. An omitted field is left alone, the next firing is recomputed from the
new spec, and the target is deliberately not editable — re-pointing a clock is a
different act with a different approval.

### Any MCP server, not the third of them that happen to be packages

The first cut of this spoke stdio only, and the library was six entries compiled
into the binary. Both were wrong, and measurably: in a sample of the published
registry, **two thirds of servers are hosted services** with no package to run
at all. A product that could install the other third has a library that is
mostly buttons doing nothing, and "we support MCP servers, except most of them"
is not a sentence worth shipping.

**All three transports.** `stdio` as before; `streamable-http`, which is one
POST per message with the answer arriving as a JSON object or as an SSE stream
scoped to that request; and the deprecated 2024-11-05 `sse` shape, where a GET
opens a stream whose first event names where to POST. 0027's `CHECK (transport
IN ('stdio'))` said in its own comment that widening it would cost a table
rebuild — this is that rebuild.

**The client was restructured rather than branched.** Message framing, the id
table, notification-versus-request classification and the timeouts are protocol
and are written once; a transport's whole job is to deliver one message and feed
what comes back into the same dispatch. stdio keeps its reader goroutine; the
HTTP one answers on the POST that asked.

**What is being agreed to is different for each, so the card says different
things.** A packaged server runs here as this server's user, outside the
sandbox, refetched at every start. A hosted one runs nothing here — genuinely
safer — and instead sends a credential on every request and every argument the
agents write. Saying the same four warnings for both would have been false in
two of them.

**A URL is part of what is approved.** The fingerprint now covers it and the
header names, because a hostname that was repointed after approval looks
identical to one that was not. Cleartext to anywhere but this machine is refused
outright rather than warned about, and a legacy server that names a POST
endpoint on another host is refused too — that is the one place this transport
lets a server redirect somebody else's credential.

**Credentials stay names on both sides.** A remote server's headers are stored
as header → NAMED VALUE and resolved at connect time, exactly as a child's
environment is. The obvious design — a column holding what to send — would have
put a live bearer token in plaintext in this database.

**The library is the published registry, read live**, and the objection that
stopped that the first time is answered rather than ignored: it is fetched only
where an operator has agreed this install may make outbound requests, through
the same switch the update check uses. An install that said no has no library
and is told why; `add by hand` is untouched, because it never reached anything.
Where a server publishes both shapes the hosted one is offered, because it runs
no code here. An entry this install could not connect to produces no entry at
all rather than a button that fails on first use.

**Caught by its own test:** scoped npm packages start with `@`, so the check for
a version separator matched the scope and left `@acme/thing` unpinned — which is
most of the registry, and the difference between approving a version and
approving whatever `latest` means tomorrow.

### MCP servers are a thing you pick, not a command line you type

Consuming MCP was built and working, in the backend — the store, the client, the
per-tool approval, the bindings, the dispatch. **Nothing under `web/src`
mentioned it.** An operator added a server by POSTing JSON with a command line
and an argument array, which means the capability shipped and effectively
nobody had it.

**A drawer beside Gears**, because they are the same kind of object: somebody
else's code, granted to an agent, behind an approval. Same card, same
review-then-approve. Drag one onto an agent on the blueprint and it is granted
there, exactly as a gear is; **a node on the canvas** says *not sandboxed ·
reaches the network*, so "what can this workspace reach" is answered by looking.
Tools are listed and approved **one at a time**, because a server's tool list is
its own claim about itself and can change between spawns — granting your issue
tracker should not have to mean granting `delete_issue`.

**The card opens with what it costs, and the wording is harder rather than
softer.** An MCP server is worse than a gear on every axis and the interface has
to say so instead of feeling like installing a plugin: it is not sandboxed and
can read this install's database and the provider keys in it; it reaches the
network by definition, so the gate that covers web search does not cover it; its
code is fetched at every spawn, so what was approved on Tuesday is not
necessarily what runs on Friday; and it is handed real credentials.

**And a library.** Adding Jira is choosing "Jira", not knowing that its server
is an npm package. Six entries to begin with, each naming what it reaches, the
command, the credentials it needs by NAME, and the prerequisite — most are an
`npx` or a `uvx` away, and an entry that did not say so produces a spawn failure
nobody can read. Compiled into the binary rather than fetched: it works offline
and cannot change under an install between review and use. An install that does
not phone home should not acquire a catalogue that does. Picking one fills in
the form and skips no gate.

**Installing by hand and editing afterwards are both in the drawer**, not only
in the API. Without the edit form two of the library's own entries were
unfinishable from the interface — the filesystem and git servers ship a
placeholder path and say to change it before approving, and there was nothing to
change it with. Saving an edit returns the server to pending, because everything
editable is inside what was approved.

**A leak closed on the way.** `GET /api/v1/mcp-servers` was open and
unredacted — survivable while nothing called it, and not once it is in a drawer.
It carries a full command line and the names of every credential a server is
handed, which is a map of an install's integrations and internal hostnames. A
member now sees the name, description and status, and nothing about how it is
spawned.

### A clock you can draw, that can dial an agent or a gear

The canvas is supposed to answer "what does this workspace do, and what may
reach what". A workspace where something fired at 03:00 every night looked, on
that canvas, exactly like one where nothing did.

**A schedule is a node now**, on a fifth layer that defaults on — hiding the
thing this was built to surface would defeat it. It says the three things
anybody asks, without being opened: **when it next fires** in words rather than
a UTC timestamp (`0 3 * * 1-5` says nothing about whose 3am it is), **whether it
is paused**, drawn dashed and dimmed like the switched-off internet gate, and
**how the last run went** — a job that has been failing every night for a week
being the single most useful thing this canvas could say, and it said nothing.

**And it can dial an agent or a gear directly.** A schedule used to point only
at an inlet task. The reasoning held as far as it went — the task already says
which agent, what to tell it and what success means — but it missed what a task
IS: a door somebody else pushes work through, with an inlet, an address, a key
and a caller. To get a nightly job you first invented a receiver nobody would
ever call, and the receivers list filled with doors that had no inlet and no
caller. That is a worse lie than the one it avoided.

All three targets land in **the same queue, the same ledger row, the same lane
and the same ceiling**. A direct schedule that skipped any of that would be a
second, weaker way to run work, and the weaker one is the one nobody watches. A
clock firing carries a null `inlet_id` and the address `clock`, so a reader sees
what started the run instead of a blank. An agent's instruction is deliberately
**not** fenced as untrusted: an operator typed it into this install, and fencing
it would be telling the agent to ignore the only thing it was given.

**A gear on a clock is fenced three ways**, because it is the most useful thing
here and the most dangerous: only an administrator may create one; the gear must
be approved when the schedule is saved and again every time it fires, since a
clock is the caller with no second gate behind it; and the run still lands in
the workspace's record, which is the only place an unattended failure becomes
visible.

**Deleting an agent or a gear no longer deletes the job that used it.** The row
survives with a null target, refuses to fire with a reason, and draws itself red
saying what to do. "It stopped, and here is why" is something an operator can
act on; "it vanished" is not.

Cutting a clock's edge deletes the schedule — the edge is the relationship here
as everywhere else on this canvas — so it is the one deletion that asks first,
since it takes the spec and the record with it. Making one is a form rather than
a gesture, for the one honest reason that a spec cannot be drawn.

**A fixed bug found while writing this**: a schedule whose firing could not be
queued had its `last_work_id` set to null, and because overlap protection is
gated on that column being non-null, one failed enqueue silently switched
`on_miss: skip` off until the next firing that worked.

### Knowing there is a new version, without being asked to trust anything

Cogitorium and Contextverse ship as binaries people install once and keep, and
until now nothing told anybody a newer one existed. Somebody `brew install`s
this in March and runs a year-old build; every fix and every security change is
invisible unless they think to go and look. The pair versions independently on
the same machine, so a Cogitorium that needs a newer `contextd` than is
installed fails a save loudly — correctly — and nothing anywhere joined those
two facts up.

**The default is neither on nor off. It is `ask`.** This product fetches nothing
at runtime and sends nothing about itself anywhere, so switching an outbound
request on by default would trade a headline promise for a convenience; but a
check that is off by default is a check nobody has, which is the state that made
this worth building. So the question is put once, in the interface, on the rail,
and nothing leaves the machine until it is answered. Three answers: check daily,
never, or **look once now** — which changes no setting, because one press is one
look and not consent to a request every morning.

**The answer is remembered**, in a new `settings` table. An earlier cut held it
in memory only, which meant the question came back after every restart — a
product that asks the same thing forever is a product that did not listen. On
Kubernetes that would have been every deploy.

**`update_check: off` is absolute and not liftable from a browser.** It is set
on the server's own disk; never asked, never checked, and *check now* is refused
with the name of the setting to change. A stored answer written before that edit
does not outrank it. The Helm chart defaults to `off`, unlike the binary: on a
cluster the person who decides what a pod may reach is the one applying the
chart, not whoever opens a browser.

**What goes out** is a GET to GitHub's public releases API carrying no
identifier, no version, no count and no usage. What comes back is a tag and the
release notes — shown as text, never as markup, because it is somebody else's
document arriving over the network. Startup never waits on it, an air-gapped
install fails one attempt quietly, and a dismissal is remembered against the
version so a notice cannot come back for something already read.

**One button on the rail, beside the account, and it goes orange when a version
is waiting** — orange rather than the accent, because the accent is whatever the
operator chose and a notice painted in it is invisible on exactly the install
whose owner picked orange. It is always present, so the settings are reachable
on a day with nothing to report, and it stops glowing once read.

**Nothing is ever replaced.** Cogitorium does not overwrite its own binary. It
works out who owns the file — Homebrew, Scoop, winget, a system package, a
container, a cluster — and **install the update** does the most it honestly can
for that owner: copies the exact command to the clipboard, opens the release
page, or offers nothing and says why. It does not RUN it. A self-updater
fighting a package manager produces a machine nobody can reason about, and a
button making this server execute a shell command because a browser asked would
be remote code execution with a friendly label.

**Three states are kept apart** where one would have been easier: *this is the
newest release*, *could not ask* (with the reason), and *nothing here can say* —
the last for a build whose version is not a release version, which is what a
source build gets. Collapsing them would let the panel claim confidence it does
not have.

The API description now covers 97 paths and 130 operations; the counts quoted in
the README, the reference and the guide were already stale before this and are
corrected.

## v1.5.0

A new shell, and the record telling more of the truth.

### The interface is a frame with a hole in it

**Every control lives on the frame; the hole holds only the work.** That one
rule replaced a header row across the top of the work carrying the brand, seven
destinations, the theme, a documentation link and the account — chrome above,
content below, which is a web page. The instrument surrounds the window now.

The **rail** stands on the left of the bezel as four floating capsules
answering four questions: where am I, what is the cavity showing, what can
crawl out over it, and the rest. Three **stages** — Chat, Blueprint, Editor —
on a track that slides vertically, because the control that moves it is a
vertical column. Eight **drawers** — Agents, Gears, Instructions, Memory,
Receivers, Queue, Variables, Terminal — and a drawer is the frame growing
inward rather than a window over the work: it comes out of an edge, the cavity
shrinks to make room, and it is rounded only on the side facing the cavity.
Dock any drawer to any of the four edges and resize it from the edge facing the
work; both are remembered per drawer.

**Appearance is a mode and a colour.** What went was eleven finished "looks" —
twenty-two visual worlds counting both modes. The interface has one geometry
now, and eleven palettes hung on one geometry are one design in eleven paint
jobs. The colour is not just the accent: every neutral is mixed towards it.

**Drag a gear or an instruction onto the blueprint** and where it lands is the
sentence — on an agent, that agent gets it; on empty canvas, every agent does.
An unapproved gear still binds and says plainly that nobody has approved it, and
offers the way to the review. Refusing the drop would enforce a rule the action
does not own.

### What the sweep found, measured rather than looked at

**Nothing in this product had cast a shadow.** `--lift` and `--lift-2` were
referenced eighteen times across three stylesheets and defined nowhere — they
went out with the looks, and every rule naming them had been dropping its whole
`box-shadow` declaration in silence ever since. `--shadow` was dead too, for a
sillier reason: `light-dark()` is a colour function and it was wrapping two
entire shadow values.

**Twenty-five pieces of text were below their WCAG minimum**, in both modes,
under every accent. `--text-dim` at 58% measured between 3.6:1 and 4.2:1 — most
of the secondary text in the product. Four more places faded with `opacity`
instead, the worst being a switched-off layer toggle at 2.35:1, which is the
control you click to switch the layer back on. Two of the eight accents could
not carry white text as a filled button, one of them the default. Now: zero
failures across 96 screen loads.

**Every drawer printed its title twice** — the drawer head names the panel and
the panel printed its own heading forty pixels below it, which a screen reader
read twice. And the blueprint never fit its own view: five agents in a
workspace, two of them below the bottom edge with no scrollbar, because
`fitView` fits at mount when the canvas is empty, measures nothing that has not
been laid out, and the layout moves again when the wires arrive.

### The record

**Each tool call carries the arguments it was made with**, abridged — every key
survives, values are cut at 200 bytes and the object at 2000, and a cut value
says so. "gear_deploy succeeded" and "gear_deploy succeeded against production"
were the same line otherwise.

**And which documents fed the run, at which versions.** Context is the input
most likely to have changed between a run that worked and one that did not, and
the record was silent about it.

**One read per document per run.** A document is fetched once and every agent in
the delegation tree gets the same bytes and the same version — it was one
`contextd file get` per document per agent per iteration, and a document that
changed mid-run could feed the orchestrator one version and its worker another.

**The record can be asked questions.** Which runs called this gear, read this
document, produced this file, or did not land — on the listing route, and as
filters in the Receivers drawer. A run with no record matches nothing, on
purpose: no record kept is not the same as a record showing nothing happened.

### Gears

**Deleting a gear no longer deletes the evidence that it ran.** Its history used
to cascade away with it, which is exactly backwards.

**Every approval, disable and reset is now a row**: who decided, when, to which
version, and what was granted at that moment. Append-only. A gear approved at v3
and edited to v7 keeps its status, and the trail is where that shows.

### Memory

**Search inside the space**, not only by path — on the Context screen, and as
`context_search` for the orchestrator. It is withheld on an unattended run: it
returns the text of files from the whole space, and on a run started through a
receiver that answer goes back to whoever holds the key.

**A save that would overwrite somebody is refused**, naming both versions. This
is a read-to-write guard and not a compare-and-swap: `contextd file put` takes
no expected-version argument, so the instant between the check and the write is
still open. Stated rather than papered over.

**Forget now removes the document**, rather than emptying it. It stopped being
possible to do properly and started being possible: `contextd` had no delete
command at all — its storage layer had the operation and nothing reached it —
so the honest thing available was to clear the file, which does take it out of
every prompt while leaving it in the space. Instead of documenting that hole
indefinitely, the command was added upstream in **Contextverse v1.0.0**, along
with `--if-version`, which turns the save guard above from a read-to-write
check into a real compare-and-swap made by contextd inside one call.

Both are soft: Contextverse keeps every version and `contextd file undelete`
brings a document back. The interface says that rather than promising an
erasure that did not happen. **This release needs Contextverse v1.0.0 or
newer** for either; an older `contextd` refuses the flag rather than ignoring
it, so a save is loudly unavailable instead of quietly going back to
last-write-wins.

### Documentation

The reference and the guide are rewritten against the shell that shipped, and
all twenty-one screenshots re-shot from a running install. `scripts/shots.mjs`
is gone; `web/scripts/shoot-docs.mjs` is the one shooter, and the test that
fails when a picture is older than `web/src` is what caught the drift.


## v1.0.1

The published API description told the truth about every route but three, and
on one of those it was wrong in a way that made the endpoint unusable to anyone
who trusted it.

**Creating a gear over the API now works from the document.** `POST
/api/v1/gears` described `args_schema` and `files` as "any JSON value". The
server has always wanted `args_schema` as a **string** holding a JSON Schema,
and `files` as an **array** of `{path, content, encoding}`. Anyone generating a
client from `docs/openapi.yaml` sent `args_schema` as the JSON object its name
invites and got `400 cannot unmarshal object into Go struct field
.args_schema of type string`; `files` had no documented shape at all, so it
could only be guessed. The document now states both. Nothing on the wire
changed — the interface and the CLI were already sending the right shapes, so
no existing caller is affected.

**Why the guard did not catch it.** `docs/openapi.yaml` is generated by
reflecting the value handed to `routeIn`, and a test fails when the document
and that value disagree. For `POST /api/v1/workspaces`, `POST /api/v1/gears`
and `POST /api/v1/gears/{id}/run` the value handed to `routeIn` was a
hand-written copy of the handler's struct rather than the struct itself. The
generator described the copy and the test compared the document to the copy, so
both stayed green while the copy drifted away from the parser. The three
handlers now decode into named types and the exported names are aliases of
them, the way `InletTaskBody` already was — an alias is the same type, so there
is nothing left that can drift.

**A new test reads the source** and requires that the type named in `routeIn`
be the type the handler decodes into, following one hop through a decode helper
and resolving aliases. Reintroducing the old copy makes it fail, which was
checked by reintroducing it.

**An external MCP server that dies now says why.** The reason a child gives up
arrives on stderr; the EOF that reveals it died arrives on stdout. Those are
two pipes with two readers and nothing between them, so a caller could be
handed `the MCP server "x" stopped: EOF` while the sentence explaining it was
still in flight. On an idle machine the stderr reader almost always won, which
is why this looked like a flaky test rather than a lost error message. The
death path now waits for stderr to reach EOF — bounded at two seconds, in case
something else inherited the write end — before waking any waiter.

## v1.0.0

The interface is rebuilt. The server is broadly the one it was — the same
routes, the same records, one new column — and almost everything an operator
touches has moved. A large amount of software was deleted rather than carried
forward, and this note says which, because an upgrade that only lists additions
is one you discover the rest of by looking for a control that is gone.

### One row across the top, and no sidebar

A white sheet on a grey ground. Everything you navigate by is in a single
header row: the brand, a centred pill of the places you work — Workspaces, Map,
Gears, Instructions, Models, plus Context and People for an administrator —
then the theme button, the documentation link and the account button. Variables
& secrets, the server-wide Terminal, the server's version and sign out are
behind the account button.

What that replaced was a 200px left rail carrying nine destinations, a collapse
toggle, the brand, the account block and the sign-out button: a sixth of every
screen given to things most of which are opened once a week. The split is by
frequency rather than by kind. Nothing that was reachable is hidden; some of it
is one click further away, and the working area is a sixth wider.

**There is no ⌘B, no ⌘J and no collapse**, because there is nothing left to
collapse. There is no global key binding in the application at all. Escape is
deliberately absent from the chrome: the approval dialog owns it, scoped to
itself, so one keypress can never both dismiss some furniture and silently
refuse a pending web search.

### A workspace is three views and four overlays

Chat, Blueprint and Editor sit side by side on a track that slides. One is on
screen; the others are off it.

**Every view stays mounted, at full size, for its whole life.** That is a
correctness requirement rather than an animation. Hiding a view with
`display:none` makes xterm's fit measure zero and React Flow's `fitView`
produce NaN; shrinking one to a strip refits xterm and destroys the scrollback
structure for good, which expanding again does not undo. Switching away from a
running terminal session or a laid-out blueprint now does nothing to it. An
off-screen view is `inert` and hidden from a screen reader, so the tab order is
not three views deep.

Agents, Receivers, Queue and Variables are **overlays**: opened from the header
one at a time, read, and gone on the next click outside, resizable from a grip
at the bottom-left corner. None of them is a place you work, so none of them
holds a permanent share of the width.

The Editor is the one view with parts, because editing genuinely is three
things at once — the Files tree, the file, and a shell in the same directory.
The tree and the shell roll up to their own header rather than closing, so
there is no "where did it go" state to recover from. Clicking a file moves the
deck to the Editor; the conversation slides off screen and keeps running.

**A shell never starts by itself.** It is behind a button that states what
opening it again does not do: a session is not restored, and the previous
one's scrollback and working directory are gone. That shell is the
workspace's own and open to anyone who can reach the workspace — the
server-wide Terminal in the account menu is still an administrator's, and they
are two different things.

Where you were is stored split by lifetime: this tab's view in
`sessionStorage`, the seed a new tab starts from in `localStorage` under a key
carrying the server and the user. One tab watching a long run while you work in
another is the normal use of this product, and a single shared key means the
tab you are not looking at decides what the one you are looking at shows.
`?layout=reset` clears both, and works at a blank screen where nothing else
would.

### What was removed

None of this is deprecated or hidden behind a setting. It is gone.

- **Floating panels.** A panel could be lifted out and dragged around the
  viewport, up to four at once, each with a remembered rectangle.
- **Docking.** Six slots, each pushing the centre or overlaying it, any panel
  in any slot. It could express more arrangements than anyone could read: six
  panels shared one side slot, so opening the blueprint, the queue and the
  variables stacked them into a tab strip that looked like windows nested
  inside a window, while the header went on listing them as separate things.
  Expressiveness was never the problem.
- **Layout presets and saved layouts.** An arrangement with no state worth
  naming needs no way to name it, and three views on a track have none.
- **The palette** — three gradient stops, a grain dial, a tint dial, a
  glass/solid switch, a blur radius, a lightness dial, a glow with an x, a y
  and a strength, and a drift toggle with a speed.
- **Custom backdrops** — an image or a looping video behind the interface.

Upgrading migrates none of it and needs no action. A stored arrangement from a
previous version is read field by field and whatever no longer exists is
dropped; a stored theme keeps its look and its mode and the dozen dead fields
beside them are ignored where they sit.

### Appearance is a look and a mode

Eleven looks — Air, Calm, Slate, Paper, Terminal, Blueprint, Ember, Mono, Nord,
Bloom, Contrast — and system, light or dark. That is the whole of the screen's
configuration.

A look is not a set of dials. It is a finished visual world — its ground, its
accent, its corners, its idea of whether a surface has an edge or a shadow —
authored once in tokens and drawn in both modes. Every colour lives in
`tokens.css` under `:root[data-look=…]`, and applying a theme writes two
attributes and nothing else, so the stylesheet can be read on its own to know
what anything will look like. The version this replaces wrote eighteen custom
properties from JavaScript, and could not.

**Air is the default, and a fresh install opens light rather than system.** The
choice is kept in `localStorage` under `cogitorium.theme`, and nothing about
appearance is fetched from the server.

An existing choice survives per field: a look that no longer exists lands on
Air rather than on an attribute nothing styles, which would render an unthemed
page.

### The install map

`/map` draws the install as one zoomable scene at three depths, because zooming
should approach a thing rather than navigate to it. The organisation, its
people and its teams are the core; the workspaces are a ring around it; open one
and its agents and their memory grow out of it. Links inside the core appear
only as you zoom in far enough to read them.

Position encodes relation: a node sits in the angular sector of whatever it
belongs to, so a grant is a short radial stub rather than a chord across the
canvas. Mush comes from edges having to travel, which is a placement problem.
Only the kinds the server actually sends are drawn — a permanently empty lane
is not a neutral omission, it is a positive claim that the install has no doors
in and no outward addresses.

It is open to every role. The scoping is the server's.

### The map endpoint is scoped to the caller instead of refused to them

`GET /api/v1/map` was admin-only. It now answers anybody, with what they can
already reach: an administrator sees the install, and everyone else sees their
own teams, the people they share a team with, and the workspaces already
visible to them. The workspace list comes from the same call that answers "may
this person use it", so the map and the workspaces page can never disagree.

Filtering this in the browser would not have been a smaller version of the same
thing — the response would still have named every workspace on the server, and
one member reading one HTTP response would have learned the shape of rooms they
hold no grant on. So the tests read the raw JSON rather than the rendered
graph: a member's map contains neither the name nor the node id of a workspace
they cannot reach, does not name a person they share no team with, and does not
name a team they are not in. And no edge outlives its own node, because an edge
pointing at something that was filtered out names the thing it points at.

`GET /api/v1/map` is no longer in the list of routes that must refuse a
non-admin, and the reason is written where that list is.

### A workspace has a colour

`PATCH /api/v1/workspaces/{id}` with `{"hue": 210}` sets it; `{"hue": null}`
takes it away. Degrees, wrapping rather than refusing — 420 is 60, -30 is 330.

**Anyone who can reach the workspace may colour it, not only its owner.** A
colour is how a team refers to a room out loud, and making it a privilege would
mean the person who works in it every day cannot fix a shade they cannot tell
apart from the one beside it. Nothing here grants access, so there is nothing
to escalate — and it goes through the same access check as every other
workspace-scoped route, so somebody outside is refused rather than told the id
exists.

Two deliberate restraints. An unset hue is **derived from the id and never
written back**: the moment a derived colour is persisted, "nobody chose this"
and "somebody chose exactly this" become the same state and an install can
never again be told apart from one that was hand-tuned. And only the hue is
stored — saturation and lightness belong to the interface, because they have to
move with the look and the mode, and a `#rrggbb` picked under one look is
unreadable under the next with no migration able to repair it.

Absent and null are kept apart on the wire, which is why the handler reads raw
bytes: an absent field means leave the colour alone and an explicit null means
clear it, and no depth of pointer expresses that difference in `encoding/json`.
Every future field on this route would otherwise erase somebody's colour as a
side effect of editing something else.

Migration 0028 adds one nullable column and there is nothing to do on upgrade.
A colour is not carried in an export bundle.

### Every select is drawn by the application

There is no `<select>` left in this interface. A native one opens the operating
system's own list — a different typeface, a different corner radius, a
different highlight, and on macOS a list that overlaps the control it came
from — and no styling reaches inside a native popup to fix it.

What is not given up in exchange, because these are the reasons people keep the
native one: a real `<button>` with `aria-expanded` and `aria-activedescendant`,
a listbox with option roles, up, down, home, end, enter, escape and tab all
behaving as expected, typing a letter to jump, and closing on an outside click
or on an ancestor scrolling. A hidden mirror of the value keeps the browser's
own form validation, so a required picker still refuses to submit empty — the
first cut of this dropped that and turned "you must pick an agent" into a
refusal the server issued after the fact.

It deliberately is not a combo box. Nothing here has a list long enough to need
searching, and when something does, that is a different control rather than an
option on this one.

### A field has a label

Forms named a field and gave an example of its value in one placeholder — "name
(e.g. anthropic, ollama)", "base URL (default: api.anthropic.com)". That fails
twice, and the second failure is the one that matters. A placeholder is clipped
rather than wrapped, so a row of three fields read "name (e.g. anthropic, o"
and "API key (optional for lo" — neither the name nor the example. And it
vanishes the moment you type, at exactly the point you would want to check you
are filling in the right box; for anyone tabbing through with a screen reader
there was no label at any point, only a hint assistive technology is free to
ignore.

The name is now a real `<label>` above the field, always there. The example is
a hint below it, where it can wrap to as many lines as it needs. A placeholder
is a bare specimen value or nothing at all.

### The documentation was rebuilt against the code

Rewritten against what the software does now rather than edited around it, and
`docs/openapi.yaml` describes **92 path items and 124 operations**, still
generated from the server's own route table — the new PATCH is in it because
the route registers itself on the way to the mux.

All fourteen screenshots are re-shot by one command,
`web/scripts/shoot-docs.mjs`, from a running install: headless, all 1440×900 at
2x, all in Air. The set they replace was thirteen pictures of software that had
been deleted, and nobody noticed for one reason — re-shooting was a manual
afternoon, so it was never done.

Claims that were wrong and are corrected, each verified in the code:

- the access map is no longer admin-only (above);
- a workspace's own shell is open to any member, and only the server-wide
  Terminal is an administrator's;
- a gear's network grant and its timeout are per gear, set at approval;
- `grant_gear` is offered only to the orchestrator, so a worker reports what it
  forged and the orchestrator hands it on;
- the blueprint's four legend buttons are independent toggles — delegation,
  tools and outward start on, memory starts off;
- `COGITORIUM_ADMIN_TOKEN` is environment-only, at least 24 characters, and
  seeds the first admin's token rather than printing a generated one;
- booleans in configuration are case-insensitive;
- the browser environment for a gear is API-only; there is no control for it in
  the interface.

## v0.15.0

A fourth look, and the blueprint arranges itself.

### Calm

The bet opposite to Instrument: most of the time nothing in an install needs
attention, so nothing in the interface asks for it.

Three rules, and everything in it is one of them. **Nothing is outlined** —
Instrument separates with hairlines and Sketch with drawn ink; this separates
with a raised surface and one soft shadow, so a screen of eight panels has no
lines in it at all. A border is kept for something you type into, because a
field with no boundary is a field nobody clicks. **Colour is state and nothing
else** — the accent marks the one selected thing and every other coloured pixel
means running, waiting or wrong, so on a quiet install the screen is grey and
the one red row cannot be missed. **Both halves are drawn** — the dark side is
not the light side inverted: it lifts surfaces with a hairline of light rather
than dropping a shadow, which a near-black ground swallows.

No grain, no glow, no drift, no gradient on a panel. Each of those is the
interface asking to be looked at.

Code, figures and logs stay in monospace: the look is about the chrome, and a
column of numbers that does not line up is not calm, it is harder to read.

Measured rather than eyeballed, in both modes — body text 10.3:1 on light and
13.8:1 on dark, muted 4.7 and 6.3, the active tab 13.2:1 once its translucent
fill is composited over the ground.

### The blueprint arranges itself

An agent with no stored position went into a single row under the orchestrator.
Fine for two; for eight it was a straight line that said nothing about which of
them delegates to which — in a product whose whole claim is that the graph IS
the program.

It is now a layered layout: rank by distance from a root, ordered within each
rank by barycentre so wires cross as little as possible. The standard shape
minus the parts that need a solver, and deterministic, so a mental map survives
a reload.

**⤢ tidy** re-lays out every agent and stores it, which is the difference
between this and a view mode. A dragged position still wins — the two hands on
the same controls, in one function.

Checked by driving the real canvas on a graph four ranks deep: with nothing
stored it drew the hierarchy, tidy persisted it with workers 1–2 under lead-a
and 3–4 under lead-b (the ordering pass doing its work), a node moved to
(-900,900) survived a reload, and tidy took it back.

## v0.14.0

The half of stage 2 that was never built: an agent can be granted somebody
else's MCP server as tools. Off unless you switch it on.

### Read this before switching it on

Everything else this product executes is its own code, or a gear whose complete
source is in this install — versioned, approved line by line, run in a container
that cannot see the server's files. An external MCP server is a **command**.
Cogitorium never sees its source, cannot version it, and the tool list is the
server's own account of itself. The child runs **on the host, as this server's
user, outside the sandbox**, so an approved MCP server can open the SQLite
database and read every provider key in it.

That is the same attack `internal/sandbox` exists to prevent, and it is written
into the package doc, the migration, the reference and the guide rather than
into a release note nobody reads twice.

What bounds it is **policy rather than isolation**:

- off unless `mcp_clients: true`;
- every install, approval and grant is admin-only, and **no agent can reach any
  of them** — there is no forge_mcp_server and no model-facing installer;
- three separate acts: install (pending), **probe** (started once, given nothing
  at all, asked what it offers), then approve the server and **each tool
  individually**;
- the command is fingerprinted at approval and recomputed at every spawn; a
  mismatch refuses and returns it to pending;
- a `sampling/createMessage` from the server is refused, so it cannot spend this
  install's model budget on text it chose.

The fingerprint covers the command line, not the bytes at the end of it.
`npx thing@latest` refetches on every spawn and nothing here notices. Said
plainly rather than left to be discovered.

### The order in runMCPTool is the feature

The grant is checked first, then the approval and the fingerprint, and only then
does anything start — because here **the spawn is the dangerous act**. Everywhere
else an unauthorised call is refused and that is the end of it; a refusal after
somebody else's binary has started on this host came too late.

Not an assertion about the shape of the code: the MCP server in the tests writes
a file the moment it starts, so "nothing ran" is checked against the filesystem.
Moving that check after the spawn fails two tests.

### Driven against real processes, not stubs

Every hazard in a client is a property of two processes and a pipe, so the tests
spawn a real second program: a notification arriving between a request and its
answer, answers returning out of order, a child dying mid-call, a request FROM
the server.

Three bugs came out of that, and two were in the tests:

- closing a pending channel when the child died handed the caller a zero-valued
  message, so a dead server was reported as "an answer this cannot read",
  quoting nothing;
- two tests **could not observe what the client wrote back at all** — a mutation
  that answered every notification, which is a protocol error, passed both. The
  counterpart now counts unsolicited responses and the tests ask it;
- `go vet`'s lostcancel found the connection's cancel function unused on Dial's
  error paths, and the process-group hook beside it, which vet does not check.

### Two latent defects in the MCP server half

Found while extracting the shared envelope into `internal/mcp/mcpwire`: the
response type declared `Result any`, which encodes but **cannot decode**, so no
client could ever have read one; and the error object dropped `data`, which the
specification defines and servers use to say what went wrong. The server's own
test is unmodified and still passes, which is the proof the wire did not move.

### Sixteen mutations

Every guard was broken to confirm its test fails: the four gates on what a model
is offered, per-tool approval, the fingerprint, name truncation, the six
protocol hazards, the admin gate, workspace scoping of a grant, and an edit
riding along with an approval.

## v0.13.0

Stage 8, the last of the parity plan: warm containers, off by default.

### What it buys and what it costs

Creating and destroying a container costs a few hundred milliseconds — noise for
a gear that runs for a minute, most of the wall clock for one that answers in
two hundred. `sandbox_pool: N` keeps N containers alive per image and hands a
run one instead.

This is the only setting in this product that trades isolation for latency, so
it is off unless an operator turns it on, and the warning at startup is a WARN
rather than an INFO. A pooled container is **not** a fresh machine: whatever a
previous run left outside its payload is still there.

The bounds are narrow and each one is enforced rather than described:

- the payload is emptied before and after every run, so no gear reads another's
  code or output;
- a run given **named values or the network** is never pooled — exactly the runs
  that could leave a credential behind. The executor decides, because only it
  knows what a run was given;
- a run with a **read-only payload** is never pooled either (below);
- a run that **timed out** retires its container, since whatever timed out is
  still in it;
- twenty runs per container, ten minutes idle, then retired;
- the confinement is identical on both paths, from one list they share.

### Two bugs the tests only found because they were driven at a real daemon

**The cleanup could never have worked.** Emptying the payload ran `rm -rf /work`
as root, and `--cap-drop=ALL` takes `CAP_DAC_OVERRIDE` with it — a root that
cannot override file permissions cannot write into a directory the sandbox user
owns. Every container failed to clear and was retired, so *nothing was ever
pooled*, silently. It clears the directory's contents as the sandbox user now,
and a read-only payload is not pooled at all rather than weakening the container
to make it possible.

**A test passed for the wrong reason.** The first version proved two runs shared
a container by comparing `/proc/sys/kernel/random/boot_id` — which is the
**host's**, identical in every container on the machine. It would have passed
whether or not anything was reused, and did, while nothing was. It compares the
container's own hostname now.

Both were found by pointing the tests at a real Docker daemon and reading what
came back, rather than by reading the code.

## v0.12.0

Stage 7 of the parity plan: an operator can give a gear a machine with a browser
in it.

### The environment is a grant, like the network

A gear runs in this install's ordinary sandbox image. It can now be granted a
different **environment** instead, on the same screen and in the same act as the
network — because an agent asking for a browser is asking for a machine that
renders untrusted pages, and that is decided while reading the source.

One environment exists, `browser`, resolving to `browser_image`. A real run:

```
BROWSER=/ms-playwright/chromium-1194/chrome-linux/chrome
SHOT=7012 TEXT=97
```

A screenshot and the page's text, written into `out/` and collected by the same
path that already carries any file a gear produces. There is no browser
pipeline and no new record — that is the point of doing it this way rather than
building a second one.

A gear **names an environment, never an image**: naming one would be
agent-authored code choosing what it runs inside, and pinning one would break
the day the operator moved. Forging a new version clears the environment along
with the approval, so an agent cannot rewrite a gear's code and inherit a
capability granted to different code.

### The constraint that made it worth testing

A gear runs as an unprivileged user with every capability dropped and no new
privileges, which is exactly where a browser's own sandbox cannot start. That a
browser renders at all under those flags is a fact about this arrangement rather
than something readable from the code, so it is established by running one: a
real container, a real Chromium, a real page, and a screenshot that is seven
kilobytes rather than a file that merely exists.

`--no-sandbox` is therefore required of a browser gear. The container is the
boundary and it is the same one every other gear has.

### Also true

The browser image is about a gigabyte and is **not** pre-fetched at startup the
way the ordinary one is, because most installs never grant it. The first gear
that needs one pays for the pull inside its own timeout — raise that gear's
timeout for its first run, or pull the image on the host.

The default is pinned to a version rather than a moving tag: an image that
changed under an approved gear would change what it runs inside without the
approval changing.

## v0.11.0

Stage 6 of the parity plan: a gear is not given its secrets.

### The credential is put in at the edge, not into the process

Until now a gear's secrets were decrypted and written into its environment. The
process this whole design treats as untrusted — agent-authored code, running
because an operator approved a source listing — held the real credential in
memory, and everything after that was a matter of it behaving. The redactor
covered what the software printed; nothing could cover what the code chose to do
with a string it had.

Now the environment carries a **stand-in**, and the `gearnet` gate — which
already sits on every outbound request with a per-run credential — substitutes
the real value on the way out. A real container, printing what it was handed:

```
HELD=cogitorium-secret-jMDrxMgeHate9cewY3k73-vfWcfPIJIF
STATUS=200 BODY=the-origin-answered
```

and the origin's own record of that same request:

```
Authorization: Bearer sk-live-the-actual-credential-value
```

A stand-in is random, minted per run, known only to this install's gate, and
void the moment the run ends. A gear that exfiltrates its environment has
exfiltrated a string that opens nothing, from anywhere, ever again.

The cryptography did not move: the same AES-256-GCM, the same per-value nonce,
the same HKDF-SHA256. What moved is the injection point.

### What it costs, said out loud

The gate cannot substitute into bytes it cannot read, and a CONNECT tunnel is
opaque by design. So for a run holding stand-ins — **and only that kind of run**
— the gate terminates TLS with its own certificate, rewrites, and opens its own
properly verified connection onward. A granted gear with no secrets is tunnelled
exactly as it was, and the gate still sees only hosts and byte counts.

For those runs the gate reads the request bodies. It is the operator's own proxy
on the operator's own machine, and it is the same boundary that already decides
which hosts may be reached at all — but a proxy that reads bodies is a different
thing from one that counts bytes, and nobody should discover that from a
release. The signing key is per-install, kept beside the database, never handed
to a gear; the certificate is written into the payload with the environment
variables curl, Go, Python and Node actually read, because setting one of them
and calling it done works only in whichever language the author tested in.

**A gear that was not granted the network gets the real value.** There is no
edge to substitute at, so a stand-in would only be a credential that cannot
work. That rule is checked in a real container rather than trusted.

### A stand-in split across two reads

Bodies larger than a megabyte are rewritten as they stream, and the first
implementation held back a fixed number of trailing bytes to catch a stand-in
straddling a chunk. That is wrong in a way that only shows at particular
payload sizes: a stand-in starting *before* the cut and ending after it was
emitted in halves, neither of which matched.

Found by a test that put one exactly one byte over a 32 KiB boundary. Each chunk
is now emitted only up to the last position from which its remainder could still
become a stand-in.

### How this was checked

Both halves, against real components. `internal/gearnet` drives a real HTTPS
origin with a real certificate through a real TLS handshake in both directions,
and asserts what the destination received. `internal/gear` runs a real gear in a
real container and asserts what the container held. Then the guards were broken
one at a time — the substitution removed, the interception switched off, the
stand-in table shared between tickets instead of per run, the certificate
withheld from the payload — and each test named the exact failure.

## v0.10.0

Stage 5 of the parity plan, and the largest: in a cluster, a gear runs as a
Kubernetes Job.

### The chart's oldest warning is gone

Every version of this chart until now said the same thing: there is no Docker
daemon inside a pod, so in-cluster a gear ran as a **subprocess of the server**
— holding the server's own file access, which is the SQLite database and every
provider key in it. Approving a gear there granted it everything the server had,
and the chart refused to enable the terminal or the outward gate at all.

Now each gear run is a **Job**. It mounts the release's own data claim with
`subPath` set to that run's directory, so the container sees its payload at
`/work` and nothing else on the volume. Run against a real cluster, a gear
written to go looking:

```
cwd: /work
here: ['.cogitorium', 'main.py']
/data -> FileNotFoundError [Errno 2] No such file or directory: '/data'
..    -> ['bin', 'dev', 'etc', 'home', 'lib', 'media', 'mnt', 'opt']
db glob: []
uid: 65532
```

The isolation is the mount, which the kubelet enforces before the container
starts, rather than anything the gear is trusted to respect. The pod also drops
every capability, refuses privilege escalation, takes a read-only root
filesystem, mounts **no** service account token, and carries the gear's timeout
as `activeDeadlineSeconds` as well as in the server. `backoffLimit: 0`, because
a gear re-run after failing may already have sent a request or spent money.

Because there is now a sandbox in-cluster, the chart's refusal of the outward
gate is lifted. The terminal stays refused, for a different and honest reason: a
terminal is an interactive attachment and a Job is run-to-completion.

### No copy step, in either direction

The payload does not travel. A gear's run directory is already a directory on
the data volume, and the Job mounts that exact path — so there is nothing to
copy in, and `out/` needs no collecting: the gear writes it and the server is
already looking at it. Caught in the act on a real cluster, while the Job was
still running:

```
$ kubectl exec -n cog <server-pod> -- cat /data/gears/writer/v1.run-…/out/answer.txt
written by the Job
```

Arguments and named values reach the container as files in a hidden control
directory rather than as fields of the Job object — a gear's named values
include secrets, and the Job's spec is readable by anything that can list Jobs
in the namespace and sits in etcd until the object is collected. The two output
streams are files for a different reason: a pod log is one stream, so a gear's
warnings would come back as its answer.

### An image that does not exist took sixty seconds to say so

Found by pointing the sandbox image at a registry that is not there. Kubernetes
does **not** fail a Job whose image cannot be pulled — the pod sits in
`ImagePullBackOff` and is retried until the deadline — so waiting for the Job to
report failure meant waiting out the gear's entire timeout and then reporting a
timeout, which is a true sentence about the wrong thing.

The pod is now asked first, and the difference is the whole point of asking:

```
59.3s  the gear's Job did not run: ContainerCreating:
 3.0s  the gear's Job did not run: ErrImagePull: failed to pull and unpack image
       "example.invalid/nope:1": … dial tcp: lookup example.invalid: no such host
```

A pod nothing will schedule is reported the same way, rather than as a gear that
hung.

### No network unless granted, with the caveat stated

Off-cluster this is `--network none` on the container and the daemon enforces
it. Kubernetes has no equivalent — a pod is on the pod network the moment it
exists — so the runner labels every gear pod with what the operator decided and
the chart ships a NetworkPolicy that cuts egress for the ungranted ones.

**A NetworkPolicy is enforced by the CNI plugin, not by Kubernetes.** On kindnet
or plain flannel the object is accepted and enforces nothing, silently. Calico
and Cilium enforce it. That is written into the chart's README, its values, and
the notes printed after `helm install`, because a promise that quietly depends
on somebody else's plugin has to say so.

### Also true, and now written down

This image carries no `python3`, `node` or `bash` — they belong to the gear's own
container. So on `sandbox: subprocess`, which is still available for a cluster
whose policy forbids the Role, only a `binary` gear can run at all. That was
already the case; it had never been said.

### How this was checked

Against a real single-node cluster, not a description of one: the image built,
loaded, and `helm install`ed, then gears forged through the API and run. A gear
returned `5` for five words; another kept its streams apart and its exit code
(`3`); one that slept past its ten-second timeout came back at ten seconds with
`timed_out` and the output it had already produced; Jobs and their control
directories were gone afterwards.

The manifest itself has a test, because every line of it is a promise made in
prose elsewhere. Dropping the `subPath` mounts the whole data volume into
agent-authored code, and nothing else in the system would notice — so that,
the absent service account token, the network label and the stream redirection
were each broken in turn to confirm the test fails.

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
