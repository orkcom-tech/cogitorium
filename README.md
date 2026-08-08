# Cogitorium

A workbench for agentic development: model catalog (API + local), workspaces
of dedicated agents behind an orchestrator chat, blueprint-style wiring
between agents, Contextverse-backed context, and a persistent catalog of
agent-forged tools (gears).

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
