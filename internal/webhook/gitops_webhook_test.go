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
	"testing"

	admissionv1 "k8s.io/api/admission/v1"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"
	"sigs.k8s.io/controller-runtime/pkg/webhook/admission"
)

func configMap() *corev1.ConfigMap {
	return &corev1.ConfigMap{
		ObjectMeta: metav1.ObjectMeta{Name: ConfigMapName, Namespace: ConfigMapNamespace},
		Data: map[string]string{
			"my-org.host":         "github.com",
			"my-org.organization": "my-org",
			"my-org.scheme":       "https",
			"my-org.primary":      "true",
			"other.host":          "gitlab.example.com",
			"other.organization":  "other-team",
			"other.scheme":        "https",
		},
	}
}

func makeRequest(kind string, annotations map[string]string, hasSecretRef bool) admission.Request {
	obj := map[string]interface{}{
		"metadata": map[string]interface{}{
			"name":        "test-resource",
			"namespace":   "flux-system",
			"annotations": annotations,
		},
		"spec": map[string]interface{}{
			"url": "changeme",
		},
	}
	if kind == "Provider" {
		obj["spec"] = map[string]interface{}{
			"type":    "github",
			"address": "changeme",
		}
	}
	if hasSecretRef {
		spec := obj["spec"].(map[string]interface{})
		spec["secretRef"] = map[string]interface{}{"name": "old-secret"}
	}

	raw, _ := json.Marshal(obj)
	return admission.Request{
		AdmissionRequest: admissionv1.AdmissionRequest{
			Name:      "test-resource",
			Namespace: "flux-system",
			Kind:      metav1.GroupVersionKind{Kind: kind},
			Object:    runtime.RawExtension{Raw: raw},
		},
	}
}

func TestHandle_NoAnnotations(t *testing.T) {
	scheme := runtime.NewScheme()
	_ = corev1.AddToScheme(scheme)
	fakeClient := fake.NewClientBuilder().WithScheme(scheme).WithObjects(configMap()).Build()
	wh := NewGitOpsWebhook(fakeClient)

	req := makeRequest("GitRepository", nil, false)
	resp := wh.Handle(context.Background(), req)
	if !resp.Allowed {
		t.Fatalf("expected allowed, got denied: %s", resp.Result.Message)
	}
}

func TestHandle_NoConnectionAnnotation(t *testing.T) {
	scheme := runtime.NewScheme()
	_ = corev1.AddToScheme(scheme)
	fakeClient := fake.NewClientBuilder().WithScheme(scheme).WithObjects(configMap()).Build()
	wh := NewGitOpsWebhook(fakeClient)

	req := makeRequest("GitRepository", map[string]string{"unrelated": "value"}, false)
	resp := wh.Handle(context.Background(), req)
	if !resp.Allowed {
		t.Fatalf("expected allowed, got denied: %s", resp.Result.Message)
	}
}

func TestHandle_GitRepository_NamedConnection(t *testing.T) {
	scheme := runtime.NewScheme()
	_ = corev1.AddToScheme(scheme)
	fakeClient := fake.NewClientBuilder().WithScheme(scheme).WithObjects(configMap()).Build()
	wh := NewGitOpsWebhook(fakeClient)

	req := makeRequest("GitRepository", map[string]string{
		AnnotationConnection: "my-org",
		AnnotationRepo:       "infra-manifests",
	}, false)

	resp := wh.Handle(context.Background(), req)
	if !resp.Allowed {
		t.Fatalf("expected allowed, got denied: %s", resp.Result.Message)
	}
	if resp.Patches == nil {
		t.Fatal("expected patches")
	}

	var foundURL, foundSecretRef bool
	for _, p := range resp.Patches {
		if p.Path == "/spec/url" && p.Value == "https://github.com/my-org/infra-manifests" {
			foundURL = true
		}
		if p.Path == "/spec/secretRef" {
			foundSecretRef = true
		}
	}
	if !foundURL {
		t.Fatalf("expected URL patch, got patches: %+v", resp.Patches)
	}
	if !foundSecretRef {
		t.Fatalf("expected secretRef patch, got patches: %+v", resp.Patches)
	}
}

