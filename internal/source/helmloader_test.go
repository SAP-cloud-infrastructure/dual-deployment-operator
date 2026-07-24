// SPDX-FileCopyrightText: 2026 SAP SE or an SAP affiliate company
// Copyright 2026.
//
// SPDX-License-Identifier: Apache-2.0

package source

import (
	"context"
	"strings"
	"testing"
)

func TestHelmLoaderRejectsUnknownScheme(t *testing.T) {
	l := newHelmLoader(nil)
	_, err := l.Load(context.Background(), "ftp://nope", "n", "1")
	if err == nil || !strings.Contains(err.Error(), "scheme") {
		t.Fatalf("expected scheme error, got %v", err)
	}
}

func TestHelmLoaderOnlineOCIAnonymous(t *testing.T) {
	requireOnline(t) // skips unless DDO_ONLINE_TESTS=1
	l := newHelmLoader(nil)
	ch, err := l.Load(context.Background(),
		"oci://keppel.global.cloud.sap/ccloud-helm", "metal-operator-remote", "0.6.2")
	if err != nil {
		t.Fatalf("anonymous OCI pull: %v", err)
	}
	if ch == nil || ch.Metadata == nil || ch.Metadata.Name == "" {
		t.Fatal("expected a parsed chart")
	}
}
