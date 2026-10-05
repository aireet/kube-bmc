# Authentication

kube-bmc is intended for the team that operates the cluster. It authenticates callers and
records who requested each power action, but it does not distinguish roles: every authenticated
user has full access. Control who can sign in through the user list (password mode) or at the
identity provider (OIDC mode).

- [Choosing a mode](#choosing-a-mode)
- [Password mode](#password-mode)
- [OIDC mode](#oidc-mode)
  - [How it works](#how-it-works)
  - [Step by step](#step-by-step)
  - [Provider guides](#provider-guides): Keycloak, Dex, Microsoft Entra ID, Okta, Google
  - [Troubleshooting](#troubleshooting)
- [Tokens for API and MCP clients](#tokens-for-api-and-mcp-clients)
- [Sessions](#sessions)

## Choosing a mode

| `auth.mode` | Dashboard sign-in | API and MCP clients | Use when |
|---|---|---|---|
| `none` (default) | None | No credentials | The dashboard is only reached through `kubectl port-forward` or an authenticating proxy |
| `password` | Username and password from an htpasswd list | ServiceAccount tokens | No identity provider is available |
| `oidc` | OpenID Connect provider | OIDC ID tokens, ServiceAccount tokens | The organization has single sign-on |

The kubectl plugin does not use these modes; it always uses the caller's kubeconfig.

## Password mode

Users are stored as bcrypt hashes in the `htpasswd` key of a Secret, one `username:hash` per
line. The Secret is read on every sign-in, so users can be added or removed without restarting
kube-bmc.

**1. Create password hashes.** The kube-bmc image includes a helper that reads the password
from standard input:

```bash
printf '%s' 'a long password' | docker run --rm -i --entrypoint kube-bmc ghcr.io/aireet/kube-bmc:v0.1.1 hash-password alice
# alice:$2a$12$...
```

`htpasswd -nbB alice 'a long password'` from Apache httpd produces the same format. Only bcrypt
hashes are accepted.

**2. Create the Secret** with the users and a random session secret:

```bash
kubectl -n kube-bmc-system create secret generic kube-bmc-auth \
  --from-literal=session-secret="$(openssl rand -base64 48)" \
  --from-file=htpasswd=./users.htpasswd
```

**3. Configure the chart:**

```yaml
auth:
  mode: password
  existingSecret: kube-bmc-auth
```

Alternatively, let the chart create the Secret by setting `auth.sessionSecret` and
`auth.password.users` (a list of htpasswd lines) instead of `auth.existingSecret`.

**Managing users:** edit the `htpasswd` key of the Secret.

```bash
kubectl -n kube-bmc-system edit secret kube-bmc-auth
```

Removing a user prevents new sign-ins; existing sessions remain valid until they expire
(`auth.sessionTTL`). To end all sessions immediately, change `session-secret` and restart the
server.

**Brute-force protection:** after five failed attempts for a username or from a client address,
further attempts are refused for 30 seconds, doubling with every further failure up to 15
minutes. A successful sign-in resets the counter. Behind a load balancer or ingress, the client
address is that of the proxy.

## OIDC mode

### How it works

kube-bmc is an OpenID Connect relying party. It never sees user passwords:

1. An unauthenticated browser is redirected to the provider's sign-in page.
2. After sign-in, the provider redirects back to `<externalURL>/auth/callback` with a one-time
   code.
3. kube-bmc exchanges the code for an ID token (authorization code flow with PKCE), verifies its
   signature, issuer, audience and nonce, and stores the identity in an encrypted session cookie.

kube-bmc discovers the provider's endpoints and signing keys from
`<issuerURL>/.well-known/openid-configuration`. Three values must match between the provider and
kube-bmc:

| Value | Provider side | kube-bmc side |
|---|---|---|
| Issuer | The provider's issuer URL | `auth.oidc.issuerURL` (exactly, including any path) |
| Client | Client ID and secret registered for kube-bmc | `auth.oidc.clientID`, `client-secret` in the Secret |
| Redirect URI | Allowed redirect URI of the client | `server.externalURL` + `/auth/callback` |

The kube-bmc server must reach the issuer URL over the network, and browsers must reach both the
issuer and `server.externalURL`.

### Step by step

1. **Choose the public URL** of the dashboard, for example `https://kube-bmc.example.com`, and
   expose the `kube-bmc` Service there (Ingress, LoadBalancer or NodePort). Use HTTPS outside of
   test environments.
2. **Register a client** at the provider:
   - Type: confidential (with a client secret), authorization code flow.
   - Redirect URI: `https://kube-bmc.example.com/auth/callback`.
   - Scopes: `openid`, `profile`, `email`.
   - Restrict which users may use the client (see the provider guides).
3. **Create the Secret** with the client secret and a random session secret:

   ```bash
   kubectl -n kube-bmc-system create secret generic kube-bmc-auth \
     --from-literal=client-secret='<client secret>' \
     --from-literal=session-secret="$(openssl rand -base64 48)"
   ```

4. **Configure the chart:**

   ```yaml
   server:
     externalURL: https://kube-bmc.example.com
   auth:
     mode: oidc
     existingSecret: kube-bmc-auth
     oidc:
       issuerURL: https://login.example.com/realms/ops
       clientID: kube-bmc
   ```

5. **Verify:** open the dashboard; you are redirected to the provider and back. The user menu shows
   your identity, for example `oidc:alice@example.com`. The server logs `user signed in`.

Claim mapping:

| Value | Default | Description |
|---|---|---|
| `auth.oidc.usernameClaim` | `email` | Claim used as username. Use `preferred_username` or `sub` if the provider does not issue `email`. When `email` is used, `email_verified: false` is rejected. |
| `auth.oidc.groupsClaim` | `groups` | Claim shown as groups in the user menu |
| `auth.oidc.usernamePrefix` | `oidc:` | Prefix of recorded usernames |
| `auth.oidc.scopes` | `openid,profile,email` | Requested scopes. Add `groups` if the provider requires it for the groups claim. |
| `auth.oidc.extraAudiences` | `[]` | Additional audiences accepted in bearer tokens from API and MCP clients |

### Provider guides

**Keycloak**

1. In the realm, create a client `kube-bmc`: *Client authentication* on, *Standard flow* on,
   *Valid redirect URIs* `https://kube-bmc.example.com/auth/callback`.
2. Copy the secret from *Credentials*.
3. To allow only the operations team, enable a client authentication flow that requires a role,
   or grant access through a dedicated realm used only by operators.
4. `issuerURL: https://keycloak.example.com/realms/<realm>`

**Dex**

Dex connects directories that do not speak OIDC, such as LDAP, GitHub, GitLab, Feishu (via its
OIDC endpoint) or SAML, and issues OIDC tokens to kube-bmc. Register kube-bmc as a static client:

```yaml
issuer: https://dex.example.com
staticClients:
  - id: kube-bmc
    name: kube-bmc
    secret: <client secret>
    redirectURIs:
      - https://kube-bmc.example.com/auth/callback
connectors:
  - type: ldap
    id: ldap
    name: Corporate LDAP
    config:
      host: ldap.example.com:636
      bindDN: cn=dex,ou=services,dc=example,dc=com
      bindPW: $LDAP_BIND_PASSWORD
      userSearch:
        baseDN: ou=people,dc=example,dc=com
        filter: "(memberOf=cn=ops,ou=groups,dc=example,dc=com)"   # only the operations team
        username: uid
        idAttr: uid
        emailAttr: mail
        nameAttr: cn
```

`issuerURL: https://dex.example.com`. Restrict sign-in with the connector's search filter or
organization settings.

**Microsoft Entra ID**

1. Register an application with the web redirect URI `https://kube-bmc.example.com/auth/callback`
   and create a client secret.
2. In *Enterprise applications*, set *Assignment required* and assign the operations group.
3. `issuerURL: https://login.microsoftonline.com/<tenant-id>/v2.0`, and
   `usernameClaim: preferred_username` (the `email` claim is often absent).

**Okta**

Create an OIDC web application with the redirect URI above, assign the operations group, and use
`issuerURL: https://<org>.okta.com` (or the URL of a custom authorization server).

**Google**

Create an OAuth client of type *Web application* with the redirect URI above. Use an *Internal*
consent screen to limit sign-in to your Google Workspace organization.
`issuerURL: https://accounts.google.com`.

### Troubleshooting

| Symptom | Cause |
|---|---|
| Server log `OIDC discovery failed` | The server cannot reach `issuerURL`, or the URL is not the issuer (check `<issuerURL>/.well-known/openid-configuration` in a browser). The server retries for two minutes at startup. |
| Provider shows "invalid redirect URI" | The redirect URI registered at the provider differs from `server.externalURL` + `/auth/callback` (scheme, host, port and path must match exactly). |
| "token exchange failed" after sign-in | Wrong client secret, or the client is configured as public. |
| "ID token rejected" | The issuer in the token differs from `issuerURL` (often a trailing slash or an internal versus public hostname), or the clocks of the provider and the cluster differ. |
| "token has no "email" claim" | Set `usernameClaim` to a claim the provider issues, such as `preferred_username`. |
| Sign-in loops back to the provider | The session cookie is not stored: `externalURL` uses `https` while the dashboard is served over `http`, or a proxy strips cookies. |

## Tokens for API and MCP clients

Programs such as AI agents authenticate with a bearer token in the `Authorization` header. In
`password` and `oidc` modes, tokens of ServiceAccounts in the kube-bmc namespace are accepted:

```bash
kubectl -n kube-bmc-system create serviceaccount mcp-agent
kubectl -n kube-bmc-system create token mcp-agent --duration=24h
```

The ServiceAccount needs no RBAC permissions; the token only proves its identity to kube-bmc.
Tokens of ServiceAccounts in other namespaces are rejected, so workloads in the cluster cannot use
their own tokens to access kube-bmc. Delete the ServiceAccount to revoke access. Set
`auth.kubernetesTokens: false` to disable these tokens.

In `oidc` mode, ID tokens issued by the provider are also accepted when their audience is the
client ID or one of `auth.oidc.extraAudiences`.

## Sessions

Browser sessions are stored in an encrypted, HTTP-only cookie (AES-256-GCM) and expire after
`auth.sessionTTL` (12 hours by default). The cookie is marked `Secure` when `server.externalURL`
uses HTTPS. All server replicas must share the same `session-secret`. Changing it signs out all
users.
