// SPDX-FileCopyrightText: 2026 SAP SE or an SAP affiliate company
// Copyright 2026.
//
// SPDX-License-Identifier: Apache-2.0

package source

import (
	"context"
	"fmt"
	"net/url"
	"os"
	"path/filepath"
	"strings"

	"helm.sh/helm/v3/pkg/action"
	"helm.sh/helm/v3/pkg/chart"
	"helm.sh/helm/v3/pkg/chart/loader"
	"helm.sh/helm/v3/pkg/cli"
	"helm.sh/helm/v3/pkg/registry"

	"sigs.k8s.io/controller-runtime/pkg/log"
)

// helmLoader is the production ChartLoader. It pulls charts from OCI (oci://) and
// classic HTTP(S) Helm repositories into a temp dir, loads them, and cleans up.
// No caching in this change (Phase 7.5).
type helmLoader struct {
	settings *cli.EnvSettings
	resolve  func(ctx context.Context, host string) (creds, error) // nil => anonymous
}

func newHelmLoader(resolve func(context.Context, string) (creds, error)) *helmLoader {
	return &helmLoader{settings: cli.New(), resolve: resolve}
}

func (l *helmLoader) Load(ctx context.Context, repo, name, version string) (*chart.Chart, error) {
	tmp, err := os.MkdirTemp("", "ddo-helm-")
	if err != nil {
		return nil, err
	}
	defer func() { _ = os.RemoveAll(tmp) }()

	var chartPath string
	switch {
	case strings.HasPrefix(repo, "oci://"):
		chartPath, err = l.pullOCI(ctx, repo, name, version, tmp)
	case strings.HasPrefix(repo, "http://"), strings.HasPrefix(repo, "https://"):
		chartPath, err = l.pullHTTP(ctx, repo, name, version, tmp)
	default:
		return nil, fmt.Errorf("source: unsupported chart repo scheme %q (only oci:// and http(s):// are supported)", repo)
	}
	if err != nil {
		return nil, err
	}
	return loader.Load(chartPath)
}

func (l *helmLoader) credFor(ctx context.Context, host string) creds {
	if l.resolve == nil {
		return creds{}
	}
	c, err := l.resolve(ctx, host)
	if err != nil {
		log.FromContext(ctx).Info(fmt.Sprintf("credential resolve failed for %s (proceeding anonymously): %v", host, err))
		return creds{}
	}
	return c
}

func (l *helmLoader) pullOCI(ctx context.Context, repo, name, version, dest string) (string, error) {
	c := l.credFor(ctx, ociHost(repo))

	opts := []registry.ClientOption{registry.ClientOptEnableCache(true)}
	if c.ok {
		opts = append(opts, registry.ClientOptBasicAuth(c.user, c.pass)) // inline, no on-disk Login
	}
	rc, err := registry.NewClient(opts...) // MUST be non-nil even anonymously
	if err != nil {
		return "", err
	}

	cfg := &action.Configuration{RegistryClient: rc}
	pull := action.NewPullWithOpts(action.WithConfig(cfg))
	pull.Settings = l.settings
	pull.Version = version
	pull.DestDir = dest
	ref := "oci://" + strings.TrimSuffix(strings.TrimPrefix(repo, "oci://"), "/") + "/" + name
	if _, err := pull.Run(ref); err != nil {
		return "", fmt.Errorf("source: oci pull %s@%s: %w", name, version, err)
	}
	return findTGZ(dest)
}

func (l *helmLoader) pullHTTP(ctx context.Context, repo, name, version, dest string) (string, error) {
	c := l.credFor(ctx, hostOf(repo))
	rc, err := registry.NewClient(registry.ClientOptEnableCache(true))
	if err != nil {
		return "", err
	}
	cfg := &action.Configuration{RegistryClient: rc}
	pull := action.NewPullWithOpts(action.WithConfig(cfg))
	pull.Settings = l.settings
	pull.RepoURL = repo
	pull.Version = version
	pull.DestDir = dest
	if c.ok {
		pull.Username = c.user
		pull.Password = c.pass
	}
	if _, err := pull.Run(name); err != nil {
		return "", fmt.Errorf("source: http repo pull %s@%s: %w", name, version, err)
	}
	return findTGZ(dest)
}

func ociHost(repo string) string { return hostOf(strings.Replace(repo, "oci://", "https://", 1)) }

func hostOf(raw string) string {
	if u, err := url.Parse(raw); err == nil && u.Host != "" {
		return u.Host
	}
	return raw
}

// findTGZ locates the pulled chart tgz in dest (helm writes <name>-<version>.tgz).
func findTGZ(dest string) (string, error) {
	entries, _ := os.ReadDir(dest)
	for _, e := range entries {
		if strings.HasSuffix(e.Name(), ".tgz") {
			return filepath.Join(dest, e.Name()), nil
		}
	}
	return "", fmt.Errorf("source: no chart tgz found in %s", dest)
}
