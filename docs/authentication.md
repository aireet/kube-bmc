# Authentication

kube-bmc is intended for the infrastructure team that operates the cluster. It authenticates
callers and records who requested each power action, but it does not distinguish roles: every
authenticated user has full access. Control who can sign in at the identity provider.

## Modes

| `auth.mode` | Dashboard | API and MCP clients |
|---|---|---|
| `none` (default) | No sign-in | No credentials |
| `oidc` | OpenID Connect sign-in | OIDC ID tokens, or tokens of ServiceAccounts in the kube-bmc namespace |

`none` is suitable only when the dashboard is reached through `kubectl port-forward` or another
authenticated path. The kubectl plugin always uses the caller's kubeconfig.

## Identities

| Source | Recorded username |
|---|---|
| Browser session (OIDC) | `<usernamePrefix><usernameClaim>`, e.g. `oidc:alice@example.com` |
| OIDC bearer token | Same as above |
| ServiceAccount token | `system:serviceaccount:kube-bmc-system:<name>` |
| kubectl-bmc | The Kubernetes username from the SelfSubjectReview API |

OIDC bearer tokens must be ID tokens issued by the configured issuer, with the client ID or one of
`auth.oidc.extraAudiences` as audience. ServiceAccount tokens are verified with the TokenReview
API; only ServiceAccounts in the kube-bmc namespace are accepted, so tokens of other workloads
in the cluster do not grant access.

## Configuring OIDC

1. Register a confidential client with your provider. Use the redirect URI
   `<externalURL>/auth/callback` and allow the scopes `openid profile email`. The authorization
   code flow uses PKCE.
2. Create a Secret with the client secret and a random session secret of at least 32 bytes:

   ```bash
   kubectl -n kube-bmc-system create secret generic kube-bmc-oidc \
     --from-literal=client-secret='<client secret>' \
     --from-literal=session-secret="$(openssl rand -base64 48)"
   ```

3. Configure the chart:

   ```yaml
   server:
     externalURL: https://kube-bmc.example.com
   auth:
     mode: oidc
     oidc:
       issuerURL: https://login.example.com
       clientID: kube-bmc
       existingSecret: kube-bmc-oidc
   ```

Sessions are stored in an encrypted, HTTP-only cookie (AES-256-GCM) and expire after
`auth.sessionTTL`. The cookie is marked `Secure` when `externalURL` uses HTTPS. All replicas must
share the same session secret.

| Provider | Notes |
|---|---|
| Keycloak | Assign the client to the operations team, or require a client role at sign-in. |
| Dex | Restrict sign-in with the connector's organization or group filters. |
| Microsoft Entra ID | Set "Assignment required" on the enterprise application; use `usernameClaim: preferred_username`. |
| Google | Use an internal OAuth consent screen to limit sign-in to your organization. |

## Tokens for automation

Automation such as an AI agent can use a ServiceAccount token:

```bash
kubectl -n kube-bmc-system create serviceaccount mcp-agent
kubectl -n kube-bmc-system create token mcp-agent --duration=24h
```

The ServiceAccount needs no RBAC permissions; the token only proves its identity to kube-bmc.
Delete the ServiceAccount to revoke access.
