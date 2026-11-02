// SPDX-FileCopyrightText: 2026 SAP SE or an SAP affiliate company
// Copyright 2026.
//
// SPDX-License-Identifier: Apache-2.0

package equivalence

import (
	"context"
	"fmt"
	"os"
	"path/filepath"

	git "github.com/go-git/go-git/v5"
	"github.com/go-git/go-git/v5/config"
	"github.com/go-git/go-git/v5/plumbing"
	"helm.sh/helm/v3/pkg/action"
	"helm.sh/helm/v3/pkg/chart/loader"
	"helm.sh/helm/v3/pkg/cli"
	"helm.sh/helm/v3/pkg/downloader"
	"helm.sh/helm/v3/pkg/getter"
	"helm.sh/helm/v3/pkg/registry"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"sigs.k8s.io/kustomize/api/krusty"
	"sigs.k8s.io/kustomize/api/types"
	"sigs.k8s.io/kustomize/kyaml/filesys"
	"sigs.k8s.io/yaml"
)

// GoldenRenderReq describes an option-A golden render: clone the wrapper chart
// from git at a pinned SHA, build its dependencies, then helm-template it.
type GoldenRenderReq struct {
	RepoURL    string   // e.g. https://github.com/sapcc/helm-charts.git
	SHA        string   // pinned commit SHA (immutable)
	ChartPath  string   // subpath within the repo, e.g. system/metal-operator-remote
	ValuesYAML [][]byte // overlay values layered over chart defaults (may be empty)
	Namespace  string   // release namespace for templating
}

// RenderGolden clones the repo at the pinned SHA, runs `helm dependency build`
// on the chart subdir, templates it client-only, and returns parsed documents.
// Fails loudly on any error (never skips).
func RenderGolden(ctx context.Context, req GoldenRenderReq) ([]*unstructured.Unstructured, error) {
	tmp, err := os.MkdirTemp("", "ddo-golden-git-")
	if err != nil {
		return nil, err
	}
	defer func() { _ = os.RemoveAll(tmp) }()

	// 1. Clone at the pinned SHA (shallow init + fetch + checkout by hash).
	repo, err := git.PlainInit(tmp, false)
	if err != nil {
		return nil, fmt.Errorf("golden: git init: %w", err)
	}
	remote, err := repo.CreateRemote(&config.RemoteConfig{Name: "origin", URLs: []string{req.RepoURL}})
	if err != nil {
		return nil, fmt.Errorf("golden: git remote: %w", err)
	}
	if err := remote.FetchContext(ctx, &git.FetchOptions{
		RefSpecs: []config.RefSpec{
			"+refs/heads/*:refs/heads/*",
			"+refs/tags/*:refs/tags/*",
		},
	}); err != nil {
		return nil, fmt.Errorf("golden: git fetch: %w", err)
	}
	w, err := repo.Worktree()
	if err != nil {
		return nil, fmt.Errorf("golden: worktree: %w", err)
	}
	if err := w.Checkout(&git.CheckoutOptions{Hash: plumbing.NewHash(req.SHA)}); err != nil {
		return nil, fmt.Errorf("golden: checkout %s: %w", req.SHA, err)
	}

	chartDir := filepath.Join(tmp, req.ChartPath)

	// 2. helm dependency build (resolves subcharts from OCI repos).
	rc, err := registry.NewClient(registry.ClientOptEnableCache(true))
	if err != nil {
		return nil, fmt.Errorf("golden: registry client: %w", err)
	}
	settings := cli.New()
	man := &downloader.Manager{
		Out:              os.Stderr,
		ChartPath:        chartDir,
		Getters:          getter.All(settings),
		RegistryClient:   rc,
		RepositoryConfig: settings.RepositoryConfig,
		RepositoryCache:  settings.RepositoryCache,
	}
	if err := man.Build(); err != nil {
		return nil, fmt.Errorf("golden: dependency build %s: %w", req.ChartPath, err)
	}

	// 3. Load + template client-only.
	ch, err := loader.Load(chartDir)
	if err != nil {
		return nil, fmt.Errorf("golden: load chart: %w", err)
	}
	vals, err := mergeValuesYAML(req.ValuesYAML)
	if err != nil {
		return nil, err
	}
	cfg := &action.Configuration{RegistryClient: rc}
	inst := action.NewInstall(cfg)
	inst.DryRun = true
	inst.ClientOnly = true
	inst.IncludeCRDs = true
	inst.ReleaseName = ch.Name()
	inst.Namespace = req.Namespace
	rel, err := inst.Run(ch, vals)
	if err != nil {
		return nil, fmt.Errorf("golden: template %s: %w", req.ChartPath, err)
	}
	return splitYAMLDocs([]byte(rel.Manifest))
}

