# Workflow versions

> **Status:** planned, not built. Recorded here so the shape is agreed before
> anything is written.

## What this is

A workflow gets versions, the way Vault gives a secret versions: every change
writes a new one, the old ones stay readable, and rolling back is picking an
earlier number rather than reconstructing what it used to say.

A version covers the whole of what the workflow is, not just the wires:

- the blueprint — agents, the wires between them, clocks
- each agent's own state — its model, its role, what it carries
- the gears it may call
- the instructions pinned to it
- the context it reads
- the memory it has accumulated

If it decides what a run does, it is in the version. A blueprint that versioned
its arrows but not the instruction an agent reads would be a version number
that does not identify a behaviour, which is worse than no version at all.

## The part that makes it work: a workflow holds copies

An instruction, a gear or an agent that is added to a workflow is a **copy** of
the one in the global space, pinned to the version it was copied at. It is not
a pointer.

This is the whole design, and it is worth being blunt about why. If a workflow
referenced the global object, then editing a gear in the library would change
every workflow that uses it, silently, including ones that are running and ones
somebody signed off on last month. The version number would then describe the
workflow's own edits and nothing else — the behaviour could change underneath a
version that never moved.

So: adding pins a copy. The library is where things are authored; a workflow is
where a particular set of them is frozen together and run.

What that costs, stated rather than discovered later:

- **Updating is an action, not an accident.** A workflow shows that the library
  has a newer version of something it holds, and adopting it is a decision that
  makes a new workflow version. Nothing arrives on its own.
- **Copies multiply.** Storage is by content, so N workflows holding the same
  unchanged gear hold one copy of its bytes and N references to it.
- **"Where did this come from" has to survive the copy.** Each copy records
  which library object and which version it came from, or the update prompt
  above has nothing to compare against.

## What a version is for

- **Reading what it used to be.** Not a diff of files — the whole workflow as
  it stood.
- **Rolling back.** One action, and it makes a new version rather than erasing
  the ones after it: history that can be rewritten is history nobody can rely
  on in an argument about what ran.
- **Saying what ran.** A completed run names the workflow version it ran under,
  so "why did it do that" is answerable a month later.
- **Handing it to somebody.** Export and import already exist; a version is
  what makes them reproducible rather than approximate.

## Open questions

- Does memory belong in the version, or beside it? It is state a run
  accumulates rather than something an author writes — rolling back a workflow
  and silently un-learning what it learned may be wrong. Possibly memory is
  versioned but not rolled back with the rest, and the rollback says so.
- What a version is named by. A number is honest; a message is useful; both is
  probably right, and the message must not be required.
- Whether a version is taken on every change or on an explicit save. Every
  change is truthful and noisy. An explicit save is readable and lies by
  omission about what was running in between.
- Whether a running workflow can be edited at all, or whether editing forks a
  new version that takes effect at the next run.

## CI/CD

Wanted, and deliberately not designed here — the shape is to be discussed. It
is recorded now because it constrains the above: whatever CI/CD turns out to
be, it will act on workflow versions rather than on live state, so versions
have to exist first and have to be addressable by name from outside.

## Changelog

- 2026-08-19 — recorded from a conversation while walking the product.
