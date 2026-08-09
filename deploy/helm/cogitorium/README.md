# Cogitorium on Kubernetes

```sh
helm install cogitorium ./deploy/helm/cogitorium \
  --namespace cogitorium --create-namespace \
  --set auth.adminToken="$(openssl rand -hex 24)"
```

Then port-forward and open it, or set `ingress.enabled`.

## Two constraints that are not preferences

**One replica.** Everything is stored in SQLite with a single writer. Two pods
on one volume corrupt the database — not "may", will. There is no
`replicaCount` value: a number you can raise is a number somebody raises during
an incident. The Deployment is pinned to one and its strategy is `Recreate`,
because even the brief overlap of a rolling update is two writers.

Horizontal scale needs a different store behind the same `store` package. That
is a project, not a values flag, and smuggling it in as one would be worse than
not having it.

**Gears are not isolated in this deployment.** Off-cluster, a gear runs in a
throwaway container with no network and none of the server's files. There is no
Docker inside a pod, so in-cluster it runs as a subprocess of the server and
holds the server's own file access — the SQLite database, and the provider API
keys in it.

So in this deployment, approving a gear grants it everything the server has.
The approval gate is the only control. The chart therefore refuses, at template
time, to enable the in-UI terminal or the outward gate: both are meaningless
without a sandbox, and the server refuses them too.

The fix is gear execution as Kubernetes Jobs — one Job per run, its own
filesystem, no service-account token, a deny-all egress policy, and
`activeDeadlineSeconds` enforced by the kernel instead of by a timer in the
server. It is designed in `docs/planning/cogitorium-kubernetes.md`. It is not
built, and this chart does not pretend otherwise.

## Credentials

Nothing sensitive goes in the ConfigMap — it is readable by anything that can
read the namespace, and it ends up in `helm get values` and in whatever GitOps
repository rendered it.

| What | Where it comes from |
|---|---|
| Admin token | `auth.adminToken` (chart creates a Secret) or `auth.existingSecret` |
| Egress key | `config.egressKey` or `config.egressExistingSecret` |
| Provider API keys | Entered in the UI, stored in the database on the volume |

**Set the admin token.** Without it the server generates one and logs it once at
first start, which on a laptop is fine and in a cluster means it sits in the pod
log for anyone who can read logs. With it, nothing sensitive is ever printed —
the server reads it from the environment and logs only that it was seeded.

It must be at least 24 characters. The chart refuses shorter at template time
and the server refuses at startup.

## Contextverse

The image carries `contextd` and initialises its space on first start, so
context and memory work without a second deployment. The space lives on the same
volume as the database, under `/data/home` — the root filesystem is read-only,
so `HOME` is set there rather than into the image.

## What the chart validates before applying anything

A chart that renders something broken and lets the cluster discover it turns a
five-second failure into a debugging session. These fail at template time:

- `config.terminal: true` — interactive code execution with no sandbox to
  contain it.
- `config.egress: true` without a sandbox — an unsandboxed gear can rewrite the
  configuration and the grants table, so the gate would be decorative.
- `persistence.enabled: false` with no `existingClaim` — the database would live
  in the pod and be destroyed on every restart.
- An `auth.adminToken` shorter than 24 characters.

## Values

See `values.yaml`; every value is commented where the reason is not obvious.
The ones most likely to need changing: `image.tag`, `persistence.size`,
`persistence.storageClass`, `ingress.*`, and `resources`.
