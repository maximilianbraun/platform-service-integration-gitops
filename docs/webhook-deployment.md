# Webhook Deployment to MCPs

## Overview

The git-connection webhook is the same `platform-service-git-connection` binary running with `--mode=webhook`. It runs on each MCP as a mutating admission webhook, intercepting `GitRepository`, `OCIRepository`, and `Provider` resources to inject URLs and secret references based on `GitConnection` configuration.

The webhook is deployed to MCPs via a **HelmRelease on the platform cluster**. Flux running on the platform cluster reconciles the HelmRelease and installs the webhook remotely onto the target MCP using a kubeconfig secret.

## Trigger: When Does the HelmRelease Get Created?

The `ConnectionReconciler` creates the webhook HelmRelease the first time a `GitConnection` successfully resolves for an MCP's scope. Specifically:

1. A `GitConnection` resource is created in a project/workspace namespace on the onboarding cluster.
2. The controller resolves the `GitProvider`, validates the connection (app installed), and generates a token.
3. The controller enumerates all `ManagedControlPlaneV2` resources in the same namespace.
4. For each MCP, the controller syncs the `git-connections` ConfigMap and token Secret.
5. As part of this sync, the controller ensures a HelmRelease exists in the MCP's tenant namespace on the platform cluster.

If no `GitConnection` has ever resolved for an MCP, no webhook is deployed there.

## HelmRelease Lifecycle

### Creation

The HelmRelease is created in the MCP's **tenant namespace** on the platform cluster. This namespace follows the naming convention `mcp--<hash>`, computed by `StableMCPNamespace(onboardingName, onboardingNamespace)`.

```
Platform Cluster
└── namespace: mcp--<hash>
    ├── OCIRepository "git-connection-webhook"  (chart source)
    └── HelmRelease "git-connection-webhook"    (targets MCP via kubeconfig)
```

### Key Properties

| Property | Value |
|----------|-------|
| Namespace | `mcp--<hash>` (tenant namespace on platform cluster) |
| Chart source | OCIRepository referencing the webhook chart in OCI registry |
| Target cluster | MCP, via `spec.kubeConfig.secretRef` pointing to the MCP access secret |
| Target namespace | `flux-system` (where the webhook runs on the MCP) |
| Install strategy | Create namespace if missing, create CRDs, 3 retries |
| Upgrade strategy | CreateReplace CRDs, 3 retries with rollback |

### HelmRelease Spec (conceptual)

```yaml
apiVersion: helm.toolkit.fluxcd.io/v2
kind: HelmRelease
metadata:
  name: git-connection-webhook
  namespace: mcp--<hash>               # tenant namespace on platform cluster
spec:
  interval: 10m
  chartRef:
    kind: OCIRepository
    name: git-connection-webhook
    namespace: mcp--<hash>
  kubeConfig:
    secretRef:
      name: <mcp-access-secret>        # contains kubeconfig for the MCP
      key: kubeconfig
  targetNamespace: flux-system
  storageNamespace: flux-system
  install:
    createNamespace: true
    remediation:
      retries: 3
  upgrade:
    remediation:
      retries: 3
      strategy: Rollback
  values:
    image:
      repository: ghcr.io/openmcp-project/platform-service-git-connection
      tag: "0.1.0"
    replicaCount: 2
```

### Reconciliation

Flux on the platform cluster watches the HelmRelease and OCIRepository. When either changes, Flux:

1. Pulls the chart from the OCI registry.
2. Connects to the MCP using the kubeconfig secret.
3. Installs or upgrades the Helm release on the MCP cluster.

## What Gets Deployed on the MCP

The webhook Helm chart (`config/webhook-chart/`) installs the following resources into `flux-system` on the MCP:

### Deployment

```yaml
apiVersion: apps/v1
kind: Deployment
metadata:
  name: git-connection-webhook
  namespace: flux-system
spec:
  replicas: 2
  template:
    spec:
      containers:
        - name: webhook
          image: ghcr.io/openmcp-project/platform-service-git-connection:<tag>
          args:
            - --mode=webhook
            - --webhook-port=9443
            - --cert-dir=/tmp/k8s-webhook-server/serving-certs
            - --metrics-bind-address=:8080
            - --health-probe-bind-address=:8081
          volumeMounts:
            - name: cert
              mountPath: /tmp/k8s-webhook-server/serving-certs
              readOnly: true
      volumes:
        - name: cert
          secret:
            secretName: git-connection-webhook-tls
```

### Service

ClusterIP service exposing port 443, forwarding to the webhook container port (9443):

```yaml
apiVersion: v1
kind: Service
metadata:
  name: git-connection-webhook
  namespace: flux-system
spec:
  ports:
    - port: 443
      targetPort: webhook
  selector:
    app.kubernetes.io/name: git-connection-webhook
```

### MutatingWebhookConfiguration

Intercepts CREATE and UPDATE operations on Flux source and notification resources:

```yaml
apiVersion: admissionregistration.k8s.io/v1
kind: MutatingWebhookConfiguration
metadata:
  name: git-connection-webhook
  annotations:
    cert-manager.io/inject-ca-from: flux-system/git-connection-webhook-tls
webhooks:
  - name: gitops.integrations.open-control-plane.io
    rules:
      - apiGroups: ["source.toolkit.fluxcd.io"]
        resources: ["gitrepositories", "ocirepositories"]
        operations: ["CREATE", "UPDATE"]
      - apiGroups: ["notification.toolkit.fluxcd.io"]
        resources: ["providers"]
        operations: ["CREATE", "UPDATE"]
    failurePolicy: Fail
    objectSelector:
      matchExpressions:
        - key: integrations.open-control-plane.io/skip-webhook
          operator: DoesNotExist
```

