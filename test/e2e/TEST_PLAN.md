# E2E Test Plan: platform-service-integration-gitops

## Overview

This document defines the end-to-end test strategy for the GitConnection platform service. Tests validate the full lifecycle from GitProvider/GitConnection creation through annotation-driven secret provisioning on MCPs, including token refresh, cross-project isolation, and webhook admission behavior.

---

## Prerequisites

### Test GitHub App

| Item | Details |
|------|---------|
| GitHub App | A dedicated test App (`openmcp-e2e-git-connection`) registered on github.com (or GHE) |
| App ID | Configured via env var `E2E_GITHUB_APP_ID` |
| Private Key | PEM file path via `E2E_GITHUB_APP_PRIVATE_KEY_PATH` |
| Test Org A | `openmcp-e2e-org-a` — App pre-installed |
| Test Org B | `openmcp-e2e-org-b` — App NOT pre-installed (for AppNotInstalled flow) |
| Test Repo | `openmcp-e2e-org-a/test-repo` — contains a simple `README.md` that Flux can clone |
| Test Repo 2 | `openmcp-e2e-org-a/test-repo-private` — private repo for verifying token-based auth |

### Platform Environment

- Local environment provisioned via `ocpctl environments apply e2e-git` (kind-based)
- Platform cluster with `openmcp-operator` installed
- `platform-service-integration-gitops` controller deployed
- At least one ClusterProvider (kind) registered

### Kubernetes Clients

| Cluster | Purpose |
|---------|---------|
| Platform cluster | GitProvider (cluster-scoped) |
| Onboarding cluster | GitConnection (namespaced), Projects, Workspaces |
| MCP clusters | GitRepository, Secrets, ConfigMaps, webhook behavior |

### Test Identity

- Service account or user identity with admin privileges on test projects/workspaces
- Configured via `E2E_IDENTITY` env var (e.g., `system:serviceaccount:openmcp-system:e2e-runner`)

---

## Test Framework

Tests follow the `platform-service-test-runner` pattern:

- Each scenario is a `TestCase` implementation with `Run()` and `Cleanup()` methods
- Tests export state (names, namespaces) for dependent test cases
- Polling with configurable timeout/interval via `WaitForReadyAndGet` / `WaitForDeletion`
- Structured logging with `controller-utils/pkg/logging`

Alternative: `sigs.k8s.io/e2e-framework` (as used in `openmcp-testing/e2e/`) for standalone test binary execution.

---

## Test Scenarios

---

### TC-01: GitProvider Creation and Validation

**Preconditions:**
- Platform cluster accessible
- GitHub App private key secret not yet created

**Steps:**
1. Create Secret `github-app-key-e2e` in `openmcp-system` containing the test App's private key PEM
2. Create a `GitProvider` resource:
   ```yaml
   apiVersion: gitops.integrations.open-control-plane.io/v1alpha1
   kind: GitProvider
   metadata:
     name: github-com-e2e
   spec:
     host: "github.com"
     type: GitHubApp
     githubApp:
       appId: <E2E_GITHUB_APP_ID>
       privateKeySecretRef:
         name: github-app-key-e2e
         namespace: openmcp-system
     tokenRefresh:
       refreshBeforeExpiry: "5m"
   ```
3. Poll until `status.phase == Ready`
4. Verify `status.conditions` has `CredentialsValid: True`
5. Verify `status.installationUrl` is non-empty and contains the App slug

**Expected Outcome:**
- GitProvider reaches `Ready` phase within 60s
- The installation URL points to a valid GitHub App installation page

**Verification:**
- `kubectl get gitprovider github-com-e2e -o jsonpath='{.status.phase}'` == `Ready`

**Cleanup:**
- Delete GitProvider `github-com-e2e`
- Delete Secret `github-app-key-e2e`

---

### TC-02: Happy Path — GitProvider + GitConnection + Annotated GitRepository + Flux Clone

