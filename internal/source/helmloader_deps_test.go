// SPDX-FileCopyrightText: 2026 SAP SE or an SAP affiliate company
// SPDX-License-Identifier: Apache-2.0

package source

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// writeChartDir writes a minimal expanded chart directory. If deps is true it adds
// a dependencies: entry to Chart.yaml; if lock is true it also writes a Chart.lock.
func writeChartDir(t *testing.T, name string, deps, lock bool) string {
	t.Helper()
	dir := t.TempDir()
	chartDir := filepath.Join(dir, name)
	if err := os.MkdirAll(filepath.Join(chartDir, "templates"), 0o755); err != nil {
		t.Fatal(err)
	}
	cy := "apiVersion: v2\nname: " + name + "\nversion: 0.1.0\n"
	if deps {
		cy += "dependencies:\n  - name: sub\n    version: 0.1.0\n    repository: oci://example.test/charts\n"
	}
	if err := os.WriteFile(filepath.Join(chartDir, "Chart.yaml"), []byte(cy), 0o644); err != nil {
		t.Fatal(err)
	}
	if lock {
		if err := os.WriteFile(filepath.Join(chartDir, "Chart.lock"), []byte("dependencies: []\ndigest: sha256:x\ngenerated: \"2026-01-01T00:00:00Z\"\n"), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	return chartDir
}

func TestRequireLockIfDeps(t *testing.T) {
	// deps + no lock => error
	if err := requireLockIfDeps(writeChartDir(t, "c", true, false)); err == nil {
		t.Fatal("expected error for dependency-declaring chart without Chart.lock")
	} else if !strings.Contains(err.Error(), "Chart.lock") {
		t.Fatalf("error should mention Chart.lock, got: %v", err)
	}
	// deps + lock => ok
	if err := requireLockIfDeps(writeChartDir(t, "c", true, true)); err != nil {
		t.Fatalf("deps+lock should pass: %v", err)
	}
	// no deps + no lock => ok (no requirement)
	if err := requireLockIfDeps(writeChartDir(t, "c", false, false)); err != nil {
		t.Fatalf("no-deps chart must not require a lock: %v", err)
	}
}
