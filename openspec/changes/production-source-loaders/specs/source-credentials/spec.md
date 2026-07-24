<!--
SPDX-FileCopyrightText: 2026 SAP SE or an SAP affiliate company
SPDX-License-Identifier: Apache-2.0
-->

## ADDED Requirements

### Requirement: Per-source credential resolution from an authSecretRef Secret

The operator MUST resolve source credentials from the Secret named by a source's optional `authSecretRef` (a Secret in the CR's namespace). When a source sets `authSecretRef`, the operator MUST read credentials from that Secret using a fixed set of keys: `username`, `password`, and `token`. When a source has no `authSecretRef`, or the referenced Secret provides no recognized keys, the loader MUST proceed anonymously.

#### Scenario: source with no authSecretRef resolves to anonymous

- **WHEN** a source has no `authSecretRef` set
- **THEN** the credential resolver returns no credentials for that source
- **AND** the loader pulls/clones anonymously

#### Scenario: basic-auth credentials are read from the Secret

- **WHEN** a source's `authSecretRef` names a Secret containing `username` and `password` keys
- **THEN** the resolver returns those as basic-auth credentials for the source's host

#### Scenario: token credential is read from the Secret

- **WHEN** a source's `authSecretRef` names a Secret containing a `token` key
- **THEN** the resolver returns the token as the credential for the source's host

#### Scenario: token wins when both token and password are present

- **WHEN** the referenced Secret contains both `token` and `password`
- **THEN** the resolver uses `token`

### Requirement: OCI and HTTP(S) Helm auth via basic credentials

The Helm `ChartLoader` MUST apply resolved credentials to both chart-pull paths: for `oci://` it MUST configure the registry client with inline basic-auth (username/password, or token used as password) WITHOUT performing a login that writes credentials to disk; for classic `http(s)://` repos it MUST set the pull action's `Username`/`Password`. TLS knobs (custom CA, skip-verify) are out of scope for this change.

#### Scenario: authenticated OCI pull uses inline basic-auth

- **WHEN** credentials resolve for an `oci://` source
- **THEN** the loader constructs the registry client with inline basic-auth (no on-disk login)
- **AND** the authenticated pull succeeds against a registry requiring those credentials

#### Scenario: authenticated classic-repo pull sets Username/Password

- **WHEN** credentials resolve for an `http(s)://` classic-repo source
- **THEN** the loader sets the pull action's `Username` and `Password`
- **AND** the authenticated pull succeeds against a repo requiring those credentials

### Requirement: git HTTPS auth via basic credentials; SSH deferred

The kustomize `RootResolver` MUST apply resolved credentials as git HTTPS basic auth (`http.BasicAuth`), using `token` as the password (with username `"git"` when only a token is provided) or `username`/`password` when both are set. SSH authentication MUST NOT be implemented in this change.

#### Scenario: authenticated HTTPS clone uses basic auth

- **WHEN** credentials resolve for a git HTTPS source
- **THEN** the resolver passes an `http.BasicAuth` auth method to the clone/fetch
- **AND** the authenticated fetch succeeds against a repo requiring those credentials

#### Scenario: SSH credentials are not consumed

- **WHEN** no HTTPS-usable keys are present
- **THEN** the resolver does not attempt SSH authentication in this change

### Requirement: Credential material is never leaked

Resolved credentials MUST NOT appear in logs or error messages. An authentication failure MUST surface an error that does not contain the token or password value.

#### Scenario: auth-failure error is token-free

- **WHEN** a pull or clone fails because the supplied credential is rejected
- **THEN** the returned error message does not contain the token or password value