**Preconditions:**
- TC-01 cleanup complete (or fresh environment)
- Test org `openmcp-e2e-org-a` has the GitHub App installed

**Steps:**
1. Create the GitProvider (same as TC-01)
2. Create a Project:
   ```
   Project: e2e-gitconn-p
   ```
3. Create a Workspace in the project:
   ```
   Workspace: e2e-gitconn-ws (in project namespace)
   ```
4. Create an MCPv2 in the workspace:
   ```
   ManagedControlPlaneV2: e2e-gitconn-mcp (in workspace namespace)
   ```
5. Wait for MCP to be Ready
6. Create a `GitConnection` in the workspace namespace:
   ```yaml
   apiVersion: gitops.integrations.open-control-plane.io/v1alpha1
   kind: GitConnection
   metadata:
     name: org-a
     namespace: <workspace-status-namespace>
   spec:
     providerRef: github-com-e2e
     organization: "openmcp-e2e-org-a"
     primary: true
   ```
7. Poll `GitConnection` until `status.phase == Ready`
8. Verify on the MCP cluster:
   - ConfigMap `git-connections` exists in `flux-system` namespace with key `org-a.host = github.com`
   - Secret `git-connection-org-a` exists in `flux-system` namespace with keys `username` and `password`
9. Apply an annotated GitRepository on the MCP:
   ```yaml
   apiVersion: source.toolkit.fluxcd.io/v1
   kind: GitRepository
   metadata:
     name: e2e-test-repo
     namespace: flux-system
     annotations:
       gitops.integrations.open-control-plane.io/connection: ""
       gitops.integrations.open-control-plane.io/repository: test-repo
   spec:
     interval: 1m
     url: changeme
     ref:
       branch: main
   ```
10. Verify the webhook mutates the resource:
    - `spec.url` == `https://github.com/openmcp-e2e-org-a/test-repo`
    - `spec.secretRef.name` == `git-connection-org-a`
11. Poll Flux GitRepository status until `Ready: True` (Flux successfully cloned)

**Expected Outcome:**
- Full pipeline from connection creation to Flux clone completes successfully
- Token-based authentication works end-to-end

**Verification:**
- `kubectl get gitrepository e2e-test-repo -n flux-system -o jsonpath='{.status.conditions[?(@.type=="Ready")].status}'` == `True`
- `kubectl get gitrepository e2e-test-repo -n flux-system -o jsonpath='{.spec.url}'` contains `github.com/openmcp-e2e-org-a/test-repo`

**Cleanup:**
- Delete GitRepository on MCP
- Delete GitConnection
- Delete MCP, Workspace, Project (reverse order)
- Delete GitProvider + Secret

---

### TC-03: AppNotInstalled Flow — Install Link — Install — Ready

**Preconditions:**
- GitProvider exists (from TC-01 setup)
- Test org `openmcp-e2e-org-b` does NOT have the GitHub App installed
- Project and Workspace created

**Steps:**
1. Create `GitConnection` referencing org `openmcp-e2e-org-b`:
   ```yaml
   apiVersion: gitops.integrations.open-control-plane.io/v1alpha1
   kind: GitConnection
   metadata:
     name: org-b
     namespace: <workspace-namespace>
   spec:
     providerRef: github-com-e2e
     organization: "openmcp-e2e-org-b"
   ```
2. Poll until `status.phase == AppNotInstalled`
3. Verify:
   - `status.installUrl` is non-empty and contains `installations/new`
   - Condition `AppInstalled` has `status: "False"` and `reason: NotFound`
4. Simulate App installation (use GitHub API to install the App on `openmcp-e2e-org-b`):
   ```bash
   # This step requires an org admin token or manual action
   # In CI: use a pre-authorized token to create the installation
   ```
5. Wait for controller to detect installation (polls every 60s)
6. Poll until `status.phase == Ready`
7. Verify Condition `AppInstalled` transitions to `status: "True"`

