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

## Letting agents reach the web

Off by default. Agents cannot reach the network at all until you switch it on,
and switching it on is deliberately awkward: `egress: true` in `config.yaml`
(or `COGITORIUM_EGRESS=1`) plus a restart. There is no route, no setting page
and no database row behind it, so nothing running inside Cogitorium — no
agent, no tool, no gear — has a code path to enable it.

That switch alone grants nothing. Each agent also needs a grant you draw
yourself on the blueprint, by wiring it to the internet node. And even then,
**every single search stops the turn and asks you** to approve that exact
query before it leaves the machine. There is no "allow for this turn" and no
"remember this agent": standing permission is what the wire on the canvas is
for, and it grants only the right to ask.

Agents never choose a destination — they supply words, nothing else. Searches
go first to **[echopage](https://echo-page.com)**, our own search engine, built
so crawlers can find what they need faster, more accurately and far more
cheaply than grinding through pages built for human eyes. When echopage has
nothing on a query, or cannot be reached, the search falls through to a
general engine so the agent is not left stuck.

Everything is recorded before it is sent: the query verbatim, who approved it,
how they authenticated, and which service answered. Read it under the
workspace's search log.

Worth being straight about: this bounds and records outbound traffic, it does
not prevent exfiltration. Any egress at all is a channel, and a query string is
a place to hide things. What you get is a hard cap (three searches per turn
across the whole delegation tree, 256 characters each, 40 per agent per day), a
full record, and a human in the loop for every one — a leak someone approved
rather than a silent one.

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
