# The plugin system

> **Brief:** People other than the maintainer write plugins. A plugin adds functionality *and* changes the interface, Jenkins-style — it can override a screen the core never designated as extensible. Authors submit to a repository we own and their plugin appears in a library screen inside the client. Restart after install is the accepted activation model.

This is a design document, and it is no longer ahead of the code. The work is
on `refactor/plugin-system`; **What is built** near the end says which parts
landed and which are still only described here.

Read the rest as the intent. Where the code and this document disagree about a
detail, the code is what runs — and the disagreement is a bug in whichever one
somebody has not fixed yet.

## The rule everything serves

**An author declares a technology. The host decides the lane. The lane the host picks is never the author's problem, and the artifact never carries a platform.**

## Cogitorium's JVM

Jenkins' guarantee is that a `.hpi` contains no platform: it is bytecode, and the host process is the runtime. Our equivalent is two engines compiled into the binary the operator already installed — Go's `html/template`, and an embedded **wazero**.

This is stronger than Jenkins, for a reason worth stating: Jenkins still requires a JVM to pre-exist on every machine it reaches, and provisioning it is the operator's problem. We have no such floor. `CGO_ENABLED=0` cross-compiles six targets from one runner, and both engines are inside the artifact. There is no "first install a runtime" step on any channel, ever.

Two mechanisms carry parity, and people conflate them:

1. **The artifact has no platform.** A template is data. A wasm module is data with an entry point. Both are byte-identical on all six targets by construction, not by portability engineering.
2. **The image seeds itself on every start.** A baked plugin is a property of the *image*, not the volume — so a wiped PVC, a fresh node, or `docker run` with no volume all come up with the same plugin set.

## Five tiers

Two are guaranteed by construction. One by probe. Two are declared capabilities with named availability.

| Tier | Languages | Works on | Author ships |
|---|---|---|---|
| **0 — bundle** | none (HTML/CSS/JS) | every channel, unconditionally | a directory |
| **A — wasm** | Rust, TinyGo, Go, Zig, C, **JS** via embedded QuickJS | every channel, unconditionally | one `.wasm` |
| **B — provisioned** | Python, Node, Bun | every channel by default; guaranteed by probe | source + vendored deps |
| **C — image** | anything | wherever the live sandbox backend reports a container runner | a digest-pinned image ref |
| **D — native** | anything | exactly the `{os, arch, libc}` rows published | per-target binaries |

**Tier 0 is the default and not the poor relation.** With `pages:`, `nav:`, `styles:` and `scripts:`, a plugin restyles the product, adds whole pages and puts an entry in the rail with no backend code at all.

**Tier A is the universal runtime.** ABI frozen and permanent: core WebAssembly + `wasi_snapshot_preview1`, bytes in / bytes out, Extism-shaped vocabulary — so every existing Extism PDK is a Cogitorium PDK on day one. Deliberately *not* the Component Model or WASI Preview 2: wazero refuses those until W3C Recommendation, and chasing them costs the six-target `CGO_ENABLED=0` build. Where wazero cannot AOT-compile, the pure-Go interpreter runs the identical module ~50× slower — degradation is speed, never compatibility. `needs: js` compiles against a QuickJS provider gzipped into the binary, so a JS plugin is single-digit KB and runs in the alpine image with no node anywhere near it.

**Tier D is the only platform-keyed structure in the entire manifest.** It exists so the answer to "but I need a native binary" is a door rather than a fight, and so the cost lands visibly on the author who chose it. `libc: any` may be claimed only when CI proves a static ELF with no `PT_INTERP`.

### Python, stated plainly

There is no healthy Python-to-core-wasm path. `componentize-py` emits a Component wazero will never load; Extism's `python-pdk` emits core wasm but is quiet since mid-2025, needs Binaryen on `PATH`, and takes pure-Python dependencies only. Handing a Python developer a 35 MB artifact against an ABI their ecosystem does not speak would end the Python story.

