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

package webhook

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"sort"
	"strings"

	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/types"
	jsonpatch "gomodules.xyz/jsonpatch/v2"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/log"
	"sigs.k8s.io/controller-runtime/pkg/webhook/admission"
)

const (
	AnnotationConnection = "integrations.open-control-plane.io/gitops-connection"
	AnnotationRepo       = "integrations.open-control-plane.io/gitops-repo"
	AnnotationOrg        = "integrations.open-control-plane.io/gitops-org"

	// ConnectionAuto is the special value for gitops-connection that triggers
	// automatic resolution to the primary connection.
	ConnectionAuto = "auto"

	ConfigMapName      = "git-connections"
	ConfigMapNamespace = "flux-system"

	SecretPrefix = "git-connection-"
)

type GitOpsWebhook struct {
	Client client.Reader
}

func NewGitOpsWebhook(client client.Reader) *GitOpsWebhook {
	return &GitOpsWebhook{Client: client}
}

func (w *GitOpsWebhook) Handle(ctx context.Context, req admission.Request) admission.Response {
	logger := log.FromContext(ctx).WithValues("name", req.Name, "namespace", req.Namespace, "kind", req.Kind.Kind)

	obj := &unstructuredResource{}
	if err := json.Unmarshal(req.Object.Raw, obj); err != nil {
		return admission.Errored(http.StatusBadRequest, fmt.Errorf("decoding object: %w", err))
	}

	annotations := obj.Metadata.Annotations
	if annotations == nil {
		return admission.Allowed("no gitops annotations")
	}

	connectionName, hasConnection := annotations[AnnotationConnection]
	if !hasConnection {
		return admission.Allowed("no gitops-connection annotation")
	}

	cm, err := w.getConnectionsConfigMap(ctx)
	if err != nil {
		return admission.Errored(http.StatusInternalServerError, fmt.Errorf("reading git-connections ConfigMap: %w", err))
	}

	if connectionName == ConnectionAuto {
		connectionName = findPrimaryConnection(cm)
		if connectionName == "" {
			return admission.Denied("gitops-connection is 'auto' (requesting primary), but no primary connection is configured. Set primary: true on a GitConnection.")
		}
	}

	host := cm.Data[connectionName+".host"]
	defaultOrg := cm.Data[connectionName+".organization"]

	if host == "" {
		available := listConnections(cm)
		return admission.Denied(fmt.Sprintf(
			"GitConnection %q is not available in this control plane. Available connections: %v",
			connectionName, available,
		))
	}

	repoName, hasRepo := annotations[AnnotationRepo]
	if !hasRepo || repoName == "" {
		return admission.Denied(fmt.Sprintf(
			"missing required annotation %q", AnnotationRepo,
		))
	}

	org := defaultOrg
	if override, ok := annotations[AnnotationOrg]; ok && override != "" {
		org = override
	}

	scheme := cm.Data[connectionName+".scheme"]
	if scheme == "" {
		scheme = "https"
	}

	constructedURL := fmt.Sprintf("%s://%s/%s/%s", scheme, host, org, repoName)
	secretName := SecretPrefix + connectionName

	patches := buildPatches(req.Kind.Kind, obj, constructedURL, secretName)
	if len(patches) == 0 {
		return admission.Allowed("no mutations needed")
	}

	logger.Info("mutating resource", "connection", connectionName, "url", constructedURL, "secretRef", secretName)
	return admission.Patched("injected git connection", patches...)
}

func (w *GitOpsWebhook) getConnectionsConfigMap(ctx context.Context) (*corev1.ConfigMap, error) {
	cm := &corev1.ConfigMap{}
	err := w.Client.Get(ctx, types.NamespacedName{Name: ConfigMapName, Namespace: ConfigMapNamespace}, cm)
	if err != nil {
		return nil, err
	}
	return cm, nil
}

func findPrimaryConnection(cm *corev1.ConfigMap) string {
	if cm.Data == nil {
		return ""
	}
	for key, val := range cm.Data {
		if strings.HasSuffix(key, ".primary") && val == "true" {
			return strings.TrimSuffix(key, ".primary")
		}
	}
	return ""
}

func listConnections(cm *corev1.ConfigMap) []string {
	if cm.Data == nil {
		return nil
	}
	seen := map[string]bool{}
	for key := range cm.Data {
		parts := strings.SplitN(key, ".", 2)
		if len(parts) == 2 && !seen[parts[0]] {
			seen[parts[0]] = true
		}
	}
	var names []string
	for name := range seen {
		names = append(names, name)
	}
	sort.Strings(names)
	return names
}

func buildPatches(kind string, obj *unstructuredResource, url, secretName string) []jsonpatch.JsonPatchOperation {
	var patches []jsonpatch.JsonPatchOperation

	switch kind {
	case "Provider":
		patches = append(patches, jsonpatch.JsonPatchOperation{
			Operation: "replace",
			Path:      "/spec/address",
			Value:     url,
		})
		patches = append(patches, ensureSecretRef(obj, secretName)...)

	default:
		patches = append(patches, jsonpatch.JsonPatchOperation{
			Operation: "replace",
			Path:      "/spec/url",
			Value:     url,
		})
		patches = append(patches, ensureSecretRef(obj, secretName)...)
	}

	return patches
}

func ensureSecretRef(obj *unstructuredResource, secretName string) []jsonpatch.JsonPatchOperation {
	var patches []jsonpatch.JsonPatchOperation

	if obj.Spec.SecretRef == nil {
		patches = append(patches, jsonpatch.JsonPatchOperation{
			Operation: "add",
			Path:      "/spec/secretRef",
			Value:     map[string]string{"name": secretName},
		})
	} else {
		patches = append(patches, jsonpatch.JsonPatchOperation{
			Operation: "replace",
			Path:      "/spec/secretRef/name",
			Value:     secretName,
		})
	}

	return patches
}

type unstructuredResource struct {
	Metadata struct {
		Name        string            `json:"name"`
		Namespace   string            `json:"namespace"`
		Annotations map[string]string `json:"annotations,omitempty"`
	} `json:"metadata"`
	Spec struct {
		URL       string `json:"url,omitempty"`
		Address   string `json:"address,omitempty"`
		SecretRef *struct {
			Name string `json:"name"`
		} `json:"secretRef,omitempty"`
	} `json:"spec"`
}
