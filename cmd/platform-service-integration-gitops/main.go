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

package main

import (
	"fmt"
	"os"

	flag "github.com/spf13/pflag"
	"k8s.io/apimachinery/pkg/runtime"
	utilruntime "k8s.io/apimachinery/pkg/util/runtime"
	clientgoscheme "k8s.io/client-go/kubernetes/scheme"
	_ "k8s.io/client-go/plugin/pkg/client/auth"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/healthz"
	"sigs.k8s.io/controller-runtime/pkg/log/zap"
	metricsserver "sigs.k8s.io/controller-runtime/pkg/metrics/server"
	ctrlwebhook "sigs.k8s.io/controller-runtime/pkg/webhook"
	"sigs.k8s.io/controller-runtime/pkg/webhook/admission"

	corev2alpha1 "github.com/openmcp-project/openmcp-operator/api/core/v2alpha1"
	integrationsv1alpha1 "github.com/maximilianbraun/platform-service-integration-gitops/api/v1alpha1"
	"github.com/maximilianbraun/platform-service-integration-gitops/internal/controller"
	"github.com/maximilianbraun/platform-service-integration-gitops/internal/providers"
	"github.com/maximilianbraun/platform-service-integration-gitops/internal/webhook"
	// +kubebuilder:scaffold:imports
)

var (
	scheme   = runtime.NewScheme()
	setupLog = ctrl.Log.WithName("setup")
)

func init() {
	utilruntime.Must(clientgoscheme.AddToScheme(scheme))
	utilruntime.Must(integrationsv1alpha1.AddToScheme(scheme))
	utilruntime.Must(corev2alpha1.AddToScheme(scheme))
	// +kubebuilder:scaffold:scheme
}

func main() {
	// The openmcp-operator invokes service providers with a "run" subcommand.
	// Strip it if present so pflag can parse the remaining flags.
	if len(os.Args) > 1 && os.Args[1] == "run" {
		os.Args = append(os.Args[:1], os.Args[2:]...)
	}

	var mode string
	var metricsAddr string
	var probeAddr string
	var webhookPort int
	var certDir string
	var environment string
	var verbosity string
	var providerName string

	flag.StringVar(&mode, "mode", "platform", "Run mode: 'platform' (controllers) or 'webhook' (mutating webhook only)")
	flag.StringVar(&metricsAddr, "metrics-bind-address", ":8080", "The address the metrics endpoint binds to.")
	flag.StringVar(&probeAddr, "health-probe-bind-address", ":8081", "The address the probe endpoint binds to.")
	flag.IntVar(&webhookPort, "webhook-port", 9443, "The port the webhook server binds to.")
	flag.StringVar(&certDir, "cert-dir", "/tmp/k8s-webhook-server/serving-certs", "Directory containing TLS certs for the webhook server.")
	// Flags injected by the openmcp-operator (accepted but not all used)
	flag.StringVar(&environment, "environment", "", "Environment name (injected by openmcp-operator)")
	flag.StringVar(&verbosity, "verbosity", "INFO", "Log verbosity (injected by openmcp-operator)")
	flag.StringVar(&providerName, "provider-name", "", "Provider name (injected by openmcp-operator)")

	opts := zap.Options{Development: true}
	flag.Parse()

	ctrl.SetLogger(zap.New(zap.UseFlagOptions(&opts)))

	switch mode {
	case "platform":
		runPlatformControllers(metricsAddr, probeAddr)
	case "webhook":
		runWebhook(metricsAddr, probeAddr, webhookPort, certDir)
	default:
		setupLog.Error(fmt.Errorf("unknown mode: %s", mode), "invalid --mode flag")
		os.Exit(1)
	}
}

func runPlatformControllers(metricsAddr, probeAddr string) {
	mgr, err := ctrl.NewManager(ctrl.GetConfigOrDie(), ctrl.Options{
		Scheme:                 scheme,
		Metrics:                metricsserver.Options{BindAddress: metricsAddr},
		HealthProbeBindAddress: probeAddr,
		LeaderElection:         false,
		LeaderElectionID:       "git-connection.gitops.integrations.open-control-plane.io",
	})
	if err != nil {
		setupLog.Error(err, "unable to start manager")
		os.Exit(1)
	}

	if err := (&controller.ProviderReconciler{
		Client: mgr.GetClient(),
	}).SetupWithManager(mgr); err != nil {
		setupLog.Error(err, "unable to create controller", "controller", "GitProvider")
		os.Exit(1)
	}

	registry := providers.NewRegistry(mgr.GetClient())
	if err := (&controller.ConnectionReconciler{
		Client:         mgr.GetClient(),
		PlatformClient: mgr.GetClient(),
		Registry:       registry,
	}).SetupWithManager(mgr); err != nil {
		setupLog.Error(err, "unable to create controller", "controller", "GitConnection")
		os.Exit(1)
	}

	// +kubebuilder:scaffold:builder

	if err := mgr.AddHealthzCheck("healthz", healthz.Ping); err != nil {
		setupLog.Error(err, "unable to set up health check")
		os.Exit(1)
	}
	if err := mgr.AddReadyzCheck("readyz", healthz.Ping); err != nil {
		setupLog.Error(err, "unable to set up ready check")
		os.Exit(1)
	}

	setupLog.Info("starting manager", "mode", "platform")
	if err := mgr.Start(ctrl.SetupSignalHandler()); err != nil {
		setupLog.Error(err, "problem running manager")
		os.Exit(1)
	}
}

func runWebhook(metricsAddr, probeAddr string, port int, certDir string) {
	mgr, err := ctrl.NewManager(ctrl.GetConfigOrDie(), ctrl.Options{
		Scheme:                 scheme,
		Metrics:                metricsserver.Options{BindAddress: metricsAddr},
		HealthProbeBindAddress: probeAddr,
		WebhookServer: ctrlwebhook.NewServer(ctrlwebhook.Options{
			Port:    port,
			CertDir: certDir,
		}),
	})
	if err != nil {
		setupLog.Error(err, "unable to start manager")
		os.Exit(1)
	}

	wh := webhook.NewGitOpsWebhook(mgr.GetClient())
	mgr.GetWebhookServer().Register("/mutate-gitops", &admission.Webhook{Handler: wh})

	if err := mgr.AddHealthzCheck("healthz", healthz.Ping); err != nil {
		setupLog.Error(err, "unable to set up health check")
		os.Exit(1)
	}
	if err := mgr.AddReadyzCheck("readyz", healthz.Ping); err != nil {
		setupLog.Error(err, "unable to set up ready check")
		os.Exit(1)
	}

	setupLog.Info("starting webhook server", "mode", "webhook", "port", port)
	if err := mgr.Start(ctrl.SetupSignalHandler()); err != nil {
		setupLog.Error(err, "problem running webhook server")
		os.Exit(1)
	}
}