**`needs: python` is permanently Tier B, and Tier B is genuinely comfortable.** A first-party stdlib-only `cogitorium` package. Dependencies vendored at *build* time on the author's machine — the host never runs pip and never resolves at install. One ~27 MB interpreter fetched once per version and shared by every Python plugin, verified against a compiled-in sha256 and the upstream attestation before a byte is unpacked.

Verified empirically, not from docs: python-build-standalone musl CPython 3.13.15 executes inside `alpine:3.21` with `--read-only` rootfs as uid 65532 — the exact shape the Helm chart deploys — relocating its own `sys.prefix` and importing correctly.

## Docker: three mechanisms, named separately

**Baked into a derived image** — this is "embeds into the ready image":

```dockerfile
FROM cogitorium:1.9
COPY plugins.txt /usr/share/cogitorium/ref/plugins.txt
RUN cogitorium plugins bake --ref /usr/share/cogitorium/ref -f plugins.txt
```

`bake` materialises the tier too, so `needs: python@>=3.11` puts CPython in the image layer. Offline-complete for Tiers 0, A, B and D — **not** for Tier C, which is still a registry pull the runtime performs; `bake` refuses to swallow a Tier-C-only plugin silently and records the image ref as a declared prerequisite to mirror.

Three base-image changes, each a build-time decision because a read-only rootfs can never make it later:

- `apk add --no-cache libstdc++` — 2.77 MB installed. Without it Node and Bun musl builds die on `libgcc_s.so.1`. That package is the entire difference between JS-on-Tier-B working and not on the container channel.
- Create the ref tree **mode-readable, not owner-readable** — 0755/0644, no chown. There is no single runtime user: the image's `adduser` gives uid 1000, the Helm pod overrides to 65532 with no passwd entry. An ownership-based ref tree is readable in compose and invisible in the cluster, silently emptying the plugin set on exactly the channel where the seed is the whole guarantee. The PVC only works because `fsGroup` chowns it, and an image layer gets no `fsGroup` treatment.
- Boot verifies the ref tree is readable as the running uid and says so in the capability profile if not.

**Seeded into the volume on every start** — the entrypoint is already idempotent; the seed slots in before its `exec` and runs on *every* start. Only `<data>/plugins/` is copied; `<data>/runtimes/` is used **in place, read-only**, because copying a 100 MB+ interpreter tree into the PVC on every start doubles storage for nothing. An operator who upgraded through the UI is not clobbered — a `.from-image` marker records which version the image supplied.

## Hypermedia: both

htmx 2.x plus its SSE extension, and Datastar 1.x. Vendored, loaded unconditionally on every page, never gated on a manifest field. ~31 KB gzipped total, served same-origin from the embed FS — so the egress gate is never involved and every channel serves byte-identical runtimes.

This works because **neither library needs a special server contract, and their contracts are already the same one.** Datastar's fetch layer sends `Accept: text/event-stream, text/html, application/json` and, when `Content-Type` contains `text/html`, treats the body as a patch payload. htmx consumes exactly that. So the host contract is one sentence — *a plugin handler renders a named template and the host returns it as `text/html` 200* — and that single response is simultaneously a valid htmx endpoint and a valid Datastar endpoint. Inventing a host-specific fragment envelope is the thing that would poison a contract both libraries already agree on.

They cannot collide: namespaces are disjoint (`hx-*` vs `data-*`), and htmx's optional `data-hx-*` spelling resolves to plugin name `hx`, which Datastar does not register and skips.

Targeting is expressed once: a handler returns `{Selector, Mode, ViewTransition}` and the host projects it onto both header families. An author who thinks in one model never learns the other exists.

Streaming picks a dialect from a header the client already sent — `Datastar-Request: true` gets `datastar-patch-elements` frames, anything else gets named-event SSE for htmx's `sse-swap`. Same template, same model. The Datastar dialect is two lines on the wire, so we emit it by hand rather than take on its Go SDK (4 direct + 4 indirect modules against a tree carrying 8 direct).

One existing endpoint changes shape: `POST /api/v1/workspaces/{id}/chat` returns `text/event-stream`. Datastar consumes that directly; htmx's SSE extension is built on `EventSource`, which is GET-only, so under htmx that flow splits into POST-then-subscribe.

