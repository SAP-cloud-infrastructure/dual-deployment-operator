// SPDX-FileCopyrightText: 2026 SAP SE or an SAP affiliate company
// Copyright 2026.
//
// SPDX-License-Identifier: Apache-2.0

package source

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"strings"
)

// keyParts are the dimensions that uniquely identify a render for caching.
// sourceKind is redundant with repoScope's scheme today but retained as an
// explicit, self-documenting kind tag (see design.md).
type keyParts struct {
	sourceKind string // "helm" | "kustomize"
	repoScope  string // "oci:<host>" | "http:<host>" | "git:<host>"
	resolvedID string // OCI digest | git commit SHA | HTTP index digest-or-version
	mode       string // "seed" | "shoot"
	inputHash  string // hash of mode-specific inputs not covered by resolvedID
	namespace  string
}

// String renders a stable, collision-resistant cache key. "|" is a safe separator
// because no component contains it (hosts, hex ids, sha256 hex, k8s namespaces).
func (k keyParts) String() string {
	return strings.Join([]string{
		k.sourceKind, k.repoScope, k.resolvedID, k.mode, k.inputHash, k.namespace,
	}, "|")
}

// hashValues returns a sorted-key canonical hash of an arbitrary values map so
// that map ordering does not change the hash. json.Marshal sorts map keys.
func hashValues(v map[string]any) string {
	b, err := json.Marshal(v)
	if err != nil {
		// A values map that fails to marshal cannot be keyed soundly; return a
		// sentinel that still hashes deterministically for identical failures.
		b = []byte("\x00unmarshalable")
	}
	sum := sha256.Sum256(b)
	return hex.EncodeToString(sum[:])
}
