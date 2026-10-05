# Changelog

All notable changes to this project are documented here. The format follows [Keep a Changelog](https://keepachangelog.com/en/1.1.0/).

## [Unreleased]

### Added
- `BMC` custom resource (`bmc.kube-bmc.io/v1alpha1`) with printer columns, owned by its Node.
- Node agent (DaemonSet): in-band IPMI collection of inventory, network, sensors, thresholds, chassis status, DCMI power and SEL; health evaluation with human-readable problems; Prometheus metrics.
- Server: Vue 3 + Naive UI dashboard (fleet overview, server detail, sensors, event log, light/dark, English/中文) and JSON API.
- Out-of-band power actions over Redfish and IPMI-over-LAN, disabled by default, confirmed by name and audited as Node events.
- Demo mode (`kube-bmc server --demo`).
- Helm chart and plain install manifest.
