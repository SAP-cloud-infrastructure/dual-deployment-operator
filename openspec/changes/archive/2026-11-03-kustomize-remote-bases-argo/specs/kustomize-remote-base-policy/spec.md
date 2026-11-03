<!--
SPDX-FileCopyrightText: 2026 SAP SE or an SAP affiliate company
SPDX-License-Identifier: Apache-2.0
-->

# Spec delta: kustomize-remote-base-policy

## ADDED Requirements

### Requirement: Remote reference classification before render

Before invoking krusty, the operator MUST walk the reachable kustomization tree
(the resolved root and any local directories it references) and classify every
`resources`, `components`, and `bases` entry as one of: a local path, a remote
git-directory base, or a remote single-file reference. Classification MUST use the
bounded public-API grammar (HTTPS repositories with a mandatory `?ref=`, plus
GitHub shorthand normalized to HTTPS) and MUST reject unsupported remote forms
(SSH, embedded userinfo credentials, URL fragments, ambiguous repository paths)
with a sanitized error that does not echo credentials. The walk MUST detect cycles
and MUST NOT follow references outside the resolved root boundary when walking
local directories.

#### Scenario: supported remote git base is classified and accepted

- **WHEN** a reachable kustomization lists a `base` of the form
  `https://github.com/org/repo//path?ref=<ref>`
- **THEN** the preflight classifies it as a remote git-directory base
- **AND** the render proceeds to krusty without error from classification

#### Scenario: SSH or credential-embedding remote form is rejected

- **WHEN** a reachable kustomization lists a remote reference using an SSH URL or
  one embedding `user:token@host` userinfo
- **THEN** the preflight fails the render with a sanitized error
- **AND** the error does not contain the credential value

#### Scenario: cycle in local references is rejected

- **WHEN** the reachable local kustomization directories form a reference cycle
- **THEN** the preflight fails the render with a cycle error rather than looping

---

### Requirement: Optional SHA-pin enforcement for remote references

The operator MUST expose an operator-level flag
`--kustomize-require-sha-pinned-remote-bases` (default `false`). When the flag is
`false`, remote references pinned by tag, branch, or SHA are all permitted (the
tag-permissive default, matching krusty and peer controllers). When the flag is
`true`, the preflight MUST reject — **before any remote fetch** — any remote
reference whose embedded ref is not a 40-character lowercase hexadecimal commit
SHA. Ref extraction MUST be: for a remote git base, the `?ref=` value; for a
`raw.githubusercontent.com` single-file URL, the path segment after
`<org>/<repo>`; for a generic single-file URL, the `?ref=` query value. A
reference with no determinable ref MUST be rejected in strict mode.

#### Scenario: strict mode rejects a tag-pinned remote base

- **WHEN** the flag is `true` and a reachable kustomization references a remote
  base pinned by a tag (not a 40-hex SHA)
- **THEN** the preflight fails the render before any fetch
- **AND** the error states a 40-character lowercase hexadecimal SHA ref is required

#### Scenario: strict mode accepts a SHA-pinned remote base

- **WHEN** the flag is `true` and the remote base is pinned by a 40-hex commit SHA
- **THEN** the preflight accepts it and the render proceeds to krusty

#### Scenario: permissive default accepts a tag-pinned remote base

- **WHEN** the flag is `false` (default) and the remote base is pinned by a tag
- **THEN** the preflight accepts it and the render proceeds to krusty

---

### Requirement: Fail-closed sanitized remote fetch

A remote reference that krusty cannot fetch (unreachable host, git fetch failure,
or a non-2xx remote single-file response) MUST fail the render closed. The
operator MUST NOT emit a silent partial render that omits the unreachable
reference, and the surfaced error MUST be sanitized so it never contains
credential material or a full fetch URL.

#### Scenario: unreachable remote base fails the render

- **WHEN** a classified remote reference cannot be fetched during krusty `Run`
- **THEN** the render returns an error
- **AND** no manifest set is produced that silently omits the unreachable content

#### Scenario: fetch error is sanitized

- **WHEN** the underlying git/HTTP fetch error contains a URL or credential hint
- **THEN** the surfaced render error does not contain the credential value
