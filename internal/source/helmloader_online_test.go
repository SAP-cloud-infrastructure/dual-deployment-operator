// SPDX-FileCopyrightText: 2026 SAP SE or an SAP affiliate company
// Copyright 2026.
//
// SPDX-License-Identifier: Apache-2.0

package source

// TestHelmLoaderOnlineOCIAnonymous exercises the production helmLoader against a
// real public OCI registry (ghcr.io) without authentication. Makes a real network
// call on every run.

import (
	"context"
	"testing"
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
