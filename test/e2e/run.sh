#!/usr/bin/env bash
# End-to-end test in a kind cluster.
#
# kind nodes have no BMC, so agents report collection errors; the test covers everything
# that does not need hardware: installation, agent registration and heartbeat,
# password sign-in, ServiceAccount tokens, the server API, MCP, the kubectl plugin and
# the BMCAction execution policy.
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

log "Installing the chart with password authentication"
USERS=$(printf 'e2e-password' | docker run --rm -i --entrypoint kube-bmc "$IMAGE" hash-password admin)
helm install kube-bmc "$ROOT/charts/kube-bmc" --namespace "$NS" --create-namespace --wait --timeout 3m \
  --set image.repository=kube-bmc --set image.tag=e2e --set image.pullPolicy=Never \
  --set agent.interval=10s --set agent.leaseDuration=30s --set server.actions.enabled=true \
  --set auth.mode=password --set auth.sessionSecret="$(head -c 48 /dev/urandom | base64)" \
  --set-string "auth.password.users[0]=$USERS"

log "Agent registers the BMC object and renews its Lease"
eventually 120 kubectl get bmc "$NODE" >/dev/null 2>&1 || fail "BMC $NODE was not created"
[ "$(kubectl get bmc "$NODE" -o jsonpath='{.metadata.ownerReferences[0].kind}')" = Node ] || fail "BMC is not owned by its Node"
eventually 60 kubectl -n "$NS" get lease "$NODE" >/dev/null 2>&1 || fail "heartbeat Lease was not created"
reason=$(kubectl get bmc "$NODE" -o jsonpath='{.status.conditions[?(@.type=="Ready")].reason}')
[ "$reason" = CollectionFailed ] || fail "expected Ready reason CollectionFailed without a BMC, got '$reason'"

log "Password sign-in"
kubectl -n "$NS" port-forward svc/kube-bmc 18080:80 >/dev/null 2>&1 &
PF_PID=$!
API=http://127.0.0.1:18080
eventually 30 curl -fsS $API/healthz >/dev/null || fail "server is not reachable"
[ "$(curl -s -o /dev/null -w '%{http_code}' $API/api/v1/bmcs)" = 401 ] || fail "API is reachable without authentication"
login() {
  curl -s -o /dev/null -w '%{http_code}' -c "$WORK/cookies" $API/auth/login \
    -H 'Content-Type: application/json' -d "{\"username\":\"admin\",\"password\":\"$1\"}"
}
[ "$(login wrong-password)" = 401 ] || fail "wrong password accepted"
[ "$(login e2e-password)" = 200 ] || fail "sign-in failed"
curl -fsS -b "$WORK/cookies" $API/api/v1/bmcs | grep -q "\"name\":\"$NODE\"" || fail "API does not list $NODE"
curl -fsS -b "$WORK/cookies" $API/api/v1/bmcs | grep -q '"stale":false' || fail "agent heartbeat not recognized"
curl -fsS -b "$WORK/cookies" $API/api/v1/me | grep -q '"username":"admin"' || fail "session identity"

log "MCP with a ServiceAccount token"
kubectl -n "$NS" create serviceaccount e2e-agent >/dev/null
TOKEN=$(kubectl -n "$NS" create token e2e-agent)
OTHER=$(kubectl -n default create token default)
mcp() {
  curl -fsS $API/mcp -H "Authorization: Bearer $1" -H 'Content-Type: application/json' \
    -H 'Accept: application/json, text/event-stream' -H 'MCP-Protocol-Version: 2025-06-18' -d "$2"
}
mcp "$TOKEN" '{"jsonrpc":"2.0","id":1,"method":"tools/list"}' | grep -q '"power_action"' || fail "MCP tools/list"
mcp "$TOKEN" '{"jsonrpc":"2.0","id":2,"method":"tools/call","params":{"name":"fleet_summary","arguments":{}}}' \
  | grep -q '"servers":1' || fail "MCP fleet_summary"
mcp "$OTHER" '{"jsonrpc":"2.0","id":3,"method":"tools/list"}' >/dev/null 2>&1 && fail "token from another namespace accepted"

log "kubectl plugin"
kubectl bmc list | grep -q "$NODE" || fail "kubectl bmc list"
kubectl bmc describe "$NODE" | grep -q "Condition Ready" || fail "kubectl bmc describe"

log "In-band power actions are executed by the node agent"
# kind nodes have no /dev/ipmi0, so the agent claims the action and reports the ipmitool failure.
kubectl bmc power "$NODE" ForceRestart --reason "e2e" --yes --wait --timeout 90s >"$WORK/power.out" 2>&1 && fail "power action succeeded without a BMC"
grep -q "Failed: ipmitool chassis power reset" "$WORK/power.out" || { cat "$WORK/power.out"; fail "agent did not execute the action"; }

log "Identify light and ClearSEL are executed by the node agent"
kubectl bmc locate "$NODE" --timeout 90s >"$WORK/locate.out" 2>&1 && fail "identify succeeded without a BMC"
grep -q "Failed: .*chassis identify" "$WORK/locate.out" || { cat "$WORK/locate.out"; fail "agent did not execute IdentifyOn"; }
kubectl bmc clear-sel "$NODE" --reason e2e --yes --timeout 90s >"$WORK/sel.out" 2>&1 && fail "ClearSEL succeeded without a BMC"
grep -q "it was not cleared" "$WORK/sel.out" || { cat "$WORK/sel.out"; fail "ClearSEL did not stop when the log could not be read"; }
[ "$(kubectl -n "$NS" get configmaps -l bmc.kube-bmc.io/sel-archive -o name | wc -l)" = 0 ] || fail "archive created although the log could not be read"

log "Power on requires out-of-band access"
kubectl bmc power "$NODE" On --reason "e2e" --yes --wait --timeout 60s >"$WORK/on.out" 2>&1 && fail "power on succeeded"
grep -q "Rejected: On requires out-of-band access" "$WORK/on.out" || { cat "$WORK/on.out"; fail "power on was not rejected"; }

log "Power actions record the requester"
[ "$(kubectl get bmcactions -o jsonpath='{.items[0].spec.requestedBy}')" = "$(kubectl auth whoami -o jsonpath='{.status.userInfo.username}')" ] \
  || fail "requestedBy is not the current user"

log "BMC objects are owned by their Node"
kubectl get bmc "$NODE" -o jsonpath='{.metadata.ownerReferences[0].uid}' | grep -q "$(kubectl get node "$NODE" -o jsonpath='{.metadata.uid}')" \
  || fail "owner reference does not point to the Node"

printf '\nPASS\n'
