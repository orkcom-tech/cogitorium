<!--
  Thank you. Three things make a pull request easy to take, and all three are
  about the reader rather than about you.
-->

## What breaks without this

<!-- One or two sentences, in terms of what a person hits. "The workspace
     terminal loses its scrollback when you switch screens" beats "refactor
     session handling". If it is a new feature, say who was stuck without it. -->

## What you changed

<!-- The shape of it, not a file list — the diff already has the file list. -->

## How you know it works

<!-- What you ran, and what it said. A test that fails before your change and
     passes after it is the strongest form of this. If you could not test
     something, say which part and why; that is useful and not held against
     you. -->

---

- [ ] `go test ./...` passes locally
- [ ] `gofmt -l .` is empty, and `cd web && npx tsc --noEmit` is clean if you
      touched the interface
- [ ] Behaviour that somebody could notice is written down in `docs/` — the
      guide for a screen, `docs/configuration.md` for a setting
- [ ] The commit message says *why*, not only *what*

<!--
  CI runs on this pull request. A first-time contributor's run waits for a
  maintainer to press a button — that is a safety default and not a comment on
  you.
-->
