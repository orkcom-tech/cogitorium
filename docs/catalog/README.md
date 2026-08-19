# The plugin catalog

This describes what `orkcom-tech/cogitorium-plugins` holds and what its CI
checks. It lives here rather than only in that repository so the rules and the
code that reads them travel together — a submission guide that drifts from the
client is a guide that tells authors the wrong thing.

## What the repository holds

One file.

```
plugins.json
```

An array of entries, five fields each:

```json
[
  {
    "id": "release-radar",
    "name": "Release Radar",
    "author": "someone",
    "description": "Watches releases and files them into a workspace.",
    "repo": "someone/cogitorium-release-radar"
  }
]
```

That is the whole schema, and it is small on purpose. Everything else about a
plugin — what it needs, what it overrides, what it asks permission for — is in
its own `plugin.yaml`, where it travels with the code rather than with a
description somebody wrote once and never updated.

**The index says where things are. It never says what is true.** A downloaded
JSON file asserting that a plugin is trustworthy would be a plugin asserting it
about itself, one indirection away.

## Where the code lives

In the author's own repository, and the bundle in its GitHub releases. Nothing
here stores or serves anybody's binaries, which is why an index costs one small
file no matter how many plugins there are.

The client builds the download URL by convention rather than asking GitHub's
API — the API needs a token to be useful at any volume and would make browsing
depend on a service being up to answer a question the URL already answers:

```
https://github.com/<repo>/releases/latest/download/<id>.zip
```

So publishing is: tag a release, attach `<id>.zip` built by
`cogitorium plugins build`.

## Submitting

Open a pull request adding one entry. CI runs; if it passes, the entry merges
automatically. Nobody waits on a human to be available.

### What CI checks

| Check | Why |
|---|---|
| The diff adds or edits entries and nothing else | A submission that also edits the workflows is not a submission |
| `id` is 3–48 lowercase characters and not reserved | It becomes a template namespace and a URL prefix |
| `id` is not already taken | Two plugins with one id is two plugins that cannot coexist |
| `repo` is `owner/name` | It is what the download URL is built from |
| Every field is present | An entry with no description is a row nobody can read |
| The release exists and carries `<id>.zip` | An entry pointing at nothing is worse than no entry |
| The bundle's `plugin.yaml` parses and validates | It would be unloadable on the first machine that tried |
| The manifest's `id` matches the entry's | Otherwise the catalog and the plugin disagree about what this is |
| Its templates parse and its names are legal | An author learns here rather than from a stranger's log |

The last three are the same code the server runs, invoked as
`cogitorium plugins check-bundle <zip>` — one implementation, so a submission
cannot pass CI and then fail to load.

## The official mark

Auto-merge means an untrusted pull request can land without a human. So the
mark that says *the maintainer read this* cannot live in this repository at
all: it lives in `orkcom-tech/cogitorium-marks`, which has no pull request
path, no bot, and no auto-merge.

Clients pin both identities **and which kinds of statement each may sign**. A
mark signed by the identity that publishes this index is rejected before its
signature is even interesting, because that identity has no authority over
marks. That single rule is what lets auto-merge and the mark coexist: the
signing capability an attacker can reach through the submission path is scoped
to a kind that grants no trust.

A client never reads a boolean. It recomputes the digest of the bytes on disk
and returns a record — verified, unmarked, or unverifiable — and
**unverifiable is displayed more loudly than unmarked**, because "I could not
check" and "nobody vouched" are different facts.

## What the mark does not mean

It means the maintainer looked at that exact version. It is not a guarantee,
not a security audit, and not a substitute for the approval step on your own
install — which is where somebody who can actually see your data decides
whether this plugin should touch it.
