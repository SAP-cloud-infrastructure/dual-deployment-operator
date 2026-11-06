// SPDX-FileCopyrightText: 2026 SAP SE or an SAP affiliate company
// Copyright 2026.
//
// SPDX-License-Identifier: Apache-2.0

package main

import (
	"crypto/tls"
	"flag"
	"os"

	// Import all Kubernetes client auth plugins (e.g. Azure, GCP, OIDC, etc.)
	// to ensure that exec-entrypoint and run can make use of them.
	_ "k8s.io/client-go/plugin/pkg/client/auth"

	"k8s.io/apimachinery/pkg/runtime"
	utilruntime "k8s.io/apimachinery/pkg/util/runtime"
	clientgoscheme "k8s.io/client-go/kubernetes/scheme"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/cache"
	clientconfig "sigs.k8s.io/controller-runtime/pkg/client/config"
	"sigs.k8s.io/controller-runtime/pkg/healthz"
	"sigs.k8s.io/controller-runtime/pkg/log/zap"
	"sigs.k8s.io/controller-runtime/pkg/metrics/filters"
	metricsserver "sigs.k8s.io/controller-runtime/pkg/metrics/server"
	"sigs.k8s.io/controller-runtime/pkg/webhook"

	dualdeploymentoperatorv1alpha1 "github.com/SAP-cloud-infrastructure/dual-deployment-operator/api/v1alpha1"
	"github.com/SAP-cloud-infrastructure/dual-deployment-operator/internal/controller"
	"github.com/SAP-cloud-infrastructure/dual-deployment-operator/internal/deliver"
	"github.com/SAP-cloud-infrastructure/dual-deployment-operator/internal/source"
	webhookv1alpha1 "github.com/SAP-cloud-infrastructure/dual-deployment-operator/internal/webhook/v1alpha1"
	// +kubebuilder:scaffold:imports
)

var (
	scheme   = runtime.NewScheme()
	setupLog = ctrl.Log.WithName("setup")
)

// requireSHAPinnedRemoteBases is populated from
// --kustomize-require-sha-pinned-remote-bases and threaded into source.Deps.
// Declared at package level so tests can verify default registration.
var requireSHAPinnedRemoteBases bool

// buildSourceDeps constructs the source.Deps the controller uses.
// Extracted from main() to allow unit tests to exercise the CLI-flag→Deps
// assignment path without starting a manager.
func buildSourceDeps(scratchDir string, requireSHAPinned bool) source.Deps {
	return source.Deps{
		ChartLoader:                 source.NewHelmLoader(scratchDir),
		RootResolver:                source.NewGitResolver(),
		RenderCache:                 source.NewRenderCache(),
		RequireSHAPinnedRemoteBases: requireSHAPinned,
	}
}

func init() {
	utilruntime.Must(clientgoscheme.AddToScheme(scheme))

	utilruntime.Must(dualdeploymentoperatorv1alpha1.AddToScheme(scheme))
	// +kubebuilder:scaffold:scheme

	flag.BoolVar(&requireSHAPinnedRemoteBases, "kustomize-require-sha-pinned-remote-bases", false,
		"When true, every remote base reference the operator parses from the top-level kustomization must be pinned "+
			"to a full 40-hex lowercase commit SHA; tags, branches, and undeterminable refs are rejected before any "+
			"fetch occurs. Transitive/nested refs that krusty resolves internally, and remote refs embedded inside a "+
			"builtin plugin config file, are NOT pre-enforced (matching Flux/Argo CD).")
}

