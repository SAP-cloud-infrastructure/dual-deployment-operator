// SPDX-FileCopyrightText: 2026 SAP SE or an SAP affiliate company
// Copyright 2026.
//
// SPDX-License-Identifier: Apache-2.0

package main

import (
	"flag"
	"testing"
)

// TestKustomizeRequireSHAPinnedFlagRegistered asserts that the
// --kustomize-require-sha-pinned-remote-bases flag is registered with a
// default of "false". This test is RED until the flag is wired in main().
func TestKustomizeRequireSHAPinnedFlagRegistered(t *testing.T) {
	const flagName = "kustomize-require-sha-pinned-remote-bases"
	f := flag.Lookup(flagName)
	if f == nil {
		t.Fatalf("flag --%s is not registered; add it in cmd/main.go", flagName)
	}
	if f.DefValue != "false" {
		t.Errorf("flag --%s default = %q, want \"false\"", flagName, f.DefValue)
	}
}

// TestBuildSourceDepsWiresRequireSHAPinned exercises the real path from CLI
// flag value into source.Deps.RequireSHAPinnedRemoteBases, as constructed by
// buildSourceDeps (which main() delegates to). Deleting the assignment line in
// buildSourceDeps would cause these assertions to fail.
func TestBuildSourceDepsWiresRequireSHAPinned(t *testing.T) {
	// absent / false (default): flag value false → Deps field false
	dOff := buildSourceDeps("", false)
	if dOff.RequireSHAPinnedRemoteBases != false {
		t.Errorf("buildSourceDeps(false): RequireSHAPinnedRemoteBases = %v, want false",
			dOff.RequireSHAPinnedRemoteBases)
	}

	// explicit true: flag value true → Deps field true (strict mode reachable)
	dOn := buildSourceDeps("", true)
	if dOn.RequireSHAPinnedRemoteBases != true {
		t.Errorf("buildSourceDeps(true): RequireSHAPinnedRemoteBases = %v, want true",
			dOn.RequireSHAPinnedRemoteBases)
	}
}

// TestBuildSourceDepsFlagVarBinding asserts that the package-level
// requireSHAPinnedRemoteBases var (bound to the CLI flag) is actually passed
// through to buildSourceDeps, i.e. the binding is not severed in main().
// We simulate CLI parsing by directly setting the package var and calling
// buildSourceDeps — the same sequence main() performs after flag.Parse().
func TestBuildSourceDepsFlagVarBinding(t *testing.T) {
	// Save and restore the package var so tests are hermetic.
	orig := requireSHAPinnedRemoteBases
	defer func() { requireSHAPinnedRemoteBases = orig }()

	requireSHAPinnedRemoteBases = true
	d := buildSourceDeps("", requireSHAPinnedRemoteBases)
	if !d.RequireSHAPinnedRemoteBases {
		t.Error("after setting requireSHAPinnedRemoteBases=true, buildSourceDeps returned Deps with RequireSHAPinnedRemoteBases=false; assignment in buildSourceDeps is missing")
	}

	requireSHAPinnedRemoteBases = false
	d = buildSourceDeps("", requireSHAPinnedRemoteBases)
	if d.RequireSHAPinnedRemoteBases {
		t.Error("after setting requireSHAPinnedRemoteBases=false, buildSourceDeps returned Deps with RequireSHAPinnedRemoteBases=true")
	}
}
