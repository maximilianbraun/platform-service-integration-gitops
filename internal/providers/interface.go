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
	"time"

	corev1 "k8s.io/api/core/v1"

	integrationsv1alpha1 "github.com/openmcp-project/platform-service-git-connection/api/v1alpha1"
)

// Token represents a generated authentication token with its metadata.
type Token struct {
	// Username is the git username for authentication (e.g., "x-access-token" for GitHub, "oauth2" for GitLab).
	Username string

	// Password is the token value used as the git password.
	Password string

	// ExpiresAt is the time when this token expires.
	ExpiresAt time.Time
}

// TokenProvider defines the interface for generating authentication tokens for git providers.
// Each git hosting backend (GitHub App, GitLab OAuth, etc.) implements this interface.
type TokenProvider interface {
	// GenerateToken creates a new short-lived authentication token for the given provider and connection.
	// The provider contains backend configuration (host, credentials), while the connection
	// contains user-specific scope (organization, repositories).
	GenerateToken(ctx context.Context, provider *integrationsv1alpha1.GitProvider, connection *integrationsv1alpha1.GitConnection) (*Token, error)

	// ValidateConnection checks that the connection configuration is valid for this provider type.
	// This includes verifying that the referenced organization exists and that the provider has access to it.
	ValidateConnection(ctx context.Context, provider *integrationsv1alpha1.GitProvider, connection *integrationsv1alpha1.GitConnection) error

	// SecretData formats the token into Kubernetes Secret data appropriate for the given resource kind.
	// Different consumers need different formats:
	//   - GitRepository: basic-auth (username/password keys)
	//   - OCIRepository: dockerconfigjson format
	//   - Provider (notifications): token key
	SecretData(token *Token, resourceKind string) (data map[string][]byte, secretType corev1.SecretType, err error)
}
