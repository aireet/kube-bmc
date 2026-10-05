# kubectl plugin

`kubectl-bmc` is a kubectl plugin that uses your kubeconfig. It does not connect to the kube-bmc
server.

## Installation

Download the archive for your platform from the
[latest release](https://github.com/aireet/kube-bmc/releases/latest), verify it against
`kubectl-bmc_checksums.txt` and place `kubectl-bmc` on your `PATH`:

```bash
curl -fsSLO https://github.com/aireet/kube-bmc/releases/latest/download/kubectl-bmc_linux_amd64.tar.gz
tar -xzf kubectl-bmc_linux_amd64.tar.gz kubectl-bmc
sudo install kubectl-bmc /usr/local/bin/
kubectl bmc version
```

Or build it from source: `go install github.com/aireet/kube-bmc/cmd/kubectl-bmc@latest`.

## Commands

| Command | Description | Required permissions |
|---|---|---|
| `kubectl bmc list [-o wide\|json\|yaml\|name] [--health H]` | BMCs with health, power and inventory | `list bmcs` |
| `kubectl bmc describe NAME` | Inventory, health, problems and recent actions | `get bmcs`, `list bmcactions` |
| `kubectl bmc sensors NAME [--type T] [--problems] [--all]` | Live sensor readings and thresholds | `get bmcs`, `pods`/`pods/proxy` in the kube-bmc namespace |
| `kubectl bmc events NAME [--limit N] [--grep TEXT]` | Newest System Event Log entries | as `sensors` |
| `kubectl bmc power NAME ACTION --reason TEXT [--yes] [--wait]` | Request a power action | `create bmcactions` |
| `kubectl bmc actions [NAME]` | Power actions, newest first | `list bmcactions` |

The `kube-bmc-viewer` and `kube-bmc-operator` roles, bound through the chart's `rbac` values,
grant these permissions (see [authentication.md](authentication.md)).

Global flags include the standard kubeconfig flags (`--context`, `--kubeconfig`, ...) and
`--kube-bmc-namespace` (default `kube-bmc-system`).

## Power actions

```console
$ kubectl bmc power gpu-01 ForceRestart --reason "kernel hang" --wait
ForceRestart will be sent to the BMC of gpu-01 (Supermicro SYS-821GE-TNHR, power On).
Every workload on the node is interrupted. Type the server name to confirm: gpu-01
bmcaction/gpu-01-forcerestart-7xk2p created
Succeeded: ForceRestart accepted by the BMC
```

The plugin sets `spec.requestedBy` to your username, as reported by the SelfSubjectReview API.
The admission policy installed by the chart rejects any other value. `--yes` skips the
confirmation prompt for use in scripts.
