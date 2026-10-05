# Changelog

All notable changes to this project are documented in this file. The format follows
[Keep a Changelog](https://keepachangelog.com/en/1.1.0/).

## [Unreleased]

### Added
- `BMC` resource (`bmc.kube-bmc.io/v1alpha1`), owned by its Node, with inventory, health,
  problems, sensor summary and SEL usage.
- Node agent: in-band IPMI collection of inventory, network, sensors, thresholds, chassis status,
  DCMI power and SEL; Prometheus metrics.
- `BMCAction` resource for power actions, executed by the server over Redfish or IPMI-over-LAN,
  with a TTL for finished actions and Node events. Disabled by default.
- Dashboard (Vue 3, Naive UI): fleet overview, server details, sensors, event log, action history,
  English and Chinese, light and dark themes.
- Authentication with OpenID Connect (authorization code flow with PKCE) and bearer tokens
  (OIDC ID tokens and Kubernetes tokens); authorization with Kubernetes RBAC through
  SubjectAccessReviews.
- MCP endpoint (`/mcp`, Streamable HTTP) with read tools, a power action tool and a diagnosis prompt.
- `kubectl bmc` plugin: list, describe, sensors, events, power and actions.
- Helm chart with `kube-bmc-viewer` and `kube-bmc-operator` roles and a ValidatingAdmissionPolicy
  that enforces `BMCAction.spec.requestedBy`.
