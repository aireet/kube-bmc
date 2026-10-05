#!/usr/bin/env bash
# End-to-end test in a kind cluster.
#
# kind nodes have no BMC, so agents report collection errors; the test covers everything
# that does not need hardware: installation, agent registration and heartbeat, the
# server API, MCP, the kubectl plugin and the BMCAction execution policy.
#
# Usage: test/e2e/run.sh            (creates and deletes the cluster "kube-bmc-e2e")
#        KEEP_CLUSTER=1 test/e2e/run.sh
set -euo pipefail

CLUSTER=${CLUSTER:-kube-bmc-e2e}
IMAGE=kube-bmc:e2e
NS=kube-bmc-system
ROOT=$(cd "$(dirname "$0")/../.." && pwd)
WORK=$(mktemp -d)
PF_PID=

log() { printf '\n==> %s\n' "$*"; }
fail() { printf 'FAIL: %s\n' "$*" >&2; exit 1; }

cleanup() {
  [ -n "$PF_PID" ] && kill "$PF_PID" 2>/dev/null || true
  if [ "${KEEP_CLUSTER:-}" != 1 ]; then kind delete cluster --name "$CLUSTER" >/dev/null 2>&1 || true; fi
  rm -rf "$WORK"
}
trap cleanup EXIT

# eventually retries a command for up to $1 seconds.
eventually() {
  local timeout=$1; shift
  local deadline=$((SECONDS + timeout))
  until "$@"; do
    [ $SECONDS -ge $deadline ] && return 1
    sleep 2
  done
}

log "Creating kind cluster $CLUSTER"
kind create cluster --name "$CLUSTER" --wait 120s
NODE=$(kubectl get nodes -o jsonpath='{.items[0].metadata.name}')

log "Building and loading $IMAGE"
docker build -q --build-arg VERSION=e2e -t "$IMAGE" "$ROOT" >/dev/null
kind load docker-image "$IMAGE" --name "$CLUSTER"
(cd "$ROOT" && CGO_ENABLED=0 go build -o "$WORK/kubectl-bmc" ./cmd/kubectl-bmc)
export PATH="$WORK:$PATH"

log "Installing the chart"
helm install kube-bmc "$ROOT/charts/kube-bmc" --namespace "$NS" --create-namespace --wait --timeout 3m \
  --set image.repository=kube-bmc --set image.tag=e2e --set image.pullPolicy=Never \
  --set agent.interval=10s --set agent.leaseDuration=30s

log "Agent registers the BMC object and renews its Lease"
eventually 120 kubectl get bmc "$NODE" >/dev/null 2>&1 || fail "BMC $NODE was not created"
[ "$(kubectl get bmc "$NODE" -o jsonpath='{.metadata.ownerReferences[0].kind}')" = Node ] || fail "BMC is not owned by its Node"
eventually 60 kubectl -n "$NS" get lease "$NODE" >/dev/null 2>&1 || fail "heartbeat Lease was not created"
reason=$(kubectl get bmc "$NODE" -o jsonpath='{.status.conditions[?(@.type=="Ready")].reason}')
[ "$reason" = CollectionFailed ] || fail "expected Ready reason CollectionFailed without a BMC, got '$reason'"

log "Server API and MCP endpoint"
kubectl -n "$NS" port-forward svc/kube-bmc 18080:80 >/dev/null 2>&1 &
PF_PID=$!
eventually 30 curl -fsS http://127.0.0.1:18080/healthz >/dev/null || fail "server is not reachable"
curl -fsS http://127.0.0.1:18080/api/v1/bmcs | grep -q "\"name\":\"$NODE\"" || fail "API does not list $NODE"
curl -fsS http://127.0.0.1:18080/api/v1/bmcs | grep -q '"stale":false' || fail "agent heartbeat not recognized"
mcp() {
  curl -fsS http://127.0.0.1:18080/mcp -H 'Content-Type: application/json' \
    -H 'Accept: application/json, text/event-stream' -H 'MCP-Protocol-Version: 2025-06-18' -d "$1"
}
mcp '{"jsonrpc":"2.0","id":1,"method":"tools/list"}' | grep -q '"power_action"' || fail "MCP tools/list"
mcp '{"jsonrpc":"2.0","id":2,"method":"tools/call","params":{"name":"fleet_summary","arguments":{}}}' \
  | grep -q '"servers":1' || fail "MCP fleet_summary"

log "kubectl plugin"
kubectl bmc list | grep -q "$NODE" || fail "kubectl bmc list"
kubectl bmc describe "$NODE" | grep -q "Condition Ready" || fail "kubectl bmc describe"

log "Power actions are rejected while disabled"
kubectl bmc power "$NODE" On --reason "e2e" --yes --wait --timeout 60s >"$WORK/power.out" 2>&1 && fail "power action succeeded"
grep -q "Rejected: power actions are disabled" "$WORK/power.out" || { cat "$WORK/power.out"; fail "power action was not rejected"; }

log "Power actions record the requester"
[ "$(kubectl get bmcactions -o jsonpath='{.items[0].spec.requestedBy}')" = "$(kubectl auth whoami -o jsonpath='{.status.userInfo.username}')" ] \
  || fail "requestedBy is not the current user"

log "BMC objects are owned by their Node"
kubectl get bmc "$NODE" -o jsonpath='{.metadata.ownerReferences[0].uid}' | grep -q "$(kubectl get node "$NODE" -o jsonpath='{.metadata.uid}')" \
  || fail "owner reference does not point to the Node"

printf '\nPASS\n'
