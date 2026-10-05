# MCP endpoint

The kube-bmc server implements the [Model Context Protocol](https://modelcontextprotocol.io) over
Streamable HTTP at `/mcp`. AI agents use it to inspect hardware and, when permitted, to request
power actions.

The endpoint is stateless, so it can be served by several replicas without session affinity.

## Tools

| Tool | Permission | Description |
|---|---|---|
| `fleet_summary` | read | Server count by health, power usage, stale agents and all servers with problems |
| `list_servers` | read | Servers with health, power, inlet temperature, model and BMC address; filter by `health` or `query` |
| `get_server` | read | Full record of one server: inventory, firmware, network, chassis, problems, Kubernetes node |
| `get_sensors` | read | Live sensor readings with thresholds; filter by `type` or `problemsOnly` |
| `get_events` | read | Newest System Event Log entries; `limit` and `query` |
| `list_actions` | read | Recent power actions, optionally for one server |
| `get_action` | read | Status of one power action |
| `power_action` | operate | Request a power action; `name`, `action` and `reason` are required |

Read-only tools carry `readOnlyHint`. `power_action` carries `destructiveHint`, so clients ask for
confirmation before calling it. The server records the caller's identity in `requestedBy` and
prefixes the reason with `[mcp]`. The action is executed asynchronously; poll `get_action`.

The `diagnose_server` prompt guides an agent through a hardware investigation of one server.

## Authentication

With `auth.mode=none` the endpoint is open. With `auth.mode=oidc` every request needs a bearer
token: an OIDC ID token from the configured issuer, or a Kubernetes token (see
[authentication.md](authentication.md)). Unauthenticated requests receive `401` with a
`WWW-Authenticate` header that points to the OAuth protected resource metadata at
`/.well-known/oauth-protected-resource` (RFC 9728).

## Clients

Claude Code:

```bash
claude mcp add --transport http kube-bmc https://kube-bmc.example.com/mcp \
  --header "Authorization: Bearer $TOKEN"
```

Generic configuration (`mcp.json`):

```json
{
  "mcpServers": {
    "kube-bmc": {
      "type": "http",
      "url": "https://kube-bmc.example.com/mcp",
      "headers": { "Authorization": "Bearer ${KUBE_BMC_TOKEN}" }
    }
  }
}
```

A ServiceAccount token bound to `kube-bmc-viewer` gives an agent read-only access:

```bash
kubectl -n kube-bmc-system create serviceaccount mcp-agent
kubectl create clusterrolebinding mcp-agent-kube-bmc-viewer \
  --clusterrole=kube-bmc-viewer --serviceaccount=kube-bmc-system:mcp-agent
export KUBE_BMC_TOKEN=$(kubectl -n kube-bmc-system create token mcp-agent --duration=24h)
```

Bind `kube-bmc-operator` only to agents that are allowed to power servers on and off, and keep
`server.powerActions.enabled` off unless power control is required.
