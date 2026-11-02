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