func TestHandle_GitRepository_PrimaryConnection(t *testing.T) {
	scheme := runtime.NewScheme()
	_ = corev1.AddToScheme(scheme)
	fakeClient := fake.NewClientBuilder().WithScheme(scheme).WithObjects(configMap()).Build()
	wh := NewGitOpsWebhook(fakeClient)

	req := makeRequest("GitRepository", map[string]string{
		AnnotationConnection: "auto",
		AnnotationRepo:       "app-configs",
	}, false)

	resp := wh.Handle(context.Background(), req)
	if !resp.Allowed {
		t.Fatalf("expected allowed, got denied: %s", resp.Result.Message)
	}

	var foundURL bool
	for _, p := range resp.Patches {
		if p.Path == "/spec/url" && p.Value == "https://github.com/my-org/app-configs" {
			foundURL = true
		}
	}
	if !foundURL {
		t.Fatalf("expected URL using primary connection, got patches: %+v", resp.Patches)
	}
}

func TestHandle_GitRepository_OrgOverride(t *testing.T) {
	scheme := runtime.NewScheme()
	_ = corev1.AddToScheme(scheme)
	fakeClient := fake.NewClientBuilder().WithScheme(scheme).WithObjects(configMap()).Build()
	wh := NewGitOpsWebhook(fakeClient)

	req := makeRequest("GitRepository", map[string]string{
		AnnotationConnection: "my-org",
		AnnotationRepo:       "base-configs",
		AnnotationOrg:        "shared-infra",
	}, false)

	resp := wh.Handle(context.Background(), req)
	if !resp.Allowed {
		t.Fatalf("expected allowed, got denied: %s", resp.Result.Message)
	}

	var foundURL bool
	for _, p := range resp.Patches {
		if p.Path == "/spec/url" && p.Value == "https://github.com/shared-infra/base-configs" {
			foundURL = true
		}
	}
	if !foundURL {
		t.Fatalf("expected URL with org override, got patches: %+v", resp.Patches)
	}
}

func TestHandle_ConnectionNotFound(t *testing.T) {
	scheme := runtime.NewScheme()
	_ = corev1.AddToScheme(scheme)
	fakeClient := fake.NewClientBuilder().WithScheme(scheme).WithObjects(configMap()).Build()
	wh := NewGitOpsWebhook(fakeClient)

	req := makeRequest("GitRepository", map[string]string{
		AnnotationConnection: "nonexistent",
		AnnotationRepo:       "some-repo",
	}, false)

	resp := wh.Handle(context.Background(), req)
	if resp.Allowed {
		t.Fatal("expected denied for nonexistent connection")
	}
	msg := resp.Result.Message
	if msg == "" {
		t.Fatal("expected error message")
	}
	if !contains(msg, "nonexistent") || !contains(msg, "not available") {
		t.Fatalf("expected error mentioning connection name and availability, got: %s", msg)
	}
}

func TestHandle_MissingRepoAnnotation(t *testing.T) {
	scheme := runtime.NewScheme()
	_ = corev1.AddToScheme(scheme)
	fakeClient := fake.NewClientBuilder().WithScheme(scheme).WithObjects(configMap()).Build()
	wh := NewGitOpsWebhook(fakeClient)

	req := makeRequest("GitRepository", map[string]string{
		AnnotationConnection: "my-org",
	}, false)

	resp := wh.Handle(context.Background(), req)
	if resp.Allowed {
		t.Fatal("expected denied for missing repo annotation")
	}
	if !contains(resp.Result.Message, AnnotationRepo) {
		t.Fatalf("expected error mentioning missing annotation, got: %s", resp.Result.Message)
	}
}

