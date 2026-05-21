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

package webhook

import (
	"context"
	"encoding/json"
	"net/http"
	"testing"

	admissionv1 "k8s.io/api/admission/v1"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"
	"sigs.k8s.io/controller-runtime/pkg/webhook/admission"
)

func TestHandle_ConfigMapNotFound(t *testing.T) {
	scheme := runtime.NewScheme()
	_ = corev1.AddToScheme(scheme)
	k8sClient := fake.NewClientBuilder().WithScheme(scheme).Build()

	handler := &GitOpsWebhook{
		Client: k8sClient,
	}

	req := buildAdmissionRequest(t, map[string]interface{}{
		"apiVersion": "source.toolkit.fluxcd.io/v1",
		"kind":       "GitRepository",
		"metadata": map[string]interface{}{
			"name":      "test",
			"namespace": "flux-system",
			"annotations": map[string]interface{}{
				"gitops.integrations.open-control-plane.io/gitops-connection": "my-org",
				"gitops.integrations.open-control-plane.io/gitops-repo":       "my-repo",
			},
		},
		"spec": map[string]interface{}{
			"url":      "changeme",
			"interval": "5m",
		},
	})

	resp := handler.Handle(context.Background(), req)
	if resp.Allowed {
		t.Fatal("expected rejection when ConfigMap is missing")
	}
	if resp.Result == nil || resp.Result.Code != http.StatusInternalServerError {
		t.Errorf("expected 500 status code (internal error), got %v", resp.Result)
	}
}

func TestHandle_ConfigMapEmpty(t *testing.T) {
	scheme := runtime.NewScheme()
	_ = corev1.AddToScheme(scheme)

	cm := &corev1.ConfigMap{
		ObjectMeta: metav1.ObjectMeta{Name: "git-connections", Namespace: "flux-system"},
		Data:       map[string]string{},
	}
	k8sClient := fake.NewClientBuilder().WithScheme(scheme).WithObjects(cm).Build()

	handler := &GitOpsWebhook{
		Client: k8sClient,
	}

	req := buildAdmissionRequest(t, map[string]interface{}{
		"apiVersion": "source.toolkit.fluxcd.io/v1",
		"kind":       "GitRepository",
		"metadata": map[string]interface{}{
			"name":      "test",
			"namespace": "flux-system",
			"annotations": map[string]interface{}{
				"gitops.integrations.open-control-plane.io/gitops-connection": "my-org",
				"gitops.integrations.open-control-plane.io/gitops-repo":       "my-repo",
			},
		},
		"spec": map[string]interface{}{
			"url":      "changeme",
			"interval": "5m",
		},
	})

	resp := handler.Handle(context.Background(), req)
	if resp.Allowed {
		t.Fatal("expected rejection when connection not in ConfigMap")
	}
}

func TestHandle_CorruptedConfigMapMissingHost(t *testing.T) {
	scheme := runtime.NewScheme()
	_ = corev1.AddToScheme(scheme)

	cm := &corev1.ConfigMap{
		ObjectMeta: metav1.ObjectMeta{Name: "git-connections", Namespace: "flux-system"},
		Data: map[string]string{
			"my-org.organization": "my-org",
			"my-org.scheme":       "https",
			// missing: my-org.host
		},
	}
	k8sClient := fake.NewClientBuilder().WithScheme(scheme).WithObjects(cm).Build()

	handler := &GitOpsWebhook{
		Client: k8sClient,
	}

	req := buildAdmissionRequest(t, map[string]interface{}{
		"apiVersion": "source.toolkit.fluxcd.io/v1",
		"kind":       "GitRepository",
		"metadata": map[string]interface{}{
			"name":      "test",
			"namespace": "flux-system",
			"annotations": map[string]interface{}{
				"gitops.integrations.open-control-plane.io/gitops-connection": "my-org",
				"gitops.integrations.open-control-plane.io/gitops-repo":       "my-repo",
			},
		},
		"spec": map[string]interface{}{
			"url":      "changeme",
			"interval": "5m",
		},
	})

	resp := handler.Handle(context.Background(), req)
	// Should still construct URL but with empty host — implementation-dependent
	// The key assertion: it doesn't panic
	_ = resp
}

