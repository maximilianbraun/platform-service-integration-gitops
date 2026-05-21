# Threat Model: platform-service-integration-gitops

## 1. System Overview

The Git Connection service provides platform-managed authentication for Flux and other GitOps consumers on Managed Control Planes (MCPs). It bridges git hosting providers (GitHub, GitLab) and per-tenant MCPs through centralized credential management.

### Component Map

```
┌─────────────────────────────────────────────────────────────────────┐
│  Platform Cluster                                                    │
│  ┌──────────────────────────────────────────────────────────────┐   │
│  │  Connection Controller                                        │   │
│  │  - Watches GitConnection resources (onboarding cluster)       │   │
│  │  - Resolves GitProvider → TokenProvider implementation        │   │
│  │  - Generates short-lived tokens via GitHub/GitLab API         │   │
│  │  - Syncs Secrets + ConfigMaps to MCPs within scope            │   │
│  └──────────────────────────────────────────────────────────────┘   │
│                                                                      │
│  GitProvider (cluster-scoped)                                        │
│  - References privateKeySecretRef in openmcp-system                  │
│                                                                      │
│  Private Key Secret (openmcp-system namespace)                       │
│  - RSA private key for GitHub App JWT signing                        │
└─────────────────────────────────────────────────────────────────────┘
           │                            │
           │ watches                    │ syncs secrets/configmaps
           ▼                            ▼
┌────────────────────┐       ┌──────────────────────────────────────┐
│  Onboarding Cluster │       │  MCP Clusters (per workspace/project) │
│                     │       │                                        │
│  GitConnection      │       │  ConfigMap: git-connections (metadata)  │
│  (namespaced)       │       │  Secret: git-connection-<name> (token)  │
│  - project-X--ws-Y  │       │                                        │
│  - project-X        │       │  Mutating Webhook                       │
└────────────────────┘       │  - Reads local ConfigMap                 │
                              │  - Rewrites URL + injects secretRef     │
                              │  - Rejects invalid connection refs       │
                              └──────────────────────────────────────┘
                                         │
                                         │ token exchange
                                         ▼
                              ┌──────────────────────────┐
                              │  GitHub / GitLab API       │
                              │  - Git clone (HTTPS)       │
                              │  - Commit status           │
                              │  - OCI registry            │
                              └──────────────────────────┘
```

### Data Flows

1. **Token Generation:** Controller reads private key from platform Secret → signs JWT → exchanges with GitHub API for installation token (1h TTL)
2. **Secret Sync:** Controller writes token as Secret to each MCP in connection's namespace scope
3. **Webhook Mutation:** On MCP, webhook reads local ConfigMap → rewrites GitRepository URL → injects secretRef
4. **Git Operations:** Flux uses the injected secret to authenticate against git hosting provider

---

## 2. Trust Boundaries

| Boundary | What crosses it | Direction |
|----------|----------------|-----------|
| **Platform Cluster ↔ Onboarding Cluster** | GitConnection spec (watch), status updates (write) | Bidirectional |
| **Platform Cluster ↔ MCP Clusters** | Secrets (push), ConfigMaps (push), webhook deployment | Platform → MCP |
| **Platform Cluster ↔ GitHub/GitLab API** | JWT (out), installation tokens (in), installation listing (in) | Bidirectional |
| **MCP ↔ GitHub/GitLab API** | Git operations using installation token | MCP → External |
| **Tenant namespace boundary** | GitConnection resources are namespaced per project/workspace | Logical isolation |
| **MCP Webhook ↔ MCP Users** | Admission decisions, URL rewriting | User → Webhook |

### Trust Assumptions

- Platform cluster is operated by trusted platform administrators
- MCP clusters are partially trusted — tenants have constrained access but the cluster control plane is managed
- Onboarding cluster admission control enforces namespace isolation between tenants
- GitHub/GitLab APIs are external and untrusted (responses must be validated)

---

## 3. Assets

