/*
Copyright 2026.

Licensed under the Apache License, Version 2.0 (the "License");
you may not use this file except in compliance with the License.
You may obtain a copy of the License at

    http://www.apache.org/licenses/LICENSE-2.0

Unless required by applicable law or agreed to in writing, software
distributed under the License is distributed on an "AS IS" BASIS,
WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
See the License for the specific language governing permissions and
limitations under the License.
*/

package controller

import (
	"context"
	"fmt"
	"time"

	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/client-go/tools/clientcmd"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/controller/controllerutil"
	"sigs.k8s.io/controller-runtime/pkg/handler"
	"sigs.k8s.io/controller-runtime/pkg/log"
	"sigs.k8s.io/controller-runtime/pkg/reconcile"

	integrationsv1alpha1 "github.com/openmcp-project/platform-service-integration-gitops/api/v1alpha1"
	"github.com/openmcp-project/platform-service-integration-gitops/internal/providers"

	corev2alpha1 "github.com/openmcp-project/openmcp-operator/api/core/v2alpha1"
)

const (
	connectionFinalizer   = "integrations.open-control-plane.io/connection-finalizer"
	secretPrefix          = "git-connection-"
	configMapName         = "git-connections"
	targetNamespace       = "flux-system"
	managedByLabel        = "integrations.open-control-plane.io/managed-by"
	connectionLabel       = "integrations.open-control-plane.io/connection"
	pollIntervalNoInstall = 60 * time.Second
)

// ConnectionReconciler reconciles GitConnection resources on the onboarding cluster.
// One GitConnection maps to N MCPs in the same namespace.
type ConnectionReconciler struct {
	// Client for the onboarding cluster (where GitConnection + ManagedControlPlaneV2 live)
	Client client.Client
	// PlatformClient for reading GitProvider + private key secrets
	PlatformClient client.Client
	// Registry maps provider types to token generators
	Registry *providers.Registry
}

// +kubebuilder:rbac:groups=integrations.open-control-plane.io,resources=gitconnections,verbs=get;list;watch;update;patch
// +kubebuilder:rbac:groups=integrations.open-control-plane.io,resources=gitconnections/status,verbs=get;update;patch
// +kubebuilder:rbac:groups=core.openmcp.cloud,resources=managedcontrolplanev2s,verbs=get;list;watch
// +kubebuilder:rbac:groups="",resources=secrets;configmaps,verbs=get;list;watch;create;update;patch;delete
// +kubebuilder:rbac:groups="",resources=events,verbs=create;patch

