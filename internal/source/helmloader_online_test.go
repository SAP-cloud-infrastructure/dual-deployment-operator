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