**Expected Outcome:**
- Phase transitions: `Pending` → `AppNotInstalled` → `Progressing` → `Ready`
- Install URL is correctly generated for the specific org

**Verification:**
- Phase sequence observed in order via polling
- `status.conditions` correctly reflect the installation state at each phase

**Notes:**
- In automated CI, this test requires either:
  - A GitHub API call to programmatically install the App (requires org admin scope)
  - A pre-created test org where the App can be uninstalled/reinstalled between runs
- For manual execution: tester clicks the install URL and approves

**Cleanup:**
- Delete GitConnection
- Optionally uninstall the App from `openmcp-e2e-org-b` (to reset for next run)

---

### TC-04: Cross-Project Isolation

**Preconditions:**
- GitProvider exists

**Steps:**
1. Create Project A (`e2e-isolation-a`) with Workspace A and MCP-A
2. Create Project B (`e2e-isolation-b`) with Workspace B and MCP-B
3. Create `GitConnection "org-a"` in Project A's workspace namespace
4. Wait for `GitConnection` to reach `Ready` in Project A
5. Verify on MCP-A:
   - ConfigMap `git-connections` has entry for `org-a` ✓
   - Secret `git-connection-org-a` exists ✓
6. Verify on MCP-B:
   - ConfigMap `git-connections` does NOT have entry for `org-a` ✗
   - Secret `git-connection-org-a` does NOT exist ✗
7. On MCP-B, attempt to apply an annotated GitRepository referencing `org-a`:
   ```yaml
   metadata:
     annotations:
       gitops.integrations.open-control-plane.io/connection: org-a
       gitops.integrations.open-control-plane.io/repository: test-repo
   ```
8. Expect webhook to REJECT the admission request with error message containing "not available in this control plane"

**Expected Outcome:**
- Secrets and ConfigMap entries are scoped to the owning project's MCPs only
- Webhook rejects cross-project references with clear error message

**Verification:**
- Step 6: `kubectl get secret git-connection-org-a -n flux-system` returns `NotFound` on MCP-B
- Step 8: `kubectl apply` returns admission rejection error

**Cleanup:**
- Delete MCPs, Workspaces, Projects (both A and B)
- Delete GitConnection, GitProvider

---

### TC-05: Token Refresh Verification

**Preconditions:**
- Full happy path setup (GitProvider, Project, Workspace, MCP, GitConnection in Ready state)
- GitProvider configured with short `refreshBeforeExpiry: "50m"` (GitHub tokens expire in 1h)

**Steps:**
1. Create a GitConnection and wait for Ready
2. Record initial `status.tokenExpiresAt` value (T1)
3. Record the Secret's `resourceVersion` on the MCP (RV1)
4. Wait until the controller refreshes the token (should happen at ~50% of token lifetime, i.e., ~30 min for 1h tokens)
   - For test acceleration: configure `refreshBeforeExpiry: "55m"` so refresh happens ~5 min after token issuance
5. Poll `status.tokenExpiresAt` until it changes (T2 > T1)
6. Verify on the MCP:
   - Secret `git-connection-<name>` has a new `resourceVersion` (RV2 != RV1)
   - Secret `data.password` is different from the initial value
7. If Flux GitRepository exists, verify it remains `Ready: True` (no auth interruption)

**Expected Outcome:**
- Token is transparently refreshed before expiry
- Secret is updated in-place on MCPs
- No Flux authentication failures during rotation

**Verification:**
- `T2 > T1` (expiry timestamp extended)
- `RV2 != RV1` (secret was updated)
- Flux GitRepository remains `Ready`

**Notes:**
- This test has a long wait time (~5-55 min depending on configuration)
- In CI: set `refreshBeforeExpiry` aggressively close to token lifetime (e.g., `"55m"` for a 60-min token) to force rapid refresh
- Alternative: mock the token provider to issue tokens with very short lifetimes (e.g., 2 min)

**Cleanup:**
- Standard teardown

---

### TC-06: New MCP Auto-Sync

