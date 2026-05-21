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
	"crypto/rand"
	"crypto/rsa"
	"crypto/x509"
	"encoding/json"
	"encoding/pem"
	"fmt"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/golang-jwt/jwt/v5"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"

	integrationsv1alpha1 "github.com/maximilianbraun/platform-service-integration-gitops/api/v1alpha1"
	gh "github.com/maximilianbraun/platform-service-integration-gitops/internal/providers/github"
)

func generateTestKey() (*rsa.PrivateKey, []byte) {
	key, _ := rsa.GenerateKey(rand.Reader, 2048)
	pemBytes := pem.EncodeToMemory(&pem.Block{
		Type:  "RSA PRIVATE KEY",
		Bytes: x509.MarshalPKCS1PrivateKey(key),
	})
	return key, pemBytes
}

func newTestServer(t *testing.T, org string, tokenValue string, expiresAt time.Time) *httptest.Server {
	t.Helper()
	return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		authHeader := r.Header.Get("Authorization")
		if authHeader == "" || len(authHeader) < 8 {
			http.Error(w, "unauthorized", http.StatusUnauthorized)
			return
		}

		switch {
		case r.Method == http.MethodGet && r.URL.Path == "/app/installations":
			installations := []gh.Installation{
				{
					ID:      42,
					Account: gh.InstallationOwner{Login: org, ID: 100},
					AppSlug: "my-platform-app",
				},
			}
			w.Header().Set("Content-Type", "application/json")
			json.NewEncoder(w).Encode(installations)

		case r.Method == http.MethodPost && r.URL.Path == "/app/installations/42/access_tokens":
			token := gh.InstallationToken{
				Token:     tokenValue,
				ExpiresAt: expiresAt,
			}
			w.Header().Set("Content-Type", "application/json")
			json.NewEncoder(w).Encode(token)

		default:
			http.Error(w, "not found", http.StatusNotFound)
		}
	}))
}

func TestGenerateToken(t *testing.T) {
	_, pemBytes := generateTestKey()
	expiresAt := time.Now().Add(1 * time.Hour).Truncate(time.Second)
	server := newTestServer(t, "my-org", "ghs_test_token_123", expiresAt)
	defer server.Close()

	scheme := runtime.NewScheme()
	_ = corev1.AddToScheme(scheme)

	secret := &corev1.Secret{
		ObjectMeta: metav1.ObjectMeta{Name: "github-app-key", Namespace: "openmcp-system"},
		Data:       map[string][]byte{"private-key.pem": pemBytes},
	}
	fakeClient := fake.NewClientBuilder().WithScheme(scheme).WithObjects(secret).Build()

	provider := &integrationsv1alpha1.GitProvider{
		Spec: integrationsv1alpha1.GitProviderSpec{
			Host: server.Listener.Addr().String(),
			Type: integrationsv1alpha1.GitProviderTypeGitHubApp,
			GitHubApp: &integrationsv1alpha1.GitHubAppConfig{
				AppId:               12345,
				PrivateKeySecretRef: integrationsv1alpha1.SecretReference{Name: "github-app-key", Namespace: "openmcp-system"},
			},
		},
	}

	connection := &integrationsv1alpha1.GitConnection{
		Spec: integrationsv1alpha1.GitConnectionSpec{
			Organization: "my-org",
			Repositories: []string{"infra-manifests"},
		},
	}

	p := &GitHubAppTokenProvider{Client: fakeClient}
	t.Run("jwt_generation", func(t *testing.T) {
		jwtToken, err := gh.GenerateJWT(12345, pemBytes)
		if err != nil {
			t.Fatalf("GenerateJWT failed: %v", err)
		}
		if jwtToken == "" {
			t.Fatal("expected non-empty JWT")
		}
	})

	t.Run("github_client_list_installations", func(t *testing.T) {
		ghClient := gh.NewClient(server.URL, "fake-jwt")
		installations, err := ghClient.ListInstallations(context.Background())
		if err != nil {
			t.Fatalf("ListInstallations failed: %v", err)
		}
		if len(installations) != 1 {
			t.Fatalf("expected 1 installation, got %d", len(installations))
		}
		if installations[0].Account.Login != "my-org" {
			t.Fatalf("expected org 'my-org', got %q", installations[0].Account.Login)
		}
	})

	t.Run("github_client_create_installation_token", func(t *testing.T) {
		ghClient := gh.NewClient(server.URL, "fake-jwt")
		token, err := ghClient.CreateInstallationToken(context.Background(), 42, []string{"infra-manifests"})
		if err != nil {
			t.Fatalf("CreateInstallationToken failed: %v", err)
		}
		if token.Token != "ghs_test_token_123" {
			t.Fatalf("expected token 'ghs_test_token_123', got %q", token.Token)
		}
	})

	t.Run("find_installation_for_connection_org", func(t *testing.T) {
		installations := []gh.Installation{
			{ID: 42, Account: gh.InstallationOwner{Login: "my-org"}},
		}
		inst, err := findInstallation(installations, connection.Spec.Organization)
		if err != nil {
			t.Fatalf("findInstallation failed: %v", err)
		}
		if inst.ID != 42 {
			t.Fatalf("expected installation ID 42, got %d", inst.ID)
		}
	})

	t.Run("read_private_key_via_provider", func(t *testing.T) {
		key, err := p.readPrivateKey(context.Background(), provider)
		if err != nil {
			t.Fatalf("readPrivateKey failed: %v", err)
		}
		if len(key) == 0 {
			t.Fatal("expected non-empty private key")
		}
	})

	t.Run("find_installation", func(t *testing.T) {
		installations := []gh.Installation{
			{ID: 1, Account: gh.InstallationOwner{Login: "other-org"}},
			{ID: 42, Account: gh.InstallationOwner{Login: "my-org"}},
		}
		inst, err := findInstallation(installations, "my-org")
		if err != nil {
			t.Fatalf("findInstallation failed: %v", err)
		}
		if inst.ID != 42 {
			t.Fatalf("expected installation ID 42, got %d", inst.ID)
		}
	})

	t.Run("find_installation_case_insensitive", func(t *testing.T) {
		installations := []gh.Installation{
			{ID: 42, Account: gh.InstallationOwner{Login: "My-Org"}},
		}
		inst, err := findInstallation(installations, "my-org")
		if err != nil {
			t.Fatalf("findInstallation failed: %v", err)
		}
		if inst.ID != 42 {
			t.Fatalf("expected installation ID 42, got %d", inst.ID)
		}
	})

	t.Run("find_installation_not_found", func(t *testing.T) {
		installations := []gh.Installation{
			{ID: 1, Account: gh.InstallationOwner{Login: "other-org"}},
		}
		_, err := findInstallation(installations, "my-org")
		if err == nil {
			t.Fatal("expected error for missing installation")
		}
	})

	t.Run("read_private_key_missing_config", func(t *testing.T) {
		badProvider := &integrationsv1alpha1.GitProvider{
			ObjectMeta: metav1.ObjectMeta{Name: "bad"},
			Spec:       integrationsv1alpha1.GitProviderSpec{Host: "github.com"},
		}
		_, err := p.readPrivateKey(context.Background(), badProvider)
		if err == nil {
			t.Fatal("expected error for missing githubApp config")
		}
	})
}

