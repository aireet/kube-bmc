# Changelog

All notable changes to this project are documented in this file. The format follows
[Keep a Changelog](https://keepachangelog.com/en/1.1.0/).

## [0.4.0] - 2026-10-06

### Added
- Hardware inventory from the host: CPU model, sockets, cores and threads; memory size, modules
  and slots; GPUs by model; PCIe slots with width, generation, occupancy and the installed device.
  Read by the agent with dmidecode, lspci and sysfs, shown in the dashboard, `kubectl bmc describe`,
  `status.hardware` and the metrics `kube_bmc_hardware_info`, `kube_bmc_gpus` and
  `kube_bmc_pcie_slots`. `kube-bmc inventory` prints the inventory of the local host.
- `kube_bmc_inlet_temperature_celsius` metric.
- Grafana dashboard (`charts/kube-bmc/dashboards/kube-bmc.json`, optional ConfigMap for the
  dashboard sidecar with `grafana.dashboard.enabled`).
- Dashboard dialog "AI agents" with the MCP endpoint and client configuration.

### Fixed
- `kubectl bmc sel-archives` shows and prints archives created by v0.3.0, which are not compressed.

## [0.3.1] - 2026-10-06

### Added
- `kubectl bmc sel-archives NAME [--show ARCHIVE]` lists and prints saved System Event Logs.

### Changed
- SEL archives are always gzip-compressed and kept per server up to
  `server.actions.selArchivesPerServer` (default 3, agent flag `--sel-archives`); older archives
  are deleted. Archives no longer expire with the action TTL.

### Fixed
- After ClearSEL, the problems derived from the log (SEL full, failed BMC logins) are removed
  immediately and the log is read again at once, instead of after the 5-minute clear delay and
  the SEL read interval.

## [0.3.0] - 2026-10-05

### Added
- `IdentifyOn` and `IdentifyOff` actions turn the chassis identify light on until it is turned
  off (255 seconds on BMCs without indefinite identify). Dashboard "Locate" menu,
  `kubectl bmc locate`, MCP tool `locate_server`.
- `ClearSEL` action saves the complete System Event Log to a ConfigMap owned by the action
  (`status.selArchive`) and clears the log only after the archive was stored. Dashboard button in
  the event log, `kubectl bmc clear-sel`, MCP tool `clear_sel`.
- `POST /api/v1/bmcs/{name}/actions`; the `/power` path remains as an alias.

### Changed
- `server.actions.enabled` and `--enable-actions` replace `server.powerActions.enabled` and
  `--enable-power-actions`, which are still accepted.

## [0.2.0] - 2026-10-05

### Added
- In-band power control: the agent of the target node executes `GracefulShutdown`, `ForceOff`,
  `ForceRestart` and `PowerCycle` through `/dev/ipmi0`, without BMC credentials or network access
  to the BMC. The server executes `On` and `GracefulRestart` out-of-band, and in-band actions that
  no agent claimed within 30 seconds.

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
