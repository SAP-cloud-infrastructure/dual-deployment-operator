// SPDX-FileCopyrightText: 2026 SAP SE or an SAP affiliate company
// Copyright 2026.
//
// SPDX-License-Identifier: Apache-2.0

package source

import (
	"context"
	"path/filepath"

	"helm.sh/helm/v3/pkg/chart"
	"helm.sh/helm/v3/pkg/chart/loader"
)

// fakeChartLoader loads a chart from a local directory, ignoring repo/name/version.
type fakeChartLoader struct{ dir string }

func (f fakeChartLoader) Load(_ context.Context, _, _, _ string) (*chart.Chart, error) {
	return loader.Load(f.dir)
}

// fakeRootResolver resolves a url+subPath to a local-disk path, mirroring the
// production go-git RootResolver's layout: filepath.Join(cloneDir, rootSubPath,
// subPath), where cloneDir is baseDir (the stand-in for the clone), rootSubPath
// is parsed from the URL's `//root` separator via splitRef, and subPath is the
// per-mode seed/shoot path.
//
// Honoring rootSubPath is essential for the boundary (#4) test: with a URL like
// git::https://host/org/repo//stacks/app?ref=<sha>, the resolver returns
// baseDir/stacks/app/<subPath>, so an overlay there that references
// ../../common must resolve back inside baseDir (the clone) and be accepted,
// while a genuine escape above baseDir must still be rejected. A resolver that
// ignored the URL (returning baseDir/subPath) could never reproduce this.
//
// If the URL has no `//` separator (or cannot be parsed), rootSubPath is empty
// and the behavior matches the simple baseDir/subPath layout.
type fakeRootResolver struct{ baseDir string }

func (f fakeRootResolver) Resolve(_ context.Context, rawURL, subPath string) (fsPath string, cleanup func(), err error) {
	var rootSubPath string
	if _, sub, _, splitErr := splitRef(rawURL); splitErr == nil {
		rootSubPath = sub
	}
	return filepath.Join(f.baseDir, rootSubPath, subPath), func() {}, nil
}
