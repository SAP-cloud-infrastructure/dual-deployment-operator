<!--
SPDX-FileCopyrightText: 2026 SAP SE or an SAP affiliate company
SPDX-License-Identifier: Apache-2.0
-->

## ADDED Requirements

### Requirement: Production git RootResolver via go-git

The operator MUST provide a production `RootResolver` implementation (`internal/source`) that satisfies the existing `RootResolver.Resolve(ctx, url, subPath) (fsPath string, cleanup func(), err error)` seam by fetching a remote git kustomize root into a local temporary directory using `github.com/go-git/go-git/v5` (pure Go, no `git` binary). It MUST return the path `<tempDir>/<subPath>` and a `cleanup` function that removes the temporary directory. The renderer logic and the `RootResolver` interface signature MUST NOT change; the resolver fetches only the ROOT — krusty fetches transitive remote bases itself.

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

### Requirement: Pinned ref resolution is fail-closed for tags, branches, and SHAs

The resolver MUST honor the pinned `?ref=` extracted from the URL and MUST fail closed if the ref cannot be resolved — it MUST NOT silently build the default branch (`HEAD`). A tag or branch ref MAY be fetched via a shallow clone (`ReferenceName` + `Depth: 1`). A commit SHA ref MUST be fetched by initializing the repo and fetching that SHA (`FetchContext` with a refspec into a temporary ref, `Depth: 1`), then checking out the commit hash.

#### Scenario: tag ref builds the pinned revision

- **WHEN** the `url` pins `?ref=v1.1.0` (a tag)
- **THEN** the resolver fetches that tag and the resolved root reflects the `v1.1.0` revision

#### Scenario: commit SHA ref is fetched and checked out

- **WHEN** the `url` pins `?ref=<40-char-sha>`
- **THEN** the resolver fetches that specific commit and checks out its hash
- **AND** the resolved root reflects exactly that commit

#### Scenario: unresolvable ref fails closed

- **WHEN** the pinned `?ref=` names a tag/branch/SHA that does not exist on the remote
- **THEN** `Resolve` returns an error
- **AND** it does NOT fall back to building the default branch

### Requirement: Anonymous clone of public repositories

The resolver MUST clone a public repository with no authentication (`Auth: nil`) when no credentials resolve for the git host, so the verified public-github path works without credential material.

#### Scenario: public repo resolves anonymously

- **WHEN** `Resolve` is called for a public git `url` and no credentials resolve for its host
- **THEN** the fetch is performed with no auth method
- **AND** the resolve succeeds
