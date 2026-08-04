# Remove Classic HTTP(S) Helm-Repo Support (OCI-only Helm Loader) — Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking. Check the trailing `- [ ]` box AFTER all of a task's steps land.

**Goal:** Make the production Helm `ChartLoader` OCI-only — delete the classic HTTP(S) Helm-repo parent-chart path (`pullHTTP`, `resolveHTTPID`, the `http(s)://` dispatch arms), enforce `oci://` at admission via CEL, adapt all affected tests, and update docs.

**Architecture:** The Helm loader (`internal/source/helmloader.go`) currently dispatches `Load`/`ResolveID`/`repoScope` on the `repo` scheme (`oci://` XOR `http(s)://`). Phase 7.6 made the HTTP(S) parent path a dead end (OCI-only subchart deps). This change drops the HTTP arms so only `oci://` is accepted, reworded loader errors as a backstop, and adds a CEL `XValidation` on `HelmSource.repo` (`startsWith('oci://')`) so a non-OCI repo is rejected at `kubectl apply`. The render-cache key struct is left unchanged (only `repoScope` loses its `http:` arm). Kustomize (git transport) is untouched.

**Tech Stack:** Go, controller-runtime/kubebuilder (CEL `+kubebuilder:validation:XValidation`), Helm SDK v3, Ginkgo/Gomega + stdlib `testing`. Build/test/lint via the SAP go-makefile-maker project: `go build ./...`, `go test ./internal/source/... ./api/...`, `make run-golangci-lint`, `make manifests generate`.

**Ground-truth references (verified against the worktree at `508334f`):**
- `internal/source/helmloader.go`: `Load` dispatch (lines 65–72), `pullHTTP` (130–156), `repoScope` (181–194), `ResolveID` dispatch (203–211), `resolveHTTPID` (238–281). Import `helm.sh/helm/v3/pkg/repo` (line 27) is used ONLY by `resolveHTTPID` (line 267 `repo.IndexFile`).
- KEEP (still used elsewhere): `hostOf` (git + OCI), `rejectURLCredentials`, the `httpClient *http.Client` field (OCI registry-client seam → keeps `net/http`), `io` (line 332 `io.Discard`), `net/url` (lines 159, 174).
- `api/v1alpha1/dualdeploymentoperator_types.go`: `HelmSource` struct (lines 45–68), `Repo` field (line 47).

---

## Task 1: Enforce OCI-only Helm repo at admission (CEL rule)

**Files:**
- Modify: `api/v1alpha1/dualdeploymentoperator_types.go` (HelmSource struct, lines 45–47)
- Regenerate: `config/crd/bases/*.yaml` (via `make manifests`)
- Test: `internal/webhook/v1alpha1/dualdeploymentoperator_webhook_test.go` (envtest admission) OR `api/v1alpha1` CEL test — use whichever the suite already exercises CEL through (webhook_suite_test uses envtest with the CRD applied).

- [ ] **Step 1: Write the failing test** — assert a non-`oci://` Helm repo is rejected at admission and `oci://` is accepted. Append to `internal/webhook/v1alpha1/dualdeploymentoperator_webhook_test.go` (this suite applies the real CRD via envtest, so CEL fires):

```go
func makeHelmCR(ns, repo string) *dualdeploymentoperatorv1alpha1.DualDeploymentOperator {
	return &dualdeploymentoperatorv1alpha1.DualDeploymentOperator{
		ObjectMeta: metav1.ObjectMeta{GenerateName: "http-cel-", Namespace: ns},
		Spec: dualdeploymentoperatorv1alpha1.DualDeploymentOperatorSpec{
			Source: dualdeploymentoperatorv1alpha1.Source{
				Helm: &dualdeploymentoperatorv1alpha1.HelmSource{
					Repo: repo, Name: "demo", Version: "1.0.0",
				},
			},
			ShootNamespace: "demo-ns",
			ShootAccess: dualdeploymentoperatorv1alpha1.ShootAccessRef{
				SecretName: "kc", Server: "https://shoot.example",
			},
		},
	}
}

var _ = Describe("HelmSource repo OCI-only CEL", func() {
	It("rejects an http(s):// helm repo", func() {
		err := k8sClient.Create(ctx, makeHelmCR("default", "https://charts.example.com"))
		Expect(err).To(HaveOccurred())
		Expect(err.Error()).To(ContainSubstring("oci://"))
	})
	It("rejects a scheme-less helm repo", func() {
		err := k8sClient.Create(ctx, makeHelmCR("default", "charts.example.com"))
		Expect(err).To(HaveOccurred())
	})
	It("accepts an oci:// helm repo", func() {
		Expect(k8sClient.Create(ctx, makeHelmCR("default", "oci://keppel.global.cloud.sap/ccloud-helm"))).To(Succeed())
	})
})
```

