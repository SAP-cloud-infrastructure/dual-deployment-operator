<!--
SPDX-FileCopyrightText: 2026 SAP SE or an SAP affiliate company
SPDX-License-Identifier: Apache-2.0
-->

# Spec delta: kustomize-root-resolver

## MODIFIED Requirements

### Requirement: Production git RootResolver via go-git

The operator MUST provide a production `RootResolver` implementation (`internal/source`) that satisfies the existing `RootResolver.Resolve(ctx, url, subPath) (fsPath string, cleanup func(), err error)` seam by fetching a remote git kustomize root into a local temporary directory using `github.com/go-git/go-git/v5` (pure Go, no `git` binary). It MUST return the path `<tempDir>/<subPath>` and a `cleanup` function that removes the temporary directory. The renderer logic and the `RootResolver` interface signature MUST NOT change; the resolver fetches only the ROOT. Transitive remote git-directory bases and remote single-file references are fetched **live by krusty during `Run`** (the Argo CD / Flux-aligned model), after the remote-base policy preflight has classified and, under strict mode, pin-checked them. Because krusty's remote-base fetch shells out to a `git` binary, the operator image MUST provide `git` on `PATH`; absent it, remote-base renders fail closed.

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

#### Scenario: transitive remote base is fetched live by krusty

- **WHEN** the resolved root references a transitive remote git-directory base that passed the policy preflight
- **AND** the operator image provides a `git` binary on `PATH`
- **THEN** krusty fetches that base live during `Run`
- **AND** the rendered output reflects the fetched base

#### Scenario: remote base render fails closed without a git binary

- **WHEN** the resolved root references a transitive remote git-directory base
- **AND** no `git` binary is available on `PATH`
- **THEN** the render fails closed with an error
- **AND** no partial manifest set omitting the base is produced