| Asset | Location | Sensitivity | Impact if compromised |
|--------|----------|-------------|----------------------|
| **GitHub App private key** | Platform cluster, `openmcp-system` namespace | Critical | Attacker can generate tokens for ANY org that installed the App |
| **Installation tokens** | Platform cluster (in-memory, transient), MCP Secrets | High | Read/write access to repositories within the token's scope |
| **GitProvider spec** | Platform cluster (cluster-scoped) | Medium | Reveals App ID and secret reference location |
| **GitConnection spec** | Onboarding cluster (namespaced) | Medium | Reveals org names, provider mappings, repo restrictions |
| **ConfigMap on MCP** | MCP cluster, `flux-system` namespace | Low–Medium | Reveals org names and git host URLs; enables targeted annotation attacks |
| **Token metadata in status** | Onboarding cluster | Low | Reveals expiration times (timing side-channel for refresh prediction) |
| **Audit logs** | Platform controller logs | Medium | Contains token generation events, installation IDs, project/workspace attribution |

---

## 4. Threat Actors

### 4.1 Malicious Tenant
- Has `workspace-admin` or `project-admin` RBAC in their own namespace
- Can create/modify GitConnection, GitRepository, and other annotated resources
- Goal: access another project's git repositories or escalate to platform-level credentials

### 4.2 Compromised MCP
- Attacker has cluster-admin on a specific MCP cluster
- Can read all Secrets and ConfigMaps on that MCP
- Goal: pivot to other MCPs, exfiltrate tokens from other tenants, or abuse tokens beyond intended scope

### 4.3 Compromised Platform Component
- Attacker has compromised a controller pod or service account on the platform cluster
- Can read platform Secrets (including the private key)
- Goal: generate tokens for arbitrary organizations, tamper with sync logic

### 4.4 External Attacker
- No cluster access; targets the system via git hosting provider or network
- Can attempt webhook callback spoofing, token interception, or API abuse
- Goal: gain repository access, deny service, or inject malicious code

---

## 5. Threats (STRIDE)

### 5.1 Spoofing

| ID | Threat | Description |
|----|--------|-------------|
| S-1 | **Cross-project GitConnection impersonation** | Tenant in Project B creates a GitConnection named identically to one in Project A, hoping to receive Project A's tokens |
| S-2 | **Annotation spoofing on MCP** | User annotates a resource with a connection name belonging to another project |
| S-3 | **Forged webhook callback** | Attacker sends fake GitHub App installation events to trick the controller into believing an App is installed |
| S-4 | **Controller identity spoofing** | Rogue pod impersonates the connection controller to push malicious secrets to MCPs |

### 5.2 Tampering

| ID | Threat | Description |
|----|--------|-------------|
| T-1 | **ConfigMap tampering on MCP** | Attacker with MCP access modifies `git-connections` ConfigMap to point to a malicious git host |
| T-2 | **Secret tampering on MCP** | Attacker replaces the token secret with credentials pointing to attacker-controlled repo |
| T-3 | **GitConnection spec tampering** | Attacker modifies connection spec to change the organization or add repositories outside their ownership |
| T-4 | **Webhook bypass** | Attacker disables or modifies the mutating webhook configuration to skip validation |

### 5.3 Repudiation

| ID | Threat | Description |
|----|--------|-------------|
| R-1 | **Unattributed token usage** | Cannot determine which connection/project/tenant triggered a specific git operation |
| R-2 | **Token sharing across connections** | Multiple connections use tokens that are indistinguishable in git hosting audit logs |
| R-3 | **Deleted GitConnection leaves orphaned activity** | User deletes connection after performing actions; audit trail loses context |

### 5.4 Information Disclosure

| ID | Threat | Description |
|----|--------|-------------|
| I-1 | **Token leakage across project boundaries** | Secrets synced to an MCP that serves multiple projects expose tokens to wrong tenants |
| I-2 | **Private key extraction** | Attacker with platform cluster access reads the GitHub App private key |
| I-3 | **Token in controller logs** | Tokens accidentally logged in controller output or Kubernetes events |
| I-4 | **Status field leaks installation metadata** | `managedSecrets` list reveals MCP names and namespace structure to GitConnection viewers |
| I-5 | **ConfigMap reveals organizational structure** | An MCP user can read the ConfigMap to discover which orgs are connected |

### 5.5 Denial of Service

