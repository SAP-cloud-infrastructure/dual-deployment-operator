// SPDX-FileCopyrightText: 2026 SAP SE or an SAP affiliate company
// Copyright 2026.
//
// SPDX-License-Identifier: Apache-2.0

package manifest

import (
	"bytes"
	"testing"
)

func TestParseMultipleDocsInOrder(t *testing.T) {
	raw := []byte(`apiVersion: v1
kind: ConfigMap
metadata:
  name: a
---
apiVersion: v1
kind: ConfigMap
metadata:
  name: b
---
apiVersion: v1
kind: ConfigMap
metadata:
  name: c
`)
	got, err := Parse(raw, OriginUpstream)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(got) != 3 {
		t.Fatalf("len = %d, want 3", len(got))
	}
	for i, want := range []string{"a", "b", "c"} {
		if got[i].Unstructured.GetName() != want {
			t.Errorf("doc[%d] name = %q, want %q", i, got[i].Unstructured.GetName(), want)
		}
	}
}

func TestParseSkipsEmptyAndCommentDocs(t *testing.T) {
	raw := []byte(`# leading comment only
---
apiVersion: v1
kind: ConfigMap
metadata:
  name: real
---

---
# trailing comment doc
`)
	got, err := Parse(raw, OriginUpstream)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(got) != 1 {
		t.Fatalf("len = %d, want 1", len(got))
	}
	if got[0].Unstructured.GetName() != "real" {
		t.Errorf("name = %q, want real", got[0].Unstructured.GetName())
	}
}

func TestParseEmptyRenderReturnsEmpty(t *testing.T) {
	got, err := Parse([]byte("# only a comment\n---\n\n"), OriginUpstream)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(got) != 0 {
		t.Fatalf("len = %d, want 0", len(got))
	}
}

func TestParseRejectsMissingAPIVersion(t *testing.T) {
	raw := []byte(`kind: ConfigMap
metadata:
  name: broken
`)
	_, err := Parse(raw, OriginUpstream)
	if err == nil {
		t.Fatal("expected error for missing apiVersion, got nil")
	}
	if !contains(err.Error(), "apiVersion") {
		t.Errorf("error = %q, want it to mention apiVersion", err.Error())
	}
}

func TestParseRejectsMissingKind(t *testing.T) {
	raw := []byte(`apiVersion: v1
metadata:
  name: broken
`)
	_, err := Parse(raw, OriginUpstream)
	if err == nil {
		t.Fatal("expected error for missing kind, got nil")
	}
	if !contains(err.Error(), "kind") {
		t.Errorf("error = %q, want it to mention kind", err.Error())
	}
}

func contains(s, sub string) bool {
	return bytes.Contains([]byte(s), []byte(sub))
}
