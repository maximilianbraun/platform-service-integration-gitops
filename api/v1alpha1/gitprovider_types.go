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

package v1alpha1

import (
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

// GitProviderType defines the type of git hosting provider.
// +kubebuilder:validation:Enum=GitHubApp;GitLabOAuth;GitLabGroupToken
type GitProviderType string

const (
	// GitProviderTypeGitHubApp uses a GitHub App for authentication.
	GitProviderTypeGitHubApp GitProviderType = "GitHubApp"
	// GitProviderTypeGitLabOAuth uses GitLab OAuth for authentication.
	GitProviderTypeGitLabOAuth GitProviderType = "GitLabOAuth"
	// GitProviderTypeGitLabGroupToken uses a GitLab group token for authentication.
	GitProviderTypeGitLabGroupToken GitProviderType = "GitLabGroupToken"
)

// GitProviderPhase represents the lifecycle phase of a GitProvider.
type GitProviderPhase string

const (
	GitProviderPhaseReady   GitProviderPhase = "Ready"
	GitProviderPhaseFailed  GitProviderPhase = "Failed"
	GitProviderPhasePending GitProviderPhase = "Pending"
)

// GitHubAppConfig holds configuration for GitHub App authentication.
type GitHubAppConfig struct {
	// AppId is the GitHub App ID.
	// +required
	AppId int64 `json:"appId"`

	// PrivateKeySecretRef is a reference to the secret containing the GitHub App private key.
	// The secret must contain a key named "private-key.pem".
	// +required
	PrivateKeySecretRef SecretReference `json:"privateKeySecretRef"`
}

// GitLabOAuthConfig holds configuration for GitLab OAuth authentication.
type GitLabOAuthConfig struct {
	// ApplicationId is the GitLab OAuth application ID.
	// +required
	ApplicationId string `json:"applicationId"`

	// SecretRef is a reference to the secret containing the client secret.
	// The secret must contain a key named "client-secret".
	// +required
	SecretRef SecretReference `json:"secretRef"`
}

// GitLabGroupTokenConfig holds configuration for GitLab group/project access token authentication.
type GitLabGroupTokenConfig struct {
	// TokenSecretRef is a reference to the secret containing the group or project access token.
	// The secret must contain a key named "token".
	// +required
	TokenSecretRef SecretReference `json:"tokenSecretRef"`
}

// SecretReference is a reference to a Kubernetes Secret in a specific namespace.
type SecretReference struct {
	// Name of the secret.
	// +required
	Name string `json:"name"`

	// Namespace of the secret.
	// +required
	Namespace string `json:"namespace"`
}

// TokenRefreshConfig holds configuration for token refresh behavior.
type TokenRefreshConfig struct {
	// RefreshBeforeExpiry defines how long before token expiry the controller should
	// refresh the token. Defaults to "10m".
	// +optional
	// +kubebuilder:default="10m"
	RefreshBeforeExpiry string `json:"refreshBeforeExpiry,omitempty"`
}

// GitProviderSpec defines the desired state of GitProvider.
type GitProviderSpec struct {
	// Host is the hostname of the git hosting provider (e.g., "github.com").
	// +required
	Host string `json:"host"`

	// Type specifies the authentication mechanism to use.
	// +required
	Type GitProviderType `json:"type"`

	// GitHubApp holds GitHub App specific configuration.
	// Required when type is GitHubApp.
	// +optional
	GitHubApp *GitHubAppConfig `json:"githubApp,omitempty"`

	// GitLabOAuth holds GitLab OAuth specific configuration.
	// Required when type is GitLabOAuth.
	// +optional
	GitLabOAuth *GitLabOAuthConfig `json:"gitlabOAuth,omitempty"`

	// GitLabGroupToken holds GitLab group token configuration.
	// Required when type is GitLabGroupToken.
	// +optional
	GitLabGroupToken *GitLabGroupTokenConfig `json:"gitlabGroupToken,omitempty"`

	// TokenRefresh configures token refresh behavior.
	// +optional
	TokenRefresh *TokenRefreshConfig `json:"tokenRefresh,omitempty"`
}

// GitProviderStatus defines the observed state of GitProvider.
type GitProviderStatus struct {
	// Phase is the current lifecycle phase of the GitProvider.
	// +optional
	Phase GitProviderPhase `json:"phase,omitempty"`

	// InstallationUrl provides the URL for users to install the App on their organization.
	// Only applicable for GitHubApp type providers.
	// +optional
	InstallationUrl string `json:"installationUrl,omitempty"`

	// Conditions represent the latest available observations of the GitProvider's state.
	// +listType=map
	// +listMapKey=type
	// +optional
	Conditions []metav1.Condition `json:"conditions,omitempty"`

	// ObservedGeneration is the last generation reconciled by the controller.
	// +optional
	ObservedGeneration int64 `json:"observedGeneration,omitempty"`
}

// GitProvider is the Schema for the gitproviders API.
// It describes a git hosting backend and its authentication mechanism.
// GitProvider is cluster-scoped and lives on the platform cluster.
// +kubebuilder:object:root=true
// +kubebuilder:subresource:status
// +kubebuilder:resource:scope=Cluster
// +kubebuilder:printcolumn:JSONPath=`.spec.host`,name="Host",type=string
// +kubebuilder:printcolumn:JSONPath=`.spec.type`,name="Type",type=string
// +kubebuilder:printcolumn:JSONPath=`.status.phase`,name="Phase",type=string
// +kubebuilder:printcolumn:name="Age",type="date",JSONPath=".metadata.creationTimestamp"
type GitProvider struct {
	metav1.TypeMeta `json:",inline"`

	// metadata is standard object metadata.
	// +optional
	metav1.ObjectMeta `json:"metadata,omitempty"`

	// spec defines the desired state of GitProvider.
	// +required
	Spec GitProviderSpec `json:"spec"`

	// status defines the observed state of GitProvider.
	// +optional
	Status GitProviderStatus `json:"status,omitempty"`
}

// +kubebuilder:object:root=true

// GitProviderList contains a list of GitProvider.
type GitProviderList struct {
	metav1.TypeMeta `json:",inline"`
	metav1.ListMeta `json:"metadata,omitempty"`
	Items           []GitProvider `json:"items"`
}

func init() {
	SchemeBuilder.Register(&GitProvider{}, &GitProviderList{})
}

// GetConditions returns the conditions of the GitProvider resource.
func (g *GitProvider) GetConditions() *[]metav1.Condition {
	return &g.Status.Conditions
}
