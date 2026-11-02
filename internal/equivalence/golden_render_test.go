// SPDX-FileCopyrightText: 2026 SAP SE or an SAP affiliate company
// Copyright 2026.
//
// SPDX-License-Identifier: Apache-2.0

package equivalence

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	git "github.com/go-git/go-git/v5"
	"github.com/go-git/go-git/v5/plumbing/object"
)

func TestGoldenRenderMetalOperatorFromGit(t *testing.T) {
	requireEquivalenceEnabled(t)
	docs, err := RenderGolden(context.Background(), GoldenRenderReq{
		RepoURL:   "https://github.com/sapcc/helm-charts.git",
		SHA:       "33e68278ace26fe23e0c2b36a9266a58c9b47707",
		ChartPath: "system/metal-operator-remote",
		Namespace: "shoot--cp--m-qa-de-1",
		ValuesYAML: [][]byte{
			[]byte("global:\n  region: qa-de-1\n"),
		},
	})
	if err != nil {
		t.Fatalf("golden render failed: %v", err)
	}
	if len(docs) == 0 {
		t.Fatal("expected rendered documents from metal-operator-remote")
	}
}

func TestGoldenRenderAPIVersions(t *testing.T) {
	root := t.TempDir()
	for name, content := range map[string]string{
		"Chart.yaml":             "apiVersion: v2\nname: capability-test\nversion: 1.0.0\n",
		"templates/monitor.yaml": "{{- if .Capabilities.APIVersions.Has \"monitoring.coreos.com/v1\" }}\napiVersion: monitoring.coreos.com/v1\nkind: ServiceMonitor\nmetadata:\n  name: fixture\n{{- end }}\n",
	} {
		p := filepath.Join(root, name)
		if err := os.MkdirAll(filepath.Dir(p), 0o700); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(p, []byte(content), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	repo, err := git.PlainInit(root, false)
	if err != nil {
		t.Fatal(err)
	}
	w, err := repo.Worktree()
	if err != nil {
		t.Fatal(err)
	}
	if _, err := w.Add("."); err != nil {
		t.Fatal(err)
	}
	sha, err := w.Commit("fixture", &git.CommitOptions{Author: &object.Signature{Name: "test", Email: "test@example.invalid"}})
	if err != nil {
		t.Fatal(err)
	}
	for _, enabled := range []bool{false, true} {
		req := GoldenRenderReq{RepoURL: root, SHA: sha.String()}
		if enabled {
			req.APIVersions = []string{"monitoring.coreos.com/v1"}
		}
		docs, err := RenderGolden(context.Background(), req)
		if err != nil {
			t.Fatal(err)
		}
		if (len(docs) == 1) != enabled || len(docs) > 1 {
			t.Fatalf("enabled=%v: got %d documents", enabled, len(docs))
		}
	}
}
