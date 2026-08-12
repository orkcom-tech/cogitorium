# Changelog

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