**Note:** ten Datastar attributes and three actions are commercially licensed and cannot ship in an Apache-2.0 project others build plugins for. Recorded as reserved-but-unavailable and flagged by the boot scan — the alternative failure mode is complete silence.

## Overrides

**One rule: ownership is encoded in the name's prefix, and parsed bytes are the only source of truth about what a plugin does.** Core owns `cog.`; a plugin with id `dark-metrics` owns `dark-metrics.`. To override, define a name in someone else's prefix. To add, define one in your own. Detection is a prefix test plus a set diff — which is what "declaration is advisory" actually requires.

Naming is `<namespace>.<area>.<subject>[.<part>]`, with `<area>` a closed set drawn from vocabulary the codebase already speaks: `shell`, `page`, `stage`, `drawer`, `list`, `row`, `field`, `action`, `empty`, `badge`, `frag`, `slot`. A name is an address, not a description of markup; every repeated unit gets its own name; names never carry a version; a shipped name is never reused for a different subject.

**The view model is the API. Markup is not, and is never promised.** Flat and addressable, additive-only, `.Ctx` on every model carrying viewer/workspace/install-mode/path/theme. Raw and rendered both (`.CreatedAt` *and* `.CreatedAtLabel`). Every slice carries `.Empty`, `.Count`, `.Total`. And the highest-leverage rule: **actions are data, not markup** — every request-causing control is `{Id, Label, Method, Href, Confirm, Target, Danger}` in `.Actions`, so a new button appears *inside* an existing override instead of breaking it.

### Addition and replacement are different operations

This is the correction that matters most.

- **Replacement — last wins.** For ordinary names the latest layer in `plugins.order` renders. This is late binding by name.
- **Addition — append slots.** Names in the `slot` area, and any name ending `.extra`, are **concatenated**: core's body, then every layer in enable order. Without this, N plugins each adding a rail destination all define the same name and only the last survives — two plugins from the catalog silently erasing each other with neither able to prevent it.
- **The common case needs no template at all.** `nav:` is a declarative list the host merges into the rail itself. Four lines of YAML.

### Aliases

`under:` is the default and what every example uses; `core:` must be typed on purpose. If someone else already wraps a name, your wrapper goes outside theirs and **both survive** — you never learned they existed.

`plugins.order` is one ordered list per install. Position is precedence, presence is the enable bit, install appends to the end. **No priority field exists anywhere in a manifest** — a plugin cannot bid for its own precedence, which is what makes the list trustworthy. Layer order is a topological sort over `requires` edges plus one edge *derived from parsed bytes*: any layer defining a name in another plugin's namespace sorts after that plugin.

### Validation

Zero-value pass with `missingkey=error`, plus an exemplar pass that also produces the approval-screen preview. Failure names plugin, template, field and the author's own file:line, with a suggestion. **Fail the plugin, not the boot** — a core template failure is the one loud exception. This is the only compatibility check that fires on the upgrade path that actually happens: a package manager or image pull replacing the binary while plugins sit in the data dir.

## Catalog, auto-merge, and the mark

**The mark is not a field. It is a computation the client performs on bytes it already holds.** The catalog says where things are; it never says what is true.

**Two repositories, and that split is load-bearing.** Auto-merge means untrusted PRs land without a human. Mark-minting means an OIDC identity that can produce a certificate saying *the owner*. These must never live together.

| repo | contains | who writes | identity it can mint |
|---|---|---|---|
| `cogitorium-plugins` | `plugins.json`, schemas, CI, index publisher | anyone, via PR + auto-merge | signs the **index** only |
| `cogitorium-marks` | marks, revocations, yanks, advisories | owner only. No PR path, no bot, no auto-merge, `workflow_dispatch` behind a protected environment | signs **marks and revocations** |

Both SANs are compiled-in constants, **and the pinning includes which statement kinds each identity may sign.** A `mark` signed by the index identity is rejected before its signature is interesting. That single rule is what lets auto-merge and the mark coexist: the signing capability reachable through the submission path is scoped to a kind that grants no trust. CODEOWNERS is defence in depth, described that way internally — a process control is exactly as strong as the platform account it runs on.

