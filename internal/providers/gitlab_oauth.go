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
	"strings"
	"time"

	corev1 "k8s.io/api/core/v1"
	"sigs.k8s.io/controller-runtime/pkg/client"

	integrationsv1alpha1 "github.com/openmcp-project/platform-service-git-connection/api/v1alpha1"
)

// GitLabOAuthTokenProvider implements TokenProvider for GitLab OAuth client credentials flow.
type GitLabOAuthTokenProvider struct {
	Client  client.Client
	BaseURL string // override for testing; if empty, derived from provider.Spec.Host
}

var _ TokenProvider = &GitLabOAuthTokenProvider{}

func (g *GitLabOAuthTokenProvider) GenerateToken(ctx context.Context, provider *integrationsv1alpha1.GitProvider, connection *integrationsv1alpha1.GitConnection) (*Token, error) {
	clientSecret, err := g.readClientSecret(ctx, provider)
	if err != nil {
		return nil, err
	}

	baseURL := g.baseURL(provider)
	appID := provider.Spec.GitLabOAuth.ApplicationId

	token, expiresIn, err := g.exchangeCredentials(ctx, baseURL, appID, clientSecret)
	if err != nil {
		return nil, fmt.Errorf("exchanging client credentials: %w", err)
	}

	return &Token{
		Username:  "oauth2",
		Password:  token,
		ExpiresAt: time.Now().Add(time.Duration(expiresIn) * time.Second),
	}, nil
}

func (g *GitLabOAuthTokenProvider) ValidateConnection(ctx context.Context, provider *integrationsv1alpha1.GitProvider, connection *integrationsv1alpha1.GitConnection) error {
	clientSecret, err := g.readClientSecret(ctx, provider)
	if err != nil {
		return err
	}

	baseURL := g.baseURL(provider)
	appID := provider.Spec.GitLabOAuth.ApplicationId

	token, _, err := g.exchangeCredentials(ctx, baseURL, appID, clientSecret)
	if err != nil {
		return fmt.Errorf("failed to authenticate with GitLab: %w", err)
	}

	if err := g.checkGroupAccess(ctx, baseURL, token, connection.Spec.Organization); err != nil {
		return fmt.Errorf("group %q not accessible: %w. Verify the OAuth application has access to this group", connection.Spec.Organization, err)
	}

	return nil
}

func (g *GitLabOAuthTokenProvider) SecretData(token *Token, resourceKind string) (map[string][]byte, corev1.SecretType, error) {
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

func (g *GitLabOAuthTokenProvider) readClientSecret(ctx context.Context, provider *integrationsv1alpha1.GitProvider) (string, error) {
	if provider.Spec.GitLabOAuth == nil {
		return "", fmt.Errorf("provider %s has no gitlabOAuth configuration", provider.Name)
	}

	ref := provider.Spec.GitLabOAuth.SecretRef
	secret := &corev1.Secret{}
	key := client.ObjectKey{Name: ref.Name, Namespace: ref.Namespace}
	if err := g.Client.Get(ctx, key, secret); err != nil {
		return "", fmt.Errorf("reading client secret %s/%s: %w", ref.Namespace, ref.Name, err)
	}

	data, ok := secret.Data["client-secret"]
	if !ok {
		return "", fmt.Errorf("secret %s/%s does not contain key \"client-secret\"", ref.Namespace, ref.Name)
	}
	return string(data), nil
}

type oauthTokenResponse struct {
	AccessToken string `json:"access_token"`
	TokenType   string `json:"token_type"`
	ExpiresIn   int    `json:"expires_in"`
}

func (g *GitLabOAuthTokenProvider) exchangeCredentials(ctx context.Context, baseURL, appID, clientSecret string) (string, int, error) {
	form := url.Values{
		"grant_type":    {"client_credentials"},
		"client_id":     {appID},
		"client_secret": {clientSecret},
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, baseURL+"/oauth/token", strings.NewReader(form.Encode()))
	if err != nil {
		return "", 0, err
	}
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")

	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return "", 0, err
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(resp.Body)
		return "", 0, fmt.Errorf("OAuth token exchange failed (status %d): %s", resp.StatusCode, string(body))
	}

	var tokenResp oauthTokenResponse
	if err := json.NewDecoder(resp.Body).Decode(&tokenResp); err != nil {
		return "", 0, fmt.Errorf("decoding token response: %w", err)
	}

	return tokenResp.AccessToken, tokenResp.ExpiresIn, nil
}

func (g *GitLabOAuthTokenProvider) checkGroupAccess(ctx context.Context, baseURL, token, groupPath string) error {
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

	if resp.StatusCode == http.StatusNotFound {
		return fmt.Errorf("group not found or no access")
	}
	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("unexpected status %d", resp.StatusCode)
	}
	return nil
}

func gitlabBaseURL(host string) string {
	return fmt.Sprintf("https://%s", host)
}

func (g *GitLabOAuthTokenProvider) baseURL(provider *integrationsv1alpha1.GitProvider) string {
	if g.BaseURL != "" {
		return g.BaseURL
	}
	return gitlabBaseURL(provider.Spec.Host)
}
