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

package providers

import (
	"context"
	"net/url"
	"testing"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"

	integrationsv1alpha1 "github.com/maximilianbraun/platform-service-integration-gitops/api/v1alpha1"
	"github.com/maximilianbraun/platform-service-integration-gitops/test/e2e/mock"
)

func TestGitLabOAuthGenerateToken(t *testing.T) {
	srv := mock.NewGitLabServer("app-123", "secret-456")
	defer srv.Close()
	srv.AddGroup("my-group", "My Group", 1)

	scheme := runtime.NewScheme()
	_ = corev1.AddToScheme(scheme)

	secret := &corev1.Secret{
		ObjectMeta: metav1.ObjectMeta{Name: "gitlab-secret", Namespace: "openmcp-system"},
		Data:       map[string][]byte{"client-secret": []byte("secret-456")},
	}
	k8sClient := fake.NewClientBuilder().WithScheme(scheme).WithObjects(secret).Build()

	// Parse mock server URL to get host
	u, _ := url.Parse(srv.URL())
	host := u.Host

	provider := &integrationsv1alpha1.GitProvider{
		Spec: integrationsv1alpha1.GitProviderSpec{
			Host: host,
			Type: integrationsv1alpha1.GitProviderTypeGitLabOAuth,
			GitLabOAuth: &integrationsv1alpha1.GitLabOAuthConfig{
				ApplicationId: "app-123",
				SecretRef:     integrationsv1alpha1.SecretReference{Name: "gitlab-secret", Namespace: "openmcp-system"},
			},
		},
	}

	connection := &integrationsv1alpha1.GitConnection{
		Spec: integrationsv1alpha1.GitConnectionSpec{
			Organization: "my-group",
		},
	}

	p := &GitLabOAuthTokenProvider{Client: k8sClient, BaseURL: srv.URL()}

	token, err := p.GenerateToken(context.Background(), provider, connection)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if token.Username != "oauth2" {
		t.Errorf("expected username 'oauth2', got %q", token.Username)
	}
	if token.Password != "glpat-mock-token-12345" {
		t.Errorf("expected mock token, got %q", token.Password)
	}
	if token.ExpiresAt.IsZero() {
		t.Error("expected non-zero expiry")
	}
	if srv.TokenCounter != 1 {
		t.Errorf("expected 1 token request, got %d", srv.TokenCounter)
	}
}

func TestGitLabOAuthValidateConnection_GroupNotFound(t *testing.T) {
	srv := mock.NewGitLabServer("app-123", "secret-456")
	defer srv.Close()
	// Don't add the group — should fail

	scheme := runtime.NewScheme()
	_ = corev1.AddToScheme(scheme)

	secret := &corev1.Secret{
		ObjectMeta: metav1.ObjectMeta{Name: "gitlab-secret", Namespace: "openmcp-system"},
		Data:       map[string][]byte{"client-secret": []byte("secret-456")},
	}
	k8sClient := fake.NewClientBuilder().WithScheme(scheme).WithObjects(secret).Build()

	u, _ := url.Parse(srv.URL())

	provider := &integrationsv1alpha1.GitProvider{
		Spec: integrationsv1alpha1.GitProviderSpec{
			Host: u.Host,
			Type: integrationsv1alpha1.GitProviderTypeGitLabOAuth,
			GitLabOAuth: &integrationsv1alpha1.GitLabOAuthConfig{
				ApplicationId: "app-123",
				SecretRef:     integrationsv1alpha1.SecretReference{Name: "gitlab-secret", Namespace: "openmcp-system"},
			},
		},
	}

	connection := &integrationsv1alpha1.GitConnection{
		Spec: integrationsv1alpha1.GitConnectionSpec{
			Organization: "nonexistent-group",
		},
	}

	p := &GitLabOAuthTokenProvider{Client: k8sClient, BaseURL: srv.URL()}

	err := p.ValidateConnection(context.Background(), provider, connection)
	if err == nil {
		t.Fatal("expected error for nonexistent group")
	}
	if !contains(err.Error(), "not accessible") {
		t.Errorf("expected 'not accessible' in error, got: %v", err)
	}
}

func TestGitLabOAuthValidateConnection_InvalidCredentials(t *testing.T) {
	srv := mock.NewGitLabServer("app-123", "correct-secret")
	defer srv.Close()

	scheme := runtime.NewScheme()
	_ = corev1.AddToScheme(scheme)

	secret := &corev1.Secret{
		ObjectMeta: metav1.ObjectMeta{Name: "gitlab-secret", Namespace: "openmcp-system"},
		Data:       map[string][]byte{"client-secret": []byte("wrong-secret")},
	}
	k8sClient := fake.NewClientBuilder().WithScheme(scheme).WithObjects(secret).Build()

	u, _ := url.Parse(srv.URL())

	provider := &integrationsv1alpha1.GitProvider{
		Spec: integrationsv1alpha1.GitProviderSpec{
			Host: u.Host,
			Type: integrationsv1alpha1.GitProviderTypeGitLabOAuth,
			GitLabOAuth: &integrationsv1alpha1.GitLabOAuthConfig{
				ApplicationId: "app-123",
				SecretRef:     integrationsv1alpha1.SecretReference{Name: "gitlab-secret", Namespace: "openmcp-system"},
			},
		},
	}

	connection := &integrationsv1alpha1.GitConnection{
		Spec: integrationsv1alpha1.GitConnectionSpec{
			Organization: "my-group",
		},
	}

	p := &GitLabOAuthTokenProvider{Client: k8sClient, BaseURL: srv.URL()}

	err := p.ValidateConnection(context.Background(), provider, connection)
	if err == nil {
		t.Fatal("expected error for invalid credentials")
	}
}