| ID | Threat | Description |
|----|--------|-------------|
| D-1 | **GitHub API rate limit exhaustion** | Malicious tenant creates many GitConnections, each triggering installation token generation; exhausts the App's API rate limit |
| D-2 | **Webhook unavailability blocks resource creation** | If the mutating webhook pod is down, all annotated resource CREATE/UPDATE operations are rejected |
| D-3 | **Token refresh storm** | Many connections refresh simultaneously (e.g., after controller restart), overwhelming the git hosting API |
| D-4 | **MCP flooding with GitConnections** | Tenant creates excessive connections, causing the controller to sync an unmanageable number of secrets |

### 5.6 Elevation of Privilege

| ID | Threat | Description |
|----|--------|-------------|
| E-1 | **Workspace viewer escalates to token access** | User with `workspace-view` RBAC reads GitConnection status, then uses information to locate and read the secret on MCP |
| E-2 | **Org override annotation bypasses repo restriction** | User annotates with `gitops-org: other-org` to access repos outside the connection's configured organization |
| E-3 | **Shared MCP cross-tenant escalation** | In shared tenancy mode, a tenant discovers and references another tenant's connection on the same MCP |
| E-4 | **Controller service account over-privilege** | If the controller SA has broad cluster-admin, compromise grants access to all platform secrets |

---

## 6. Mitigations

### Scope Enforcement (S-1, S-2, I-1, E-3)

| Control | Mechanism |
|---------|-----------|
| **Sync radius** | Controller ONLY syncs secrets/ConfigMap entries to MCPs within the connection's namespace scope. An MCP in `project-B--ws-prod` never receives secrets for connections in `project-A--ws-dev`. |
| **Webhook rejection** | Webhook checks local ConfigMap; if connection name is absent, admission is denied with a clear error. |
| **Namespace isolation** | GitConnection names are unique per-namespace (K8s guarantee). Same-named connections in different namespaces produce independent, isolated secrets. |
| **Label-based garbage collection** | Controller owns secrets with `gitops.integrations.open-control-plane.io/managed-by` label. Manually created fakes without correct labels are garbage-collected. |

### RBAC (E-1, E-4, T-3)

| Control | Mechanism |
|---------|-----------|
| **workspace-view cannot read MCP secrets** | MCP RBAC does not grant secret read to workspace-view role |
| **Controller least-privilege SA** | Controller service account scoped to: read GitProvider, read/write GitConnection status, read platform secrets in `openmcp-system`, write secrets/configmaps to MCP namespaces only |
| **Admission webhook on GitConnection** | Validates that `primary: true` is unique per namespace; prevents spec fields that exceed tenant's scope |

### Private Key Protection (I-2, S-4)

| Control | Mechanism |
|---------|-----------|
| **Key confinement** | Private key never leaves platform cluster. Only the controller pod in `openmcp-system` can read it. |
| **No key on MCPs** | MCPs receive only short-lived installation tokens (1h TTL), never the signing key |
| **Pod identity** | Controller deployment uses a dedicated ServiceAccount; pod security standards prevent privilege escalation |

### Audit Trail (R-1, R-2, R-3)

| Control | Mechanism |
|---------|-----------|
| **Structured audit logging** | Every `token_generated` and `secret_synced` event includes `connection`, `project`, `workspace`, `provider`, `organization`, and `installationId` |
| **Kubernetes Events** | Events emitted on GitConnection resource for state transitions |
| **Finalizer on deletion** | Ensures cleanup is logged before resource removal; orphaned secrets are detected and removed |
| **Git hosting attribution** | Each installation token is scoped to a specific org installation; GitHub audit log shows which App installation performed actions |

### Rate Limiting / DoS Protection (D-1, D-3, D-4)

| Control | Mechanism |
|---------|-----------|
| **Token refresh at 50% lifetime** | Avoids thundering herd — tokens refresh gradually, not all at expiry |
| **Per-tenant connection quota** | Platform quota service limits the number of GitConnections per project/workspace |
| **GitHub API rate limit awareness** | Controller respects `X-RateLimit-Remaining` headers; backs off when approaching limits |
| **Webhook failure policy** | Configurable `failurePolicy: Fail` (strict) vs `Ignore` (permissive) based on deployment tolerance |

### Webhook Integrity (T-1, T-2, T-4, D-2)

