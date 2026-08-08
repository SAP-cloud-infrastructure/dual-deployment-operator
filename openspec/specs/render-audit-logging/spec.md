<!--
SPDX-FileCopyrightText: 2026 SAP SE or an SAP affiliate company
SPDX-License-Identifier: Apache-2.0
-->

# Spec: Render Audit Logging

## Purpose

Defines the resolved-id audit log line emitted per render, so that every render is auditable and reproducible even when the CR pins a mutable ref (e.g. `?ref=main`). Deliberately keeps auditing to the log stream — no CRD `status` field is added.

## Requirements

### Requirement: Log the resolved content id per render

The system MUST emit a structured log line recording the resolved content id used for each render, so that every render is auditable and reproducible even when the CR pins a mutable ref (e.g. `?ref=main`). The log line MUST identify the source and its resolved id (git commit SHA / OCI manifest digest / HTTP chart digest-or-version). The system MUST NOT log credentials or rendered values. This change MUST NOT add a CRD `status` field for the resolved id (logging only).

#### Scenario: resolved id is logged on render

- **WHEN** a render resolves a source to a content id
- **THEN** a structured log line records the source and its resolved id
- **AND** the line contains no credentials and no rendered values

#### Scenario: mutable ref still yields a reproducible audit record

- **WHEN** a CR pins a mutable ref such as `?ref=main`
- **THEN** the logged resolved id is the concrete commit SHA (or digest) that was actually rendered
- **AND** no CRD `status` field is written for the resolved id

### Requirement: Log reconcile-pipeline stage boundaries

The system MUST emit structured log lines at the boundaries of the reconcile pipeline so that a reconcile — and in particular a delete that blocks — is diagnosable from logs alone, without attaching a debugger. Log lines MUST follow the Kubernetes logging message-style guidelines (capitalized, no trailing period, past tense, object type named, balanced key/value pairs) and MUST NOT contain credentials, tokens, or rendered values. Once-per-reconcile milestones SHALL be logged at the default verbosity (`Info` / V(0)); per-resource detail MAY be logged at higher verbosity (V(1)).

The following stages MUST be instrumented: source pull of the upstream chart or kustomization (start and result, with source kind, ref, and resolved content id); render and validation per mode (with mode and manifest count); render-cache hit vs. miss (with resolved content id and hit/miss); and per-target apply, prune, and delete (with cluster, and counts such as applied/degraded/pruned/deleted). In `reconcileDelete` specifically, the log MUST record the finalizer decision (whether shoot cleanup completed, whether the `force-delete` override was honored, or whether the reconcile is blocking and retrying).

In addition to the per-target apply summary, the system MUST log the **apply status of each individual delivered resource** at `V(1)`: for every `applier.Apply` result the log line MUST record the target cluster, the resource kind/name/namespace, the resulting health (`Healthy`/`Progressing`/`Degraded`/`Unknown`), and any operator-authored message. This makes a `Degraded` or `Progressing` resource visible in logs rather than only in the CR's `status.seedResources`/`status.shootResources`. The message field carries only operator-authored health/conflict text — never rendered values or secrets.

This requirement is observability-only: it MUST NOT change reconcile behavior and MUST NOT add any CRD `status` field.

#### Scenario: source pull is logged with its resolved id

- **WHEN** the reconciler pulls the upstream chart or kustomization for a render
- **THEN** a structured log line records the source kind, ref, and resolved content id
- **AND** the line contains no credentials, tokens, or rendered values

#### Scenario: render-cache hit and miss are logged

- **WHEN** a render is served from the render cache
- **THEN** a structured log line records the resolved content id and `cache=hit`
- **WHEN** a render is not in the cache and is rendered fresh
- **THEN** a structured log line records the resolved content id and `cache=miss`

#### Scenario: per-resource apply status is logged

- **WHEN** the reconciler applies a delivered resource to its target cluster
- **THEN** a `V(1)` structured log line records the cluster, the resource kind/name/namespace, and its resulting health
- **AND** when the health is `Degraded` or `Progressing`, the operator-authored message is included
- **AND** the line contains no rendered values or secrets

#### Scenario: delete finalizer decision is logged

- **WHEN** `reconcileDelete` reaches its finalizer decision
- **THEN** a structured log line records the outcome (shoot cleanup complete, force-deleted, or blocked-and-retrying) with the target cluster and delete counts
- **AND** the line contains no credentials or tokens

#### Scenario: logging does not alter behavior

- **WHEN** the reconcile pipeline runs with the stage logging in place
- **THEN** apply, prune, delete, and finalizer behavior are unchanged
- **AND** no CRD `status` field is added for logging purposes
