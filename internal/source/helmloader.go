// SPDX-FileCopyrightText: 2026 SAP SE or an SAP affiliate company
// Copyright 2026.
//
// SPDX-License-Identifier: Apache-2.0

package source

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strings"

	"helm.sh/helm/v3/pkg/action"
	"helm.sh/helm/v3/pkg/chart"
	"helm.sh/helm/v3/pkg/chart/loader"
	"helm.sh/helm/v3/pkg/chartutil"
	"helm.sh/helm/v3/pkg/cli"
	"helm.sh/helm/v3/pkg/downloader"
	"helm.sh/helm/v3/pkg/getter"
	"helm.sh/helm/v3/pkg/registry"
)

// helmLoader is the production ChartLoader. It pulls charts from OCI (oci://)
// registries into a temp dir, loads them, and cleans up.
// No caching in this change (Phase 7.5).
type helmLoader struct {
	settings *cli.EnvSettings
	resolve  func(ctx context.Context, host string) (creds, error) // nil => anonymous
	// httpClient overrides the Helm registry client's HTTP client. Production MUST
	// leave it nil; only tests set it, to trust an in-process self-signed TLS registry.
	httpClient *http.Client
	// scratchDir bases the per-Load temp dir; empty uses os.TempDir(). MUST be set
	// to a writable mounted volume when the container root FS is read-only, else
	// os.MkdirTemp("") fails at runtime.
	scratchDir string
}

func newHelmLoader(resolve func(context.Context, string) (creds, error)) *helmLoader {
	return &helmLoader{settings: cli.New(), resolve: resolve}
}

func (l *helmLoader) newScratchDir() (string, error) {
	return os.MkdirTemp(l.scratchDir, "ddo-helm-")
}

func (l *helmLoader) Load(ctx context.Context, repoURL, name, version string) (*chart.Chart, error) {
	if err := rejectURLCredentials(repoURL); err != nil {
		return nil, err
	}

	tmp, err := l.newScratchDir()
	if err != nil {
		return nil, err
	}
	defer func() { _ = os.RemoveAll(tmp) }()

	var chartPath string
	switch {
	case strings.HasPrefix(repoURL, "oci://"):
		chartPath, err = l.pullOCI(ctx, repoURL, name, version, tmp)
	default:
		return nil, errors.New("source: unsupported chart repo scheme (only oci:// is supported)")
	}
	if err != nil {
		return nil, err
	}
	// Resolve declared subchart dependencies at pull time. downloader.Manager works
	// on an unpacked directory, so expand the pulled .tgz first.
	chartDir, err := l.expandAndResolveDeps(ctx, chartPath, tmp)
	if err != nil {
		return nil, err
	}
	return loader.Load(chartDir)
}

func (l *helmLoader) credFor(ctx context.Context, _ string) (creds, error) {
	if l.resolve == nil {
		return creds{}, nil // no resolver configured => anonymous
	}
	return l.resolve(ctx, "")
}

func (l *helmLoader) pullOCI(ctx context.Context, repoURL, name, version, dest string) (string, error) {
	c, err := l.credFor(ctx, ociHost(repoURL))
	if err != nil {
		return "", fmt.Errorf("source: resolve credentials: %w", err)
	}

	rc, err := l.ociRegistryClient(c)
	if err != nil {
		return "", err
	}

	cfg := &action.Configuration{RegistryClient: rc}
	pull := action.NewPullWithOpts(action.WithConfig(cfg))
	pull.Settings = l.settings
	pull.Version = version
	pull.DestDir = dest
	ref := "oci://" + strings.TrimSuffix(strings.TrimPrefix(repoURL, "oci://"), "/") + "/" + name
	if _, err := pull.Run(ref); err != nil {
		return "", fmt.Errorf("source: oci pull %s@%s: %w", name, version, err)
	}
	return findTGZ(dest)
}

// ociRegistryClient builds the registry client used for both OCI chart pulls and
// dependency resolution, so the same cache / optional basic-auth / test httpClient
// seam applies to subchart fetches. Production leaves httpClient nil.
func (l *helmLoader) ociRegistryClient(c creds) (*registry.Client, error) {
	opts := []registry.ClientOption{registry.ClientOptEnableCache(true)}
	if c.ok {
		opts = append(opts, registry.ClientOptBasicAuth(c.user, c.pass)) // inline, no on-disk Login
	}
	if l.httpClient != nil {
		opts = append(opts, registry.ClientOptHTTPClient(l.httpClient))
	}
	return registry.NewClient(opts...)
}