**Preconditions:**
- GitProvider, Project, Workspace, GitConnection all exist and Ready
- At least one MCP already has the synced secret

**Steps:**
1. Start with existing setup where `GitConnection "org-a"` is Ready and secrets synced to MCP-1
2. Create a second MCPv2 (`e2e-mcp-2`) in the same workspace
3. Wait for MCP-2 to reach Ready
4. Poll the GitConnection status until `managedSecrets` includes an entry for MCP-2
5. Verify on MCP-2:
   - ConfigMap `git-connections` exists with `org-a` entries
   - Secret `git-connection-org-a` exists with valid token data
6. Apply annotated GitRepository on MCP-2:
   ```yaml
   metadata:
     annotations:
       gitops.integrations.open-control-plane.io/connection: ""
       gitops.integrations.open-control-plane.io/repository: test-repo
   ```
7. Verify webhook mutates URL and injects secretRef
8. Verify Flux clones successfully

**Expected Outcome:**
- New MCPs automatically receive connection secrets without user intervention
- Annotation-based provisioning works immediately on new MCPs

**Verification:**
- `GitConnection.status.managedSecrets` lists MCP-2
- Flux GitRepository on MCP-2 reaches `Ready`

**Cleanup:**
- Delete MCP-2, then standard teardown

---

### TC-07: Primary Connection Resolution

**Preconditions:**
- Workspace with an MCP ready
- Two GitConnections: one primary (`primary: true`) and one non-primary

**Steps:**
1. Create GitConnection `org-a` with `primary: true`
2. Create GitConnection `org-other` with `primary: false` (different org or same with different name)
3. Wait for both to reach Ready
4. On the MCP, apply a GitRepository with EMPTY connection annotation:
   ```yaml
   annotations:
     gitops.integrations.open-control-plane.io/connection: ""
     gitops.integrations.open-control-plane.io/repository: test-repo
   ```
5. Verify webhook resolves to the primary connection:
   - `spec.url` contains the primary connection's org
   - `spec.secretRef.name` == `git-connection-org-a`
6. Apply another GitRepository with EXPLICIT connection name:
   ```yaml
   annotations:
     gitops.integrations.open-control-plane.io/connection: org-other
     gitops.integrations.open-control-plane.io/repository: other-repo
   ```
7. Verify it resolves to `org-other`:
   - `spec.url` contains `org-other`'s org
   - `spec.secretRef.name` == `git-connection-org-other`

**Expected Outcome:**
- Empty annotation value resolves to the primary connection
- Explicit name resolves to the named connection

**Verification:**
- URL and secretRef on both GitRepositories match their respective connections

**Cleanup:**
- Delete GitRepositories, GitConnections, standard teardown

---

### TC-08: Primary Connection Uniqueness Validation

**Preconditions:**
- Workspace namespace exists

**Steps:**
1. Create GitConnection `conn-a` with `primary: true` — succeeds
2. Attempt to create GitConnection `conn-b` with `primary: true` in the same namespace
3. Expect creation to be REJECTED by validating webhook with error: "only one connection per namespace can be primary"

**Expected Outcome:**
- Only one primary connection per namespace is allowed
- Second creation attempt fails with clear validation error

**Verification:**
- `kubectl apply` of second primary connection returns admission error

**Cleanup:**
- Delete conn-a

---

### TC-09: Org Override Annotation

**Preconditions:**
- GitConnection `org-a` is Ready (connected to `openmcp-e2e-org-a`)
- The GitHub App also has access to a different org (or the same org works for URL construction)

**Steps:**
1. Apply a GitRepository on the MCP with org override:
   ```yaml
   annotations:
     gitops.integrations.open-control-plane.io/connection: org-a
     gitops.integrations.open-control-plane.io/repository: cross-org-repo
     gitops.integrations.open-control-plane.io/organization: openmcp-e2e-org-shared
   ```
