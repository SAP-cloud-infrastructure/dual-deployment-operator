//go:build online

// SPDX-FileCopyrightText: 2026 SAP SE or an SAP affiliate company
// Copyright 2026.
//
// SPDX-License-Identifier: Apache-2.0

package source

import (
	"os"
	"testing"
)

func requireOnline(t *testing.T) {
	t.Helper()
	if os.Getenv("DDO_ONLINE_TESTS") != "1" {
		t.Skip("set DDO_ONLINE_TESTS=1 to run online tests")
	}
}
