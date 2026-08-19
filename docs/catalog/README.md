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

**Additions only.** Editing or removing an entry that is already listed does
not merge itself, and this is the one rule worth explaining rather than just
enforcing: an edit can point an id people have already installed at a
**different repository**, which hands that plugin's download URL to whoever
opened the pull request. Nothing in a public JSON file can establish who owns
an id — the author of a pull request is whoever opened it, which is exactly the
claim under question. Additions cannot do that, which is why additions are the
thing that merges itself.

The rule is `cogitorium plugins check-catalog plugins.json --base <the current
one>`, which exits non-zero on an edit or a removal and says which entry
moved.

### What CI checks

| Check | Why |
|---|---|
| The diff touches only `plugins.json` and `verified.json` | A submission that also edits the workflows is not a submission |
| The file has no field nobody implements | A field nobody reads is a field an author believes in |
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

None of these are implemented in the workflow itself. A validator written there
would be a second opinion about the format, and the two would disagree
eventually — with the disagreement landing on an author who did nothing
wrong.

## The verified list

A second file, `verified.json`, listing plugins somebody on the team has
actually read:

```json
[
  { "id": "release-radar", "version": "1.2.0", "by": "eduard",
    "note": "reads a feed, writes nothing" }
]
```

`id` is the only required field. `version` is worth filling in: a plugin is not
a fixed thing, and *we checked 1.2.0* beside an installed 1.4.0 is a more
useful sentence than a badge that says nothing about which code anybody saw.

**The mechanism is who may merge this file.** Ordinary submissions to
`plugins.json` auto-merge on green CI; this one goes through CODEOWNERS
review. An author can add themselves to the catalog without waiting for
anybody. Nobody can add themselves here.

There are no signatures and no keys. Those defend against somebody who
controls this repository — and if that has happened, they are serving whatever
they like to every client anyway, including the next release's pinned keys.
GitHub's access control is the mechanism; cryptography on top of it would be
decoration.

### What a client shows

Three states, not a badge:

| State | Means |
|---|---|
| `verified` | the team read the version you have |
| `verified-other-version` | they read a different one, and it says which |
| `unchecked` | nobody has looked — the ordinary state, and not an accusation |

A missing or unreachable `verified.json` leaves everything `unchecked`, which
is true rather than a guess in either direction.

## Knowing an update exists

A catalog entry carries no version — an author tags a release and never
touches this repository again, which is what makes submitting cheap. But a
client cannot know an update exists without a version from somewhere.

The obvious answer is asking GitHub once per installed plugin, and it is the
one worth avoiding: not because GitHub is untrustworthy, but because it would
mean every install continuously telling a third party exactly which plugins it
runs.

So the catalog's own CI polls each listed repository on a schedule and
publishes `index.json` — the same entries with the current version filled in.
A client fetches that **whole file**, with no query string, no install id and
no list of what it has, and diffs locally. What an install runs stays its own
business by construction rather than by policy.

`index.json` is generated. Nobody edits it, and a catalog that has not
published one yet still works — clients fall back to `plugins.json` and simply
cannot tell which versions exist, which is reported as "cannot tell" rather
than as "up to date".

## What verified does not mean

It means somebody on the team read that version. It is not a guarantee, not a
security audit, and not a substitute for the approval step on your own install
— which is where somebody who can actually see your data decides whether this
plugin should touch it.