func TestHandle_Provider_AddressRewrite(t *testing.T) {
	scheme := runtime.NewScheme()
	_ = corev1.AddToScheme(scheme)
	fakeClient := fake.NewClientBuilder().WithScheme(scheme).WithObjects(configMap()).Build()
	wh := NewGitOpsWebhook(fakeClient)

	req := makeRequest("Provider", map[string]string{
		AnnotationConnection: "my-org",
		AnnotationRepo:       "infra-manifests",
	}, false)

	resp := wh.Handle(context.Background(), req)
	if !resp.Allowed {
		t.Fatalf("expected allowed, got denied: %s", resp.Result.Message)
	}

	var foundAddress bool
	for _, p := range resp.Patches {
		if p.Path == "/spec/address" && p.Value == "https://github.com/my-org/infra-manifests" {
			foundAddress = true
		}
	}
	if !foundAddress {
		t.Fatalf("expected address patch for Provider kind, got patches: %+v", resp.Patches)
	}
}

func TestHandle_ExistingSecretRef_Replace(t *testing.T) {
	scheme := runtime.NewScheme()
	_ = corev1.AddToScheme(scheme)
	fakeClient := fake.NewClientBuilder().WithScheme(scheme).WithObjects(configMap()).Build()
	wh := NewGitOpsWebhook(fakeClient)

	req := makeRequest("GitRepository", map[string]string{
		AnnotationConnection: "my-org",
		AnnotationRepo:       "infra-manifests",
	}, true)

	resp := wh.Handle(context.Background(), req)
	if !resp.Allowed {
		t.Fatalf("expected allowed, got denied: %s", resp.Result.Message)
	}

	var foundReplace bool
	for _, p := range resp.Patches {
		if p.Path == "/spec/secretRef/name" && p.Operation == "replace" {
			foundReplace = true
			if p.Value != "git-connection-my-org" {
				t.Fatalf("expected secretRef name 'git-connection-my-org', got %v", p.Value)
			}
		}
	}
	if !foundReplace {
		t.Fatalf("expected replace patch for existing secretRef, got patches: %+v", resp.Patches)
	}
}

func TestHandle_NoPrimaryConfigured(t *testing.T) {
	scheme := runtime.NewScheme()
	_ = corev1.AddToScheme(scheme)

	cm := &corev1.ConfigMap{
		ObjectMeta: metav1.ObjectMeta{Name: ConfigMapName, Namespace: ConfigMapNamespace},
		Data: map[string]string{
			"my-org.host":         "github.com",
			"my-org.organization": "my-org",
		},
	}
	fakeClient := fake.NewClientBuilder().WithScheme(scheme).WithObjects(cm).Build()
	wh := NewGitOpsWebhook(fakeClient)

	req := makeRequest("GitRepository", map[string]string{
		AnnotationConnection: "auto",
		AnnotationRepo:       "some-repo",
	}, false)

	resp := wh.Handle(context.Background(), req)
	if resp.Allowed {
		t.Fatal("expected denied when no primary connection is configured")
	}
	if !contains(resp.Result.Message, "primary") {
		t.Fatalf("expected error about primary, got: %s", resp.Result.Message)
	}
}

func TestListConnections(t *testing.T) {
	cm := configMap()
	names := listConnections(cm)
	if len(names) != 2 {
		t.Fatalf("expected 2 connections, got %d: %v", len(names), names)
	}
	if names[0] != "my-org" || names[1] != "other" {
		t.Fatalf("unexpected connections: %v", names)
	}
}

func TestFindPrimaryConnection(t *testing.T) {
	cm := configMap()
	primary := findPrimaryConnection(cm)
	if primary != "my-org" {
		t.Fatalf("expected primary 'my-org', got %q", primary)
	}
}

func contains(s, substr string) bool {
	return len(s) >= len(substr) && (s == substr || len(s) > 0 && containsSubstr(s, substr))
}

func containsSubstr(s, substr string) bool {
	for i := 0; i <= len(s)-len(substr); i++ {
		if s[i:i+len(substr)] == substr {
			return true
		}
	}
	return false
}
