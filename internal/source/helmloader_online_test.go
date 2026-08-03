// SPDX-FileCopyrightText: 2026 SAP SE or an SAP affiliate company
// Copyright 2026.
//
// SPDX-License-Identifier: Apache-2.0

package source

// TestHelmLoaderOnlineOCIAnonymous exercises the production helmLoader against a
// real public OCI registry (ghcr.io) without authentication.
// TestHelmLoaderOnlineHTTPRepo exercises the classic HTTP(S) repo path (pullHTTP)
// against a real public Helm repo. Both make real network calls on every run.

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"testing"
	"time"

	"helm.sh/helm/v3/pkg/chart"
	"helm.sh/helm/v3/pkg/chartutil"
	"helm.sh/helm/v3/pkg/helmpath"
	"helm.sh/helm/v3/pkg/repo"
)

func TestHelmLoaderOnlineOCIAnonymous(t *testing.T) {
	l := newHelmLoader(nil)
	ch, err := l.Load(context.Background(),
		"oci://ghcr.io/stefanprodan/charts", "podinfo", "6.1.0")
	if err != nil {
		t.Fatalf("anonymous OCI pull: %v", err)
	}
	if ch == nil || ch.Metadata == nil || ch.Metadata.Name != "podinfo" {
		t.Fatalf("expected podinfo chart, got %+v", ch)
	}
}

func TestHelmLoaderOnlineHTTPRepo(t *testing.T) {
	l := newHelmLoader(nil)
	ch, err := l.Load(context.Background(),
		"https://prometheus-community.github.io/helm-charts", "kube-state-metrics", "8.0.0")
	if err != nil {
		t.Fatalf("anonymous HTTP repo pull: %v", err)
	}
	if ch == nil || ch.Metadata == nil || ch.Metadata.Name != "kube-state-metrics" {
		t.Fatalf("expected kube-state-metrics chart, got %+v", ch)
	}
}

