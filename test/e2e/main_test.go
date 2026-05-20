//go:build e2e

package e2e

import (
	"fmt"
	"os"
	"testing"

	"sigs.k8s.io/e2e-framework/pkg/env"
	"sigs.k8s.io/e2e-framework/pkg/envconf"

	"github.com/openmcp-project/openmcp-testing/pkg/providers"
	"github.com/openmcp-project/openmcp-testing/pkg/setup"
)

var testenv env.Environment

func TestMain(m *testing.M) {
	version := getVersionOrDefault()

	openmcp := setup.OpenMCPSetup{
		Namespace: "openmcp-system",
		Operator: setup.OpenMCPOperatorSetup{
			Name:         "openmcp-operator",
			Image:        "ghcr.io/openmcp-project/images/openmcp-operator:v0.18.1",
			Environment:  "debug",
			PlatformName: "platform",
		},
		ClusterProviders: []providers.ClusterProviderSetup{
			{Name: "kind", Image: "ghcr.io/openmcp-project/images/cluster-provider-kind:v0.2.0"},
		},
		ServiceProviders: []providers.ServiceProviderSetup{
			{
				Name:               "git-connection",
				Image:              fmt.Sprintf("ghcr.io/openmcp-project/images/platform-service-integration-gitops:%s", version),
				LoadImageToCluster: true,
			},
		},
	}

	testenv = env.NewWithConfig(envconf.New().WithNamespace(openmcp.Namespace))
	openmcp.Bootstrap(testenv)
	os.Exit(testenv.Run(m))
}

func getVersionOrDefault() string {
	if v := os.Getenv("E2E_VERSION"); v != "" {
		return v
	}
	return "latest"
}
