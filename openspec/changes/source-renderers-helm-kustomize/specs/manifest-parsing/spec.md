# Spec: Manifest Parsing

## Purpose

Defines the `internal/manifest` package: the `Manifest` value type, the multi-document YAML parser that turns rendered output into a slice of typed manifests, and the origin-tagging rule that classifies each manifest as `upstream` or `additions`. This layer is the shared output contract consumed by both source renderers and, downstream, by Phase 3 transformations.

## ADDED Requirements

### Requirement: Manifest type

The `internal/manifest` package SHALL define a `Manifest` type that wraps a rendered Kubernetes object together with its origin classification. The type MUST expose the parsed object as an `*unstructured.Unstructured` and an `Origin` value. The package MUST define an `Origin` string type with exactly two constants: `OriginUpstream` (value `"upstream"`) and `OriginAdditions` (value `"additions"`). The `Manifest` type MUST NOT carry any per-resource target/destination field — destination is determined by which render produced the manifest, not by any field on the manifest.

#### Scenario: Manifest exposes unstructured object and origin

- **WHEN** a `Manifest` is constructed from a parsed Kubernetes object
- **THEN** its unstructured object is accessible as `*unstructured.Unstructured`
- **AND** its `Origin` is one of `OriginUpstream` or `OriginAdditions`

#### Scenario: Origin constants have stable string values

- **WHEN** the `Origin` constants are marshaled or compared as strings
- **THEN** `OriginUpstream` equals `"upstream"`
- **AND** `OriginAdditions` equals `"additions"`

---

### Requirement: Multi-document YAML parsing

The package SHALL provide a `Parse(raw []byte, fallback Origin) ([]Manifest, error)` function that splits a multi-document YAML byte stream into individual documents and returns one `Manifest` per real Kubernetes object. The parser MUST split on YAML document separators and MUST skip documents that are empty, whitespace-only, comment-only, or parse to a null/empty object. The parser MUST preserve all other documents as `*unstructured.Unstructured` in input order.

#### Scenario: Multiple documents parsed in order

- **WHEN** `Parse` receives a byte stream containing three valid Kubernetes documents separated by `---`
- **THEN** it returns a slice of three `Manifest` values
- **AND** the manifests appear in the same order as the input documents

#### Scenario: Empty and comment-only documents skipped

- **WHEN** `Parse` receives a stream containing a valid document, a trailing `---` with only whitespace, and a comment-only document
- **THEN** it returns only the manifests for the valid document(s)
- **AND** no error is returned for the skipped empty/comment documents

#### Scenario: Empty render returns empty slice

- **WHEN** `Parse` receives a byte stream that contains no real Kubernetes objects (empty, whitespace, or only comments/nulls)
- **THEN** it returns an empty (non-nil or nil) slice
- **AND** it returns no error

---

### Requirement: Per-document validation

The parser SHALL require every real document to declare both `apiVersion` and `kind`. A document that parses to a non-empty object but is missing either `apiVersion` or `kind` MUST cause `Parse` to return an error identifying the offending document. The parser MUST NOT silently drop a malformed object.

#### Scenario: Document missing apiVersion is rejected

- **WHEN** `Parse` receives a document that has `kind` but no `apiVersion`
- **THEN** `Parse` returns an error
- **AND** the error indicates a document is missing `apiVersion`

#### Scenario: Document missing kind is rejected

- **WHEN** `Parse` receives a document that has `apiVersion` but no `kind`
- **THEN** `Parse` returns an error
- **AND** the error indicates a document is missing `kind`

---

### Requirement: Origin tagging

The parser SHALL classify each manifest's `Origin` by reading the annotation `dual-deployment-operator.cc.sap/origin` from the object's metadata. When the annotation value is exactly `"additions"`, the manifest's `Origin` MUST be `OriginAdditions`. When the annotation is absent, empty, or holds any other value, the manifest's `Origin` MUST be set to the `fallback` argument (which callers pass as `OriginUpstream`). The parser MUST leave the origin annotation in place on the object and MUST NOT strip it during parsing.

#### Scenario: Annotation value additions classifies as additions

- **WHEN** `Parse` receives a document whose metadata annotation `dual-deployment-operator.cc.sap/origin` equals `additions`
- **AND** the `fallback` argument is `OriginUpstream`
- **THEN** the resulting manifest's `Origin` is `OriginAdditions`

#### Scenario: Missing annotation falls back to upstream

- **WHEN** `Parse` receives a document with no `dual-deployment-operator.cc.sap/origin` annotation
- **AND** the `fallback` argument is `OriginUpstream`
- **THEN** the resulting manifest's `Origin` is `OriginUpstream`

#### Scenario: Unknown annotation value falls back

- **WHEN** `Parse` receives a document whose `dual-deployment-operator.cc.sap/origin` annotation holds a value other than `additions` (for example `foreign` or empty string)
- **AND** the `fallback` argument is `OriginUpstream`
- **THEN** the resulting manifest's `Origin` is `OriginUpstream`

#### Scenario: Origin annotation retained on the object

- **WHEN** `Parse` classifies a manifest carrying the `dual-deployment-operator.cc.sap/origin` annotation
- **THEN** the annotation remains present on the returned object's metadata
