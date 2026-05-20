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
	"net/http"
	"net/http/httptest"
	"testing"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"

	integrationsv1alpha1 "github.com/openmcp-project/platform-service-git-connection/api/v1alpha1"
)

func TestGenerateToken_PrivateKeySecretNotFound(t *testing.T) {
	scheme := runtime.NewScheme()
	_ = corev1.AddToScheme(scheme)
	k8sClient := fake.NewClientBuilder().WithScheme(scheme).Build()

	provider := &integrationsv1alpha1.GitProvider{
		Spec: integrationsv1alpha1.GitProviderSpec{
			Host: "github.com",
			Type: integrationsv1alpha1.GitProviderTypeGitHubApp,
			GitHubApp: &integrationsv1alpha1.GitHubAppConfig{
				AppId:               12345,
				PrivateKeySecretRef: integrationsv1alpha1.SecretReference{Name: "nonexistent", Namespace: "openmcp-system"},
			},
		},
	}
	connection := &integrationsv1alpha1.GitConnection{
		Spec: integrationsv1alpha1.GitConnectionSpec{Organization: "my-org"},
	}

	p := &GitHubAppTokenProvider{Client: k8sClient}
	_, err := p.GenerateToken(context.Background(), provider, connection)
	if err == nil {
		t.Fatal("expected error when private key secret is missing")
	}
	if !containsStr(err.Error(), "reading private key secret") {
		t.Errorf("expected 'reading private key secret' in error, got: %v", err)
	}
}

func TestGenerateToken_PrivateKeyInvalidPEM(t *testing.T) {
	scheme := runtime.NewScheme()
	_ = corev1.AddToScheme(scheme)

	secret := &corev1.Secret{
		ObjectMeta: metav1.ObjectMeta{Name: "bad-key", Namespace: "openmcp-system"},
		Data:       map[string][]byte{"private-key.pem": []byte("this is not a PEM key")},
	}
	k8sClient := fake.NewClientBuilder().WithScheme(scheme).WithObjects(secret).Build()

	provider := &integrationsv1alpha1.GitProvider{
		Spec: integrationsv1alpha1.GitProviderSpec{
			Host: "github.com",
			Type: integrationsv1alpha1.GitProviderTypeGitHubApp,
			GitHubApp: &integrationsv1alpha1.GitHubAppConfig{
				AppId:               12345,
				PrivateKeySecretRef: integrationsv1alpha1.SecretReference{Name: "bad-key", Namespace: "openmcp-system"},
			},
		},
	}
	connection := &integrationsv1alpha1.GitConnection{
		Spec: integrationsv1alpha1.GitConnectionSpec{Organization: "my-org"},
	}

	p := &GitHubAppTokenProvider{Client: k8sClient}
	_, err := p.GenerateToken(context.Background(), provider, connection)
	if err == nil {
		t.Fatal("expected error for invalid PEM key")
	}
}

func TestGenerateToken_PrivateKeySecretMissingKey(t *testing.T) {
	scheme := runtime.NewScheme()
	_ = corev1.AddToScheme(scheme)

	secret := &corev1.Secret{
		ObjectMeta: metav1.ObjectMeta{Name: "wrong-key", Namespace: "openmcp-system"},
		Data:       map[string][]byte{"wrong-key-name.pem": []byte("data")},
	}
	k8sClient := fake.NewClientBuilder().WithScheme(scheme).WithObjects(secret).Build()

	provider := &integrationsv1alpha1.GitProvider{
		Spec: integrationsv1alpha1.GitProviderSpec{
			Host: "github.com",
			Type: integrationsv1alpha1.GitProviderTypeGitHubApp,
			GitHubApp: &integrationsv1alpha1.GitHubAppConfig{
				AppId:               12345,
				PrivateKeySecretRef: integrationsv1alpha1.SecretReference{Name: "wrong-key", Namespace: "openmcp-system"},
			},
		},
	}
	connection := &integrationsv1alpha1.GitConnection{
		Spec: integrationsv1alpha1.GitConnectionSpec{Organization: "my-org"},
	}

	p := &GitHubAppTokenProvider{Client: k8sClient}
	_, err := p.GenerateToken(context.Background(), provider, connection)
	if err == nil {
		t.Fatal("expected error for missing private-key.pem key in secret")
	}
	if !containsStr(err.Error(), "private-key.pem") {
		t.Errorf("expected error about 'private-key.pem', got: %v", err)
	}
}

func TestGenerateToken_GitHubAppConfigNil(t *testing.T) {
	scheme := runtime.NewScheme()
	_ = corev1.AddToScheme(scheme)
	k8sClient := fake.NewClientBuilder().WithScheme(scheme).Build()

	provider := &integrationsv1alpha1.GitProvider{
		Spec: integrationsv1alpha1.GitProviderSpec{
			Host:      "github.com",
			Type:      integrationsv1alpha1.GitProviderTypeGitHubApp,
			GitHubApp: nil,
		},
	}
	connection := &integrationsv1alpha1.GitConnection{
		Spec: integrationsv1alpha1.GitConnectionSpec{Organization: "my-org"},
	}

	p := &GitHubAppTokenProvider{Client: k8sClient}
	_, err := p.GenerateToken(context.Background(), provider, connection)
	if err == nil {
		t.Fatal("expected error when githubApp config is nil")
	}
	if !containsStr(err.Error(), "no githubApp configuration") {
		t.Errorf("expected 'no githubApp configuration' in error, got: %v", err)
	}
}