// rejectURLCredentials rejects a repo URL with embedded userinfo (user:token@host)
// so credentials can never leak into error strings / status conditions; use
// authSecretRef instead. Mirrors the kustomize resolver's guard.
func rejectURLCredentials(repoURL string) error {
	u, err := url.Parse(strings.Replace(repoURL, "oci://", "https://", 1))
	if err != nil {
		return errors.New("source: chart repo url is not parseable")
	}
	if u.User != nil {
		return errors.New("source: chart repo url must not embed credentials in the URL; use authSecretRef")
	}
	return nil
}

func ociHost(repoURL string) string {
	return hostOf(strings.Replace(repoURL, "oci://", "https://", 1))
}

func hostOf(raw string) string {
	if u, err := url.Parse(raw); err == nil && u.Host != "" {
		return u.Host
	}
	return raw
}

// repoScope returns the transport+host+path scope for the cache key.
func (l *helmLoader) repoScope(repoURL string) (string, error) {
	switch {
	case strings.HasPrefix(repoURL, "oci://"):
		return "oci:" + ociHost(repoURL), nil
	default:
		return "", errors.New("source: unsupported chart repo scheme for repoScope (only oci:// is supported)")
	}
}

// ResolveID resolves the chart to its immutable id without pulling blob layers.
// OCI: the manifest digest (sha256:...).
func (l *helmLoader) ResolveID(ctx context.Context, repoURL, name, version string) (string, error) {
	if err := rejectURLCredentials(repoURL); err != nil {
		return "", err
	}
	switch {
	case strings.HasPrefix(repoURL, "oci://"):
		return l.resolveOCIDigest(ctx, repoURL, name, version)
	default:
		return "", errors.New("source: unsupported chart repo scheme (only oci:// is supported)")
	}
}

func (l *helmLoader) resolveOCIDigest(ctx context.Context, repoURL, name, version string) (string, error) {
	_ = ctx // Helm v3.21.3 registry.Client.Resolve takes no ctx; kept for future cancellation
	c, err := l.credFor(ctx, ociHost(repoURL))
	if err != nil {
		return "", fmt.Errorf("source: resolve credentials: %w", err)
	}
	opts := []registry.ClientOption{registry.ClientOptEnableCache(true)}
	if c.ok {
		opts = append(opts, registry.ClientOptBasicAuth(c.user, c.pass))
	}
	if l.httpClient != nil {
		opts = append(opts, registry.ClientOptHTTPClient(l.httpClient))
	}
	rc, err := registry.NewClient(opts...)
	if err != nil {
		return "", err
	}
	ref := strings.TrimSuffix(strings.TrimPrefix(repoURL, "oci://"), "/") + "/" + name + ":" + version
	desc, err := rc.Resolve(ref)
	if err != nil {
		return "", fmt.Errorf("source: resolve oci digest %s:%s: %w", name, version, err)
	}
	return desc.Digest.String(), nil
}

// findTGZ locates the pulled chart tgz in dest (helm writes <name>-<version>.tgz).
func findTGZ(dest string) (string, error) {
	entries, err := os.ReadDir(dest)
	if err != nil {
		return "", fmt.Errorf("source: read chart dir: %w", err)
	}
	for _, e := range entries {
		if strings.HasSuffix(e.Name(), ".tgz") {
			return filepath.Join(dest, e.Name()), nil
		}
	}
	return "", fmt.Errorf("source: no chart tgz found in %s", dest)
}

// expandAndResolveDeps expands the pulled chart .tgz under tmp, fails closed if it
// declares dependencies without a Chart.lock, runs downloader.Manager.Build() to
// fetch the pinned subcharts, and returns the chart directory to load.
func (l *helmLoader) expandAndResolveDeps(ctx context.Context, chartPath, tmp string) (string, error) {
	unpacked := filepath.Join(tmp, "unpacked")
	if err := os.MkdirAll(unpacked, 0o755); err != nil {
		return "", err
	}
	f, err := os.Open(chartPath)
	if err != nil {
		return "", err
	}
	defer func() { _ = f.Close() }()
	if err := chartutil.Expand(unpacked, f); err != nil {
		return "", fmt.Errorf("source: expand chart: %w", err)
	}
	chartDir, err := singleChildDir(unpacked)
	if err != nil {
		return "", err
	}
	needsBuild, err := resolveDepsPlan(chartDir)
	if err != nil {
		return "", err
	}
	if !needsBuild {
		return chartDir, nil
	}
	c, err := l.credFor(ctx, "")
	if err != nil {
		return "", fmt.Errorf("source: resolve credentials for deps: %w", err)
	}
	rc, err := l.ociRegistryClient(c)
	if err != nil {
		return "", err
	}
	m := &downloader.Manager{
		Out:              io.Discard,
		ChartPath:        chartDir,
		Getters:          getter.All(l.settings),
		RepositoryConfig: l.settings.RepositoryConfig,
		RepositoryCache:  l.settings.RepositoryCache,
		RegistryClient:   rc,
		SkipUpdate:       true,
	}
	if err := m.Build(); err != nil {
		return "", fmt.Errorf("source: build dependencies: %w", err)
	}
	return chartDir, nil
}

