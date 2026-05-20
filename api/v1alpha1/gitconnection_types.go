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

// GitConnectionPhase represents the lifecycle phase of a GitConnection.
type GitConnectionPhase string

const (
	GitConnectionPhasePending         GitConnectionPhase = "Pending"
	GitConnectionPhaseAppNotInstalled GitConnectionPhase = "AppNotInstalled"
	GitConnectionPhaseProgressing     GitConnectionPhase = "Progressing"
	GitConnectionPhaseReady           GitConnectionPhase = "Ready"
	GitConnectionPhaseFailed          GitConnectionPhase = "Failed"
)

// Condition types for GitConnection.
const (
	// ConditionTypeAppInstalled indicates whether the App is installed on the target org.
	ConditionTypeAppInstalled = "AppInstalled"
	// ConditionTypeTokenValid indicates whether the current token is valid.
	ConditionTypeTokenValid = "TokenValid"
	// ConditionTypeSecretsSynced indicates whether secrets have been synced to all MCPs in scope.
	ConditionTypeSecretsSynced = "SecretsSynced"
)

// GitConnectionSpec defines the desired state of GitConnection.
type GitConnectionSpec struct {
	// ProviderRef is the name of the GitProvider resource to use for authentication.
	// This references a cluster-scoped GitProvider on the platform cluster.
	// +required
	ProviderRef string `json:"providerRef"`

	// Organization is the name of the organization or group on the git hosting provider.
	// The controller will resolve provider-specific details (e.g., GitHub App installation ID)
	// automatically based on this name.
	// +required
	Organization string `json:"organization"`

	// Primary marks this connection as the default for the namespace.
	// When a resource annotation specifies an empty connection name, the primary connection is used.
	// Only one connection per namespace can be primary.
	// +optional
	// +kubebuilder:default=false
	Primary bool `json:"primary,omitempty"`

	// Repositories optionally restricts the token scope to specific repositories.
	// If empty, the token has access to all repositories the App is installed on.
	// +optional
	Repositories []string `json:"repositories,omitempty"`
}

// ManagedSecret describes a secret that has been synced to an MCP.
type ManagedSecret struct {
	// MCP is the name of the Managed Control Plane where the secret is synced.
	// +required
	MCP string `json:"mcp"`

	// Project is the project that owns the MCP.
	// +required
	Project string `json:"project"`

	// Workspace is the workspace that owns the MCP.
	// +required
	Workspace string `json:"workspace"`

	// SecretName is the name of the synced secret on the MCP.
	// +required
	SecretName string `json:"secretName"`

	// Namespace is the namespace of the synced secret on the MCP.
	// +required
	Namespace string `json:"namespace"`
}

// GitConnectionStatus defines the observed state of GitConnection.
type GitConnectionStatus struct {
	// Phase is the current lifecycle phase of the GitConnection.
	// +optional
	Phase GitConnectionPhase `json:"phase,omitempty"`

	// TokenExpiresAt is the expiration time of the current token.
	// +optional
	TokenExpiresAt *metav1.Time `json:"tokenExpiresAt,omitempty"`

	// InstallUrl provides the URL for the user to install the App on their organization.
	// Only set when phase is AppNotInstalled.
	// +optional
	InstallUrl string `json:"installUrl,omitempty"`

	// Conditions represent the latest available observations of the GitConnection's state.
	// +listType=map
	// +listMapKey=type
	// +optional
	Conditions []metav1.Condition `json:"conditions,omitempty"`

	// ManagedSecrets lists the secrets that have been synced to MCPs.
	// +optional
	ManagedSecrets []ManagedSecret `json:"managedSecrets,omitempty"`

	// ObservedGeneration is the last generation reconciled by the controller.
	// +optional
	ObservedGeneration int64 `json:"observedGeneration,omitempty"`
}

// GitConnection is the Schema for the gitconnections API.
// It represents a user-managed, provider-agnostic connection to a git hosting organization.
// GitConnection is namespaced and lives on the onboarding cluster in a project or workspace namespace.
// +kubebuilder:object:root=true
// +kubebuilder:subresource:status
// +kubebuilder:resource:scope=Namespaced
// +kubebuilder:printcolumn:JSONPath=`.spec.providerRef`,name="Provider",type=string
// +kubebuilder:printcolumn:JSONPath=`.spec.organization`,name="Organization",type=string
// +kubebuilder:printcolumn:JSONPath=`.spec.primary`,name="Primary",type=boolean
// +kubebuilder:printcolumn:JSONPath=`.status.phase`,name="Phase",type=string
// +kubebuilder:printcolumn:name="Age",type="date",JSONPath=".metadata.creationTimestamp"
type GitConnection struct {
	metav1.TypeMeta `json:",inline"`

	// metadata is standard object metadata.
	// +optional
	metav1.ObjectMeta `json:"metadata,omitempty"`

	// spec defines the desired state of GitConnection.
	// +required
	Spec GitConnectionSpec `json:"spec"`

	// status defines the observed state of GitConnection.
	// +optional
	Status GitConnectionStatus `json:"status,omitempty"`
}

// +kubebuilder:object:root=true

// GitConnectionList contains a list of GitConnection.
type GitConnectionList struct {
	metav1.TypeMeta `json:",inline"`
	metav1.ListMeta `json:"metadata,omitempty"`
	Items           []GitConnection `json:"items"`
}

func init() {
	SchemeBuilder.Register(&GitConnection{}, &GitConnectionList{})
}

// GetConditions returns the conditions of the GitConnection resource.
func (g *GitConnection) GetConditions() *[]metav1.Condition {
	return &g.Status.Conditions
}

// Finalizer returns the finalizer string for the GitConnection resource.
func (g *GitConnection) Finalizer() string {
	return GroupVersion.Group + "/finalizer"
}