Resources with the label `integrations.open-control-plane.io/skip-webhook` bypass mutation.

### RBAC

- **ServiceAccount** `git-connection-webhook` in `flux-system`
- **ClusterRole** with read access to ConfigMaps (used to look up `git-connections` ConfigMap)
- **ClusterRoleBinding** connecting the ServiceAccount to the ClusterRole

### TLS Certificates

The webhook requires TLS. Two options are supported:

#### Option A: cert-manager (recommended)

If cert-manager is installed on the MCP, the `MutatingWebhookConfiguration` annotation `cert-manager.io/inject-ca-from: flux-system/git-connection-webhook-tls` triggers automatic CA injection. A `Certificate` resource creates and rotates the `git-connection-webhook-tls` Secret.

#### Option B: Self-signed / pre-provisioned

If cert-manager is not available, the controller pre-generates a self-signed CA + serving certificate and includes them in the HelmRelease values. The chart creates the `git-connection-webhook-tls` Secret directly, and the `MutatingWebhookConfiguration` has `caBundle` set inline.

## Updates

The HelmRelease is updated when:

1. **New service version** -- When the `platform-service-git-connection` image tag changes (e.g., new release deployed), the OCIRepository detects the new chart version and Flux upgrades the HelmRelease.
2. **Chart values change** -- If the controller updates HelmRelease values (replica count, resource limits, image tag), Flux reconciles the change onto the MCP.
3. **Periodic reconciliation** -- Flux re-checks at the configured interval (default 10m) that the deployed state matches the desired state.

## Removal

When **all** `GitConnection` resources for an MCP's scope are deleted:

1. The `ConnectionReconciler` finalizer fires for each deleted `GitConnection`.
2. The finalizer cleans up managed Secrets and ConfigMap entries on the MCP.
3. When no `GitConnection` remains that targets the MCP, the controller deletes the HelmRelease from the tenant namespace.
4. Flux detects the HelmRelease deletion and uninstalls the Helm release from the MCP.
5. All webhook resources (Deployment, Service, MutatingWebhookConfiguration, RBAC, TLS Secret) are removed from the MCP.

## Flow Diagram

```
Onboarding Cluster              Platform Cluster                    MCP Cluster
──────────────────              ────────────────                    ───────────

GitConnection                   mcp--<hash> namespace
(project-X--ws-Y)              ┌──────────────────────────────┐
       │                        │                              │
       │  reconcile             │  OCIRepository               │
       ├───────────────────────►│  (webhook chart from OCI)    │
       │                        │         │                    │
       │                        │         ▼                    │
       │                        │  HelmRelease                 │
       │                        │  (kubeConfig → MCP secret)   │
       │                        │         │                    │
       │                        └─────────┼────────────────────┘
       │                                  │
       │                          Flux reconciles
       │                                  │
       │                                  ▼
       │                        ┌─────────────────────────────────────────┐
       │                        │  flux-system namespace (on MCP)         │
       │                        │                                         │
       │                        │  ┌─────────────────────────────────┐    │
       │                        │  │ Deployment                      │    │
       │                        │  │   --mode=webhook                │    │
       │                        │  │   --webhook-port=9443           │    │
       │                        │  └─────────────────────────────────┘    │
       │                        │                                         │
       │                        │  ┌─────────────────────────────────┐    │
       │                        │  │ Service (443 → 9443)            │    │
       │                        │  └─────────────────────────────────┘    │
       │                        │                                         │
       │                        │  ┌─────────────────────────────────┐    │
       │                        │  │ MutatingWebhookConfiguration    │    │
       │                        │  │   GitRepository, OCIRepository, │    │
       │                        │  │   Provider                      │    │
       │                        │  └─────────────────────────────────┘    │
       │                        │                                         │
       │  sync ConfigMap +      │  ┌─────────────────────────────────┐    │
       ├────────────────────────┼─►│ ConfigMap: git-connections      │    │
       │  sync Secret           │  │ Secret: git-connection-<name>   │    │
       │                        │  └─────────────────────────────────┘    │
       │                        └─────────────────────────────────────────┘
       │
       │  User creates annotated GitRepository
       │                                  │
       │                                  ▼
       │                        Webhook intercepts CREATE/UPDATE
       │                        ├── Reads ConfigMap (local)
       │                        ├── Rewrites spec.url
       │                        └── Injects spec.secretRef
```

## Summary

| Aspect | Detail |
|--------|--------|
| Binary | `platform-service-git-connection --mode=webhook` |
| Deployment mechanism | HelmRelease on platform cluster, Flux installs remotely |
| Trigger | First successful GitConnection reconcile for an MCP's scope |
| Location (platform) | Tenant namespace `mcp--<hash>` |
| Location (MCP) | `flux-system` namespace |
| Resources on MCP | Deployment, Service, MutatingWebhookConfiguration, ServiceAccount, ClusterRole, ClusterRoleBinding, TLS Secret |
| Update trigger | New chart version in OCI registry, or values change |
| Cleanup | HelmRelease deleted when no GitConnections remain for the MCP |
