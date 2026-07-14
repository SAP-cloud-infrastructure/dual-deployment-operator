// SPDX-FileCopyrightText: 2026 SAP SE or an SAP affiliate company
// Copyright 2026.
//
// SPDX-License-Identifier: Apache-2.0

// Package manifest defines the Manifest value type and the multi-document
// YAML parser that turns rendered source output into origin-tagged manifests.
package manifest

import "k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"

// OriginAnnotation is the metadata annotation a chart/kustomization author sets
// on resources their own team wrote. Its presence with value "additions"
// classifies a manifest as OriginAdditions; absence defaults to the caller's
// fallback (upstream). It never influences host-vs-remote routing.
const OriginAnnotation = "dual-deployment-operator.cc.sap/origin"

// Origin classifies who authored a manifest: the upstream chart/kustomization
// or our own additions. It is NOT a destination/routing signal.
type Origin string

const (
	// OriginUpstream marks a resource that came from the upstream subchart or reference.
	OriginUpstream Origin = "upstream"
	// OriginAdditions marks a resource authored by our team (carries OriginAnnotation).
	OriginAdditions Origin = "additions"
)

// Manifest is a single rendered Kubernetes object plus its origin classification.
// It carries no target/destination field: destination is implicit from which
// render (host or remote) produced it.
type Manifest struct {
	Unstructured *unstructured.Unstructured
	Origin       Origin
}