2. Verify webhook constructs URL using the override org:
   - `spec.url` == `https://github.com/openmcp-e2e-org-shared/cross-org-repo`
3. Verify `secretRef` still references the connection's secret:
   - `spec.secretRef.name` == `git-connection-org-a`

**Expected Outcome:**
- Org override annotation changes the constructed URL org segment
- Secret reference unchanged (same connection token)

**Verification:**
- `spec.url` contains `openmcp-e2e-org-shared` instead of `openmcp-e2e-org-a`
- `spec.secretRef.name` == `git-connection-org-a`

**Cleanup:**
- Delete GitRepository

---

### TC-10: Webhook Rejection Cases

**Preconditions:**
- MCP with webhook deployed, at least one valid GitConnection synced

#### TC-10a: Missing Repo Annotation

**Steps:**
1. Apply GitRepository with connection annotation but WITHOUT `gitops-repo`:
   ```yaml
   annotations:
     gitops.integrations.open-control-plane.io/connection: org-a
     # missing: gitops.integrations.open-control-plane.io/repository
   ```
2. Expect REJECTION with error mentioning "missing required annotation" and `gitops-repo`

#### TC-10b: Unknown Connection Name

**Steps:**
1. Apply GitRepository referencing a non-existent connection:
   ```yaml
   annotations:
     gitops.integrations.open-control-plane.io/connection: does-not-exist
     gitops.integrations.open-control-plane.io/repository: test-repo
   ```
2. Expect REJECTION with error: `GitConnection "does-not-exist" is not available in this control plane`
3. Verify error message lists available connections

#### TC-10c: No Annotation (pass-through)

**Steps:**
1. Apply a GitRepository WITHOUT any `gitops-connection` annotation:
   ```yaml
   apiVersion: source.toolkit.fluxcd.io/v1
   kind: GitRepository
   metadata:
     name: plain-repo
     namespace: flux-system
   spec:
     interval: 5m
     url: https://github.com/public/repo
     ref:
       branch: main
   ```
2. Expect the resource to be ACCEPTED without mutation
3. Verify `spec.url` remains unchanged, no `secretRef` injected

**Expected Outcome:**
- Webhook rejects invalid annotation combinations with actionable error messages
- Unannotated resources pass through without modification

**Verification:**
- `kubectl apply` error output for rejection cases
- `kubectl get` of pass-through resource shows original spec unchanged

**Cleanup:**
- Delete test resources

---

### TC-11: Garbage Collection of Manually Created Secrets

**Preconditions:**
- GitConnection `org-a` syncing secrets to an MCP

**Steps:**
1. On the MCP, manually create a Secret with the platform label:
   ```yaml
   apiVersion: v1
   kind: Secret
   metadata:
     name: git-connection-fake
     namespace: flux-system
     labels:
       gitops.integrations.open-control-plane.io/managed-by: "true"
       gitops.integrations.open-control-plane.io/connection: fake
   type: Opaque
   data:
     username: dGVzdA==
     password: dGVzdA==
   ```
2. Wait for the next controller reconcile (up to 60s)
3. Poll the Secret — expect it to be deleted (garbage collected)

**Expected Outcome:**
- Secrets bearing the platform label but not owned by a real GitConnection are cleaned up

**Verification:**
- `kubectl get secret git-connection-fake -n flux-system` returns `NotFound` after reconcile

**Cleanup:**
- None (secret is garbage collected)

---

### TC-12: Full URL Fallback (Webhook Skips URL Rewrite)

**Preconditions:**
- GitConnection `org-a` synced to MCP

**Steps:**
1. Apply a GitRepository with a full URL (has scheme):
   ```yaml
   annotations:
     gitops.integrations.open-control-plane.io/connection: org-a
     gitops.integrations.open-control-plane.io/repository: test-repo
   spec:
     url: https://github.com/openmcp-e2e-org-a/test-repo
   ```
2. Verify webhook does NOT rewrite the URL (it already has `https://`)
3. Verify webhook DOES inject `secretRef`:
   - `spec.secretRef.name` == `git-connection-org-a`

