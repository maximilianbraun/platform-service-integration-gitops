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
	"strings"

	corev1 "k8s.io/api/core/v1"
	"sigs.k8s.io/controller-runtime/pkg/client"

	integrationsv1alpha1 "github.com/openmcp-project/platform-service-git-connection/api/v1alpha1"
	gh "github.com/openmcp-project/platform-service-git-connection/internal/providers/github"
)

// GitHubAppTokenProvider implements TokenProvider for GitHub App authentication.
type GitHubAppTokenProvider struct {
	Client  client.Client
	BaseURL string // override for testing; if empty, derived from provider.Spec.Host via apiURL()
}

var _ TokenProvider = &GitHubAppTokenProvider{}

func (g *GitHubAppTokenProvider) GenerateToken(ctx context.Context, provider *integrationsv1alpha1.GitProvider, connection *integrationsv1alpha1.GitConnection) (*Token, error) {
	privateKey, err := g.readPrivateKey(ctx, provider)
	if err != nil {
		return nil, err
	}

	jwtToken, err := gh.GenerateJWT(provider.Spec.GitHubApp.AppId, privateKey)
	if err != nil {
		return nil, fmt.Errorf("generating JWT: %w", err)
	}

	ghClient := gh.NewClient(g.resolveBaseURL(provider), jwtToken)

	installations, err := ghClient.ListInstallations(ctx)
	if err != nil {
		return nil, fmt.Errorf("listing installations: %w", err)
	}

	installation, err := findInstallation(installations, connection.Spec.Organization)
	if err != nil {
		return nil, err
	}

	installToken, err := ghClient.CreateInstallationToken(ctx, installation.ID, connection.Spec.Repositories)
	if err != nil {
		return nil, fmt.Errorf("creating installation token: %w", err)
	}

	return &Token{
		Username:  "x-access-token",
		Password:  installToken.Token,
		ExpiresAt: installToken.ExpiresAt,
	}, nil
}

func (g *GitHubAppTokenProvider) ValidateConnection(ctx context.Context, provider *integrationsv1alpha1.GitProvider, connection *integrationsv1alpha1.GitConnection) error {
	privateKey, err := g.readPrivateKey(ctx, provider)
	if err != nil {
		return err
	}

	jwtToken, err := gh.GenerateJWT(provider.Spec.GitHubApp.AppId, privateKey)
	if err != nil {
		return fmt.Errorf("generating JWT: %w", err)
	}

	ghClient := gh.NewClient(g.resolveBaseURL(provider), jwtToken)

	installations, err := ghClient.ListInstallations(ctx)
	if err != nil {
		return fmt.Errorf("listing installations: %w", err)
	}

	_, err = findInstallation(installations, connection.Spec.Organization)
	if err != nil {
		installURL := provider.Status.InstallationUrl
		if installURL == "" {
			installURL = fmt.Sprintf("https://%s/apps/%s/installations/new", provider.Spec.Host, appSlugFromInstallations(installations))
		}
		return fmt.Errorf("GitHub App is not installed on organization %q. Install it here: %s", connection.Spec.Organization, installURL)
	}

	return nil
}

func (g *GitHubAppTokenProvider) SecretData(token *Token, resourceKind string) (map[string][]byte, corev1.SecretType, error) {
	switch resourceKind {
	case "GitRepository":
		return map[string][]byte{
			"username": []byte(token.Username),
			"password": []byte(token.Password),
		}, corev1.SecretTypeOpaque, nil

	case "OCIRepository":
		dockerConfig := map[string]interface{}{
			"auths": map[string]interface{}{
				"ghcr.io": map[string]string{
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

func (g *GitHubAppTokenProvider) readPrivateKey(ctx context.Context, provider *integrationsv1alpha1.GitProvider) ([]byte, error) {
	if provider.Spec.GitHubApp == nil {
		return nil, fmt.Errorf("provider %s has no githubApp configuration", provider.Name)
	}

	ref := provider.Spec.GitHubApp.PrivateKeySecretRef
	secret := &corev1.Secret{}
	key := client.ObjectKey{Name: ref.Name, Namespace: ref.Namespace}
	if err := g.Client.Get(ctx, key, secret); err != nil {
		return nil, fmt.Errorf("reading private key secret %s/%s: %w", ref.Namespace, ref.Name, err)
	}

	pemData, ok := secret.Data["private-key.pem"]
	if !ok {
		return nil, fmt.Errorf("secret %s/%s does not contain key \"private-key.pem\"", ref.Namespace, ref.Name)
	}
	return pemData, nil
}

func findInstallation(installations []gh.Installation, org string) (*gh.Installation, error) {
	for i := range installations {
		if strings.EqualFold(installations[i].Account.Login, org) {
			return &installations[i], nil
		}
	}
	return nil, fmt.Errorf("no installation found for organization %q", org)
}

func (g *GitHubAppTokenProvider) resolveBaseURL(provider *integrationsv1alpha1.GitProvider) string {
	if g.BaseURL != "" {
		return g.BaseURL
	}
	return apiURL(provider.Spec.Host)
}

func apiURL(host string) string {
	if host == "github.com" {
		return "https://api.github.com"
	}
	return fmt.Sprintf("https://%s/api/v3", host)
}

func appSlugFromInstallations(installations []gh.Installation) string {
	if len(installations) > 0 && installations[0].AppSlug != "" {
		return installations[0].AppSlug
	}
	return "github-app"
}
