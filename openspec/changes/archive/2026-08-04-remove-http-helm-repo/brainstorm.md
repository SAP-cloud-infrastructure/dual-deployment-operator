## Design Summary

Make the Helm `ChartLoader` (`internal/source/helmloader.go`) **OCI-only**, removing the classic HTTP(S) Helm-repo parent-chart path shipped in Phase 7. Phase 7.6 already restricted **subchart dependency** resolution to OCI (HTTP(S)-repo subchart deps are rejected fail-closed because `downloader.Manager.Build()` needs HTTP repos pre-registered/index-cached, which the operator does not do at reconcile time). That turned the HTTP(S) **parent** path into a near-dead end — it renders correctly only for charts with no dependencies or all-vendored dependencies, a case no fleet operator uses (the whole fleet publishes to the keppel OCI registry, verified in Phase 7). Removing the HTTP(S) parent path drops maintenance and test surface and makes the loader OCI-only end to end. A non-`oci://` Helm `repo` is rejected at admission (CEL) with the loader error as a backstop. The kustomize git/HTTP transport (krusty remote bases) is a different mechanism and is explicitly out of scope.

## Alternatives Considered

### Option A: Enforce OCI-only via CEL on the CRD + reword the loader `default` arm (chosen)
- **Approach**: Add a `+kubebuilder:validation:XValidation` rule on `HelmSource.repo` requiring an `oci://` prefix (mirroring the existing kustomize `url` `ref=` CEL rule). Delete `pullHTTP` and `resolveHTTPID`; drop the `http://`/`https://` dispatch arms in `Load`, `ResolveID`, and `repoScope`; reword the loader `default` arm to name OCI explicitly as the backstop. Let the compiler + `make run-golangci-lint` drive dead-import cleanup.
- **Pros**: Admission-time rejection with a clear message (fails fast, not opaquely at reconcile); no new webhook logic (the validating webhook is a pass-through stub — all admission validation is already CEL on the CRD); defense in depth (CEL + loader error); matches the phase spec exactly.
- **Cons**: Adds one more CEL rule to the CRD surface (slightly larger blast radius than a pure code change; requires `make manifests generate`).
- **Why not chosen**: it *is* chosen.

### Option B: Enforce only in the loader (no CEL rule)
- **Approach**: Delete the HTTP code paths and let the existing "unsupported scheme" loader error reject non-`oci://` repos at reconcile time; no CRD change.
- **Pros**: Smallest diff; no CRD/CEL regeneration; no admission surface change.
- **Cons**: A misconfigured CR is accepted at admission and fails only later at reconcile, surfaced as a CR status condition rather than a clear `kubectl apply` rejection — worse operator UX. Diverges from the phase spec's explicit "validate, do not silently narrow" instruction.
- **Why not chosen**: fails to reject the bad input at admission; the phase spec calls for admission-level rejection.

### Option C: Add custom validation logic in the validating webhook
- **Approach**: Implement the `oci://` check in `DualDeploymentOperatorCustomValidator.ValidateCreate/Update` in `internal/webhook/v1alpha1/`.
- **Pros**: Could express richer, cross-field logic if ever needed.
- **Cons**: The webhook is intentionally a pass-through stub — the project's stated v1 convention is "all admission validation is handled by CEL rules on the CRD". Adding logic here contradicts that convention and reintroduces a webhook code path (and its cert/serving concerns) for a check CEL handles natively.
- **Why not chosen**: violates the established CEL-only admission convention; CEL is the idiomatic seam here.

## Agreed Approach

**Option A.** Enforce OCI-only at admission with a CEL rule on `HelmSource.repo` (`oci://` prefix required), keep the loader error as a backstop, and delete the HTTP(S) parent-chart code and tests. This matches the phase spec's "validate, do not silently narrow" directive, respects the project's CEL-only admission convention (the validating webhook stays a pass-through stub), and gives defense in depth. Constraints that made this right: the fleet is already OCI-only (keppel, verified Phase 7); Phase 7.6 made the HTTP parent path a dead end; the CRD already uses CEL for the analogous kustomize `url` constraint, so the pattern is established.

## Key Decisions

- **Enforcement seam = CEL on the CRD, not the webhook**: The validating webhook (`internal/webhook/v1alpha1/dualdeploymentoperator_webhook.go`) is a pass-through stub; v1 convention is all admission validation via CEL. Add `XValidation` requiring `self.repo.startsWith('oci://')` on `HelmSource`, analogous to the kustomize `url` `ref=` rule. Rationale: idiomatic, no new webhook/cert surface, admission-time rejection.
- **`oci://` prefix required (allowlist), not http(s):// blocklist**: The CEL rule requires the `oci://` prefix rather than only rejecting `http(s)://`. Rationale: matches the phase spec ("only `oci://` accepted") and the fleet's actual usage; rejects any non-OCI scheme uniformly at admission instead of leaving a gap for the loader to cover.
- **Loader error kept as backstop**: `Load`/`ResolveID`/`repoScope` `default` arm reworded to name OCI explicitly. Rationale: defense in depth for anything that bypasses admission (e.g. direct loader use in tests/tools).
- **Keep `hostOf` / `rejectURLCredentials`**: still used by the OCI path — do not remove.
- **Compiler-driven cleanup**: remove now-unused HTTP-only helpers/imports (`net/http`, `io`, `repo.IndexFile` handling) by following the compiler + `make run-golangci-lint`, not by guessing.
- **Live-CR pre-check = documented assumption, not active cluster query**: The fleet publishes to keppel OCI (verified Phase 7) and the operator is not deployed live yet (image publish is a future phase), so there are no live CRs to migrate. Record "all fleet CRs use `oci://`" as an assumption in the change; the new CEL rule + loader error catch any future stray. Rationale: no cluster access needed, no live consumers exist.
- **Own OpenSpec change, branched from `origin/main` (Phase 7.6 head `508334f`)**: touches the CRD validation surface (larger blast radius than 7.6), so it is its own `sdd-plus-superpowers` change, not folded into an earlier phase.
- **Kustomize path untouched**: kustomize remote bases (krusty git/HTTP) are a different transport and are explicitly out of scope.

## Open Questions

- (none blocking) — both forks (CEL shape, live-CR check) were resolved during brainstorming.
