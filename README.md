# k8s-daemonset-coverage

[![ci](https://github.com/moveeeax/k8s-daemonset-coverage/actions/workflows/ci.yml/badge.svg)](https://github.com/moveeeax/k8s-daemonset-coverage/actions/workflows/ci.yml)
[![go](https://img.shields.io/badge/go-1.22%2B-00ADD8)](https://go.dev/)
[![license](https://img.shields.io/badge/license-MIT-blue)](LICENSE)

Find the nodes a DaemonSet is silently **not** running on, and the exact reason for each one.

`kubectl get ds` prints `DESIRED 40 / READY 40` and looks perfectly healthy, because `DESIRED`
already excludes every node the scheduler decided was ineligible. The number that matters —
nodes in the cluster minus nodes actually covered — is not printed anywhere. So the log shipper
covers 40 of 52 nodes, nothing is red, and you find out the week you go looking for logs from the
one node that broke.

```
NAMESPACE   DAEMONSET      SCOPE  COVERED  COVERAGE
logging     fluent-bit     6      2        33.3%
monitoring  node-exporter  6      4        66.7%

logging/fluent-bit — 4 uncovered node(s):
  ip-10-0-0-5                  untolerated-taint  (taint node-role.kubernetes.io/control-plane:NoSchedule is not tolerated)
  ip-10-0-2-21                 pod-not-ready  (pod phase=Running, Ready condition is not True)
  ip-10-0-2-22                 node-unschedulable  (spec.unschedulable=true (cordoned))
  ip-10-0-3-31                 untolerated-taint  (taint nvidia.com/gpu=true:NoSchedule is not tolerated)
```

## Install

```sh
go install github.com/moveeeax/k8s-daemonset-coverage/cmd/k8s-daemonset-coverage@latest
```

Or build from a checkout:

```sh
go build -o k8s-daemonset-coverage ./cmd/k8s-daemonset-coverage
```

## Usage

```sh
# every DaemonSet in the cluster
k8s-daemonset-coverage

# one namespace, machine-readable, only the ones with a gap
k8s-daemonset-coverage -n monitoring --format json | jq '.daemonsets[] | select(.coveragePct < 100)'

# an agent that is only supposed to cover workers, gated in CI
k8s-daemonset-coverage --expect-labels node-role.kubernetes.io/worker= --fail-under 100

# no cluster needed: replay a snapshot
k8s-daemonset-coverage --snapshot examples/cluster-snapshot.json
```

| Flag | Meaning |
| --- | --- |
| `-n`, `--namespace` | namespace to inspect (default: all namespaces) |
| `--kubeconfig` | explicit kubeconfig path (default: standard loading rules, including in-cluster) |
| `--context` | kubeconfig context to use |
| `--expect-labels` | node labels the DaemonSet is meant to cover; other nodes leave the scope entirely |
| `--format` | `text` (default) or `json` |
| `--fail-under` | exit 1 if any DaemonSet is below this coverage percentage |
| `--fail-on-pending` | exit 1 if any DaemonSet pod is `Pending` |
| `--snapshot` | read a JSON cluster snapshot instead of talking to a cluster |
| `--timeout` | API timeout (default `30s`) |

Exit codes: `0` clean, `1` a threshold was breached, `2` an error.

`--expect-labels` follows label-selector semantics: `key=value` requires that exact value
(including the empty value that node-role labels carry, so `node-role.kubernetes.io/worker=`
works), while a bare `key` only requires the label to be present.

## Reasons

Every uncovered node gets one stable reason, usable in `jq` and in alerts:

| Reason | What happened |
| --- | --- |
| `untolerated-taint` | a `NoSchedule`/`NoExecute` taint the DaemonSet does not tolerate; the taint key is printed |
| `nodeselector-mismatch` | `spec.nodeSelector` does not match the node labels; both sides are printed |
| `nodeaffinity-mismatch` | no required `nodeSelectorTerm` matched |
| `node-unschedulable` | the node is cordoned (`spec.unschedulable=true`) |
| `pod-missing` | the node is schedulable for this DaemonSet but carries no pod |
| `pod-pending` | the pod exists and is stuck `Pending` |
| `pod-not-ready` | the pod is running but its `Ready` condition is not `True` |

## How it works

The tool lists nodes, DaemonSets and pods, then re-implements the node predicates the scheduler
uses to accept or reject a DaemonSet pod:

- **taints/tolerations** — `NoSchedule` and `NoExecute` only (`PreferNoSchedule` cannot keep a pod
  off a node), with `Equal`/`Exists` operators, effect-scoped tolerations and the empty-key
  wildcard.
- **nodeSelector** — every key must match exactly.
- **nodeAffinity** — `requiredDuringSchedulingIgnoredDuringExecution`: terms OR-ed, expressions
  AND-ed, operators `In`, `NotIn`, `Exists`, `DoesNotExist`, `Gt`, `Lt`, plus `matchFields` on
  `metadata.name`. Preferred affinity is ignored, because it never blocks scheduling.

Cordon is checked before taints on purpose: a cordoned node also carries
`node.kubernetes.io/unschedulable:NoSchedule`, which DaemonSets tolerate by default, so reporting
it as a taint finding would be both noisy and wrong. "You cordoned this node" is the actionable
answer.

The predicate engine (`internal/predicate`) and the coverage math (`internal/coverage`) take plain
API objects and return findings, so the rules are table-driven unit-testable and the Kubernetes
client (`internal/kube`) is only an adapter. That is also why `--snapshot` exists: the same engine
runs against a JSON file, which is how the example above is produced in CI.

Coverage counts scheduling-ineligible nodes **against** the DaemonSet. A nodeSelector matching 3 of
5 nodes reports 60%, not 100% — the point of the tool is the gap between the cluster and the
DaemonSet, not the gap the scheduler already accepted.

## RBAC

Read-only, cluster-scoped for nodes:

```yaml
apiVersion: rbac.authorization.k8s.io/v1
kind: ClusterRole
metadata:
  name: daemonset-coverage
rules:
  - apiGroups: [""]
    resources: ["nodes", "pods"]
    verbs: ["get", "list"]
  - apiGroups: ["apps"]
    resources: ["daemonsets"]
    verbs: ["get", "list"]
```

## Development

```sh
go test ./...        # no cluster required
go vet ./...
```

`examples/cluster-snapshot.json` is a six-node cluster with a GPU taint, a cordoned node, a
not-ready pod and a pending pod — one fixture covering every reason the tool can emit.

## License

MIT
