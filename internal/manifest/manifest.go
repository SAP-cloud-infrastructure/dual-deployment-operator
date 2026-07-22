// SPDX-FileCopyrightText: 2026 SAP SE or an SAP affiliate company
// Copyright 2026.
//
// SPDX-License-Identifier: Apache-2.0

// Package manifest defines the Manifest value type and the multi-document
// YAML parser that turns rendered source output into origin-tagged manifests.
package manifest

import (
	"crypto/sha256"
	"encoding/hex"

	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
)

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

// OwnedByLabel is the CR-identity ownership label key stamped on every applied
// object. Its value is OwnedByValue(namespace, name) — a fixed-length hash, NOT the
// raw "<ns>_<name>", because a Kubernetes label value is capped at 63 chars (a raw
// join of a shoot-cp namespace + operator name overflows that) and a single-"_" join
// is non-injective. Reused by prune safety and the cluster-scoped conflict guard.
const OwnedByLabel = "dual-deployment-operator.cc.sap/owned-by"

// OwnedByValue derives the stable, fixed-length ownership label value for a CR from
// its namespace and name. The value is the first 16 hex chars (64 bits) of
// sha256("<namespace>/<name>"): well under the 63-char label-value limit, injective
// across the ns/name boundary (the "/" separator cannot appear in either DNS-1123
// component), and deterministic.
func OwnedByValue(namespace, name string) string {
	sum := sha256.Sum256([]byte(namespace + "/" + name))
	return hex.EncodeToString(sum[:])[:16]
}

// StripInternalAnnotations removes the origin authorship annotation before apply
// and drops the annotations map entirely if it becomes empty.
func (m Manifest) StripInternalAnnotations() {
	anns := m.Unstructured.GetAnnotations()
	if anns == nil {
		return
	}
	delete(anns, OriginAnnotation)
	if len(anns) == 0 {
		m.Unstructured.SetAnnotations(nil)
		return
	}
	m.Unstructured.SetAnnotations(anns)
}

// SetOwnedByLabel stamps the CR-identity ownership label. The caller passes the value
// from OwnedByValue(cr.Namespace, cr.Name).
func (m Manifest) SetOwnedByLabel(ownedBy string) {
	labels := m.Unstructured.GetLabels()
	if labels == nil {
		labels = map[string]string{}
	}
	labels[OwnedByLabel] = ownedBy
	m.Unstructured.SetLabels(labels)
}