func TestGenerateToken_GitHubAPIRateLimited(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("X-RateLimit-Remaining", "0")
		w.Header().Set("X-RateLimit-Reset", "9999999999")
		w.WriteHeader(http.StatusForbidden)
		w.Write([]byte(`{"message":"API rate limit exceeded"}`))
	}))
	defer srv.Close()

	p, provider, connection := setupWithMockServer(t, srv.URL)
	_, err := p.GenerateToken(context.Background(), provider, connection)
	if err == nil {
		t.Fatal("expected error when rate limited")
	}
	if !containsStr(err.Error(), "listing installations") {
		t.Errorf("expected installation listing error, got: %v", err)
	}
}

func TestGenerateToken_GitHubAPIInternalError(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusInternalServerError)
		w.Write([]byte(`{"message":"Internal Server Error"}`))
	}))
	defer srv.Close()

	p, provider, connection := setupWithMockServer(t, srv.URL)
	_, err := p.GenerateToken(context.Background(), provider, connection)
	if err == nil {
		t.Fatal("expected error on GitHub 500")
	}
}

func TestGenerateToken_GitHubAPITokenCreationFails(t *testing.T) {
	callCount := 0
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		callCount++
		if r.URL.Path == "/app/installations" {
			w.Header().Set("Content-Type", "application/json")
			w.Write([]byte(`[{"id":1,"account":{"login":"my-org"},"app_slug":"test-app"}]`))
			return
		}
		// Token creation endpoint fails
		w.WriteHeader(http.StatusUnprocessableEntity)
		w.Write([]byte(`{"message":"Could not create installation token"}`))
	}))
	defer srv.Close()

	p, provider, connection := setupWithMockServer(t, srv.URL)
	_, err := p.GenerateToken(context.Background(), provider, connection)
	if err == nil {
		t.Fatal("expected error when token creation fails")
	}
	if !containsStr(err.Error(), "creating installation token") {
		t.Errorf("expected 'creating installation token' in error, got: %v", err)
	}
}

func TestGenerateToken_GitHubAPINetworkError(t *testing.T) {
	// Use a server that immediately closes
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {}))
	srv.Close() // close immediately

	p, provider, connection := setupWithMockServer(t, srv.URL)
	_, err := p.GenerateToken(context.Background(), provider, connection)
	if err == nil {
		t.Fatal("expected error on network failure")
	}
}

func TestValidateConnection_GitHubAPIDown(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusServiceUnavailable)
		w.Write([]byte(`{"message":"Service Unavailable"}`))
	}))
	defer srv.Close()

	p, provider, connection := setupWithMockServer(t, srv.URL)
	err := p.ValidateConnection(context.Background(), provider, connection)
	if err == nil {
		t.Fatal("expected error when GitHub API is unavailable")
	}
}

func TestGenerateToken_EmptyInstallationList(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.Write([]byte(`[]`))
	}))
	defer srv.Close()

	p, provider, connection := setupWithMockServer(t, srv.URL)
	_, err := p.GenerateToken(context.Background(), provider, connection)
	if err == nil {
		t.Fatal("expected error when no installations exist")
	}
	if !containsStr(err.Error(), "no installation found") {
		t.Errorf("expected 'no installation found' in error, got: %v", err)
	}
}

// --- helpers ---

func setupWithMockServer(t *testing.T, serverURL string) (*GitHubAppTokenProvider, *integrationsv1alpha1.GitProvider, *integrationsv1alpha1.GitConnection) {
	t.Helper()

	_, keyPEM := generateTestKey()

	scheme := runtime.NewScheme()
	_ = corev1.AddToScheme(scheme)
	secret := &corev1.Secret{
		ObjectMeta: metav1.ObjectMeta{Name: "test-key", Namespace: "openmcp-system"},
		Data:       map[string][]byte{"private-key.pem": keyPEM},
	}
	k8sClient := fake.NewClientBuilder().WithScheme(scheme).WithObjects(secret).Build()

	provider := &integrationsv1alpha1.GitProvider{
		ObjectMeta: metav1.ObjectMeta{Name: "github-test"},
		Spec: integrationsv1alpha1.GitProviderSpec{
			Host: "test.example.com",
			Type: integrationsv1alpha1.GitProviderTypeGitHubApp,
			GitHubApp: &integrationsv1alpha1.GitHubAppConfig{
				AppId:               99999,
				PrivateKeySecretRef: integrationsv1alpha1.SecretReference{Name: "test-key", Namespace: "openmcp-system"},
			},
		},
	}
	connection := &integrationsv1alpha1.GitConnection{
		Spec: integrationsv1alpha1.GitConnectionSpec{Organization: "my-org"},
	}

	p := &GitHubAppTokenProvider{Client: k8sClient, BaseURL: serverURL}
	return p, provider, connection
}

func containsStr(s, substr string) bool {
	return len(s) >= len(substr) && len(s) > 0 && stringContains(s, substr)
}

func stringContains(s, sub string) bool {
	for i := 0; i <= len(s)-len(sub); i++ {
		if s[i:i+len(sub)] == sub {
			return true
		}
	}
	return false
}