func TestGitLabGroupTokenGenerateToken(t *testing.T) {
	srv := mock.NewGitLabServer("", "")
	defer srv.Close()
	srv.AddGroup("infra-team", "Infra Team", 42)

	scheme := runtime.NewScheme()
	_ = corev1.AddToScheme(scheme)

	secret := &corev1.Secret{
		ObjectMeta: metav1.ObjectMeta{Name: "group-token", Namespace: "openmcp-system"},
		Data:       map[string][]byte{"token": []byte("glpat-group-token-xyz")},
	}
	k8sClient := fake.NewClientBuilder().WithScheme(scheme).WithObjects(secret).Build()

	u, _ := url.Parse(srv.URL())

	provider := &integrationsv1alpha1.GitProvider{
		Spec: integrationsv1alpha1.GitProviderSpec{
			Host: u.Host,
			Type: integrationsv1alpha1.GitProviderTypeGitLabGroupToken,
			GitLabGroupToken: &integrationsv1alpha1.GitLabGroupTokenConfig{
				TokenSecretRef: integrationsv1alpha1.SecretReference{Name: "group-token", Namespace: "openmcp-system"},
			},
		},
	}

	connection := &integrationsv1alpha1.GitConnection{
		Spec: integrationsv1alpha1.GitConnectionSpec{
			Organization: "infra-team",
		},
	}

	p := &GitLabGroupTokenProvider{Client: k8sClient}

	token, err := p.GenerateToken(context.Background(), provider, connection)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if token.Username != "gitlab-token" {
		t.Errorf("expected username 'gitlab-token', got %q", token.Username)
	}
	if token.Password != "glpat-group-token-xyz" {
		t.Errorf("expected group token, got %q", token.Password)
	}
}

func TestGitLabGroupTokenValidateConnection_Success(t *testing.T) {
	srv := mock.NewGitLabServer("", "")
	defer srv.Close()
	srv.AddGroup("infra-team", "Infra Team", 42)

	scheme := runtime.NewScheme()
	_ = corev1.AddToScheme(scheme)

	secret := &corev1.Secret{
		ObjectMeta: metav1.ObjectMeta{Name: "group-token", Namespace: "openmcp-system"},
		Data:       map[string][]byte{"token": []byte("glpat-valid-token")},
	}
	k8sClient := fake.NewClientBuilder().WithScheme(scheme).WithObjects(secret).Build()

	u, _ := url.Parse(srv.URL())

	provider := &integrationsv1alpha1.GitProvider{
		Spec: integrationsv1alpha1.GitProviderSpec{
			Host: u.Host,
			Type: integrationsv1alpha1.GitProviderTypeGitLabGroupToken,
			GitLabGroupToken: &integrationsv1alpha1.GitLabGroupTokenConfig{
				TokenSecretRef: integrationsv1alpha1.SecretReference{Name: "group-token", Namespace: "openmcp-system"},
			},
		},
	}

	connection := &integrationsv1alpha1.GitConnection{
		Spec: integrationsv1alpha1.GitConnectionSpec{
			Organization: "infra-team",
		},
	}

	p := &GitLabGroupTokenProvider{Client: k8sClient, BaseURL: srv.URL()}

	err := p.ValidateConnection(context.Background(), provider, connection)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
}

func TestGitLabOAuthSecretData_AllFormats(t *testing.T) {
	p := &GitLabOAuthTokenProvider{}
	token := &Token{Username: "oauth2", Password: "glpat-test"}

	// GitRepository
	data, secretType, err := p.SecretData(token, "GitRepository")
	if err != nil {
		t.Fatalf("GitRepository: %v", err)
	}
	if secretType != corev1.SecretTypeOpaque {
		t.Errorf("expected Opaque, got %s", secretType)
	}
	if string(data["username"]) != "oauth2" {
		t.Errorf("expected 'oauth2', got %q", string(data["username"]))
	}

	// OCIRepository
	data, secretType, err = p.SecretData(token, "OCIRepository")
	if err != nil {
		t.Fatalf("OCIRepository: %v", err)
	}
	if secretType != corev1.SecretTypeDockerConfigJson {
		t.Errorf("expected DockerConfigJson, got %s", secretType)
	}
	if _, ok := data[".dockerconfigjson"]; !ok {
		t.Error("missing .dockerconfigjson key")
	}

	// Provider
	data, secretType, err = p.SecretData(token, "Provider")
	if err != nil {
		t.Fatalf("Provider: %v", err)
	}
	if string(data["token"]) != "glpat-test" {
		t.Errorf("expected token value, got %q", string(data["token"]))
	}

	// Unknown
	_, _, err = p.SecretData(token, "Unknown")
	if err == nil {
		t.Error("expected error for unknown kind")
	}
}

func contains(s, substr string) bool {
	return len(s) >= len(substr) && (s == substr || len(s) > 0 && containsSubstring(s, substr))
}

func containsSubstring(s, sub string) bool {
	for i := 0; i <= len(s)-len(sub); i++ {
		if s[i:i+len(sub)] == sub {
			return true
		}
	}
	return false
}
