<!--
SPDX-FileCopyrightText: 2026 SAP SE or an SAP affiliate company
SPDX-License-Identifier: Apache-2.0
-->

# Spec Delta: source-credentials

## MODIFIED Requirements

### Requirement: git HTTPS auth via basic credentials; SSH deferred

The kustomize `RootResolver` MUST apply resolved credentials as git HTTPS basic auth (`http.BasicAuth`), using `token` as the password (with username `"git"` when only a token is provided) or `username`/`password` when both are set. The CR's `authSecretRef` MUST be bound to the root HTTPS origin (scheme, hostname and effective port). Transitively discovered remote bases on that origin use those credentials; other origins MUST be anonymous. Missing credentials also mean anonymous access. SSH authentication MUST NOT be implemented. Error paths MUST NOT expose credential values. Credential resolution and authorization-context isolation MUST precede private base-cache and kustomize render-cache hits, including after credential rotation.

#### Scenario: authenticated HTTPS clone uses basic auth

- **WHEN** credentials resolve for a git HTTPS source
- **THEN** the resolver passes an `http.BasicAuth` auth method to the clone/fetch
- **AND** the authenticated fetch succeeds against a repo requiring those credentials

#### Scenario: wrong credentials produce an auth failure

- **WHEN** credentials resolve for a git HTTPS source but the credentials are rejected by the server
- **THEN** the resolver returns an error indicating authentication failure
- **AND** the error message does not contain the credential values

#### Scenario: SSH credentials are not consumed

- **WHEN** no HTTPS-usable keys are present
- **THEN** the resolver does not attempt SSH authentication in this change

#### Scenario: a remote base fetch applies per-host basic auth

- **WHEN** a transitively discovered remote base points at the root HTTPS origin and credentials resolve
- **THEN** that base's fetch uses an `http.BasicAuth` method built from those credentials
- **AND** a remote base on another origin is fetched anonymously without forwarding those credentials
