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

**Gears run as Kubernetes Jobs.** One Job per run. It mounts this release's own
data claim with `subPath` set to that run's directory, so the gear sees its
payload at `/work` and nothing else on the volume — not the SQLite database and
not the provider keys in it. The kubelet enforces that before the container
starts; nothing depends on the gear respecting a boundary.

The Job's pod runs as the same unprivileged user that owns the payload, drops
every capability, refuses privilege escalation, takes a read-only root
filesystem, mounts **no** service account token, and carries the gear's timeout
as `activeDeadlineSeconds` as well as in the server. `backoffLimit: 0`, because
a gear re-run after failing may already have sent a request or spent money.

Two properties this chart cannot deliver on its own, stated rather than assumed:

- **The claim is ReadWriteOnce**, which one node may mount for several pods. So
  gear Jobs are pinned to the server's own node. On a multi-node cluster under
  pressure a gear waits for room there instead of running elsewhere. A
  ReadWriteMany claim removes the pin's necessity but not the single-writer rule
  above.
- **"No network unless granted" is a NetworkPolicy**, and a NetworkPolicy is
  enforced by the CNI plugin rather than by Kubernetes. `gearNetworkPolicy`
  selects gear pods the operator did not grant the network and cuts their
  egress — on Calico or Cilium. On kindnet, or flannel with no policy add-on,
  the object is accepted and enforces nothing, silently. Check yours; until you
  have, treat an ungranted gear as networked. Off-cluster this is
  `--network none` on the container and needs no such caveat.

`sandbox: subprocess` is still available for a cluster whose policy forbids the
Role above, and on it a gear runs as a child of the server with the server's own
file access — approving one grants it everything the server has, and the
terminal and the outward gate stay refused. Note also that this image carries no
`python3`, `node` or `bash`: on that setting only a `binary` gear can run at all.

**The terminal is not available in-cluster on either setting.** A terminal is an
interactive attachment and a gear Job is run-to-completion; the Kubernetes
backend implements running a gear, not attaching to one. The chart refuses it at
template time and the server refuses it at startup.

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

- `config.terminal: true` — on `subprocess` there is no sandbox containing it;
  on `kubernetes` there is no interactive attachment to give.
- `config.egress: true` on `sandbox: subprocess` — an unsandboxed gear can
  rewrite the configuration and the grants table, so the gate would be
  decorative.
- `sandbox: kubernetes` with `serviceAccount.automountServiceAccountToken:
  false` — the server would have no credential to create Jobs with.
- A `config.sandbox` other than `kubernetes` or `subprocess`. There is no Docker
  daemon inside a pod.
- `persistence.enabled: false` with no `existingClaim` — the database would live
  in the pod and be destroyed on every restart.
- An `auth.adminToken` shorter than 24 characters.

## Values

See `values.yaml`; every value is commented where the reason is not obvious.
The ones most likely to need changing: `image.tag`, `persistence.size`,
`persistence.storageClass`, `ingress.*`, and `resources`.
