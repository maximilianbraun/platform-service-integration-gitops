---
authors:
  - maximiliantech
---

# GitOps Integration: Git Connection for Projects and Workspaces

## Context and Problem Statement

Users of OpenControlPlane need a platform-managed way to connect Git hosting organizations (GitHub, GitLab, etc.) to their Projects or Workspaces so that Flux and other consumers on Managed Control Planes can authenticate against git repos without per-MCP manual secret management.

Today, Flux on MCPs uses `GitRepository` resources that reference a `secretRef`, but there is no platform-level mechanism to provision and rotate that secret across all MCPs in a user's scope. Users must manually create secrets per MCP, and tokens (GitHub App installation tokens, GitLab project tokens, etc.) have varying lifetimes and rotation needs.

The desired outcome: a user creates a `GitConnection` resource at their workspace or project level, annotates resources on their MCPs to declare which connection they need, and the platform provisions the exact secret each resource references — auto-refreshing, provider-transparent.

## Decision Drivers

* Users should not manage tokens or secrets on MCPs manually
* Adding a new git hosting backend must not require API-breaking changes
* Private keys and long-lived credentials must never leave the platform cluster
* Cross-project/workspace isolation must be enforced by default
* Manifests should be portable across environments (same YAML, different backing provider)
* Common-case UX should be minimal (no provider-internal IDs in user-facing specs)

## Considered Options

### 1. Service Architecture

* **A) Standalone platform service** (`platform-service-git-connection`) — follows existing service patterns (`platform-service-gateway`, `platform-service-dns`)
* **B) Embedded in `platform-service-project-workspace`** — extend existing project/workspace controller with git connection logic

### 2. Provider Abstraction Model

* **A) `GitProvider` + `GitConnection` with typed union** — cluster-scoped provider config, namespaced user connection; `spec.type` enum with per-type struct fields
* **B) Single `GitConnection` CRD with opaque provider config** — all provider details in an untyped `providerConfig` field
* **C) One CRD per provider** — `GitHubConnection`, `GitLabConnection`, etc.

### 3. MCP Resource Mutation Strategy

* **A) Annotation-driven URL injection via mutating webhook** — webhook on MCPs rewrites `spec.url` and injects `secretRef` based on annotations; reads local ConfigMap only
* **B) Controller-based patching** — platform controller watches GitRepositories on MCPs and patches them directly
* **C) User writes full URL + secretRef manually** — platform only provisions the secret, users reference it themselves

### 4. Token Management Model

* **A) External token management** — private key stays on platform cluster, controller generates short-lived tokens centrally and pushes them to MCPs
* **B) Flux-native GitHub App auth** — deploy private key as secret on each MCP, let Flux handle token generation natively
* **C) Shared long-lived PAT** — distribute a single personal access token to all MCPs

### 5. Authorization Model

* **A) Sync radius enforcement** — secrets are only synced to MCPs within the connection's scope (workspace or project); webhook rejects references to unavailable connections
* **B) RBAC-only enforcement** — rely on Kubernetes RBAC to prevent cross-project secret reads
* **C) Network policies** — isolate secret access via network-level controls

### 6. GitHub App Installation Discovery

* **A) Auto-discovery** — controller lists GitHub App installations via API, matches by org name; user never provides `installationId`
* **B) User-provided `installationId`** — require users to look up and specify the installation ID in the spec

## Decision Outcome

### 1. Standalone platform service (Option A)

Git connection management is a self-contained domain with its own lifecycle, CRDs, and provider-specific logic. Embedding it in the project-workspace service would violate single-responsibility and create a deployment coupling (git provider changes would require redeploying project management).

### 2. `GitProvider` + `GitConnection` with typed union (Option A)

Two CRDs with clear separation: `GitProvider` (cluster-scoped, operator-managed, describes a git hosting backend) and `GitConnection` (namespaced, user-managed, provider-agnostic). The typed union (`spec.type` + per-type struct fields) gives CRD-level validation and makes adding GitLab a non-breaking additive change (new enum value + new struct field).

Rejected: opaque provider config (Option B) loses CRD validation and IDE support. Per-provider CRDs (Option C) proliferate resources and complicate inheritance resolution.

### 3. Annotation-driven URL injection via mutating webhook (Option A)

A lightweight mutating webhook on each MCP reads annotations (`gitops-connection`, `gitops-repo`) and a local ConfigMap to rewrite `spec.url` and inject `secretRef`. No cross-cluster calls in the admission path. Manifests become portable — the same YAML works in any environment by changing only the `GitConnection` definition.

Rejected: controller-based patching (Option B) introduces race conditions with user updates and requires watching all GitRepositories cross-cluster. Manual secretRef (Option C) defeats the purpose of platform-managed connections.

### 4. External token management (Option A)

Private keys never leave the platform cluster. The controller generates short-lived installation tokens centrally, pushes them to MCPs as secrets, and refreshes at 50% token lifetime. This provides centralized audit logging and works for non-Flux consumers (notification providers, OCI registries).

Rejected: Flux-native auth (Option B) requires deploying private keys to every MCP — unacceptable blast radius. Shared PATs (Option C) cannot be scoped per-org or rotated automatically.

### 5. Sync radius enforcement (Option A)

The controller only syncs ConfigMap entries and secrets to MCPs within the connection's namespace scope. An MCP in Project B never receives secrets for connections in Project A. The webhook provides a secondary enforcement layer with immediate user-facing error messages on admission.

Rejected: RBAC-only (Option B) is insufficient because users can create arbitrary resources and the enforcement boundary is the connection's scope, not the user's RBAC. Network policies (Option C) operate at the wrong abstraction layer.

### 6. Auto-discovery of GitHub App installations (Option A)

The controller calls the GitHub API to list installations and matches by organization name. If the App is not yet installed, the status surfaces an `installUrl` for the user. The user never provides an `installationId` — it is an internal implementation detail.

Rejected: user-provided `installationId` (Option B) leaks GitHub internals into the user-facing API, is error-prone, and breaks when installations are recreated.

## Consequences

* Good, because users get zero-touch secret management — create a `GitConnection` once, annotate resources on MCPs, and tokens are provisioned and rotated automatically.
* Good, because provider transparency allows adding GitLab or other backends without user-facing API changes.
* Good, because private keys are confined to the platform cluster with centralized audit.
* Good, because manifest portability eliminates environment-specific URLs and secret references from user YAML.
* Good, because scope enforcement prevents cross-project token leakage by design.
* Bad, because the mutating webhook adds a dependency on each MCP — webhook unavailability blocks resource creation.
* Bad, because auto-discovery depends on GitHub API availability during reconciliation; a GitHub outage blocks new connection setup.
* Bad, because external token management means a brief window of expired tokens is possible if the controller is delayed (mitigated by 50% lifetime refresh).