> CR shape verified against `api/v1alpha1/dualdeploymentoperator_types.go`: `Spec.Source` (Helm XOR Kustomize), required `Spec.ShootNamespace` (pattern `^[a-z0-9]([-a-z0-9]*[a-z0-9])?$`), `Spec.ShootAccess` = `ShootAccessRef{SecretName, Server, TokenKey?, CAKey?}`. If the webhook suite already has a CR-builder helper, reuse it instead of adding `makeHelmCR`; confirm with `grep -n 'DualDeploymentOperatorSpec{' internal/webhook/v1alpha1/*.go`.

- [ ] **Step 2: Run test to verify it fails**

Run: `KUBEBUILDER_ASSETS=$(setup-envtest use 1.36 -p path) go test ./internal/webhook/v1alpha1/... -run HelmSource -v`
Expected: FAIL — the http:// and scheme-less cases currently succeed (no CEL rule yet).

- [ ] **Step 3: Add the CEL rule to `HelmSource`** — insert an `XValidation` marker directly above `type HelmSource struct` (line 45), mirroring the kustomize `url` rule style:

```go
// HelmSource references a chart in an OCI registry. repo MUST be an oci:// reference.
// +kubebuilder:validation:XValidation:rule="self.repo.startsWith('oci://')",message="helm repo must be an oci:// reference"
type HelmSource struct {
	// +kubebuilder:validation:MinLength=1
	Repo string `json:"repo"`
```

- [ ] **Step 4: Regenerate manifests**

Run: `make manifests generate`
Expected: `config/crd/bases/*.yaml` gains the `x-kubernetes-validations` entry on `spec.source.helm`; no other unexpected diff. Inspect with `git diff --stat config/crd`.

- [ ] **Step 5: Run test to verify it passes**

Run: `KUBEBUILDER_ASSETS=$(setup-envtest use 1.36 -p path) go test ./internal/webhook/v1alpha1/... -run HelmSource -v`
Expected: PASS (all three cases).

- [ ] **Step 6: Commit**

```bash
git add api/v1alpha1/dualdeploymentoperator_types.go config/crd internal/webhook/v1alpha1/dualdeploymentoperator_webhook_test.go
git commit -m "feat(api): enforce oci:// helm repo via CEL (Phase 7.7)"
```

- [x] Task 1 complete

---

## Task 2: Remove the HTTP path from the Helm loader (Load / ResolveID / repoScope)

**Files:**
- Modify: `internal/source/helmloader.go` — `Load` dispatch (65–72), delete `pullHTTP` (130–156), `repoScope` http arm (185–190), `ResolveID` dispatch (206–208), delete `resolveHTTPID` (238–281), drop import `helm.sh/helm/v3/pkg/repo` (27).

- [ ] **Step 1: Adapt the failing repoScope test first (RED)** — remove the `https://` sub-case from `TestHelmLoader_repoScope` in `internal/source/helmloader_test.go` (lines 415–421), leaving only the OCI assertion:

```go
func TestHelmLoader_repoScope(t *testing.T) {
	l := &helmLoader{}
	s, err := l.repoScope("oci://reg.example.com/charts")
	if err != nil {
		t.Fatalf("oci repoScope error: %v", err)
	}
	if s != "oci:reg.example.com" {
		t.Fatalf("oci repoScope = %q", s)
	}
}
```

- [ ] **Step 2: Run to confirm the package still builds/fails as expected**

Run: `go build ./internal/source/... 2>&1 | head`
Expected: at this point still builds (code not yet changed); the edited test compiles. (This step is a checkpoint, not a hard RED.)

- [ ] **Step 3: Edit `Load` dispatch (lines 65–72)** — drop the http arm, reword the default:

```go
	var chartPath string
	switch {
	case strings.HasPrefix(repoURL, "oci://"):
		chartPath, err = l.pullOCI(ctx, repoURL, name, version, tmp)
	default:
		return nil, errors.New("source: unsupported chart repo scheme (only oci:// is supported)")
	}
```

- [ ] **Step 4: Edit `ResolveID` dispatch (lines 206–208)** — drop the http arm, reword the default:

```go
	switch {
	case strings.HasPrefix(repoURL, "oci://"):
		return l.resolveOCIDigest(ctx, repoURL, name, version)
	default:
		return "", errors.New("source: unsupported chart repo scheme (only oci:// is supported)")
	}
```

