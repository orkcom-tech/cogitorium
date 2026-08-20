---
layout: default
title: Configuration
permalink: /configuration/
description: Every setting Cogitorium accepts — the config file, the environment, the defaults, and what each one refuses to do. Checked against the source, so it cannot fall behind.
---

# Configuration

Every setting this server accepts, in one place. There was no such place: the
guide explained a dozen of them where they came up and the rest existed only in
`internal/config/config.go`, so the answer to "what can I set" was "read the
source".

A test keeps this honest. `TestEveryConfigurationKeyIsDocumented` reads the
`Config` struct and every `COGITORIUM_*` this server looks up, and fails if one
of them is missing from this file. A setting added without a line here does not
merge.

## Where it comes from

Later wins:

1. **Defaults** — what you get having written nothing.
2. **The file** — `--config <path>`, else `$COGITORIUM_CONFIG`, else
   `<data-dir>/config.yaml` if it exists.
3. **The environment** — `COGITORIUM_*`.
4. **Flags** — `--listen`, `--data`, `--log-level`, `--config`.

A file is YAML and every key below is top-level:

```yaml
listen: 0.0.0.0:8688
data_dir: /data
sandbox: docker
terminal: false
```

Booleans read from the environment are strict: only `1` and `true` (any case)
count as on. `COGITORIUM_EGRESS=yes` is **off**, and it overrides a config file
that said on — a value that looks affirmative and is not would be the worst way
to find out what is enabled.

## Where it listens, and what it owns

| Key | Environment | Default | What it does |
|---|---|---|---|
| `listen` | `COGITORIUM_LISTEN` | `127.0.0.1:8688` | The HTTP address. Loopback means one machine; anything else requires a token for every request. |
| `data_dir` | `COGITORIUM_DATA_DIR` | `~/.cogitorium` | The SQLite database and everything the server owns on disk. |
| `public_url` | — | empty | How this install is reachable from outside, so a run's files can be linked in a callback. Empty leaves the links out. |
| `callback_hosts` | — | empty | Hosts a callback may be sent to. Empty means none, which is what makes the callback path unreachable rather than merely unused. |
| `contextd_path` | `COGITORIUM_CONTEXTD` | `contextd` | The Contextverse binary, resolved from `PATH` by default. |
| `log_level` | `COGITORIUM_LOG_LEVEL` | `info` | `debug`, `info`, `warn` or `error`. |
| `log_format` | `COGITORIUM_LOG_FORMAT` | `text` | `text` or `json`. |
| `metrics_listen` | `COGITORIUM_METRICS_LISTEN` | empty (**off**) | The Prometheus endpoint's own address. It is unauthenticated, which is why it is a separate listener and why it has to be asked for. |
| `gear_proxy_listen` | `COGITORIUM_GEAR_PROXY_LISTEN` | `127.0.0.1:8689` | Where the gear network gate listens. Only a gear granted the network is given the credential for it. |
| `update_check` | `COGITORIUM_UPDATE_CHECK` | `ask` | `ask`, `on` or `off`. `ask` is what keeps a fresh install from talking to GitHub before anybody agreed to it. |

## Credentials

Nothing here belongs in a config file that ends up in a repository. The three
that have no YAML key at all are env-only for exactly that reason.

| Key | Environment | Default | What it does |
|---|---|---|---|
| — | `COGITORIUM_ADMIN_TOKEN` | generated | Seeds the first admin's token instead of printing a generated one. At least 24 characters. |
| — | `COGITORIUM_ADMIN_PASSWORD` | none | Seeds the first admin's password, for a deployment nobody is standing in front of. |
| — | `COGITORIUM_SECRET_KEY` | none | Encrypts this install's own secret store. Without it, storing a secret is refused rather than done badly. |
| `variables_dir` | `COGITORIUM_VARIABLES_DIR` | empty | A directory of files read as named values — how a Kubernetes ConfigMap is mounted. |
| `secrets_dir` | `COGITORIUM_SECRETS_DIR` | empty | The same for a mounted Secret. |
| `orchestrator_secrets` | `COGITORIUM_ORCHESTRATOR_SECRETS` | on | `off` stops the orchestrator reading and writing named values. Written here it is absolute: no screen can lift it. |

## Models

