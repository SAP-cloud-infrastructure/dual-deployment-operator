// SPDX-FileCopyrightText: 2026 SAP SE or an SAP affiliate company
// Copyright 2026.
//
// SPDX-License-Identifier: Apache-2.0

package equivalence

import (
	"fmt"
	"os"
	"path/filepath"

	"github.com/SAP-cloud-infrastructure/dual-deployment-operator/api/v1alpha1"
	"sigs.k8s.io/yaml"
)

// Fixture pins one operator's equivalence inputs (design decision A: golden is
// rendered from git source at a pinned SHA).
type Fixture struct {
	Operator      string           `json:"operator"`
	ChartFullname string           `json:"chartFullname"`
	RepoURL       string           `json:"repoURL"`
	SHA           string           `json:"sha"`
	ChartPath     string           `json:"chartPath"`
	OverlayValues string           `json:"overlayValues"`
	CRFile        string           `json:"cr"`
	Exclusions    []ExclusionEntry `json:"exclusions"`

	CR         *v1alpha1.DualDeploymentOperator `json:"-"`
	OverlayRaw []byte                           `json:"-"`
	Dir        string                           `json:"-"`
}

// LoadFixture reads fixture.yaml plus the referenced CR and overlay-values file.
func LoadFixture(dir string) (*Fixture, error) {
	b, err := os.ReadFile(filepath.Join(dir, "fixture.yaml"))
	if err != nil {
		return nil, err
	}
	var f Fixture
	if err := yaml.Unmarshal(b, &f); err != nil {
		return nil, fmt.Errorf("fixture: parse fixture.yaml: %w", err)
	}
	f.Dir = dir

	crBytes, err := os.ReadFile(filepath.Join(dir, f.CRFile))
	if err != nil {
		return nil, err
	}
	cr := &v1alpha1.DualDeploymentOperator{}
	if err := yaml.Unmarshal(crBytes, cr); err != nil {
		return nil, fmt.Errorf("fixture: parse cr: %w", err)
	}
	f.CR = cr

	f.OverlayRaw, err = os.ReadFile(filepath.Join(dir, f.OverlayValues))
	if err != nil {
		return nil, err
	}
	return &f, nil
}