// KustomizeGoldenRenderReq describes a kustomize-based golden render: clone the
// source repo from git at a pinned SHA and run krusty.Run on the given subpath.
type KustomizeGoldenRenderReq struct {
	RepoURL       string // e.g. https://github.com/sapcc/helm-charts.git
	SHA           string // pinned commit SHA (immutable)
	KustomizePath string // subpath within the repo to build, e.g. system/kustomize/ipam-capi-remote/managedresources
	Namespace     string // namespace to assign to namespaced resources
}

// RenderGoldenKustomize clones the repo at the pinned SHA, runs krusty.Run on
// the given kustomize subpath, and returns the parsed documents. Fails loudly
// on any error (never skips). The krusty build runs with LoadRestrictionsNone,
// matching the production kustomizeSource.Render behaviour, so krusty will
// fetch transitive remote bases during the build — this requires network access
// to github.com and raw.githubusercontent.com (same requirement as production).
func RenderGoldenKustomize(ctx context.Context, req KustomizeGoldenRenderReq) ([]*unstructured.Unstructured, error) {
	tmp, err := os.MkdirTemp("", "ddo-golden-kustomize-git-")
	if err != nil {
		return nil, err
	}
	defer func() { _ = os.RemoveAll(tmp) }()

	// 1. Clone at the pinned SHA (full fetch so arbitrary historical SHAs resolve).
	repo, err := git.PlainInit(tmp, false)
	if err != nil {
		return nil, fmt.Errorf("golden-kustomize: git init: %w", err)
	}
	remote, err := repo.CreateRemote(&config.RemoteConfig{Name: "origin", URLs: []string{req.RepoURL}})
	if err != nil {
		return nil, fmt.Errorf("golden-kustomize: git remote: %w", err)
	}
	if err := remote.FetchContext(ctx, &git.FetchOptions{
		RefSpecs: []config.RefSpec{
			"+refs/heads/*:refs/heads/*",
			"+refs/tags/*:refs/tags/*",
		},
	}); err != nil {
		return nil, fmt.Errorf("golden-kustomize: git fetch: %w", err)
	}
	w, err := repo.Worktree()
	if err != nil {
		return nil, fmt.Errorf("golden-kustomize: worktree: %w", err)
	}
	if err := w.Checkout(&git.CheckoutOptions{Hash: plumbing.NewHash(req.SHA)}); err != nil {
		return nil, fmt.Errorf("golden-kustomize: checkout %s: %w", req.SHA, err)
	}

	kustomizeDir := filepath.Join(tmp, req.KustomizePath)

	// 2. Run krusty with LoadRestrictionsNone (matches production kustomize source
	// behaviour; required because the overlay's remote bases live outside the
	// overlay subdir and krusty must be allowed to follow those URLs).
	opts := krusty.MakeDefaultOptions()
	opts.LoadRestrictions = types.LoadRestrictionsNone
	kust := krusty.MakeKustomizer(opts)
	resMap, err := kust.Run(filesys.MakeFsOnDisk(), kustomizeDir)
	if err != nil {
		return nil, fmt.Errorf("golden-kustomize: krusty build %s: %w", req.KustomizePath, err)
	}
	yamlBytes, err := resMap.AsYaml()
	if err != nil {
		return nil, fmt.Errorf("golden-kustomize: serialize: %w", err)
	}
	docs, err := splitYAMLDocs(yamlBytes)
	if err != nil {
		return nil, fmt.Errorf("golden-kustomize: parse docs: %w", err)
	}

	// 3. Apply namespace to namespaced resources (mirrors manifest.ApplyNamespace).
	for _, d := range docs {
		if d.GetNamespace() == "" {
			d.SetNamespace(req.Namespace)
		}
	}
	return docs, nil
}

func mergeValuesYAML(docs [][]byte) (map[string]any, error) {
	out := map[string]any{}
	for _, d := range docs {
		if len(d) == 0 {
			continue
		}
		m := map[string]any{}
		if err := yaml.Unmarshal(d, &m); err != nil {
			return nil, fmt.Errorf("golden: parse values: %w", err)
		}
		out = deepMerge(out, m)
	}
	return out, nil
}

func deepMerge(base, over map[string]any) map[string]any {
	for k, v := range over {
		if bv, ok := base[k].(map[string]any); ok {
			if ov, ok := v.(map[string]any); ok {
				base[k] = deepMerge(bv, ov)
				continue
			}
		}
		base[k] = v
	}
	return base
}
