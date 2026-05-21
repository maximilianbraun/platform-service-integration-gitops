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
	"fmt"

	"sigs.k8s.io/controller-runtime/pkg/client"

	integrationsv1alpha1 "github.com/maximilianbraun/platform-service-integration-gitops/api/v1alpha1"
)

// Registry maps GitProviderType values to their TokenProvider implementations.
type Registry struct {
	providers map[integrationsv1alpha1.GitProviderType]TokenProvider
}

// NewRegistry creates a new provider registry with all supported provider types registered.
func NewRegistry(client client.Client) *Registry {
	r := &Registry{
		providers: make(map[integrationsv1alpha1.GitProviderType]TokenProvider),
	}

	// Register supported providers
	r.providers[integrationsv1alpha1.GitProviderTypeGitHubApp] = &GitHubAppTokenProvider{
		Client: client,
	}
	r.providers[integrationsv1alpha1.GitProviderTypeGitLabOAuth] = &GitLabOAuthTokenProvider{
		Client: client,
	}
	r.providers[integrationsv1alpha1.GitProviderTypeGitLabGroupToken] = &GitLabGroupTokenProvider{
		Client: client,
	}

	return r
}

// Get returns the TokenProvider for the given provider type.
// Returns an error if the provider type is not supported.
func (r *Registry) Get(providerType integrationsv1alpha1.GitProviderType) (TokenProvider, error) {
	provider, ok := r.providers[providerType]
	if !ok {
		return nil, fmt.Errorf("unsupported provider type: %s", providerType)
	}
	return provider, nil
}