func (r *ConnectionReconciler) Reconcile(ctx context.Context, req ctrl.Request) (ctrl.Result, error) {
	logger := log.FromContext(ctx)

	conn := &integrationsv1alpha1.GitConnection{}
	if err := r.Client.Get(ctx, req.NamespacedName, conn); err != nil {
		if apierrors.IsNotFound(err) {
			return ctrl.Result{}, nil
		}
		return ctrl.Result{}, err
	}

	if !conn.DeletionTimestamp.IsZero() {
		return r.reconcileDelete(ctx, conn)
	}

	if !controllerutil.ContainsFinalizer(conn, connectionFinalizer) {
		controllerutil.AddFinalizer(conn, connectionFinalizer)
		if err := r.Client.Update(ctx, conn); err != nil {
			return ctrl.Result{}, err
		}
	}

	// Step 1: Resolve the GitProvider from platform cluster
	provider := &integrationsv1alpha1.GitProvider{}
	if err := r.PlatformClient.Get(ctx, types.NamespacedName{Name: conn.Spec.ProviderRef}, provider); err != nil {
		if apierrors.IsNotFound(err) {
			return r.setConnectionPhase(ctx, conn, integrationsv1alpha1.GitConnectionPhaseFailed,
				"ProviderNotFound", fmt.Sprintf("GitProvider %q not found", conn.Spec.ProviderRef))
		}
		return ctrl.Result{}, err
	}

	if provider.Status.Phase != integrationsv1alpha1.GitProviderPhaseReady {
		return r.setConnectionPhase(ctx, conn, integrationsv1alpha1.GitConnectionPhasePending,
			"ProviderNotReady", fmt.Sprintf("GitProvider %q is not Ready", conn.Spec.ProviderRef))
	}

	// Step 2: Select TokenProvider and validate the connection
	tokenProvider, err := r.Registry.Get(provider.Spec.Type)
	if err != nil {
		return r.setConnectionPhase(ctx, conn, integrationsv1alpha1.GitConnectionPhaseFailed,
			"UnsupportedProvider", err.Error())
	}

	if err := tokenProvider.ValidateConnection(ctx, provider, conn); err != nil {
		// App not installed → set phase and installUrl, requeue to poll
		conn.Status.Phase = integrationsv1alpha1.GitConnectionPhaseAppNotInstalled
		conn.Status.InstallUrl = provider.Status.InstallationUrl
		setConnectionCondition(conn, integrationsv1alpha1.ConditionTypeAppInstalled,
			metav1.ConditionFalse, "NotFound", err.Error())
		if err := r.Client.Status().Update(ctx, conn); err != nil {
			return ctrl.Result{}, err
		}
		logger.Info("app not installed, polling", "organization", conn.Spec.Organization)
		return ctrl.Result{RequeueAfter: pollIntervalNoInstall}, nil
	}

	setConnectionCondition(conn, integrationsv1alpha1.ConditionTypeAppInstalled,
		metav1.ConditionTrue, "Installed", "App is installed on the organization")

	// Step 3: Generate token
	conn.Status.Phase = integrationsv1alpha1.GitConnectionPhaseProgressing
	token, err := tokenProvider.GenerateToken(ctx, provider, conn)
	if err != nil {
		return r.setConnectionPhase(ctx, conn, integrationsv1alpha1.GitConnectionPhaseFailed,
			"TokenGenerationFailed", fmt.Sprintf("failed to generate token: %v", err))
	}

	if token == nil {
		return r.setConnectionPhase(ctx, conn, integrationsv1alpha1.GitConnectionPhaseFailed,
			"TokenGenerationFailed", "token provider returned nil token")
	}

	expiresAt := metav1.NewTime(token.ExpiresAt)
	conn.Status.TokenExpiresAt = &expiresAt
	setConnectionCondition(conn, integrationsv1alpha1.ConditionTypeTokenValid,
		metav1.ConditionTrue, "Valid", fmt.Sprintf("Token expires at %s", token.ExpiresAt.Format(time.RFC3339)))

	// Step 4: Enumerate all MCPs in this namespace
	mcpList := &corev2alpha1.ManagedControlPlaneV2List{}
	if err := r.Client.List(ctx, mcpList, client.InNamespace(conn.Namespace)); err != nil {
		return ctrl.Result{}, fmt.Errorf("listing MCPs in namespace %s: %w", conn.Namespace, err)
	}

	// Step 5: Sync secrets to each MCP
	secretData, secretType, err := tokenProvider.SecretData(token, "GitRepository")
	if err != nil {
		return r.setConnectionPhase(ctx, conn, integrationsv1alpha1.GitConnectionPhaseFailed,
			"SecretFormatError", fmt.Sprintf("failed to format secret data: %v", err))
	}

	var managedSecrets []integrationsv1alpha1.ManagedSecret
	var syncErrors []error

	for i := range mcpList.Items {
		mcp := &mcpList.Items[i]
		if mcp.DeletionTimestamp != nil {
			continue
		}

		mcpClient, err := r.mcpClientFromAccess(ctx, mcp)
		if err != nil {
			logger.Error(err, "failed to get MCP client", "mcp", mcp.Name)
			syncErrors = append(syncErrors, err)
			continue
		}

		if err := r.syncConfigMap(ctx, mcpClient, conn, provider); err != nil {
			logger.Error(err, "failed to sync ConfigMap", "mcp", mcp.Name)
			syncErrors = append(syncErrors, err)
			continue
		}

		if err := r.syncSecret(ctx, mcpClient, conn, secretData, secretType); err != nil {
			logger.Error(err, "failed to sync Secret", "mcp", mcp.Name)
			syncErrors = append(syncErrors, err)
			continue
		}

		project, workspace := parseNamespace(conn.Namespace)
		managedSecrets = append(managedSecrets, integrationsv1alpha1.ManagedSecret{
			MCP:        mcp.Name,
			Project:    project,
			Workspace:  workspace,
			SecretName: secretPrefix + conn.Name,
			Namespace:  targetNamespace,
		})
	}

	conn.Status.ManagedSecrets = managedSecrets

	if len(syncErrors) > 0 {
		setConnectionCondition(conn, integrationsv1alpha1.ConditionTypeSecretsSynced,
			metav1.ConditionFalse, "PartialSync",
			fmt.Sprintf("synced to %d/%d MCPs, %d errors", len(managedSecrets), len(mcpList.Items), len(syncErrors)))
	} else {
		setConnectionCondition(conn, integrationsv1alpha1.ConditionTypeSecretsSynced,
			metav1.ConditionTrue, "Synced",
			fmt.Sprintf("synced to %d MCPs", len(managedSecrets)))
	}

	// Step 6: Set final status
	conn.Status.Phase = integrationsv1alpha1.GitConnectionPhaseReady
	conn.Status.InstallUrl = ""
	conn.Status.ObservedGeneration = conn.Generation
	if err := r.Client.Status().Update(ctx, conn); err != nil {
		return ctrl.Result{}, err
	}

	// Requeue at 50% of token lifetime for proactive refresh
	requeueAfter := time.Until(token.ExpiresAt) / 2
	if requeueAfter < 30*time.Second {
		requeueAfter = 30 * time.Second
	}

	logger.Info("connection reconciled", "phase", conn.Status.Phase, "mcps", len(managedSecrets), "requeueAfter", requeueAfter)
	return ctrl.Result{RequeueAfter: requeueAfter}, nil
}

