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
	"crypto/x509"
	"encoding/pem"
	"fmt"
	"time"

	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/controller/controllerutil"
	"sigs.k8s.io/controller-runtime/pkg/log"
	"sigs.k8s.io/controller-runtime/pkg/reconcile"

	integrationsv1alpha1 "github.com/maximilianbraun/platform-service-integration-gitops/api/v1alpha1"
)

const (
	providerFinalizer       = "gitops.integrations.open-control-plane.io/provider-finalizer"
	conditionCredentialsValid = "CredentialsValid"
	privateKeyField         = "private-key.pem"
	requeueInterval         = 5 * time.Minute
)

// ProviderReconciler reconciles GitProvider resources on the platform cluster.
type ProviderReconciler struct {
	client.Client
}

// +kubebuilder:rbac:groups=gitops.integrations.open-control-plane.io,resources=gitproviders,verbs=get;list;watch;update;patch
// +kubebuilder:rbac:groups=gitops.integrations.open-control-plane.io,resources=gitproviders/status,verbs=get;update;patch
// +kubebuilder:rbac:groups="",resources=secrets,verbs=get;list;watch
// +kubebuilder:rbac:groups="",resources=events,verbs=create;patch

func (r *ProviderReconciler) Reconcile(ctx context.Context, req ctrl.Request) (ctrl.Result, error) {
	logger := log.FromContext(ctx)

	provider := &integrationsv1alpha1.GitProvider{}
	if err := r.Get(ctx, req.NamespacedName, provider); err != nil {
		if apierrors.IsNotFound(err) {
			return ctrl.Result{}, nil
		}
		return ctrl.Result{}, err
	}

	if !provider.DeletionTimestamp.IsZero() {
		controllerutil.RemoveFinalizer(provider, providerFinalizer)
		return ctrl.Result{}, r.Update(ctx, provider)
	}

	if !controllerutil.ContainsFinalizer(provider, providerFinalizer) {
		controllerutil.AddFinalizer(provider, providerFinalizer)
		if err := r.Update(ctx, provider); err != nil {
			return ctrl.Result{}, err
		}
	}

	if provider.Spec.Type != integrationsv1alpha1.GitProviderTypeGitHubApp {
		logger.Info("unsupported provider type, skipping", "type", provider.Spec.Type)
		return ctrl.Result{}, nil
	}

	if provider.Spec.GitHubApp == nil {
		return r.setFailed(ctx, provider, "GitHubApp config is required for GitHubApp type providers")
	}

	secret := &corev1.Secret{}
	secretRef := provider.Spec.GitHubApp.PrivateKeySecretRef
	err := r.Get(ctx, types.NamespacedName{Name: secretRef.Name, Namespace: secretRef.Namespace}, secret)
	if err != nil {
		if apierrors.IsNotFound(err) {
			return r.setFailed(ctx, provider, fmt.Sprintf("private key secret %s/%s not found", secretRef.Namespace, secretRef.Name))
		}
		return ctrl.Result{}, err
	}

	keyData, ok := secret.Data[privateKeyField]
	if !ok {
		return r.setFailed(ctx, provider, fmt.Sprintf("secret %s/%s missing key %q", secretRef.Namespace, secretRef.Name, privateKeyField))
	}

	if err := validateRSAPrivateKey(keyData); err != nil {
		return r.setFailed(ctx, provider, fmt.Sprintf("invalid private key: %v", err))
	}

	// TODO: Fetch app slug via GET /app using a JWT to build accurate install URL.
	// For now, construct a best-effort URL using the app ID.
	installURL := fmt.Sprintf("https://%s/apps/%d/installations/new", provider.Spec.Host, provider.Spec.GitHubApp.AppId)

	provider.Status.Phase = integrationsv1alpha1.GitProviderPhaseReady
	provider.Status.InstallationUrl = installURL
	provider.Status.ObservedGeneration = provider.Generation
	setCondition(provider, conditionCredentialsValid, metav1.ConditionTrue, "Valid", "Private key is a valid RSA key")

	if err := r.Status().Update(ctx, provider); err != nil {
		return ctrl.Result{}, err
	}

	logger.Info("provider reconciled successfully", "phase", provider.Status.Phase)
	return ctrl.Result{RequeueAfter: requeueInterval}, nil
}

func (r *ProviderReconciler) setFailed(ctx context.Context, provider *integrationsv1alpha1.GitProvider, reason string) (reconcile.Result, error) {
	logger := log.FromContext(ctx)
	logger.Info("provider validation failed", "reason", reason)

	provider.Status.Phase = integrationsv1alpha1.GitProviderPhaseFailed
	provider.Status.ObservedGeneration = provider.Generation
	setCondition(provider, conditionCredentialsValid, metav1.ConditionFalse, "Invalid", reason)

	if err := r.Status().Update(ctx, provider); err != nil {
		return ctrl.Result{}, err
	}
	return ctrl.Result{RequeueAfter: requeueInterval}, nil
}

func (r *ProviderReconciler) SetupWithManager(mgr ctrl.Manager) error {
	return ctrl.NewControllerManagedBy(mgr).
		For(&integrationsv1alpha1.GitProvider{}).
		Named("gitprovider").
		Complete(r)
}

func validateRSAPrivateKey(data []byte) error {
	block, _ := pem.Decode(data)
	if block == nil {
		return fmt.Errorf("no PEM block found")
	}
	if _, err := x509.ParsePKCS1PrivateKey(block.Bytes); err != nil {
		// Try PKCS8 as fallback
		if _, err2 := x509.ParsePKCS8PrivateKey(block.Bytes); err2 != nil {
			return fmt.Errorf("not a valid PKCS1 or PKCS8 RSA private key: %v", err)
		}
	}
	return nil
}

func setCondition(provider *integrationsv1alpha1.GitProvider, condType string, status metav1.ConditionStatus, reason, message string) {
	now := metav1.Now()
	for i, c := range provider.Status.Conditions {
		if c.Type == condType {
			if c.Status != status {
				provider.Status.Conditions[i].LastTransitionTime = now
			}
			provider.Status.Conditions[i].Status = status
			provider.Status.Conditions[i].Reason = reason
			provider.Status.Conditions[i].Message = message
			provider.Status.Conditions[i].ObservedGeneration = provider.Generation
			return
		}
	}
	provider.Status.Conditions = append(provider.Status.Conditions, metav1.Condition{
		Type:               condType,
		Status:             status,
		LastTransitionTime: now,
		Reason:             reason,
		Message:            message,
		ObservedGeneration: provider.Generation,
	})
}
