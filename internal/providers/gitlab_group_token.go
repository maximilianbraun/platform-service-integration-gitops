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
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"time"

	corev1 "k8s.io/api/core/v1"
	"sigs.k8s.io/controller-runtime/pkg/client"

	integrationsv1alpha1 "github.com/maximilianbraun/platform-service-integration-gitops/api/v1alpha1"
)

// GitLabGroupTokenProvider implements TokenProvider for static GitLab group/project access tokens.
type GitLabGroupTokenProvider struct {
	Client  client.Client
	BaseURL string // override for testing; if empty, derived from provider.Spec.Host
}

var _ TokenProvider = &GitLabGroupTokenProvider{}

func (g *GitLabGroupTokenProvider) GenerateToken(ctx context.Context, provider *integrationsv1alpha1.GitProvider, connection *integrationsv1alpha1.GitConnection) (*Token, error) {
	token, err := g.readToken(ctx, provider)
	if err != nil {
		return nil, err
	}

	return &Token{
		Username:  "gitlab-token",
		Password:  token,
		ExpiresAt: time.Now().Add(24 * time.Hour),
	}, nil
}

func (g *GitLabGroupTokenProvider) ValidateConnection(ctx context.Context, provider *integrationsv1alpha1.GitProvider, connection *integrationsv1alpha1.GitConnection) error {
	token, err := g.readToken(ctx, provider)
	if err != nil {
		return err
	}

	baseURL := g.resolveBaseURL(provider)
	if err := g.checkGroupAccess(ctx, baseURL, token, connection.Spec.Organization); err != nil {
		return fmt.Errorf("group %q not accessible with provided token: %w", connection.Spec.Organization, err)
	}

	return nil
}

func (g *GitLabGroupTokenProvider) resolveBaseURL(provider *integrationsv1alpha1.GitProvider) string {
	if g.BaseURL != "" {
		return g.BaseURL
	}
	return gitlabBaseURL(provider.Spec.Host)
}

func (g *GitLabGroupTokenProvider) SecretData(token *Token, resourceKind string) (map[string][]byte, corev1.SecretType, error) {
	switch resourceKind {
	case "GitRepository":
		return map[string][]byte{
			"username": []byte(token.Username),
			"password": []byte(token.Password),
		}, corev1.SecretTypeOpaque, nil

	case "OCIRepository":
		host := "registry.gitlab.com"
		dockerConfig := map[string]interface{}{
			"auths": map[string]interface{}{
				host: map[string]string{
					"username": token.Username,
					"password": token.Password,
				},
			},
		}
		data, err := json.Marshal(dockerConfig)
		if err != nil {
			return nil, "", fmt.Errorf("marshaling docker config: %w", err)
		}
		return map[string][]byte{
			".dockerconfigjson": data,
		}, corev1.SecretTypeDockerConfigJson, nil

	case "Provider":
		return map[string][]byte{
			"token": []byte(token.Password),
		}, corev1.SecretTypeOpaque, nil

	default:
		return nil, "", fmt.Errorf("unsupported resource kind: %s", resourceKind)
	}
}

func (g *GitLabGroupTokenProvider) readToken(ctx context.Context, provider *integrationsv1alpha1.GitProvider) (string, error) {
	if provider.Spec.GitLabGroupToken == nil {
		return "", fmt.Errorf("provider %s has no gitlabGroupToken configuration", provider.Name)
	}

	ref := provider.Spec.GitLabGroupToken.TokenSecretRef
	secret := &corev1.Secret{}
	key := client.ObjectKey{Name: ref.Name, Namespace: ref.Namespace}
	if err := g.Client.Get(ctx, key, secret); err != nil {
		return "", fmt.Errorf("reading token secret %s/%s: %w", ref.Namespace, ref.Name, err)
	}

	data, ok := secret.Data["token"]
	if !ok {
		return "", fmt.Errorf("secret %s/%s does not contain key \"token\"", ref.Namespace, ref.Name)
	}
	return string(data), nil
}

func (g *GitLabGroupTokenProvider) checkGroupAccess(ctx context.Context, baseURL, token, groupPath string) error {
	reqURL := fmt.Sprintf("%s/api/v4/groups/%s", baseURL, url.PathEscape(groupPath))
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, reqURL, nil)
	if err != nil {
		return err
	}
	req.Header.Set("PRIVATE-TOKEN", token)

	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()

	body, _ := io.ReadAll(resp.Body)

	if resp.StatusCode == http.StatusNotFound || resp.StatusCode == http.StatusForbidden {
		return fmt.Errorf("group not found or access denied (status %d): %s", resp.StatusCode, string(body))
	}
	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("unexpected status %d: %s", resp.StatusCode, string(body))
	}
	return nil
}