// singleChildDir returns the sole subdirectory of parent (the expanded chart root).
func singleChildDir(parent string) (string, error) {
	entries, err := os.ReadDir(parent)
	if err != nil {
		return "", fmt.Errorf("source: read expanded dir: %w", err)
	}
	for _, e := range entries {
		if e.IsDir() {
			return filepath.Join(parent, e.Name()), nil
		}
	}
	return "", fmt.Errorf("source: expanded chart has no directory in %s", parent)
}

// resolveDepsPlan decides whether downloader.Manager.Build() must run for the
// expanded chart at chartDir, and fails closed on dependencies the loader cannot
// resolve deterministically. It returns needsBuild=true only when at least one
// declared dependency is NOT already vendored under charts/ and therefore must be
// fetched.
//
// Two regimes:
//
//   - Fully vendored (no dependency needs fetching): accepted with needsBuild=false
//     regardless of repository scheme — Helm loads the vendored subcharts directly,
//     no Build runs. A chart may declare repository: https://... and vendor charts/.
//   - Build required (at least one dependency is unvendored): Build() reprocesses
//     the ENTIRE Chart.lock (hasAllRepos + downloadAll over every dependency), so
//     EVERY declared dependency — vendored or not — must be Build-safe: an OCI
//     (oci://) or file:// repository. A classic HTTP(S) dependency repo would make
//     Build fail with ErrRepoNotFound (the operator does not register repos or
//     cache indexes at reconcile time), so it is rejected fail-closed even if that
//     particular subchart happens to be vendored. A Chart.lock is also required so
//     Build never falls back to Update() (semver re-negotiation against the index).
func resolveDepsPlan(chartDir string) (needsBuild bool, err error) {
	meta, err := chartutil.LoadChartfile(filepath.Join(chartDir, "Chart.yaml"))
	if err != nil {
		return false, fmt.Errorf("source: read Chart.yaml: %w", err)
	}
	if len(meta.Dependencies) == 0 {
		return false, nil
	}
	fetch := false
	for _, d := range meta.Dependencies {
		if !vendoredSubchartExists(chartDir, d.Name) {
			fetch = true
		}
	}
	if !fetch {
		return false, nil
	}
	for _, d := range meta.Dependencies {
		if d.Repository == "" {
			// Build's downloadAll loads an empty-repository dep via loader.LoadDir,
			// which needs an unpacked charts/<name> directory — a packed .tgz alone
			// makes Build fail. Require the unpacked form in the Build regime.
			if !unpackedSubchartExists(chartDir, d.Name) {
				return false, fmt.Errorf("source: chart %q dependency %q declares no repository but is not vendored as an unpacked charts/%s directory; Build cannot resolve it — provide an oci:// repository or vendor it unpacked", meta.Name, d.Name, d.Name)
			}
			continue
		}
		if !strings.HasPrefix(d.Repository, "file://") && !registry.IsOCI(d.Repository) {
			return false, fmt.Errorf("source: chart %q dependency %q uses unsupported repository %q; when any dependency must be fetched, every dependency repository must be oci:// (or file://) because Build re-resolves the whole Chart.lock — vendor all subcharts, or republish this one via OCI", meta.Name, d.Name, d.Repository)
		}
	}
	if _, err := os.Stat(filepath.Join(chartDir, "Chart.lock")); err != nil {
		if os.IsNotExist(err) {
			return false, fmt.Errorf("source: chart %q declares unvendored dependencies but has no Chart.lock; commit Chart.lock for deterministic resolution", meta.Name)
		}
		return false, fmt.Errorf("source: stat Chart.lock: %w", err)
	}
	return true, nil
}

// vendoredSubchartExists reports whether subchart name is already present under
// chartDir/charts as either an unpacked directory or a packed <name>-*.tgz.
func vendoredSubchartExists(chartDir, name string) bool {
	if unpackedSubchartExists(chartDir, name) {
		return true
	}
	matches, err := filepath.Glob(filepath.Join(chartDir, "charts", name+"-*.tgz"))
	return err == nil && len(matches) > 0
}

// unpackedSubchartExists reports whether subchart name is present under
// chartDir/charts as an unpacked directory (the form Build's downloadAll requires
// for an empty-repository dependency, via loader.LoadDir).
func unpackedSubchartExists(chartDir, name string) bool {
	fi, err := os.Stat(filepath.Join(chartDir, "charts", name))
	return err == nil && fi.IsDir()
}