**Minting:** the owner dispatches with `{id, version, expected_digest}`; the workflow **fetches the artifact itself and computes the digest**, refusing to sign unless it matches both the input and the index. A protected Environment with the owner as sole reviewer means minting takes a second approval in GitHub's own UI. Signing is cosign keyless — the same mechanism the release pipeline already runs. No private key exists anywhere.

**Client verification never reads a boolean.** It recomputes the digest of bytes on disk, chains to compiled-in Fulcio/Rekor/TSA roots, checks the SAN and issuer, checks the kind is authorised for that identity, checks the subject digest matches, then checks for a later revocation. The API returns `{state: verified|unmarked|unverifiable, identity, issued_at, checked_at, failed_at?, note}` — no bare flag crosses any boundary, and **unverifiable is displayed louder than unmarked**.

Distribution follows Obsidian: one `plugins.json` of five-field entries (`id, name, author, description, repo`); authors host artifacts in their own GitHub Releases; the client fetches through the existing egress consent gate.

## What an author does

```
$ cogitorium plugins new my-thing
$ cogitorium plugins dev ./my-thing --watch
```

`new` writes a five-line `plugin.yaml`, a `templates/` folder and a `theme.css`. `dev` registers that absolute path as a dev layer — no version dir, no archive, no digest, no signature — and `--watch` re-execs on change. Restart-on-install is the accepted model, so automating the restart is not designing around it; **it is the dev loop.**

To change how something looks: open the template inspector in the running install (or read `registry.json`, which ships in every release, so this works offline at the exact version targeted). It gives three things — the name, the model with every field typed, and core's current body. Copy the name, write markup against the fields. Nothing in the manifest had to say so.

`cogitorium plugins new my-thing --override cog.row.gear` seeds the bundle with core's current body, so the starting point already renders.

To add rather than replace: same name, `{{ template "under:cog.row.gear" . }}` inside.

To add a rail entry: four lines of `nav:`. To add a whole page with no backend: three lines of `pages:`.

**At that point the author is finished and never chose a language.**

Behaviour costs one line — `needs: js@>=2020`, `needs: python@>=3.11`. A technology and a constraint. The author never writes a URL, a version, an architecture, a platform, or the word `wasm`.

Every tier calls the same fixed list with identical names and semantics: `cog.log`, `cog.render`, `cog.http`, `cog.api`, `cog.kv` (with `cas` and `incr`, because two instances *will* race), `cog.enqueue`, `cog.config`, `cog.now`, `cog.rand`. An author who outgrows the embedded JS engine and moves to a provisioned node runtime rewrites nothing but their build command.

Handlers return `{template, model}` and the host renders through the same layer stack — so a backend participates in late binding instead of emitting opaque HTML from outside the mechanism.

## Order of work

Ordered by dependency, not by time.

| # | Step | Depends on |
|---|---|---|
| 1 | Channel capability profile and its probes | — |
| 2 | Server-rendered template surface + the two registries | — |
| 3 | `plugin.yaml`, bundle format, published schema | — |
| 4 | Tier registry, resolver, `runtime_unavailable` fact | 1, 3 |
| 5 | Ordered layer stack: ledger, append slots, aliases, `plugins.order` | 2, 3 |
| 6 | Load-time validation and the failure policy | 5 |
| 7 | Lifecycle + the restart controller | 4, 6 |
| 8 | Tier 0 delivery: pages, nav, assets, auth default | 2, 5, 7 |
| 9 | Host ABI and the `cog.*` gateway | 3, 5 |
| 10 | Grant plumbing (hosts, secrets, api) | 9 |
| 11 | Tier A: wazero + embedded JS provider | 1, 9, 10 |
| 12 | The supervised worker | 1, 9 |
| 13 | Tier B provisioning | 4, 10, 12 |
| 14 | Tiers C and D | 4, 10, 12 |
| 15 | Author toolchain and SDKs | 6, 8, 9, 11, 13 |
| 16 | The approval screen | 6, 8, 10, 13, 14 |
| 17 | Container and cluster channels (`bake`, seed, Helm) | 7, 13 |
| 18 | Catalog and its CI | 3, 6, 9, 15 |
| 19 | The mark, statement family, client verification | 18 |
| 20 | Update discovery, offline behaviour, compatibility ratchets | 5, 7, 18, 19 |