func (r *ConnectionReconciler) reconcileDelete(ctx context.Context, conn *integrationsv1alpha1.GitConnection) (ctrl.Result, error) {
	logger := log.FromContext(ctx)

	// Clean up managed secrets from all MCPs
	mcpList := &corev2alpha1.ManagedControlPlaneV2List{}
	if err := r.Client.List(ctx, mcpList, client.InNamespace(conn.Namespace)); err != nil {
		logger.Error(err, "failed to list MCPs for cleanup")
	} else {
		for i := range mcpList.Items {
			mcp := &mcpList.Items[i]
			mcpClient, err := r.mcpClientFromAccess(ctx, mcp)
			if err != nil {
				logger.Error(err, "failed to get MCP client for cleanup", "mcp", mcp.Name)
				continue
			}
			r.deleteSecret(ctx, mcpClient, conn)
			r.removeFromConfigMap(ctx, mcpClient, conn)
		}
	}

	controllerutil.RemoveFinalizer(conn, connectionFinalizer)
	return ctrl.Result{}, r.Client.Update(ctx, conn)
}

func (r *ConnectionReconciler) setConnectionPhase(ctx context.Context, conn *integrationsv1alpha1.GitConnection, phase integrationsv1alpha1.GitConnectionPhase, reason, message string) (ctrl.Result, error) {
	conn.Status.Phase = phase
	conn.Status.ObservedGeneration = conn.Generation

	condType := integrationsv1alpha1.ConditionTypeTokenValid
	if phase == integrationsv1alpha1.GitConnectionPhaseFailed || phase == integrationsv1alpha1.GitConnectionPhasePending {
		setConnectionCondition(conn, condType, metav1.ConditionFalse, reason, message)
	}

	if err := r.Client.Status().Update(ctx, conn); err != nil {
		return ctrl.Result{}, err
	}
	return ctrl.Result{RequeueAfter: requeueInterval}, nil
}

