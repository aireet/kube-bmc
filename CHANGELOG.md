# Changelog

All notable changes to this project are documented in this file. The format follows
[Keep a Changelog](https://keepachangelog.com/en/1.1.0/).

## [0.1.1] - 2026-10-05

### Fixed
- kubectl-bmc release archives contained a `.` entry, so extracting them in a directory not
  owned by the user (for example `/tmp`) failed. The archives now contain only `kubectl-bmc`
  and `LICENSE`.

## [0.1.0] - 2026-10-05

### Added
- `BMC` resource (`bmc.kube-bmc.io/v1alpha1`), owned by its Node, with inventory, health,
  problems, sensor summary and SEL usage.
- Node agent: in-band IPMI collection of inventory, network, sensors, thresholds, chassis status,
  DCMI power and SEL; Prometheus metrics.
- `BMCAction` resource for power actions, executed by the server over Redfish or IPMI-over-LAN,
  with a TTL for finished actions and Node events. Disabled by default.
- Dashboard (Vue 3, Naive UI): fleet overview, server details, sensors, event log, action history,
  English and Chinese, light and dark themes.
- Authentication modes `none`, `password` (bcrypt htpasswd list in a Secret, with brute-force
  lockout and a `kube-bmc hash-password` helper) and `oidc` (authorization code flow with PKCE);
  bearer tokens for API and MCP clients (OIDC ID tokens and tokens of ServiceAccounts in the
  kube-bmc namespace).
- MCP endpoint (`/mcp`, Streamable HTTP) with read tools, a power action tool and a diagnosis prompt.
- `kubectl bmc` plugin: list, describe, sensors, events, power and actions.
- Helm chart and plain install manifest.
- Agent heartbeat Lease and deadband status writes: the status is written on meaningful changes,
  problems are debounced, and readings are refreshed every 10 minutes.
- Decoding of sensor-specific SEL events that ipmitool leaves undescribed, and a security
  problem for repeated failed BMC logins.
- Fallback to a raw IPMI request for the LAN gateway when `lan print` stops early.