## What is built

Written down here rather than left to be inferred from a diff, because the
distance between "designed" and "running" is the thing everybody gets wrong
about a document like this one.

**Landed and running end to end.** The channel capability probe. `plugin.yaml`,
the bundle format and the builder. The ordered layer stack — ledger, append
slots, `under:`/`core:` aliases, `plugins.order`. Load-time validation with
per-plugin blame and the drop-and-recompose policy. Tier 0 delivery: pages,
rail entries, assets, the auth default. Approval bound to the sha256 of the
bytes on disk, from the command line and from the browser. Workspace panels
through `mounts:`. The tier registry and resolver. All five tiers dispatching: wasm on wazero, a
provisioned interpreter fetched and shared, an OCI image on the sandbox that
already runs gears, a native binary, and `needs: js` on a JavaScript engine
compiled into the binary. All nine `cog.*` calls, identical on every tier. A
restart the product can perform on itself. The template inspector,
`registry.json`, the published manifest schema, exemplar validation and the
approval-screen preview. The catalog: fetch, cache,
search, the three-state verified check, install-with-crosscheck, the browse and
install screens, and its CI — `check-catalog` and `check-bundle`, with
additions auto-merging and edits held for a person.

The conversion, since: every product screen a template can render is one.
Instructions, models, context, gears, workspaces, variables, queue, receivers,
MCP, agents, memory, the transcript, the terminal gate, plugins and the people
lists — each verified against a running server before its React component was
deleted, and each overridable by name from that moment.

Four SDKs. Python, and now Go, TinyGo and Rust. The Go one is a single package
with two transports: the same source builds to a WebAssembly module and to a
native binary, so the tier stays the operator's decision and never reaches back
into the author's code. A test reads every SDK and fails if one of them cannot
make one of the nine calls.

**Described here and not built.** Four screens stay with the client, and the
reason is the same one in each case: a template renders a thing that exists at
a moment, and these exist in motion. The install map and the blueprint are
drawn canvases somebody drags. The file editor is live text. The terminal is a
socket.

The chat stage is the fifth, and it is the one that was a judgement rather than
a fact: it posts a turn and streams the answer on the same request. Hypermedia
would need that split into a send and a subscribe, with a per-workspace broker
in between — which changes what cancelling a turn means. That is a redesign of
the thing the product is for, and it was declined rather than deferred.

**Dropped rather than deferred.** An Extism-shaped ABI. The plan wanted it so
that "every existing Extism PDK is a Cogitorium PDK on day one", and that was
the right instinct when the alternative was every author implementing a wire
protocol. It is no longer the alternative: `needs: js` has an engine compiled
into the binary and needs no SDK at all, Python has one file of standard
library, and the guest contract is three exports any language reaches in about
thirty lines. Reshaping the runtime and every guest to match somebody else's
vocabulary would buy a compatibility two SDKs already cover — and would tie
this contract to another project's release schedule forever.

A second hypermedia dialect. The plan named htmx *and* Datastar, loaded
unconditionally. Two dialects on every page is two vocabularies every plugin
author has to learn before overriding anything, and the second earns nothing
the first does not already do. htmx and its SSE extension are vendored; the SSE
half is there because the chat stage streams and a conversion that could not
carry streaming would stop at the screen that matters most.

The mark — cosign, a transparency log,
statement families, `verified|unmarked|unverifiable`. It would have tripled the
dependency graph to defend against somebody who controls the catalog
repository, and if that has happened they are already serving whatever they
like. The mechanism is who may merge `verified.json`, which is CODEOWNERS.