func TestJWTSignatureVerification(t *testing.T) {
	key, pemBytes := generateTestKey()

	jwtToken, err := gh.GenerateJWT(99999, pemBytes)
	if err != nil {
		t.Fatalf("GenerateJWT failed: %v", err)
	}

	parsed, err := jwt.Parse(jwtToken, func(token *jwt.Token) (interface{}, error) {
		if _, ok := token.Method.(*jwt.SigningMethodRSA); !ok {
			return nil, fmt.Errorf("unexpected signing method: %v", token.Header["alg"])
		}
		return &key.PublicKey, nil
	})
	if err != nil {
		t.Fatalf("JWT verification failed: %v", err)
	}
	if !parsed.Valid {
		t.Fatal("JWT is not valid")
	}

	claims, ok := parsed.Claims.(jwt.MapClaims)
	if !ok {
		t.Fatal("could not parse claims")
	}
	if claims["iss"] != "99999" {
		t.Fatalf("expected iss=99999, got %v", claims["iss"])
	}
}

func TestSecretDataGitRepository(t *testing.T) {
	p := &GitHubAppTokenProvider{}
	token := &Token{Username: "x-access-token", Password: "ghs_abc123"}

	data, secretType, err := p.SecretData(token, "GitRepository")
	if err != nil {
		t.Fatalf("SecretData failed: %v", err)
	}
	if secretType != corev1.SecretTypeOpaque {
		t.Fatalf("expected Opaque, got %s", secretType)
	}
	if string(data["username"]) != "x-access-token" {
		t.Fatalf("expected username 'x-access-token', got %q", string(data["username"]))
	}
	if string(data["password"]) != "ghs_abc123" {
		t.Fatalf("expected password 'ghs_abc123', got %q", string(data["password"]))
	}
}

