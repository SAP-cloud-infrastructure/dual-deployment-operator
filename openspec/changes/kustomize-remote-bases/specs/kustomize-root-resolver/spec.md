<!--
SPDX-FileCopyrightText: 2026 SAP SE or an SAP affiliate company
SPDX-License-Identifier: Apache-2.0
-->

# Spec Delta: kustomize-root-resolver

## MODIFIED Requirements

### Requirement: Production git RootResolver via go-git

The operator MUST provide a production `RootResolver` implementation (`internal/source`) that satisfies the existing `RootResolver.Resolve(ctx, url, subPath) (fsPath string, cleanup func(), err error)` seam by fetching a remote git kustomize root into a local temporary directory using `github.com/go-git/go-git/v5` (pure Go, no `git` binary). It MUST return the path `<tempDir>/<subPath>` and a `cleanup` function that removes the temporary directory. The temporary directory MUST be created under the operator's configured source scratch directory (`--source-scratch-dir`, via `NewGitResolver(scratchDir)`), falling back to `os.TempDir()` only when the scratch directory is empty, so the resolver works under `securityContext.readOnlyRootFilesystem: true` (mirroring the Helm loader). The renderer logic and the `RootResolver` interface signature MUST NOT change. The resolver fetches the ROOT; transitive remote bases are NO LONGER left for krusty to fetch at build time — they are discovered, pinned, fetched, and localized by the kustomize-remote-bases prefetch step before krusty runs (see the kustomize-remote-bases capability). The resolver's own responsibility remains fetching the single root repository at its pinned ref.

#### Scenario: pinned root is fetched and the mode subpath returned

- **WHEN** `Resolve` is called with a public git `url` carrying a pinned `?ref=` and `subPath = "seed"`
- **THEN** the resolver fetches the repository at the pinned ref into a temporary directory
- **AND** returns `<tempDir>/seed` as the filesystem path plus a non-nil cleanup function

#### Scenario: cleanup removes the temporary directory

- **WHEN** the caller invokes the returned `cleanup` function
- **THEN** the temporary directory created by `Resolve` is removed

#### Scenario: cleanup is safe on the error path

- **WHEN** `Resolve` returns an error after creating the temporary directory
- **THEN** it removes the temporary directory itself (no leak) OR returns a non-nil cleanup that is safe to call
- **AND** the caller's `defer cleanup()` never panics on a nil function

#### Scenario: transitive remote bases are not fetched by the resolver or krusty

- **WHEN** the resolved root references a transitive remote base
- **THEN** the resolver still fetches only the root repository
- **AND** the remote base is acquired by the kustomize-remote-bases prefetch step, not by krusty at build time

#### Scenario: root checkout is created under the configured scratch directory

- **WHEN** `NewGitResolver` is constructed with a non-empty `--source-scratch-dir` and `Resolve` is called under a read-only root filesystem
- **THEN** the root checkout temp directory is created under that scratch directory
- **AND** the resolve succeeds without writing to a read-only path
- **AND** an empty scratch directory falls back to `os.TempDir()`