The restart controller at step 7 is the part an earlier draft assumed the environment provides: on a supervised channel the process exits and kubelet/compose/systemd restarts it; on an unsupervised channel (Windows portable exe, plain local binary, macOS `.app`) the install re-execs itself.

## Honest limits

- **A Tier B or D worker is not isolated from the server's filesystem.** It is a supervised child process running as the server's OS user. The gear sandbox cannot carry it: the Docker backend copies payloads in rather than bind-mounting (`internal/sandbox/docker.go:157`) precisely so it works against a remote or rootless daemon, and the Kubernetes Job mounts one `subPath` by deliberate design (`internal/sandbox/kube.go:329`). Bolting mounts onto both would break the first backend's rationale and weaken the second's isolation. The plugins page says `not isolated from the server's files` — the same words already used for the subprocess sandbox backend. **Tier C is the isolated option** and costs a container start per invocation. Warm-and-unisolated versus cold-and-isolated is a trade the author reads and chooses.
- **Python has no universal tier and never will under this design.**
- **Tier B is guaranteed by probe, not by construction.** A `noexec` data mount, a Podman-backed emptyDir, or an AppLocker policy denying execution from a user profile will refuse it — each a named refusal naming the mount, delivered on the approval screen before install.
- **A derived image is offline-complete for Tiers 0, A, B and D only.** Tier C is a registry pull.
- **Deno cannot be provisioned on the alpine image** — upstream publishes no musl artifact. Bun covers the same ground.
- **The desktop channel is a three-target build**, all `CGO_ENABLED=1` on native runners because webview needs cgo. The server binary's six-target `CGO_ENABLED=0` claim is separately intact.
- **Identity pinning does not survive administrative compromise of the marks repository.** The design puts the broad attack surface and the trust anchor in different repos, relies on public Rekor monitoring for detection, and has a bounded recovery path. It does not claim compromise is impossible.
- **Revocations are additive and monotone** — a stale cache can miss a revocation but never mis-apply one. The UI shows `catalog last fetched <date>` rather than pretending currency.
- **A stock-Go wasm guest carries the Go runtime.** Measured on the same sample plugin, built three ways: 3.1 MiB from `go`, 1.1 MiB from TinyGo, 124 KiB from Rust. The SDK READMEs say so, so a toolchain is chosen knowingly rather than discovered through an OOM. Note also that a plugin module must be built `-buildmode=c-shared`: the default builds a command, which runs once and ends, and a module that has ended has no exports left to call.
- **Multi-arch derived images cannot carry a pre-warmed AOT cache** — ~314 ms per module on first boot after an image upgrade, ~11 ms from cache after.
- **A path is anonymous unless something claims it.** This was once "every non-`/api/` path is anonymous by construction", and the conversion made that dangerous rather than merely true: the first converted screen answered 200 with its content to anybody who asked. A page now registers its first path segment as authenticated space, which covers the screen and everything nested under it, and browsers get a redirect where the API still gets 401. Plugin routes get `auth: token` by default via the same mechanism; `auth: none` remains reachable and is shown in red on the approval screen.

## Open decisions

- Sigstore trust-root rotation: ship-and-require-a-host-update, allow a signed root update through the egress gate (putting a network dependency back into a path advertised as offline), or carry an overlapping root set. Needs deciding before the first rotation, not during it.
- Whether Tier B runtimes are shared by exact version or by constraint satisfaction.
- Offline verification of ref-seeded plugins: keyless verification is a transparency-log lookup by nature; an air-gapped cluster needs the result recorded at bake time.
- Who may sign a yank — an author withdrawing their own broken release versus a second identity with authority over statements clients act on.
- Whether a missing signature on a Tier D native row is a refusal or a warning. Darwin rows are already a hard requirement because the kernel kills unsigned Mach-O binaries.
- Whether held plugins are auto-retried after a later host or dependency update, or whether re-enabling is always an operator action.
- Whether an image-baked `plugins.order` wins over an operator's reorder on restart. With late binding by name, order is semantics.

## Changelog

- Unreleased — created on `refactor/plugin-system`.