- [ ] **Step 5: Edit `repoScope` (lines 181–194)** — drop the http arm, reword the default:

```go
// repoScope returns the transport+host scope for the cache key.
func (l *helmLoader) repoScope(repoURL string) (string, error) {
	switch {
	case strings.HasPrefix(repoURL, "oci://"):
		return "oci:" + ociHost(repoURL), nil
	default:
		return "", errors.New("source: unsupported chart repo scheme for repoScope (only oci:// is supported)")
	}
}
```

- [ ] **Step 6: Delete `pullHTTP` (lines 130–156) and `resolveHTTPID` (lines 238–281) entirely.** Then remove the now-unused `"helm.sh/helm/v3/pkg/repo"` import (line 27).

- [ ] **Step 7: Update the `ResolveID` doc comment (lines 196–198)** — drop the HTTP sentence:

```go
// ResolveID resolves the chart to its immutable id without pulling blob layers.
// OCI: the manifest digest (sha256:...).
```

- [ ] **Step 8: Build + let the compiler/linter confirm no other dead imports**

Run: `go build ./... && make run-golangci-lint`
Expected: builds clean. If the linter flags any other now-unused symbol/import, remove exactly what it names (do NOT remove `hostOf`, `rejectURLCredentials`, the `httpClient` field, `io`, `net/url`, or `net/http` — all still used). Confirm `net/http` still referenced: `grep -n 'http\.\|httpClient' internal/source/helmloader.go`.

- [ ] **Step 9: Commit**

```bash
git add internal/source/helmloader.go internal/source/helmloader_test.go
git commit -m "feat(source): make Helm loader OCI-only, drop HTTP(S) repo path (Phase 7.7)"
```

- [x] Task 2 complete

---

## Task 3: Remove dead HTTP-repo tests + add loader-level rejection test

