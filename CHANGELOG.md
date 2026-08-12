# Changelog

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
