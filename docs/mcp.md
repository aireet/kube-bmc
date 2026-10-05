# MCP endpoint

The kube-bmc server implements the [Model Context Protocol](https://modelcontextprotocol.io) over
Streamable HTTP at `/mcp`. AI agents use it to inspect hardware and, when permitted, to request
power actions.

The endpoint is stateless, so it can be served by several replicas without session affinity.

## Tools

| Tool | Description |
|---|---|
| `fleet_summary` | Server count by health, power usage, stale agents and all servers with problems |
| `list_servers` | Servers with health, power, inlet temperature, model and BMC address; filter by `health` or `query` |
| `get_server` | Full record of one server: inventory, firmware, network, chassis, problems, Kubernetes node |
| `get_sensors` | Live sensor readings with thresholds; filter by `type` or `problemsOnly` |
| `get_events` | Newest System Event Log entries; `limit` and `query` |
| `list_actions` | Recent power actions, optionally for one server |
| `get_action` | Status of one power action |
| `power_action` | Request a power action; `name`, `action` and `reason` are required |
| `locate_server` | Turn the identify light on or off; `name` and `on` |
| `clear_sel` | Save the System Event Log to a ConfigMap and clear it; `name` and `reason` are required |

Read-only tools carry `readOnlyHint`. `power_action` and `clear_sel` carry `destructiveHint`, so
clients ask for confirmation before calling them. The server records the caller's identity in `requestedBy` and
prefixes the reason with `[mcp]`. The action is executed asynchronously; poll `get_action`.

The `diagnose_server` prompt guides an agent through a hardware investigation of one server.

## Authentication

With `auth.mode=none` the endpoint is open. With `password` or `oidc` every request needs a bearer
token: the token of a ServiceAccount in the kube-bmc namespace, or in `oidc` mode an ID token from
the configured issuer (see [authentication.md](authentication.md)). Unauthenticated requests receive `401` with a
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

A ServiceAccount token for an agent:

```bash
kubectl -n kube-bmc-system create serviceaccount mcp-agent
export KUBE_BMC_TOKEN=$(kubectl -n kube-bmc-system create token mcp-agent --duration=24h)
```

Every authenticated client can call `power_action` and `clear_sel`. Keep `server.actions.enabled`
off unless actions are required, and review agent requests in the action history.
