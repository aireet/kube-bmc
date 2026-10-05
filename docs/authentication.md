# Authentication and authorization

kube-bmc separates authentication (who is calling) from authorization (what they may do).
Authorization always uses Kubernetes RBAC, so the dashboard, the MCP endpoint and the kubectl
plugin follow the same rules.

## Modes

| `auth.mode` | Dashboard | API and MCP | Authorization |
|---|---|---|---|
| `none` (default) | No sign-in | No credentials | Every request is allowed |
| `oidc` | OpenID Connect sign-in | OIDC or Kubernetes bearer tokens | Kubernetes RBAC |

The kubectl plugin always uses the caller's kubeconfig and is authorized by the API server.

## Identities

| Source | Username | Groups |
|---|---|---|
| Browser session (OIDC) | `<usernamePrefix><usernameClaim>`, e.g. `oidc:alice@example.com` | `<groupsPrefix><group>` for each value of `groupsClaim` |
| OIDC bearer token | Same as above | Same as above |
| Kubernetes bearer token | Result of a TokenReview, e.g. `system:serviceaccount:ops:agent` | Result of the TokenReview |

OIDC bearer tokens must be ID tokens issued by the configured issuer, with the client ID or one of
`auth.oidc.extraAudiences` as audience. Tokens from other issuers are sent to the TokenReview API
when `auth.kubernetesTokens` is enabled.

If the API server itself is configured with the same OIDC provider and prefixes, users have the
same identity in kubectl and in kube-bmc.

## Permissions

Each request is checked with a SubjectAccessReview. Results are cached for 30 seconds.

| Permission | Required RBAC | Granted by |
|---|---|---|
| Read BMCs, live data and actions | `list bmcs.bmc.kube-bmc.io` | `kube-bmc-viewer`, `kube-bmc-operator`, `view` |
| Request power actions | `create bmcactions.bmc.kube-bmc.io` | `kube-bmc-operator` |

Bind the roles with chart values or plain RBAC:

```yaml
rbac:
  viewers:
    - { kind: Group, name: "oidc:engineering", apiGroup: rbac.authorization.k8s.io }
  operators:
    - { kind: Group, name: "oidc:sre", apiGroup: rbac.authorization.k8s.io }
    - { kind: ServiceAccount, name: ops-agent, namespace: ops }
```

The chart also binds the namespaced `kube-bmc-agent-proxy` role to these subjects. It allows
`kubectl bmc sensors` and `kubectl bmc events` to read agent data through the pod proxy.

## Configuring OIDC

1. Register a confidential client with your provider. Use the redirect URI
   `<externalURL>/auth/callback` and request the scopes `openid profile email groups`. The
   authorization code flow uses PKCE.
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
       usernameClaim: email
       groupsClaim: groups
   rbac:
     operators:
       - { kind: Group, name: "oidc:sre", apiGroup: rbac.authorization.k8s.io }
   ```

Sessions are stored in an encrypted, HTTP-only cookie (AES-256-GCM) and expire after
`auth.sessionTTL`. The cookie is marked `Secure` when `externalURL` uses HTTPS. All replicas must
share the same session secret.

Provider notes:

| Provider | Notes |
|---|---|
| Keycloak | Add a "Group Membership" mapper with the claim name `groups` and "Full group path" disabled. |
| Dex | Groups are provided by connectors such as LDAP or GitHub. |
| Microsoft Entra ID | Use `usernameClaim: preferred_username`; groups are object IDs unless configured otherwise. |
| Google | Google does not issue a groups claim; bind roles to users. |

## Service accounts for automation

Automation such as an AI agent can authenticate with a ServiceAccount token:

```bash
kubectl -n kube-bmc-system create serviceaccount mcp-agent
kubectl create clusterrolebinding mcp-agent-kube-bmc-viewer \
  --clusterrole=kube-bmc-viewer --serviceaccount=kube-bmc-system:mcp-agent
kubectl -n kube-bmc-system create token mcp-agent --duration=24h
```

## Requested-by enforcement

`BMCAction.spec.requestedBy` records who requested an action. A ValidatingAdmissionPolicy
rejects objects whose `requestedBy` differs from the authenticated user, except when created by
the kube-bmc server, which sets it to the authenticated dashboard or MCP user.