// mcpClientFromAccess builds a client for the MCP using its access secret.
func (r *ConnectionReconciler) mcpClientFromAccess(ctx context.Context, mcp *corev2alpha1.ManagedControlPlaneV2) (client.Client, error) {
	accessRef, ok := mcp.Status.Access["default"]
	if !ok {
		return nil, fmt.Errorf("MCP %s has no default access", mcp.Name)
	}

	secret := &corev1.Secret{}
	if err := r.Client.Get(ctx, types.NamespacedName{
		Name:      accessRef.Name,
		Namespace: mcp.Namespace,
	}, secret); err != nil {
		return nil, fmt.Errorf("reading access secret for MCP %s: %w", mcp.Name, err)
	}

	kubeconfigData, ok := secret.Data["kubeconfig"]
	if !ok {
		return nil, fmt.Errorf("access secret for MCP %s missing 'kubeconfig' key", mcp.Name)
	}

	restConfig, err := clientcmd.RESTConfigFromKubeConfig(kubeconfigData)
	if err != nil {
		return nil, fmt.Errorf("building REST config for MCP %s: %w", mcp.Name, err)
	}

	mcpClient, err := client.New(restConfig, client.Options{})
	if err != nil {
		return nil, fmt.Errorf("creating client for MCP %s: %w", mcp.Name, err)
	}

	return mcpClient, nil
}

func (r *ConnectionReconciler) syncConfigMap(ctx context.Context, mcpClient client.Client, conn *integrationsv1alpha1.GitConnection, provider *integrationsv1alpha1.GitProvider) error {
	cm := &corev1.ConfigMap{}
	key := types.NamespacedName{Name: configMapName, Namespace: targetNamespace}
	err := mcpClient.Get(ctx, key, cm)
	if apierrors.IsNotFound(err) {
		cm = &corev1.ConfigMap{
			ObjectMeta: metav1.ObjectMeta{
				Name:      configMapName,
				Namespace: targetNamespace,
				Labels: map[string]string{
					managedByLabel: "true",
				},
			},
			Data: map[string]string{},
		}
		return mcpClient.Create(ctx, cm)
	}
	if err != nil {
		return err
	}

	if cm.Data == nil {
		cm.Data = map[string]string{}
	}
	cm.Data[conn.Name+".host"] = provider.Spec.Host
	cm.Data[conn.Name+".organization"] = conn.Spec.Organization
	cm.Data[conn.Name+".scheme"] = "https"
	if conn.Spec.Primary {
		cm.Data[conn.Name+".primary"] = "true"
	} else {
		delete(cm.Data, conn.Name+".primary")
	}

	return mcpClient.Update(ctx, cm)
}

func (r *ConnectionReconciler) syncSecret(ctx context.Context, mcpClient client.Client, conn *integrationsv1alpha1.GitConnection, data map[string][]byte, secretType corev1.SecretType) error {
	secretName := secretPrefix + conn.Name
	secret := &corev1.Secret{}
	key := types.NamespacedName{Name: secretName, Namespace: targetNamespace}

	err := mcpClient.Get(ctx, key, secret)
	if apierrors.IsNotFound(err) {
		secret = &corev1.Secret{
			ObjectMeta: metav1.ObjectMeta{
				Name:      secretName,
				Namespace: targetNamespace,
				Labels: map[string]string{
					managedByLabel:  "true",
					connectionLabel: conn.Name,
				},
			},
			Type: secretType,
			Data: data,
		}
		return mcpClient.Create(ctx, secret)
	}
	if err != nil {
		return err
	}

	secret.Data = data
	secret.Type = secretType
	if secret.Labels == nil {
		secret.Labels = map[string]string{}
	}
	secret.Labels[managedByLabel] = "true"
	secret.Labels[connectionLabel] = conn.Name

	return mcpClient.Update(ctx, secret)
}

