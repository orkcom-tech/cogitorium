# Contributing

The short version: **fork, branch, open a pull request.** Nobody outside the
organisation can push to `main`, and nobody inside pushes anything to it that
would surprise the others. That is the whole of the process.

## Getting it running first

Do this before anything else — a change you cannot run is a change nobody can
review.

```sh
git clone https://github.com/orkcom-tech/cogitorium
cd cogitorium
make build          # builds the interface, then the binary
./bin/cogitorium serve
```

Go 1.25 and Node 22. `make build` runs the two in the right order: the server
embeds `web/dist`, so the interface has to exist before the binary does.
Contextverse's `contextd` is a separate program the context screens need —
`scripts/ci/install-contextd.sh` fetches the matching one, and without it the
server still starts and says so.

If you have no model provider yet:

```sh
docker compose -f docker-compose.starter.yml up
```

That brings up Cogitorium, a local model, and the wiring between them.

## Before you open the pull request

```sh
go test ./... -race      # what CI runs
gofmt -l .               # must print nothing
go vet ./...
cd web && npx tsc --noEmit && npm test
```

Two generated files fail the build if you changed what they describe, and both
regenerate rather than being edited:

```sh
UPDATE_REGISTRY=1 go test ./internal/view/  -run TestTheRegistry        # docs/registry.json
go test ./internal/server -run TestOpenAPI -update                      # docs/openapi.yaml
```

## What gets a change taken

**Say what breaks.** The most useful first line of a pull request is what a
person hits and who hits it. `file:line` is not an explanation.

**Bring a test that would have caught it.** Not coverage for its own sake — the
test that fails before your change and passes after it. If the thing cannot be
tested, say so in the pull request; that is a real answer.

**Write the reason down, not the summary.** This codebase's comments explain
*why* a thing is the way it is, usually including what was tried before and what
it cost. A comment that restates the code is noise; a comment that says "this
used to be X and here is what broke" is why the next person does not undo it.
Commit messages are held to the same standard.

**Documentation is part of the change.** If somebody could notice what you did,
it belongs in `docs/`: the guide for a screen, `docs/configuration.md` for a
setting. A test fails if a setting exists and that page does not describe it.

## What is unlikely to be taken

- A change that widens what agent-authored code may reach, without the
  reasoning for it. The sandbox, the approval gate and the network grants are
  the load-bearing walls here.
- A dependency added for something the standard library does.
- A reformat, rename or restructure bundled with a behaviour change — split
  them, or the review cannot separate what is mechanical from what is not.
- A feature with no way to tell whether it worked.

## Reporting something instead of fixing it

An issue is welcome and does not need a patch attached. Say what you did, what
you expected and what happened, with the version (`cogitorium version`) and how
you installed it.

**A security problem is not an issue.** See [SECURITY.md](SECURITY.md).

## Plugins

A plugin is its own repository and does not go here. The authoring guide is at
<https://orkcom-tech.github.io/cogitorium/plugins/>, and listing yours in the
shared catalogue is a pull request to
[`cogitorium-plugins`](https://github.com/orkcom-tech/cogitorium-plugins).

## Licence

By opening a pull request you agree that your contribution is licensed under
the same terms as this project — see [LICENSE](LICENSE). There is no separate
agreement to sign.
