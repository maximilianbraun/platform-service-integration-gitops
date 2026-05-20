# platform-service-integration-gitops

> Yes, `integration-service-gitops` would have been the better name. We'll get it right in v2.

A platform service for OpenMCP that provides project/workspace-scoped Git connections with automatic token management and secret provisioning across Managed Control Planes. Users create a `GitConnection` resource referencing their git hosting organization, and the controller handles token generation, rotation, and multi-MCP secret sync — no per-MCP manual secret management required.

## Architecture

This service runs as a hybrid **ServiceProvider + PlatformService** with two operational modes:

- **`--mode=platform`** (Connection Controller): Runs on the platform cluster. Watches `GitProvider` and `GitConnection` resources, generates tokens, and syncs secrets/ConfigMaps to MCPs in scope. Requeues at 50% token lifetime for proactive refresh.

- **`--mode=webhook`** (Mutating Webhook): Deployed on each MCP. Intercepts annotated `GitRepository`, `OCIRepository`, and notification `Provider` resources. Rewrites `spec.url` to the full host/org/repo form and injects `secretRef` — all from a local ConfigMap (no cross-cluster calls in the admission path).

```
Onboarding Cluster        Platform Cluster              MCP Clusters
──────────────────        ────────────────              ────────────
GitConnection ──watch──►  Connection Controller
                                  │
                                  ├── Resolve GitProvider
                                  ├── Generate short-lived token
                                  ├── Sync ConfigMap + Secret to MCPs
                                  └── Requeue at 50% lifetime
                                                        Mutating Webhook
                                                        ────────────────
                                                        On annotated resource:
                                                        ├── Read local ConfigMap
                                                        ├── Rewrite URL
                                                        └── Inject secretRef
```

## Quick Start

### 1. Platform Operator: Register a Git Provider

```yaml
apiVersion: integrations.open-control-plane.io/v1alpha1
kind: GitProvider
metadata:
  name: github-com
spec:
  host: "github.com"
  type: GitHubApp
  githubApp:
    appId: 987654
    privateKeySecretRef:
      name: github-app-key
      namespace: openmcp-system
```

See [`examples/platform/`](examples/platform/) for provider and secret templates.

### 2. End User: Create a GitConnection

```yaml
apiVersion: integrations.open-control-plane.io/v1alpha1
kind: GitConnection
metadata:
  name: my-org
  namespace: project-platform-team--ws-dev
spec:
  providerRef: github-com
  organization: "my-org"
  primary: true
```

See [`examples/user/gitconnection-primary.yaml`](examples/user/gitconnection-primary.yaml).

### 3. On MCP: Annotate Resources

```yaml
apiVersion: source.toolkit.fluxcd.io/v1
kind: GitRepository
metadata:
  name: infra
  namespace: flux-system
  annotations:
    integrations.open-control-plane.io/gitops-connection: auto
    integrations.open-control-plane.io/gitops-repo: infra-manifests
spec:
  interval: 5m
  url: changeme
  ref:
    branch: main
```

The webhook rewrites `url` to `https://github.com/my-org/infra-manifests` and injects `secretRef`.

## Building

```bash
go build ./...
go test ./...
make manifests    # regenerate CRDs
make build        # full build (fmt + vet + binary)
```

## Project Structure

```
├── api/v1alpha1/              # CRD type definitions (GitProvider, GitConnection)
├── cmd/                       # Service entrypoint (--mode=platform|webhook)
├── config/
│   ├── crd/                   # Generated CRD manifests
│   └── webhook-chart/         # Helm chart for MCP webhook deployment
├── examples/
│   ├── platform/              # Operator-managed resources (GitProvider, secrets)
│   └── user/                  # User-managed resources (GitConnection, annotated workloads)
├── internal/
│   ├── controller/            # Platform-mode controllers (provider, connection)
│   ├── providers/             # Token provider implementations (GitHub App, GitLab OAuth, etc.)
│   ├── sync/                  # Secret syncer (pushes tokens to MCPs)
│   └── webhook/               # MCP-mode mutating webhook
└── test/e2e/                  # End-to-end tests with mock git servers
```

## Links

- [Design Document](../docs/adrs/2026-05-20-git-connection-design.md)
- [Examples](examples/)
- [Webhook Helm Chart](config/webhook-chart/)