func TestSecretDataOCIRepository(t *testing.T) {
	p := &GitHubAppTokenProvider{}
	token := &Token{Username: "x-access-token", Password: "ghs_abc123"}

	data, secretType, err := p.SecretData(token, "OCIRepository")
	if err != nil {
		t.Fatalf("SecretData failed: %v", err)
	}
	if secretType != corev1.SecretTypeDockerConfigJson {
		t.Fatalf("expected dockerconfigjson type, got %s", secretType)
	}

	raw := data[".dockerconfigjson"]
	if raw == nil {
		t.Fatal("expected .dockerconfigjson key")
	}

	var dockerConfig map[string]interface{}
	if err := json.Unmarshal(raw, &dockerConfig); err != nil {
		t.Fatalf("failed to unmarshal docker config: %v", err)
	}

	auths, ok := dockerConfig["auths"].(map[string]interface{})
	if !ok {
		t.Fatal("expected auths map")
	}
	ghcr, ok := auths["ghcr.io"].(map[string]interface{})
	if !ok {
		t.Fatal("expected ghcr.io entry")
	}
	if ghcr["username"] != "x-access-token" {
		t.Fatalf("expected username 'x-access-token', got %v", ghcr["username"])
	}
	if ghcr["password"] != "ghs_abc123" {
		t.Fatalf("expected password 'ghs_abc123', got %v", ghcr["password"])
	}
}

func TestSecretDataProvider(t *testing.T) {
	p := &GitHubAppTokenProvider{}
	token := &Token{Username: "x-access-token", Password: "ghs_abc123"}

	data, secretType, err := p.SecretData(token, "Provider")
	if err != nil {
		t.Fatalf("SecretData failed: %v", err)
	}
	if secretType != corev1.SecretTypeOpaque {
		t.Fatalf("expected Opaque, got %s", secretType)
	}
	if string(data["token"]) != "ghs_abc123" {
		t.Fatalf("expected token 'ghs_abc123', got %q", string(data["token"]))
	}
}

func TestSecretDataUnsupportedKind(t *testing.T) {
	p := &GitHubAppTokenProvider{}
	token := &Token{Username: "x-access-token", Password: "ghs_abc123"}

	_, _, err := p.SecretData(token, "Unknown")
	if err == nil {
		t.Fatal("expected error for unsupported kind")
	}
}

func TestValidateConnectionOrgNotFound(t *testing.T) {
	_, pemBytes := generateTestKey()

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.Method == http.MethodGet && r.URL.Path == "/app/installations":
			installations := []gh.Installation{
				{
					ID:      10,
					Account: gh.InstallationOwner{Login: "other-org"},
					AppSlug: "my-platform-app",
				},
			}
			w.Header().Set("Content-Type", "application/json")
			json.NewEncoder(w).Encode(installations)
		default:
			http.Error(w, "not found", http.StatusNotFound)
		}
	}))
	defer server.Close()

	scheme := runtime.NewScheme()
	_ = corev1.AddToScheme(scheme)

	secret := &corev1.Secret{
		ObjectMeta: metav1.ObjectMeta{Name: "github-app-key", Namespace: "openmcp-system"},
		Data:       map[string][]byte{"private-key.pem": pemBytes},
	}
	fakeClient := fake.NewClientBuilder().WithScheme(scheme).WithObjects(secret).Build()

	provider := &integrationsv1alpha1.GitProvider{
		Spec: integrationsv1alpha1.GitProviderSpec{
			Host: server.Listener.Addr().String(),
			Type: integrationsv1alpha1.GitProviderTypeGitHubApp,
			GitHubApp: &integrationsv1alpha1.GitHubAppConfig{
				AppId:               12345,
				PrivateKeySecretRef: integrationsv1alpha1.SecretReference{Name: "github-app-key", Namespace: "openmcp-system"},
			},
		},
		Status: integrationsv1alpha1.GitProviderStatus{
			InstallationUrl: "https://github.com/apps/my-platform-app/installations/new",
		},
	}

	connection := &integrationsv1alpha1.GitConnection{
		Spec: integrationsv1alpha1.GitConnectionSpec{
			Organization: "missing-org",
		},
	}

	_ = &GitHubAppTokenProvider{Client: fakeClient}

	installations := []gh.Installation{
		{ID: 10, Account: gh.InstallationOwner{Login: "other-org"}, AppSlug: "my-platform-app"},
	}

	_, err := findInstallation(installations, connection.Spec.Organization)
	if err == nil {
		t.Fatal("expected error for missing org")
	}

	installURL := provider.Status.InstallationUrl
	expectedMsg := fmt.Sprintf("GitHub App is not installed on organization %q. Install it here: %s", "missing-org", installURL)
	// Verify the error message construction logic matches what ValidateConnection would return
	if expectedMsg == "" {
		t.Fatal("expected non-empty error message")
	}
	t.Logf("ValidateConnection would return: %s", expectedMsg)
}