Seeded, never enforced. A provider is created only when this install has no
provider by that name; nothing is updated or removed afterwards, so an address
changed on the Models screen stays changed. See the
[starter](https://github.com/orkcom-tech/cogitorium/blob/main/deploy/starter/cogitorium.yaml).

| Key | Environment | Default | What it does |
|---|---|---|---|
| `providers` | — | empty | Providers and their models, known on first start so a deployment comes up ready rather than empty. |
| `orchestrator_model` | — | empty | The model a new workspace's orchestrator thinks with, as `<provider>/<model>`. Only set when nobody has chosen one. |

```yaml
providers:
  - name: local
    kind: openai-compatible        # or: anthropic
    base_url: http://ollama:11434/v1
    key_env: SOME_ENV_VAR          # the variable holding the key, never the key
    models:
      - name: qwen2.5:7b           # what the provider calls it
        label: local               # what you read on a screen
orchestrator_model: local/qwen2.5:7b
```

## Running agent-authored code

| Key | Environment | Default | What it does |
|---|---|---|---|
| `sandbox` | `COGITORIUM_SANDBOX` | `auto` | `docker`, `kubernetes`, `subprocess`, or `auto` — which uses Docker when it answers and says plainly when it does not. It never picks `kubernetes`, which is a deployment rather than a guess. **`subprocess` does not isolate anything**: a gear runs with this server's file access, including the database and the provider keys in it. |
| `sandbox_image` | `COGITORIUM_SANDBOX_IMAGE` | `python:3.12-alpine` | The image gears run in. |
| `sandbox_runtime` | `COGITORIUM_SANDBOX_RUNTIME` | empty | An OCI runtime the daemon has been configured with — `runsc` for gVisor, `kata-runtime` for Kata. Cogitorium selects one and refuses at startup if the daemon lacks it; the isolation belongs to the runtime, not to this product. |
| `sandbox_pool` | `COGITORIUM_SANDBOX_POOL` | `0` (off) | Warm containers per image. The one setting that trades isolation for latency: a pooled container has a history. Runs given named values or the network are never pooled. |
| `browser_image` | `COGITORIUM_BROWSER_IMAGE` | pinned Playwright image | The container a gear granted the browser environment runs in. Large, and never pulled at startup. |
| `egress` | `COGITORIUM_EGRESS` | `false` | Whether agents may reach the internet at all. Off on a fresh install. |
| `egress_key` | `COGITORIUM_EGRESS_KEY` | empty | The credential for the outward gate. Never baked into defaults. |
| `mcp_clients` | `COGITORIUM_MCP_CLIENTS` | `false` | Lets an operator install external MCP servers. An approved one runs on the host as this server's user, outside the sandbox — policy, not isolation. |

### On Kubernetes

Only the claim has to be supplied: it names the volume the data directory is
on, and a gear Job mounts that same claim at its own subPath.

| Key | Environment | Default | What it does |
|---|---|---|---|
| `kube_namespace` | `COGITORIUM_KUBE_NAMESPACE` | the pod's own | Where gear Jobs are created. |
| `kube_claim` | `COGITORIUM_KUBE_CLAIM` | empty | The claim the data directory is on. |
| `kube_node` | `COGITORIUM_KUBE_NODE` | downward API | A ReadWriteOnce volume attaches to one node, so a Job scheduled elsewhere waits forever. |
| `kube_cpu` | `COGITORIUM_KUBE_CPU` | cluster's own | Bounds one gear Job. |
| `kube_memory` | `COGITORIUM_KUBE_MEMORY` | cluster's own | The same. |

## The terminal

| Key | Environment | Default | What it does |
|---|---|---|---|
| `terminal` | `COGITORIUM_TERMINAL` | **on** | A shell in the UI. |

It is on, and it is a shell on the machine this server runs on, as the account
it runs as — the same reach you already have by sitting at that machine, and
the terminal an editor would have given you. The exception is a workspace
terminal on an install other people can reach: a member is not the operator, so
there they get the sandbox instead.

`terminal: false` refuses it entirely, and on a shared install it should. The
setting distinguishes "written false" from "not written", so writing it once
holds across restarts and across changes to the default.

## Work

| Key | Environment | Default | What it does |
|---|---|---|---|
| `queue_workers` | — | `4` | How many queued deliveries run at once. |
| `queue_max_per_workspace` | — | `50` | A burst, not a backlog: large enough that an ordinary spike waits rather than being refused. |
| `budget_run_tokens` | — | `0` (no ceiling) | A token ceiling for one run. |

## Plugins

| Key | Environment | Default | What it does |
|---|---|---|---|
| `plugins` | — | empty | Per-plugin settings, keyed by plugin id. Deliberately untyped: the host has no idea what any plugin's settings mean and should not pretend to. |

```yaml
plugins:
  release-radar:
    repository: orkcom-tech/cogitorium
```

Read-only from a plugin's side. What a plugin wants to remember goes in its own
storage — a plugin writing its own configuration would be a plugin granting
itself something.

## Also

`COGITORIUM_CONFIG` names the config file itself, and is the second place
`--config` looks.