// TestHelmLoaderOnlineResolvesRemoteDependency verifies that expandAndResolveDeps
// fetches a declared subchart from a real public Helm repository over the network.
//
// Real-network online test — no skip, no gate, no env var; matches the convention
// of TestHelmLoaderOnlineOCIAnonymous and TestHelmLoaderOnlineHTTPRepo.
// CI (with egress to prometheus-community.github.io) passes; a network-restricted
// sandbox fails with a DNS/dial error, which is the expected outcome there.
//
// What is tested: expandAndResolveDeps on a locally-built unvendored parent .tgz
// (deps declared in Chart.yaml, Chart.lock committed, empty charts/) calls
// downloader.Manager.Build() (SkipUpdate:true), which fetches
// prometheus-node-exporter 4.43.0 from the real prometheus-community Helm repo.
// After Build, charts/prometheus-node-exporter must exist in the expanded chart dir.
//
// Setup note: Helm v3's downloader.Manager.Build() requires HTTP repositories to be
// registered in repositories.yaml AND their index files pre-cached (SkipUpdate:true
// does not download the index).  This test creates a temporary Helm home, registers
// the repo, fetches the live index (real-network call #1), and caches it before
// calling expandAndResolveDeps, which then downloads the subchart (real-network #2).
// No production code is modified; only l.settings.RepositoryConfig/Cache are pointed
// at the temp paths before the call.
func TestHelmLoaderOnlineResolvesRemoteDependency(t *testing.T) {
	const (
		subName    = "prometheus-node-exporter"
		subVersion = "4.43.0"
		subRepo    = "https://prometheus-community.github.io/helm-charts"
		repoName   = "prometheus-community"
	)

	// Set up a temporary Helm home: repositories.yaml + cached index.
	helmHome := t.TempDir()
	cacheDir := filepath.Join(helmHome, "cache")
	if err := os.MkdirAll(cacheDir, 0o755); err != nil {
		t.Fatalf("create cache dir: %v", err)
	}

	// Write repositories.yaml with the prometheus-community repo registered.
	reposYAMLPath := filepath.Join(helmHome, "repositories.yaml")
	rf := repo.NewFile()
	rf.Add(&repo.Entry{Name: repoName, URL: subRepo})
	if err := rf.WriteFile(reposYAMLPath, 0o644); err != nil {
		t.Fatalf("write repositories.yaml: %v", err)
	}

	// Fetch the live index.yaml and write it to the repo cache.
	// Real-network call #1; in a network-restricted sandbox this fails with a DNS error.
	indexReq, err := http.NewRequestWithContext(context.Background(), http.MethodGet, subRepo+"/index.yaml", http.NoBody)
	if err != nil {
		t.Fatalf("build index request: %v", err)
	}
	indexResp, err := http.DefaultClient.Do(indexReq)
	if err != nil {
		t.Fatalf("fetch index.yaml from %s: %v", subRepo, err)
	}
	defer func() { _ = indexResp.Body.Close() }()
	if indexResp.StatusCode != http.StatusOK {
		t.Fatalf("fetch index.yaml: HTTP %d", indexResp.StatusCode)
	}
	indexData, err := io.ReadAll(indexResp.Body)
	if err != nil {
		t.Fatalf("read index.yaml body: %v", err)
	}
	// helmpath.CacheIndexFile("prometheus-community") == "prometheus-community-index.yaml"
	indexCachePath := filepath.Join(cacheDir, helmpath.CacheIndexFile(repoName))
	if err := os.WriteFile(indexCachePath, indexData, 0o644); err != nil {
		t.Fatalf("write cached index: %v", err)
	}

	// Build the unvendored parent .tgz with a committed Chart.lock.
	// No sub-chart object is attached, so chartutil.Save writes an archive without
	// any charts/ entry — the subchart's presence after Build is entirely network-sourced.
	dep := &chart.Dependency{Name: subName, Version: subVersion, Repository: subRepo}
	deps := []*chart.Dependency{dep}

	// Compute Chart.lock digest: sha256(json([chart-yaml-deps, lock-deps])).
	hashInput, err := json.Marshal([2][]*chart.Dependency{deps, deps})
	if err != nil {
		t.Fatalf("marshal deps for digest: %v", err)
	}
	sum := sha256.Sum256(hashInput)
	digest := "sha256:" + hex.EncodeToString(sum[:])

	parent := &chart.Chart{
		Metadata: &chart.Metadata{
			APIVersion:   chart.APIVersionV2,
			Name:         "onlineparent",
			Version:      "0.1.0",
			Dependencies: deps,
		},
		Lock: &chart.Lock{
			Generated:    time.Now(),
			Digest:       digest,
			Dependencies: deps,
		},
	}
	parentTGZPath, err := chartutil.Save(parent, t.TempDir())
	if err != nil {
		t.Fatalf("save parent chart: %v", err)
	}

	// Fixture invariant: archive must NOT vendor the subchart.
	// assertNoVendoredSubcharts is defined in helmloader_deps_test.go (same package).
	assertNoVendoredSubcharts(t, parentTGZPath)

	// Wire the temp Helm home into the loader and resolve deps.
	// expandAndResolveDeps reads l.settings.RepositoryConfig/Cache for the
	// downloader.Manager; pointing them at the temp paths keeps the test hermetic
	// w.r.t. the host's own Helm config while still reaching the real network.
	l := newHelmLoader(nil) // anonymous — prometheus-community requires no auth
	l.settings.RepositoryConfig = reposYAMLPath
	l.settings.RepositoryCache = cacheDir
	tmp := t.TempDir()

	chartDir, err := l.expandAndResolveDeps(context.Background(), parentTGZPath, tmp)
	if err != nil {
		t.Fatalf("expandAndResolveDeps: %v", err)
	}

	// After Build(), the subchart is present as a .tgz in charts/.
	// Helm's downloader writes <name>-<version>.tgz to charts/ (not an unpacked directory).
	subchartTGZ := filepath.Join(chartDir, "charts", subName+"-"+subVersion+".tgz")
	if _, err := os.Stat(subchartTGZ); err != nil {
		t.Fatalf("expected charts/%s-%s.tgz to exist after Build fetched it from %s: %v",
			subName, subVersion, subRepo, err)
	}
}
