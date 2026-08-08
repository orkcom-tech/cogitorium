# Cogitorium

A workbench for agentic development: model catalog (API + local), workspaces
of dedicated agents behind an orchestrator chat, blueprint-style wiring
between agents, Contextverse-backed context, and a persistent catalog of
agent-forged tools (gears).

The vision: a maximally modular, configurable, transparent AI OS. No
telemetry, no tracking, no junk — every behavior local, inspectable, and
explainable. From developers, for developers. Your models can live anywhere:
a local process, your homelab, a provider API — Cogitorium doesn't care and
doesn't phone home.

> **Status: early development.** Nothing here is stable.

## Run

Local (Go ≥1.25 and Node ≥22):

```sh
make build
./bin/cogitorium serve
```

Docker:

```sh
docker compose up --build
```

Then open <http://127.0.0.1:8688>.

Configuration precedence: flags > `COGITORIUM_*` env > `config.yaml` (in the
data dir, default `~/.cogitorium`) > defaults. See `cogitorium serve --help`.

## Testing end to end

`scripts/e2e.sh` exercises the whole pipeline against real components — a
real local model, a real `contextd` space, the real binary. There are no
mocks or stubs anywhere in this repository; when something cannot be
verified (a model too small to drive a tool call, say) the script reports it
as skipped rather than pretending it passed.

```bash
docker compose -f docker-compose.models.yml up -d
docker compose -f docker-compose.models.yml exec ollama ollama pull qwen2.5:0.5b
contextd init solo --name e2e   # once, if you have no context space yet
make build && ./scripts/e2e.sh
```

## License

[BUSL-1.1](./LICENSE): free to use as a tool — including production use, solo
or self-hosted for your team. What it forbids is offering Cogitorium itself
to third parties as a hosted or embedded service competing with Cogitorium's
products. Converts to Apache-2.0 on the change date.
