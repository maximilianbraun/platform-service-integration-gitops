//go:build e2e

package e2e

import (
	"context"
	"testing"
	"time"

	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"sigs.k8s.io/e2e-framework/klient/wait"
	"sigs.k8s.io/e2e-framework/pkg/envconf"
	"sigs.k8s.io/e2e-framework/pkg/features"

	"github.com/openmcp-project/openmcp-testing/pkg/clusterutils"
	openmcpconditions "github.com/openmcp-project/openmcp-testing/pkg/conditions"
	"github.com/openmcp-project/openmcp-testing/pkg/providers"
	"github.com/openmcp-project/openmcp-testing/pkg/resources"
)

const (
	mcpName        = "test-mcp"
	connectionName = "e2e-org"
)

func TestGitConnectionHappyPath(t *testing.T) {
	feature := features.New("GitConnection happy path").
		Setup(providers.CreateMCP(mcpName, wait.WithTimeout(5*time.Minute))).
		Assess("platform resources are applied", assessPlatformResources).
		Assess("GitConnection reaches Ready", assessConnectionReady).
		Assess("secret is synced to MCP", assessSecretOnMCP).
		Assess("webhook mutates GitRepository", assessWebhookMutation).
		Teardown(providers.DeleteMCP(mcpName, wait.WithTimeout(5*time.Minute))).
		Feature()

	testenv.Test(t, feature)
}

func TestGitConnectionAppNotInstalled(t *testing.T) {
	feature := features.New("GitConnection AppNotInstalled flow").
		Setup(providers.CreateMCP(mcpName, wait.WithTimeout(5*time.Minute))).
		Assess("platform resources applied", assessPlatformResources).
		Assess("connection for unknown org shows AppNotInstalled", assessAppNotInstalled).
		Teardown(providers.DeleteMCP(mcpName, wait.WithTimeout(5*time.Minute))).
		Feature()

	testenv.Test(t, feature)
}

func TestGitConnectionCrossProjectIsolation(t *testing.T) {
	feature := features.New("cross-project isolation").
		Setup(providers.CreateMCP("tenant-a", wait.WithTimeout(5*time.Minute))).
		Setup(providers.CreateMCP("tenant-b", wait.WithTimeout(5*time.Minute))).
		Assess("platform resources applied", assessPlatformResources).
		Assess("tenant-a has its connection", assessTenantAHasConnection).
		Assess("tenant-b cannot use tenant-a connection", assessTenantBIsolated).
		Teardown(providers.DeleteMCP("tenant-a", wait.WithTimeout(5*time.Minute))).
		Teardown(providers.DeleteMCP("tenant-b", wait.WithTimeout(5*time.Minute))).
		Feature()

	testenv.Test(t, feature)
}

// --- Assess implementations ---

func assessPlatformResources(ctx context.Context, t *testing.T, c *envconf.Config) context.Context {
	platformCfg, err := clusterutils.ConfigByPrefix("platform", "openmcp-system")
	if err != nil {
		t.Fatal(err)
	}

	objList, err := resources.CreateObjectsFromDir(ctx, platformCfg, "platform")
	if err != nil {
		t.Fatalf("failed to apply platform resources: %v", err)
	}

	for i := range objList.Items {
		obj := &objList.Items[i]
		if err := wait.For(openmcpconditions.Match(obj, platformCfg, "CredentialsValid", corev1.ConditionTrue), wait.WithTimeout(2*time.Minute)); err != nil {
			t.Errorf("platform resource %s not ready: %v", obj.GetName(), err)
		}
	}

	return ctx
}

func assessConnectionReady(ctx context.Context, t *testing.T, c *envconf.Config) context.Context {
	objList, err := clusterutils.ImportToOnboardingCluster(ctx, "onboarding", wait.WithTimeout(3*time.Minute))
	if err != nil {
		t.Fatalf("failed to apply onboarding resources: %v", err)
	}

	onboardingCfg, err := clusterutils.OnboardingConfig()
	if err != nil {
		t.Fatal(err)
	}

	for i := range objList.Items {
		obj := &objList.Items[i]
		if err := wait.For(openmcpconditions.Match(obj, onboardingCfg, "TokenValid", corev1.ConditionTrue), wait.WithTimeout(3*time.Minute)); err != nil {
			t.Errorf("GitConnection %s not ready: %v", obj.GetName(), err)
		}
	}

	return ctx
}

func assessSecretOnMCP(ctx context.Context, t *testing.T, c *envconf.Config) context.Context {
	platformCfg, err := clusterutils.ConfigByPrefix("platform", "openmcp-system")
	if err != nil {
		t.Fatal(err)
	}

	mcpCfg, err := clusterutils.MCPConfig(ctx, platformCfg, mcpName)
	if err != nil {
		t.Fatal(err)
	}

	secret := &corev1.Secret{}
	secretName := "git-connection-" + connectionName

	err = wait.For(func(ctx context.Context) (bool, error) {
		if err := mcpCfg.Client().Resources().Get(ctx, secretName, "flux-system", secret); err != nil {
			return false, nil
		}
		_, hasUser := secret.Data["username"]
		_, hasPass := secret.Data["password"]
		return hasUser && hasPass, nil
	}, wait.WithTimeout(2*time.Minute))

	if err != nil {
		t.Fatalf("secret %s not found on MCP: %v", secretName, err)
	}

	if string(secret.Data["username"]) != "x-access-token" {
		t.Errorf("expected username 'x-access-token', got %q", string(secret.Data["username"]))
	}

	return ctx
}