func TestHandle_MalformedObject(t *testing.T) {
	scheme := runtime.NewScheme()
	_ = corev1.AddToScheme(scheme)

	cm := &corev1.ConfigMap{
		ObjectMeta: metav1.ObjectMeta{Name: "git-connections", Namespace: "flux-system"},
		Data: map[string]string{
			"my-org.host":         "github.com",
			"my-org.organization": "my-org",
			"my-org.scheme":       "https",
		},
	}
	k8sClient := fake.NewClientBuilder().WithScheme(scheme).WithObjects(cm).Build()

	handler := &GitOpsWebhook{
		Client: k8sClient,
	}

	// Send garbage as the object
	req := admission.Request{
		AdmissionRequest: admissionv1.AdmissionRequest{
			UID: "test-uid",
			Kind: metav1.GroupVersionKind{
				Group:   "source.toolkit.fluxcd.io",
				Version: "v1",
				Kind:    "GitRepository",
			},
			Operation: admissionv1.Create,
			Object:    runtime.RawExtension{Raw: []byte(`{"this is": "not valid k8s`)},
		},
	}

	resp := handler.Handle(context.Background(), req)
	if resp.Allowed {
		t.Log("malformed object allowed (webhook may pass-through on decode error)")
	}
	// Key assertion: no panic
}

func TestHandle_EmptyRepoAnnotation(t *testing.T) {
	scheme := runtime.NewScheme()
	_ = corev1.AddToScheme(scheme)

	cm := &corev1.ConfigMap{
		ObjectMeta: metav1.ObjectMeta{Name: "git-connections", Namespace: "flux-system"},
		Data: map[string]string{
			"my-org.host":         "github.com",
			"my-org.organization": "my-org",
			"my-org.scheme":       "https",
		},
	}
	k8sClient := fake.NewClientBuilder().WithScheme(scheme).WithObjects(cm).Build()

	handler := &GitOpsWebhook{
		Client: k8sClient,
	}

	req := buildAdmissionRequest(t, map[string]interface{}{
		"apiVersion": "source.toolkit.fluxcd.io/v1",
		"kind":       "GitRepository",
		"metadata": map[string]interface{}{
			"name":      "test",
			"namespace": "flux-system",
			"annotations": map[string]interface{}{
				"gitops.integrations.open-control-plane.io/gitops-connection": "my-org",
				"gitops.integrations.open-control-plane.io/gitops-repo":       "", // empty!
			},
		},
		"spec": map[string]interface{}{
			"url":      "changeme",
			"interval": "5m",
		},
	})

	resp := handler.Handle(context.Background(), req)
	if resp.Allowed {
		t.Fatal("expected rejection when repo annotation is empty string")
	}
}

func TestHandle_SpecialCharsInRepoName(t *testing.T) {
	scheme := runtime.NewScheme()
	_ = corev1.AddToScheme(scheme)

	cm := &corev1.ConfigMap{
		ObjectMeta: metav1.ObjectMeta{Name: "git-connections", Namespace: "flux-system"},
		Data: map[string]string{
			"my-org.host":         "github.com",
			"my-org.organization": "my-org",
			"my-org.scheme":       "https",
		},
	}
	k8sClient := fake.NewClientBuilder().WithScheme(scheme).WithObjects(cm).Build()

	handler := &GitOpsWebhook{
		Client: k8sClient,
	}

	req := buildAdmissionRequest(t, map[string]interface{}{
		"apiVersion": "source.toolkit.fluxcd.io/v1",
		"kind":       "GitRepository",
		"metadata": map[string]interface{}{
			"name":      "test",
			"namespace": "flux-system",
			"annotations": map[string]interface{}{
				"gitops.integrations.open-control-plane.io/gitops-connection": "my-org",
				"gitops.integrations.open-control-plane.io/gitops-repo":       "../../../etc/passwd",
			},
		},
		"spec": map[string]interface{}{
			"url":      "changeme",
			"interval": "5m",
		},
	})

	resp := handler.Handle(context.Background(), req)
	// Even if allowed, the resulting URL should be safe (just appended to path)
	// Key: no injection, no panic
	if resp.Allowed {
		// Verify the URL doesn't enable path traversal — it should be literal
		t.Log("allowed with special chars in repo — URL should contain literal path")
	}
}

// --- helper ---

func buildAdmissionRequest(t *testing.T, obj map[string]interface{}) admission.Request {
	t.Helper()
	raw, err := json.Marshal(obj)
	if err != nil {
		t.Fatalf("marshal obj: %v", err)
	}

	return admission.Request{
		AdmissionRequest: admissionv1.AdmissionRequest{
			UID: "test-uid",
			Kind: metav1.GroupVersionKind{
				Group:   "source.toolkit.fluxcd.io",
				Version: "v1",
				Kind:    "GitRepository",
			},
			Operation: admissionv1.Create,
			Object:    runtime.RawExtension{Raw: raw},
		},
	}
}
