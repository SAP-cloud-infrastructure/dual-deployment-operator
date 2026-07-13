// SPDX-FileCopyrightText: 2026 SAP SE or an SAP affiliate company
// Copyright 2026.
//
// SPDX-License-Identifier: Apache-2.0

package source

import (
	"context"
	"encoding/json"
	"fmt"

	"helm.sh/helm/v3/pkg/action"
	"helm.sh/helm/v3/pkg/chartutil"

	apiextensionsv1 "k8s.io/apiextensions-apiserver/pkg/apis/apiextensions/v1"

	v1alpha1 "github.com/SAP-cloud-infrastructure/dual-deployment-operator/api/v1alpha1"
	"github.com/SAP-cloud-infrastructure/dual-deployment-operator/internal/manifest"
)

type helmSource struct {
	spec   *v1alpha1.HelmSource
	loader ChartLoader
}

func (h *helmSource) Render(ctx context.Context, mode Mode) ([]manifest.Manifest, error) {
	ch, err := h.loader.Load(ctx, h.spec.Repo, h.spec.Name, h.spec.Version)
	if err != nil {
		return nil, fmt.Errorf("source: load chart: %w", err)
	}

	userVals, err := mergeValues(h.spec.Values, h.modeValues(mode))
	if err != nil {
		return nil, err
	}
	if _, set := userVals["mode"]; set {
		return nil, fmt.Errorf("source: 'mode' is operator-controlled and must not be set in values")
	}
	userVals["mode"] = string(mode)

	cfg := new(action.Configuration)
	inst := action.NewInstall(cfg)
	inst.DryRun = true
	inst.ClientOnly = true
	inst.IncludeCRDs = true
	inst.ReleaseName = h.spec.Name
	inst.Namespace = "default"

	rel, err := inst.RunWithContext(ctx, ch, userVals)
	if err != nil {
		return nil, fmt.Errorf("source: helm render (mode=%s): %w", mode, err)
	}
	return manifest.Parse([]byte(rel.Manifest), manifest.OriginUpstream)
}

func (h *helmSource) modeValues(mode Mode) *apiextensionsv1.JSON {
	switch mode {
	case ModeHost:
		return h.spec.HostValues
	case ModeRemote:
		return h.spec.RemoteValues
	}
	return nil
}

// mergeValues merges common values then mode-specific values (mode-specific wins).
func mergeValues(common, modeSpecific *apiextensionsv1.JSON) (map[string]interface{}, error) {
	base := map[string]interface{}{}
	for _, j := range []*apiextensionsv1.JSON{common, modeSpecific} {
		if j == nil || len(j.Raw) == 0 {
			continue
		}
		m := map[string]interface{}{}
		if err := json.Unmarshal(j.Raw, &m); err != nil {
			return nil, fmt.Errorf("source: parse values: %w", err)
		}
		base = chartutil.CoalesceTables(m, base)
	}
	return base, nil
}