| Control | Mechanism |
|---------|-----------|
| **Controller reconciles ConfigMap/Secret** | Any manual tampering on MCP is overwritten on next reconcile loop (configurable interval) |
| **Webhook TLS** | Webhook uses a cert signed by the cluster CA; apiserver validates the webhook endpoint |
| **Webhook HA** | Multiple replicas with pod disruption budget to prevent single-point failure |

### Org Override Control (E-2)

| Control | Mechanism |
|---------|-----------|
| **Token scope restriction** | Even with `gitops-org` override annotation, the installation token is only valid for repos the GitHub App installation has access to — the token cannot access repos in orgs where the App isn't installed |
| **Repository restriction in spec** | `spec.repositories` limits token scope to named repos regardless of annotation |

---

## 7. Residual Risks

| ID | Risk | Severity | Acceptance Rationale |
|----|------|----------|---------------------|
| RR-1 | **Private key compromise on platform cluster** grants access to all org installations | Critical | Accepted because platform cluster is highest-trust tier with restricted access; risk mitigated by operational controls (HSM integration is a future enhancement) |
| RR-2 | **1-hour token exposure window** after MCP compromise | High | Accepted because tokens auto-expire and controller can be triggered to revoke/rotate; detection mechanisms alert on anomalous usage |
| RR-3 | **GitHub API availability** blocks new connection setup and token refresh | Medium | Accepted because 50% lifetime refresh buffer provides grace period; documented as operational dependency |
| RR-4 | **Shared rate limit** across all tenants using same GitHub App | Medium | Accepted because per-tenant quotas limit connection count; monitoring alerts before limit exhaustion; separate GitHub Apps per tenant group is a future option |
| RR-5 | **ConfigMap on MCP reveals org names** to any pod with configmap read access | Low | Accepted because org names are generally non-sensitive (public GitHub orgs); sensitive GitLab groups should use separate MCPs |
| RR-6 | **Webhook bypass during webhook downtime** allows unauthenticated resources to be created | Medium | Accepted if `failurePolicy: Fail` is used (blocks creation); if `Ignore` is used, Flux will fail on missing secret — no silent data exfil |
| RR-7 | **Token cannot be revoked mid-lifetime** once issued | Medium | GitHub installation tokens cannot be revoked via API. If compromised, must wait for expiry (max 1h) or remove the App installation entirely |

---

## 8. Recommendations

### Short-term (before GA)

1. **Enforce `failurePolicy: Fail`** on the mutating webhook in production — prevents resource creation when webhook is unavailable, avoiding silent misconfiguration.

2. **Add token generation rate limiting** in the controller — cap the number of token generation requests per minute per tenant to prevent intentional or accidental API rate limit exhaustion.

3. **Implement secret access audit** — log (and alert on) any direct `kubectl get secret git-connection-*` access on MCPs to detect token exfiltration attempts.

4. **Add NetworkPolicy** on MCP — restrict which pods can read secrets in `flux-system` namespace to only the Flux source-controller and notification-controller.

5. **Validate `gitops-org` override** — webhook should verify the override org is within the App installation's accessible orgs before admitting the resource.

### Medium-term (post-GA hardening)

6. **HSM integration for private key** — store the GitHub App private key in a Hardware Security Module (e.g., AWS CloudHSM, Azure Dedicated HSM) so it never exists in plaintext in etcd.

7. **Per-tenant GitHub Apps** — for high-isolation deployments, allow each project to bring their own GitHub App, eliminating shared rate limits and reducing blast radius.

8. **Mutual TLS for MCP sync** — authenticate the controller to MCP clusters using mTLS rather than long-lived kubeconfig tokens.

9. **Token usage correlation** — correlate GitHub API audit logs with platform audit logs to provide end-to-end attribution from tenant action to git operation.

10. **Implement admission policy engine** — use OPA/Gatekeeper or Kyverno on MCPs to enforce that only the connection controller can create/modify secrets with the `managed-by` label.

### Operational

11. **Monitor GitHub API rate limit consumption** per App installation and alert at 80% threshold.

12. **Runbook for private key rotation** — document the process for rotating the GitHub App private key without downtime (dual-key period).

13. **Regular review of controller RBAC** — ensure the service account permissions haven't drifted beyond minimum necessary.

14. **Penetration testing** — schedule cross-tenant isolation testing as part of release qualification.
