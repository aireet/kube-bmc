# Security Policy

kube-bmc runs a privileged DaemonSet and can, when explicitly enabled, power servers on and off. We take security reports seriously.

## Reporting a vulnerability

Please **do not open a public issue**. Report it privately through
[GitHub Security Advisories](https://github.com/aireet/kube-bmc/security/advisories/new).
We will acknowledge within 3 business days and keep you updated until a fix is released.

## Verifying releases

Container images are signed with [Sigstore](https://www.sigstore.dev/) keyless signing and carry
an SBOM and SLSA provenance attestation:

```bash
cosign verify ghcr.io/aireet/kube-bmc:v0.1.0 \
  --certificate-identity-regexp 'https://github.com/aireet/kube-bmc/.github/workflows/release.yml@refs/tags/v.*' \
  --certificate-oidc-issuer https://token.actions.githubusercontent.com
```

Release artifacts (`install.yaml`, `kubectl-bmc_checksums.txt`) come with Sigstore bundles
(`*.sigstore.json`), which can be checked with `cosign verify-blob --bundle`.

## Supported versions

Security fixes are released for the latest minor version.

## Hardening checklist

- Leave `server.powerActions.enabled=false` unless you need it; when enabled, put the dashboard behind an authenticating proxy.
- Expose the dashboard through an authenticated Ingress rather than a NodePort or LoadBalancer.
- Isolate BMC management networks; prefer Redfish over IPMI-over-LAN.
- Use a dedicated, least-privileged BMC account (Operator role is enough for power control).
