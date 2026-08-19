# What the orchestrator may do

> **Status:** the remit is decided; the gap between it and the code is measured
> below and not yet closed.

## The remit

The orchestrator can build and unbuild everything in its workflow.

Told to accomplish something, it must be able to create the agents that will do
it, give them the models and roles they need, forge or fetch the gears they
call, bind the instructions and context they read, wire them to each other, and
put whatever runs on a clock — all of it from the user's stated intent, without
the user going round the back to a screen.

And it must be able to take those things away again. Destruction is not the
opposite of control; it is control. A thing that can only add is a thing that
fills a workspace with what it got wrong the first time, and leaves the person
to sweep up after an assistant that was supposed to save them the sweeping.

Two limits, both deliberate:

- **It does not edit an element's code in flight.** Forging a gear is authoring;
  rewriting a running one underneath whoever approved it is not, and the
  approval is bound to the bytes for exactly this reason.
- **It works within its own workspace.** The library outside it is shared, and
  a workflow that could quietly rewrite what other workflows use is the problem
  [[workflow-versions]] exists to prevent.

## Delegating what it cannot do itself

The orchestrator is whatever model the operator chose, and that model may not
be able to write code. That is not a reason to refuse the job: the correct
answer is for it to create an agent on a model that can, hand it the work, and
carry on. Choosing the tool includes choosing the worker.

The machinery for this already exists — `models_list` to see what is offered,
`agent_create` to make one on a chosen model, `delegate` to hand over. What is
missing is not this part.

## What has been built since

Removals, each the opposite of something that already existed: `agent_delete`
(refusing to delete the agent taking the turn), `wire_cut`, `revoke_gear`.

Clocks: `schedule_create`, `schedule_list`, `schedule_update`,
`schedule_delete`, found by name rather than id.

Named values: `env_list` always, and `env_get`, `env_set`, `env_delete` when
the operator has not switched them off. Only the orchestrator ever sees them; a
worker agent receives values the way a gear does — declared by name, supplied
by the host, unseen. The switch withholds the tools rather than offering them
and refusing, because a tool a model can see is a tool it will try.

Still untouched: MCP servers, receivers, the queue.

## What was there when this was written, measured

Twenty tools. **Every one of them creates, reads, or binds. Not one removes
anything.**

| Can do | Cannot do | Exists in the API |
|---|---|---|
| `agent_create`, `agent_update` | delete an agent | `DELETE /agents/{id}` |
| `wire_create` | cut a wire | `DELETE /wires/{id}` |
| `forge_gear`, `grant_gear` | revoke a grant, delete a gear | `DELETE /gear-bindings/{id}`, `DELETE /gears/{id}` |
| `save_instruction` | delete an instruction | `DELETE /instructions/{id}` |
| `context_bind`, `context_unbind` | — the one pair that is whole | |

And whole subsystems it cannot touch at all, though the product can:

| Subsystem | Tools it has | Routes the API has |
|---|---|---|
| **Schedules / clocks** | none | 6 |
| **Variables and secrets** | none | 6 |
| **MCP servers** | none | 14 |
| **Receivers** | none | several |
| **Queue** | none | 2 |

The schedules row is the one that contradicts the remit outright: a user asking
for something to happen nightly is asking for a clock, and the orchestrator has
no way to make one. It can create the agent that would run and then has to tell
the person to go and set the timer themselves.

## What this implies

- Every create gets its opposite. Not for symmetry — because an orchestrator
  that cannot undo its own mistake makes the person clean up after it.
- Schedules, variables, MCP servers and receivers get tools, because each is
  something a user describes in the same sentence as the work: "every morning",
  "using my API key", "from our ticket system".
- A removal is still a removal: it goes through the same refusals a person's
  would, and it says what it did. "I deleted the agent you asked about" is an
  answer; a silently smaller workspace is not.
- Approval does not move. Forging a gear leaves it pending exactly as it does
  today. Being able to create is not being able to authorise.

## Changelog

- 2026-08-19 — remit stated, and the gap measured against the code.