//nolint:gocyclo
func main() {
	var metricsAddr string
	var metricsCertPath, metricsCertName, metricsCertKey string
	var webhookCertPath, webhookCertName, webhookCertKey string
	var enableLeaderElection bool
	var probeAddr string
	var secureMetrics bool
	var enableHTTP2 bool
	var sourceScratchDir string
	var tlsOpts []func(*tls.Config)
	flag.StringVar(&metricsAddr, "metrics-bind-address", "0", "The address the metrics endpoint binds to. "+
		"Use :8443 for HTTPS or :8080 for HTTP, or leave as 0 to disable the metrics service.")
	flag.StringVar(&probeAddr, "health-probe-bind-address", ":8081", "The address the probe endpoint binds to.")
	flag.BoolVar(&enableLeaderElection, "leader-elect", false,
		"Enable leader election for controller manager. "+
			"Enabling this will ensure there is only one active controller manager.")
	flag.BoolVar(&secureMetrics, "metrics-secure", true,
		"If set, the metrics endpoint is served securely via HTTPS. Use --metrics-secure=false to use HTTP instead.")
	flag.StringVar(&webhookCertPath, "webhook-cert-path", "", "The directory that contains the webhook certificate.")
	flag.StringVar(&webhookCertName, "webhook-cert-name", "tls.crt", "The name of the webhook certificate file.")
	flag.StringVar(&webhookCertKey, "webhook-cert-key", "tls.key", "The name of the webhook key file.")
	flag.StringVar(&metricsCertPath, "metrics-cert-path", "",
		"The directory that contains the metrics server certificate.")
	flag.StringVar(&metricsCertName, "metrics-cert-name", "tls.crt", "The name of the metrics server certificate file.")
	flag.StringVar(&metricsCertKey, "metrics-cert-key", "tls.key", "The name of the metrics server key file.")
	flag.BoolVar(&enableHTTP2, "enable-http2", false,
		"If set, HTTP/2 will be enabled for the metrics and webhook servers")
	flag.StringVar(&sourceScratchDir, "source-scratch-dir", "",
		"Base directory for the Helm chart loader's per-render temp dir. Leave empty to use the OS temp dir; "+
			"set to a writable mounted volume when the container root filesystem is read-only.")
	opts := zap.Options{
		Development: true,
	}
	opts.BindFlags(flag.CommandLine)
	// Register the controller-runtime --kubeconfig flag so a local run can point the
	// seed client at an explicit kubeconfig. When unset, GetConfigOrDie() falls back to
	// KUBECONFIG, then $HOME/.kube/config, then in-cluster config.
	clientconfig.RegisterFlags(flag.CommandLine)
	flag.Parse()

	// When a dedicated source scratch dir is configured (required under a
	// read-only root filesystem), point the process TMPDIR at it so Go's
	// os.MkdirTemp and git's clone temp files land on the writable mount.
	// Done in-process (not only via the Deployment env) so a Helm values list
	// override of the container env cannot silently drop it and strand clones
	// on a read-only /tmp.
	if sourceScratchDir != "" {
		if err := os.Setenv("TMPDIR", sourceScratchDir); err != nil {
			setupLog.Error(err, "unable to set TMPDIR from --source-scratch-dir")
			os.Exit(1)
		}
	}

	// Constrain git-CLI-driven fetches to HTTPS only. GIT_ALLOW_PROTOCOL behaves
	// as git's protocol.allow=never with https=always: any protocol not listed is
	// denied, so ssh://, git://, file:// and others are blocked at the git-CLI
	// layer. This covers the fetches krusty runs by shelling out to the `git`
	// binary for remote bases and the submodules it resolves internally.
	//
	// This is set via os.Setenv in main() (not only as a Deployment env var) so
	// a Helm manager.env list-override cannot silently drop it.
	//
	// What this does NOT cover:
	//   - The operator's OWN go-git calls (the kustomize RootResolver that clones
	//     the root URL). go-git does not honor GIT_ALLOW_PROTOCOL — it still
	//     accepts http:// and file://. The root URL's transport is NOT constrained
	//     here; CEL admission only requires a ref= on the root URL. If HTTPS-only
	//     must hold for the root it needs a separate, explicit scheme check.
	//   - krusty's own net/http GET of a remote FILE inside a remote base (that
	//     is Go http, not git); constrained by classifyHTTPURL on top-level refs.
	//
	// Containment of a hung or slow remote fetch (both the git-CLI fetches above
	// and krusty's/Helm's own net/http GETs) is operational, matching Flux CD and
	// Argo CD: destination control via the chart's optional egress NetworkPolicy
	// plus the Gardener egress labels on the manager pod, and the operator's
	// reconcile/pod lifecycle. The operator does not mutate http.DefaultTransport
	// in-process (that global-transport hack was removed as an anti-pattern — see
	// the kustomize-remote-bases guide).
	if err := os.Setenv("GIT_ALLOW_PROTOCOL", "https"); err != nil {
		// os.Setenv only fails on Windows with invalid env var names; log and
		// continue rather than exit so the operator still starts on other issues.
		setupLog.Error(err, "Failed to set GIT_ALLOW_PROTOCOL")
	}

	ctrl.SetLogger(zap.New(zap.UseFlagOptions(&opts)))

	// if the enable-http2 flag is false (the default), http/2 should be disabled
	// due to its vulnerabilities. More specifically, disabling http/2 will
	// prevent from being vulnerable to the HTTP/2 Stream Cancellation and
	// Rapid Reset CVEs. For more information see:
	// - https://github.com/advisories/GHSA-qppj-fm5r-hxr3
	// - https://github.com/advisories/GHSA-4374-p667-p6c8
	disableHTTP2 := func(c *tls.Config) {
		setupLog.Info("Disabling HTTP/2")
		c.NextProtos = []string{"http/1.1"}
	}

	if !enableHTTP2 {
		tlsOpts = append(tlsOpts, disableHTTP2)
	}

	// Initial webhook TLS options
	webhookTLSOpts := tlsOpts
	webhookServerOptions := webhook.Options{
		TLSOpts: webhookTLSOpts,
	}

	if webhookCertPath != "" {
		setupLog.Info("Initializing webhook certificate watcher using provided certificates",
			"webhook-cert-path", webhookCertPath, "webhook-cert-name", webhookCertName, "webhook-cert-key", webhookCertKey)

		webhookServerOptions.CertDir = webhookCertPath
		webhookServerOptions.CertName = webhookCertName
		webhookServerOptions.KeyName = webhookCertKey
	}

	webhookServer := webhook.NewServer(webhookServerOptions)

	// Metrics endpoint is enabled in 'config/default/kustomization.yaml'. The Metrics options configure the server.
	// More info:
	// - https://pkg.go.dev/sigs.k8s.io/controller-runtime@v0.24.1/pkg/metrics/server
	// - https://book.kubebuilder.io/reference/metrics.html
	metricsServerOptions := metricsserver.Options{
		BindAddress:   metricsAddr,
		SecureServing: secureMetrics,
		TLSOpts:       tlsOpts,
	}

	if secureMetrics {
		// FilterProvider is used to protect the metrics endpoint with authn/authz.
		// These configurations ensure that only authorized users and service accounts
		// can access the metrics endpoint. The RBAC are configured in 'config/rbac/kustomization.yaml'. More info:
		// https://pkg.go.dev/sigs.k8s.io/controller-runtime@v0.24.1/pkg/metrics/filters#WithAuthenticationAndAuthorization
		metricsServerOptions.FilterProvider = filters.WithAuthenticationAndAuthorization
	}

	// If the certificate is not specified, controller-runtime will automatically
	// generate self-signed certificates for the metrics server. While convenient for development and testing,
	// this setup is not recommended for production.
	//
	// TODO(user): If you enable certManager, uncomment the following lines:
	// - [METRICS-WITH-CERTS] at config/default/kustomization.yaml to generate and use certificates
	// managed by cert-manager for the metrics server.
	// - [PROMETHEUS-WITH-CERTS] at config/prometheus/kustomization.yaml for TLS certification.
	if metricsCertPath != "" {
		setupLog.Info("Initializing metrics certificate watcher using provided certificates",
			"metrics-cert-path", metricsCertPath, "metrics-cert-name", metricsCertName, "metrics-cert-key", metricsCertKey)

		metricsServerOptions.CertDir = metricsCertPath
		metricsServerOptions.CertName = metricsCertName
		metricsServerOptions.KeyName = metricsCertKey
	}

	// Per-shoot design mandate: scope the cache to the operator's own namespace
	// (POD_NAMESPACE, set via the downward API). Empty means local dev — watch all.
	var cacheOpts cache.Options
	if namespace := os.Getenv("POD_NAMESPACE"); namespace != "" {
		cacheOpts.DefaultNamespaces = map[string]cache.Config{namespace: {}}
		setupLog.Info("Cache scoped to namespace", "namespace", namespace)
	} else {
		setupLog.Info("POD_NAMESPACE not set, cache watching all namespaces (local dev)")
	}

	mgr, err := ctrl.NewManager(ctrl.GetConfigOrDie(), ctrl.Options{
		Scheme:                 scheme,
		Metrics:                metricsServerOptions,
		WebhookServer:          webhookServer,
		Cache:                  cacheOpts,
		HealthProbeBindAddress: probeAddr,
		LeaderElection:         enableLeaderElection,
		LeaderElectionID:       "9ca8e82a.cc.sap",
		// LeaderElectionReleaseOnCancel defines if the leader should step down voluntarily
		// when the Manager ends. This requires the binary to immediately end when the
		// Manager is stopped, otherwise, this setting is unsafe. Setting this significantly
		// speeds up voluntary leader transitions as the new leader don't have to wait
		// LeaseDuration time first.
		//
		// In the default scaffold provided, the program ends immediately after
		// the manager stops, so would be fine to enable this option. However,
		// if you are doing or is intended to do any operation such as perform cleanups
		// after the manager stops then its usage might be unsafe.
		LeaderElectionReleaseOnCancel: true,
	})
	if err != nil {
		setupLog.Error(err, "Failed to start manager")
		os.Exit(1)
	}

	recorder := mgr.GetEventRecorder("dual-deployment-operator")
	if err := (&controller.DualDeploymentOperatorReconciler{
		Client:   mgr.GetClient(),
		Scheme:   mgr.GetScheme(),
		Recorder: recorder,
		SeedApplier: &deliver.SSAApplier{
			Client:       mgr.GetClient(),
			FieldManager: controller.FieldManagerName,
			Cluster:      "seed",
		},
		SourceDeps: buildSourceDeps(sourceScratchDir, requireSHAPinnedRemoteBases),
	}).SetupWithManager(mgr); err != nil {
		setupLog.Error(err, "Failed to create controller", "controller", "dualdeploymentoperator")
		os.Exit(1)
	}
	if os.Getenv("ENABLE_WEBHOOKS") != "false" {
		if err := webhookv1alpha1.SetupDualDeploymentOperatorWebhookWithManager(mgr); err != nil {
			setupLog.Error(err, "Failed to create webhook", "webhook", "DualDeploymentOperator")
			os.Exit(1)
		}
	}
	// +kubebuilder:scaffold:builder

	if err := mgr.AddHealthzCheck("healthz", healthz.Ping); err != nil {
		setupLog.Error(err, "Failed to set up health check")
		os.Exit(1)
	}
	if err := mgr.AddReadyzCheck("readyz", healthz.Ping); err != nil {
		setupLog.Error(err, "Failed to set up ready check")
		os.Exit(1)
	}

	setupLog.Info("Starting manager")
	if err := mgr.Start(ctrl.SetupSignalHandler()); err != nil {
		setupLog.Error(err, "Failed to run manager")
		os.Exit(1)
	}
}