**Files:**
- Modify: `internal/source/helmloader_test.go` — delete `TestHelmLoader_ResolveID_HTTPIndexDigest` (454–482), `_ResolveID_HTTPVersionFallback` (483–509), `_ResolveID_HTTPUnkeyable` (510–533), `_repoScope_HTTPPathDistinguishes` (534–548), `_ResolveID_RejectsURLCredentials_HTTP` (562–574), `_resolveHTTPID_HTTP4xx` (593–609), `_resolveHTTPID_BasicAuth` (610–647). Also delete the `makeMinimalChartTGZ` helper (426–453) IF it is used only by the deleted tests.
- Modify: `internal/source/helmloader_online_test.go` — delete `TestHelmLoaderOnlineHTTPRepo` (30–end).
- Modify: `internal/source/helmloader_authed_test.go` — delete `TestHelmLoaderAuthedHTTPRepo` (325–end) and its sole helper `authedHelmRepoServer` (279–324).
- Modify: `internal/source/cachekey_test.go` — delete `TestCacheKey_OCIvsHTTPNoCollision` (29–37).
- KEEP: `TestHelmLoaderRejectsUnknownScheme` (ftp://), `TestHelmLoader_ResolveID_UnsupportedScheme` (gopher://), `TestHelmLoader_rejectURLCredentials_Unparsable`, `TestHostOf`, and `gitresolver_authed_test.go::TestGitResolverHTTPSBasicAuth` (git transport, out of scope).

- [ ] **Step 1: Delete the dead test functions** listed above. For each deleted file, after deletion check for now-unused imports/helpers:

```bash
grep -n 'makeMinimalChartTGZ' internal/source/*.go   # if only its definition remains, delete it too
grep -n 'httptest\|"net/http"\|repo\.' internal/source/helmloader_online_test.go internal/source/helmloader_authed_test.go
```

Remove any import left unused by the deletions (compiler will name them).

- [ ] **Step 2: Add a loader-level OCI-only rejection test** to `internal/source/helmloader_test.go` (complements the CEL admission test from Task 1; proves the backstop):

```go
func TestHelmLoader_Load_RejectsHTTPScheme(t *testing.T) {
	l := newHelmLoader(nil)
	_, err := l.Load(context.Background(), "https://charts.example.com", "demo", "1.0.0")
	if err == nil || !strings.Contains(err.Error(), "oci://") {
		t.Fatalf("expected oci-only error for https repo, got %v", err)
	}
}

func TestHelmLoader_ResolveID_RejectsHTTPScheme(t *testing.T) {
	l := newHelmLoader(nil)
	_, err := l.ResolveID(context.Background(), "https://charts.example.com", "demo", "1.0.0")
	if err == nil || !strings.Contains(err.Error(), "oci://") {
		t.Fatalf("expected oci-only error for https repo, got %v", err)
	}
}
```

- [ ] **Step 3: Run the offline source suite**

Run: `go test ./internal/source/... -run 'HelmLoader|CacheKey|HostOf|repoScope' -v`
Expected: PASS; the new `RejectsHTTPScheme` tests pass; no compile errors from removed helpers.

- [ ] **Step 4: Guard — no lingering Helm-repo HTTP test remains**

Run: `grep -rin 'http' internal/source/*_test.go | grep -iv 'oci://\|https://shoot\|git\|GitResolver\|RejectsHTTPScheme'`
Expected: only the retained git/kustomize transport references and intentional http:// rejection cases remain; no `resolveHTTPID`/`repoScope`-HTTP/`AuthedHTTPRepo`/`OnlineHTTPRepo`/`OCIvsHTTPNoCollision` references.

- [ ] **Step 5: Commit**

```bash
git add internal/source/helmloader_test.go internal/source/helmloader_online_test.go internal/source/helmloader_authed_test.go internal/source/cachekey_test.go
git commit -m "test(source): drop classic HTTP Helm-repo tests, add OCI-only rejection tests (Phase 7.7)"
```

- [x] Task 3 complete

---

## Task 4: Update docs (spec deltas already written; sync repo docs)

**Files:**
- Modify: `README.md` — change "Production Helm ChartLoader (OCI + classic HTTP(S) repos)" → "OCI-only" (two occurrences: the `internal/source` bullet in the Status paragraph, and any other "OCI + HTTP(S)" phrasing).
- Modify: `AGENTS.md` — same "OCI + HTTP(S) repos" → "OCI-only" in the `internal/source/*` line.
- Modify: `docs/design.md` §3.3 — drop the HTTP(S) parent-repo mention from the Helm source discriminator.
- Modify: `docs/context.md` — add a short revision note recording the OCI-only narrowing (HTTP parent + Phase 7.6 OCI-only subchart rule = dead end).

- [ ] **Step 1: Locate the exact phrases**

Run:
```bash
grep -rn 'OCI + HTTP\|HTTP(S) repos\|classic HTTP\|http(s)://' README.md AGENTS.md docs/design.md docs/context.md
```
Expected: a small set of hits to edit.

- [ ] **Step 2: Edit README.md + AGENTS.md** — replace "OCI + (classic )HTTP(S) repos" with "OCI-only" and drop the "HTTP index digest-or-version" clause from the render-cache resolved-id description (it now reads "git SHA / OCI digest").

- [ ] **Step 3: Edit docs/design.md §3.3** — remove the `http(s)://` arm from the Helm source discriminator description so it states OCI is the only Helm transport.

- [ ] **Step 4: Add a revision note to docs/context.md** — a short "Revision N: Helm loader narrowed to OCI-only (Phase 7.7)" paragraph: HTTP(S) parent path + Phase 7.6 OCI-only subchart rule made HTTP a dead end; fleet is keppel-OCI; enforced by CEL + loader.

- [ ] **Step 5: Verify no stale HTTP-Helm-repo claim remains**

Run: `grep -rn 'HTTP(S) repos\|classic HTTP.*Helm\|OCI + HTTP' README.md AGENTS.md docs/design.md`
Expected: no matches (kustomize HTTP/git references are fine and expected).

- [ ] **Step 6: Commit**

```bash
git add README.md AGENTS.md docs/design.md docs/context.md
git commit -m "docs: Helm loader is OCI-only (Phase 7.7)"
```

- [x] Task 4 complete

---

## Task 5: Full gate + spec sync

**Files:** none (verification + openspec bookkeeping)

- [ ] **Step 1: Full build/test/lint gate**

Run:
```bash
go build ./...
KUBEBUILDER_ASSETS=$(setup-envtest use 1.36 -p path) go test ./... 2>&1 | tail -30
make run-golangci-lint
```
Expected: build exit 0; tests pass (or only pre-existing unrelated failures, explicitly noted); lint clean on the diff.

- [ ] **Step 2: Codegen is clean**

Run: `make manifests generate && git status --porcelain config/ api/`
Expected: no un-committed generated diff beyond Task 1's CEL rule (already committed).

- [ ] **Step 3: Validate the OpenSpec change**

Run: `openspec validate remove-http-helm-repo`
Expected: `Change 'remove-http-helm-repo' is valid`.

- [ ] **Step 4: Commit any residual (if codegen produced a diff)**

```bash
git add -A && git commit -m "chore: regenerate manifests (Phase 7.7)"   # only if there is a diff
```

- [x] Task 5 complete