func (r *ConnectionReconciler) deleteSecret(ctx context.Context, mcpClient client.Client, conn *integrationsv1alpha1.GitConnection) {
	logger := log.FromContext(ctx)
	secretName := secretPrefix + conn.Name
	secret := &corev1.Secret{
		ObjectMeta: metav1.ObjectMeta{
			Name:      secretName,
			Namespace: targetNamespace,
		},
	}
	if err := mcpClient.Delete(ctx, secret); err != nil && !apierrors.IsNotFound(err) {
		logger.Error(err, "failed to delete secret from MCP", "secret", secretName)
	}
}

func (r *ConnectionReconciler) removeFromConfigMap(ctx context.Context, mcpClient client.Client, conn *integrationsv1alpha1.GitConnection) {
	logger := log.FromContext(ctx)
	cm := &corev1.ConfigMap{}
	key := types.NamespacedName{Name: configMapName, Namespace: targetNamespace}
	if err := mcpClient.Get(ctx, key, cm); err != nil {
		if !apierrors.IsNotFound(err) {
			logger.Error(err, "failed to get configmap for cleanup")
		}
		return
	}

	delete(cm.Data, conn.Name+".host")
	delete(cm.Data, conn.Name+".organization")
	delete(cm.Data, conn.Name+".scheme")
	delete(cm.Data, conn.Name+".primary")

	if err := mcpClient.Update(ctx, cm); err != nil {
		logger.Error(err, "failed to update configmap after cleanup")
	}
}

// parseNamespace extracts project and workspace from a namespace name.
// Convention: "project-<project>--ws-<workspace>" for workspace namespaces,
// "project-<project>" for project namespaces.
func parseNamespace(ns string) (project, workspace string) {
	// TODO: Use openmcp-operator utility functions for proper parsing
	// For now, return the namespace as project and empty workspace
	return ns, ""
}

func (r *ConnectionReconciler) SetupWithManager(mgr ctrl.Manager) error {
	return ctrl.NewControllerManagedBy(mgr).
		For(&integrationsv1alpha1.GitConnection{}).
		Watches(
			&corev2alpha1.ManagedControlPlaneV2{},
			handler.EnqueueRequestsFromMapFunc(r.mapMCPToConnections),
		).
		Named("gitconnection").
		Complete(r)
}

// mapMCPToConnections triggers reconciliation of all GitConnections in the same namespace
// when a new MCP appears or changes.
func (r *ConnectionReconciler) mapMCPToConnections(ctx context.Context, obj client.Object) []reconcile.Request {
	connList := &integrationsv1alpha1.GitConnectionList{}
	if err := r.Client.List(ctx, connList, client.InNamespace(obj.GetNamespace())); err != nil {
		return nil
	}

	requests := make([]reconcile.Request, len(connList.Items))
	for i, conn := range connList.Items {
		requests[i] = reconcile.Request{
			NamespacedName: types.NamespacedName{
				Name:      conn.Name,
				Namespace: conn.Namespace,
			},
		}
	}
	return requests
}

func setConnectionCondition(conn *integrationsv1alpha1.GitConnection, condType string, status metav1.ConditionStatus, reason, message string) {
	now := metav1.Now()
	for i, c := range conn.Status.Conditions {
		if c.Type == condType {
			if c.Status != status {
				conn.Status.Conditions[i].LastTransitionTime = now
			}
			conn.Status.Conditions[i].Status = status
			conn.Status.Conditions[i].Reason = reason
			conn.Status.Conditions[i].Message = message
			conn.Status.Conditions[i].ObservedGeneration = conn.Generation
			return
		}
	}
	conn.Status.Conditions = append(conn.Status.Conditions, metav1.Condition{
		Type:               condType,
		Status:             status,
		LastTransitionTime: now,
		Reason:             reason,
		Message:            message,
		ObservedGeneration: conn.Generation,
	})
}