**Expected Outcome:**
- Full URLs are preserved; only secretRef is injected

**Verification:**
- `spec.url` unchanged from what user provided
- `spec.secretRef.name` present

**Cleanup:**
- Delete GitRepository

---

## Test Execution

### Local Execution

```bash
# Set up local environment
ocpctl environments apply e2e-git --config test/e2e/env-config.yaml

# Export required env vars
export E2E_GITHUB_APP_ID=<app-id>
export E2E_GITHUB_APP_PRIVATE_KEY_PATH=<path-to-pem>
export E2E_IDENTITY=system:serviceaccount:openmcp-system:e2e-runner

# Run tests
go test ./test/e2e/... -v -timeout 30m
```

### CI Execution

Tests are registered as an `E2ETestSpecification` CR:
```yaml
apiVersion: testing.open-control-plane.io/v1alpha1
kind: E2ETestSpecification
metadata:
  name: git-connection-e2e
spec:
  testCases:
    - name: createGitProvider
      config:
        appId: "${E2E_GITHUB_APP_ID}"
    - name: createProject
      config:
        identity: "${E2E_IDENTITY}"
    - name: createWorkspace
    - name: createManagedControlPlaneV2
    - name: createGitConnection
      config:
        organization: "openmcp-e2e-org-a"
        providerRef: "github-com-e2e"
    - name: verifySecretSync
    - name: verifyAnnotatedGitRepository
    - name: verifyFluxClone
  cleanup: true
```

### Timeouts

| Operation | Timeout |
|-----------|---------|
| GitProvider → Ready | 60s |
| Project → Ready | 2min |
| Workspace → Ready | 2min |
| MCP → Ready | 5min |
| GitConnection → Ready | 3min |
| Secret sync to MCP | 2min |
| Flux clone | 3min |
| Token refresh (accelerated) | 10min |
| Full suite | 30min |

---

## Teardown

All test cases implement `Cleanup()` which runs in reverse order:

1. Delete GitRepositories on MCPs
2. Delete GitConnections
3. Delete MCPs (wait for full deletion)
4. Delete Workspaces (wait for full deletion)
5. Delete Projects (wait for full deletion)
6. Delete GitProvider
7. Delete private key Secret

Failed test runs leave resources tagged with `labels: {test-case: <name>}` for debugging. A cleanup job removes resources older than 2h.

---

## Test Data Matrix

| Scenario | Provider | Org | App Installed | Primary | Expected Phase |
|----------|----------|-----|---------------|---------|----------------|
| TC-02 Happy | github-com-e2e | openmcp-e2e-org-a | Yes | Yes | Ready |
| TC-03 NotInstalled | github-com-e2e | openmcp-e2e-org-b | No | No | AppNotInstalled → Ready |
| TC-04 Isolation | github-com-e2e | openmcp-e2e-org-a | Yes | Yes | Ready (only in scope) |
| TC-05 Refresh | github-com-e2e | openmcp-e2e-org-a | Yes | Yes | Ready (token rotates) |
| TC-07 Primary | github-com-e2e | openmcp-e2e-org-a | Yes | Yes/No | Both Ready |

---

## Risk Areas

1. **Token refresh timing** — Tests depending on token lifetime are inherently slow. Mitigation: configure aggressive refresh window or mock short-lived tokens.
2. **GitHub API rate limits** — Multiple test runs may hit GitHub API limits. Mitigation: use conditional requests, cache installations.
3. **MCP provisioning time** — MCPs can take 3-5 minutes to become Ready. Mitigation: reuse MCPs across scenarios where possible.
4. **Webhook deployment timing** — After MCP creation, the webhook may not be immediately available. Mitigation: poll for webhook endpoint readiness before testing admission.
5. **App installation state** — TC-03 requires toggling App installation state between runs. Mitigation: dedicated org with automation to uninstall before test.
