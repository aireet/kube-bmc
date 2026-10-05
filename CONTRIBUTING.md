# Contributing to kube-bmc

Thanks for helping! Bug reports, hardware fixtures, docs and code are all welcome.

## Getting started

Prerequisites: Go 1.26+, Node.js 22+, and optionally Docker, Helm and a cluster with real BMCs.

```bash
git clone https://github.com/aireet/kube-bmc && cd kube-bmc
make demo    # dashboard with a synthetic fleet on http://localhost:8080
make test
make lint
```

The UI lives in `ui/` (Vue 3 + Naive UI + TypeScript). `make dev` runs Vite with hot reload against a demo backend.

## Hardware fixtures

The parsers in `internal/ipmi` are tested against real `ipmitool` output in `internal/ipmi/testdata`. If kube-bmc misreads your hardware, the most useful thing you can send is the raw output of:

```bash
ipmitool mc info; ipmitool lan print 1; ipmitool fru print 0; ipmitool chassis status
ipmitool sdr elist; ipmitool sensor; ipmitool sel info; ipmitool sel elist last 30; ipmitool dcmi power reading
```

**Please remove serial numbers, MAC and IP addresses** before attaching it to an issue.

## Pull requests

- Keep changes focused; one topic per PR.
- Add or update tests for behavior changes. `make test lint` must pass.
- After changing `api/`, run `make generate` and commit the result.
- Use clear commit messages (`area: what changed`), e.g. `ipmi: parse Dell lan channel 8`.
- The agent must stay read-only towards the BMC. Anything that changes BMC state belongs in the server, behind a flag that is off by default.

## Reporting bugs

Please include the kube-bmc version, BMC vendor/model/firmware, `kubectl get bmc <name> -o yaml`, and agent logs (`kubectl -n kube-bmc-system logs ds/kube-bmc-agent`).

By contributing you agree that your contributions are licensed under the Apache License 2.0.