func assessWebhookMutation(ctx context.Context, t *testing.T, c *envconf.Config) context.Context {
	platformCfg, err := clusterutils.ConfigByPrefix("platform", "openmcp-system")
	if err != nil {
		t.Fatal(err)
	}

	mcpCfg, err := clusterutils.MCPConfig(ctx, platformCfg, mcpName)
	if err != nil {
		t.Fatal(err)
	}

	objList, err := resources.CreateObjectsFromDir(ctx, mcpCfg, "mcp")
	if err != nil {
		t.Fatalf("failed to apply MCP resources: %v", err)
	}

	for i := range objList.Items {
		obj := &objList.Items[i]
		if obj.GetKind() != "GitRepository" {
			continue
		}

		// Re-read to get mutated version
		updated := &unstructured.Unstructured{}
		updated.SetGroupVersionKind(obj.GroupVersionKind())
		if err := mcpCfg.Client().Resources().Get(ctx, obj.GetName(), obj.GetNamespace(), updated); err != nil {
			t.Fatalf("failed to re-read GitRepository: %v", err)
		}

		spec, _ := updated.Object["spec"].(map[string]interface{})
		if spec == nil {
			t.Fatal("GitRepository has no spec")
		}

		url, _ := spec["url"].(string)
		if url == "" || url == "changeme" {
			t.Errorf("expected URL to be rewritten by webhook, got %q", url)
		}

		secretRef, _ := spec["secretRef"].(map[string]interface{})
		if secretRef == nil {
			t.Error("expected secretRef to be injected by webhook")
		} else {
			expectedName := "git-connection-" + connectionName
			if secretRef["name"] != expectedName {
				t.Errorf("expected secretRef.name = %q, got %q", expectedName, secretRef["name"])
			}
		}
	}

	return ctx
}

func assessAppNotInstalled(ctx context.Context, t *testing.T, c *envconf.Config) context.Context {
	onboardingCfg, err := clusterutils.OnboardingConfig()
	if err != nil {
		t.Fatal(err)
	}

	conn := &unstructured.Unstructured{}
	conn.SetAPIVersion("gitops.integrations.open-control-plane.io/v1alpha1")
	conn.SetKind("GitConnection")
	conn.SetName("no-app-org")
	conn.SetNamespace("project-e2e--ws-dev")
	conn.Object["spec"] = map[string]interface{}{
		"providerRef":  "github-com",
		"organization": "org-without-app-installed",
	}

	if err := onboardingCfg.Client().Resources().Create(ctx, conn); err != nil {
		t.Fatalf("failed to create connection: %v", err)
	}

	// Wait for AppNotInstalled phase
	err = wait.For(func(ctx context.Context) (bool, error) {
		if err := onboardingCfg.Client().Resources().Get(ctx, "no-app-org", "project-e2e--ws-dev", conn); err != nil {
			return false, nil
		}
		status, _ := conn.Object["status"].(map[string]interface{})
		if status == nil {
			return false, nil
		}
		phase, _ := status["phase"].(string)
		return phase == "AppNotInstalled", nil
	}, wait.WithTimeout(2*time.Minute))

	if err != nil {
		t.Fatalf("expected AppNotInstalled phase: %v", err)
	}

	// Verify installUrl is set
	status, _ := conn.Object["status"].(map[string]interface{})
	installUrl, _ := status["installUrl"].(string)
	if installUrl == "" {
		t.Error("expected installUrl to be set in status")
	} else {
		t.Logf("installUrl: %s", installUrl)
	}

	// Cleanup
	_ = onboardingCfg.Client().Resources().Delete(ctx, conn)

	return ctx
}

func assessTenantAHasConnection(ctx context.Context, t *testing.T, c *envconf.Config) context.Context {
	platformCfg, err := clusterutils.ConfigByPrefix("platform", "openmcp-system")
	if err != nil {
		t.Fatal(err)
	}

	mcpCfg, err := clusterutils.MCPConfig(ctx, platformCfg, "tenant-a")
	if err != nil {
		t.Fatal(err)
	}

	secret := &corev1.Secret{}
	secretName := "git-connection-" + connectionName

	err = wait.For(func(ctx context.Context) (bool, error) {
		if err := mcpCfg.Client().Resources().Get(ctx, secretName, "flux-system", secret); err != nil {
			return false, nil
		}
		return true, nil
	}, wait.WithTimeout(2*time.Minute))

	if err != nil {
		t.Fatalf("tenant-a should have secret %s: %v", secretName, err)
	}

	return ctx
}

func assessTenantBIsolated(ctx context.Context, t *testing.T, c *envconf.Config) context.Context {
	platformCfg, err := clusterutils.ConfigByPrefix("platform", "openmcp-system")
	if err != nil {
		t.Fatal(err)
	}

	mcpCfg, err := clusterutils.MCPConfig(ctx, platformCfg, "tenant-b")
	if err != nil {
		t.Fatal(err)
	}

	// Give the controller time to (not) sync
	time.Sleep(10 * time.Second)

	configMap := &corev1.ConfigMap{}
	err = mcpCfg.Client().Resources().Get(ctx, "git-connections", "flux-system", configMap)
	if err == nil {
		key := connectionName + ".host"
		if _, exists := configMap.Data[key]; exists {
			t.Errorf("tenant-b should NOT have connection %q in its ConfigMap", connectionName)
		}
	}
	// If configMap doesn't exist, that's correct (no connections synced to this tenant)

	return ctx
}

// --- Utilities ---

// (no helpers needed beyond what openmcp-testing provides)
