# Deployment Chart (Chart 1) Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Generate and finalize the `dual-deployment-operator` controller Helm chart (chart 1) at repo-root `chart/` from the existing `config/*` kustomize scaffold via the kubebuilder helm plugin, keppel-free and per-shoot-deployable.

**Architecture:** The kubebuilder `helm.kubebuilder.io/v2-alpha` plugin reads `config/*` (the single source of truth) and emits `chart/` (Chart.yaml, values.yaml, templates/ — including the CRD as a toggled template under `templates/crd/`). Post-generation we apply a bounded set of customizations the plain scaffold omits — keppel-free image default, Gardener egress pod labels, leader-election, probes — and verify via `helm lint`/`helm template`. Chart 1 is namespace-agnostic so a downstream wrapper (chart 2, in `sapcc/helm-charts`) consumes it as a subchart and the pipeline installs it per `shoot--cp--*` namespace.

**Tech Stack:** kubebuilder v4.15.0 (helm/v2-alpha plugin), Helm v3+/v4, Go 1.22+ (`cmd/main.go`), REUSE/SPDX headers, go-makefile-maker.

**Verification commands (SAP go-makefile-maker project — use these, NOT `make test`/`make lint`/`make build`):**
- build: `go build ./...`
- test: `KUBEBUILDER_ASSETS=$(setup-envtest use 1.36 -p path) go test ./...`
- lint: `make run-golangci-lint`
- codegen: `make manifests generate`
- helm: `helm lint chart/` and `helm template dual-deployment-operator chart/`

---

## File Structure

- Create: `chart/Chart.yaml` — chart metadata (name `dual-deployment-operator`, version, appVersion) — plugin-generated.
- Create: `chart/values.yaml` — image (repository/tag), replicas, RBAC toggles, egress labels — plugin-generated, then customized.
- Create: `chart/templates/crd/dualdeploymentoperators.dual-deployment-operator.cc.sap.yaml` — the `DualDeploymentOperator` CRD as a toggled template (gated by `.Values.crd.enabled`, `helm.sh/resource-policy: keep` when `.Values.crd.keep`) — plugin-generated.
- Create: `chart/templates/manager/manager.yaml` (or `chart/templates/deployment.yaml`) — controller Deployment — plugin-generated, then customized (egress labels, probes, leader-elect).
- Create: `chart/templates/rbac/*.yaml` — broad seed applier ClusterRole/ClusterRoleBinding + leader-election Role — plugin-generated from `config/rbac`.
- Modify: `PROJECT` — plugin registers `helm.kubebuilder.io/v2-alpha` with pinned output — plugin-written.
- Modify: `cmd/main.go:178` — uncomment `LeaderElectionReleaseOnCancel: true`.
- Modify: `config/manager/manager.yaml` — set `--leader-elect=true` (currently bare `--leader-elect`) so the generated chart inherits it; confirm image default source.

> **Regeneration discipline:** the plugin owns `chart/**` only — it never touches `cmd/`, `internal/`, `api/` (verified: `ironcore-dev/metal-operator`, same kubebuilder v4.15.0 + helm/v2-alpha, hand-maintained `cmd/main.go` coexists). Post-gen customizations under `chart/` are overwritten by a later `--force` and MUST be re-applied — Task 7 records them.

---

## Task 1: Prepare config/* + cmd for chart generation

**Files:**
- Modify: `cmd/main.go:178`
- Modify: `config/manager/manager.yaml:64`

- [ ] **Step 1: Enable LeaderElectionReleaseOnCancel in cmd/main.go**

Edit `cmd/main.go` — uncomment the option at line 178 so the outgoing leader releases the lease on graceful shutdown:

```go
		LeaderElection:         enableLeaderElection,
		LeaderElectionID:       "9ca8e82a.cc.sap",
		// LeaderElectionReleaseOnCancel defines if the leader should step down voluntarily
		// when the Manager ends. This requires the binary to immediately end when the
		// Manager is stopped, otherwise, this setting is unsafe. Setting this significantly
		// speeds up voluntary leader transitions as the new leader don't have to wait
		// LeaseDuration time first.
		LeaderElectionReleaseOnCancel: true,
```

- [ ] **Step 2: Set --leader-elect=true in the manager manifest**

Edit `config/manager/manager.yaml` line 64: change the bare flag to explicit `true` so the generated chart's Deployment carries it:

```yaml
        args:
          - --leader-elect=true
          - --health-probe-bind-address=:8081
```

- [ ] **Step 3: Verify build + codegen stay green**

Run: `go build ./... && make manifests generate`
Expected: build exit 0; `git status --short config/ api/` shows no drift.

- [ ] **Step 4: Commit**

```bash
git add cmd/main.go config/manager/manager.yaml
git commit -m "chore: enable leader-election release-on-cancel and explicit --leader-elect=true for chart"
```

- [ ] **Task 1 complete**

---

## Task 2: Generate chart/ via the kubebuilder helm plugin

**Files:**
- Create: `chart/**` (Chart.yaml, values.yaml, templates/ — including `templates/crd/`)
- Modify: `PROJECT`

- [ ] **Step 1: Run the helm plugin with output at repo root**

Run: `KUSTOMIZE="$PWD/bin/kustomize" kubebuilder edit --plugins=helm/v2-alpha --output-dir=.`
Expected: creates `chart/` at repo root; updates `PROJECT` with a `helm.kubebuilder.io/v2-alpha` plugin entry. (The `KUSTOMIZE` override points at a prebuilt kustomize v5 CLI in `bin/` because the plugin runs `make build-installer` internally and the Makefile's default `KUSTOMIZE ?= go run sigs.k8s.io/kustomize/kustomize/v5` has no resolvable go.sum entry; install it once with `GOBIN="$PWD/bin" go install sigs.k8s.io/kustomize/kustomize/v5@v5.8.1` and restore go.mod/go.sum afterward.)

- [ ] **Step 2: Verify the chart skeleton exists**

Run: `ls chart/ chart/templates/ chart/templates/crd/`
Expected: `chart/Chart.yaml`, `chart/values.yaml`, `chart/templates/` present, and `chart/templates/crd/` contains the CRD template.

- [ ] **Step 3: Verify PROJECT records the plugin + output dir**

Run: `grep -A3 "helm.kubebuilder.io/v2-alpha" PROJECT`
Expected: plugin block present with an `output`/`manifests` field (deterministic regeneration target).

- [ ] **Step 4: Verify regeneration did NOT touch hand-written source**

Run: `git status --short cmd/ internal/ api/`
Expected: empty (no changes under `cmd/`, `internal/`, `api/` — plugin scope is `chart/` + `PROJECT` only).

- [ ] **Step 5: Verify the chart lints and templates**

Run: `helm lint chart/ && helm template dual-deployment-operator chart/ >/dev/null`
Expected: `1 chart(s) linted, 0 chart(s) failed`; template renders with no error.

- [ ] **Step 6: Commit the generated chart**

```bash
git add chart/ PROJECT
git commit -m "feat: generate dual-deployment-operator controller chart via kubebuilder helm plugin"
```

- [ ] **Task 2 complete**

---

## Task 3: CRD ships with the chart, toggled and kept (verify + assert)

> **Spec note:** the `helm.kubebuilder.io/v2-alpha` plugin emits the CRD as a **templated** manifest at `chart/templates/crd/…` gated by `.Values.crd.enabled` (default `true`), with `.Values.crd.keep` (default `true`) stamping `helm.sh/resource-policy: keep`. This is the plugin's native layout; the spec was amended to accept it (keeps the chart fully generated). Do NOT move the CRD to an untemplated `chart/crds/` dir.

**Files:**
- Verify: `chart/templates/crd/dualdeploymentoperators.dual-deployment-operator.cc.sap.yaml`
- Verify: `chart/values.yaml` (`crd.enabled`, `crd.keep`)

- [ ] **Step 1: Confirm the CRD ships as a toggled template**

Run:
```bash
ls chart/templates/crd/
head -12 chart/templates/crd/dualdeploymentoperators.dual-deployment-operator.cc.sap.yaml
```
Expected: the CRD file exists under `chart/templates/crd/`, wrapped in `{{- if .Values.crd.enabled }}`, and its annotations include a `{{- if .Values.crd.keep }} "helm.sh/resource-policy": keep` block.

- [ ] **Step 2: Confirm crd.enabled / crd.keep default to true**

Run: `grep -nA4 "^crd:" chart/values.yaml`
Expected: `crd:` block with `enabled: true` and `keep: true`.

- [ ] **Step 3: Confirm default render includes the CRD; disabled render omits it**

Run:
```bash
export KUSTOMIZE="$PWD/bin/kustomize"
helm template dual-deployment-operator chart/ | grep -c "kind: CustomResourceDefinition"        # expect 1
helm template dual-deployment-operator chart/ --set crd.enabled=false | grep -c "kind: CustomResourceDefinition"  # expect 0
helm template dual-deployment-operator chart/ | grep -c 'helm.sh/resource-policy: keep'          # expect >=1 (keep default true)
```
Expected: `1`, then `0`, then `>=1`.

- [ ] **Step 4: Commit (if the plugin needed a re-run)**

```bash
git add chart/
git commit -m "test: assert CRD ships as toggled template with resource-policy keep" --allow-empty
```

- [ ] **Task 3 complete**

---

## Task 4: Keppel-free image default in values.yaml

**Files:**
- Modify: `chart/values.yaml`

- [ ] **Step 1: Inspect the generated image block**

Run: `grep -nA4 "image:" chart/values.yaml`
Expected: a `manager.image.repository` + `manager.image.tag` block (kubebuilder default, likely `repository: controller`).

- [ ] **Step 2: Set the keppel-free ghcr default, empty tag**

Edit `chart/values.yaml` so the manager image is:

```yaml
manager:
  image:
    repository: ghcr.io/SAP-cloud-infrastructure/dual-deployment-operator
    tag: ""            # defaults to .Chart.AppVersion in the template
```

Keep the surrounding generated keys (pullPolicy, resources, etc.) as-is.

- [ ] **Step 3: Assert the rendered image resolves to the ghcr path at AppVersion**

Run:
```bash
APPVER=$(grep '^appVersion:' chart/Chart.yaml | awk '{print $2}' | tr -d '"')
helm template dual-deployment-operator chart/ | grep -E "image: .*dual-deployment-operator"
```
Expected: `image: ghcr.io/SAP-cloud-infrastructure/dual-deployment-operator:<appVersion>`.

- [ ] **Step 4: Assert NO keppel reference anywhere in the chart**

Run: `! grep -rn "keppel" chart/ && echo "keppel-free OK"`
Expected: `keppel-free OK`.

- [ ] **Step 5: Assert image is overridable (subchart-style)**

Run:
```bash
helm template dual-deployment-operator chart/ \
  --set manager.image.repository=keppel.global.cloud.sap/ccloud-ghcr-io-mirror/SAP-cloud-infrastructure/dual-deployment-operator \
  --set manager.image.tag=sha-deadbeef | grep -E "image: .*ccloud-ghcr-io-mirror.*:sha-deadbeef"
```
Expected: the override path + tag appear in the rendered Deployment.

- [ ] **Step 6: Commit**

```bash
git add chart/values.yaml
git commit -m "feat: keppel-free ghcr image default in chart values (overridable by wrapper)"
```

- [ ] **Task 4 complete**

---

## Task 5: Gardener egress labels + probes on the Deployment

**Files:**
- Modify: `chart/values.yaml` and/or `chart/templates/manager/manager.yaml` (whichever the plugin uses to stamp pod labels)

- [ ] **Step 1: Locate the pod template metadata.labels in the generated Deployment**

Run: `grep -rn "template:" -A6 chart/templates/ | grep -n "labels" ; ls chart/templates/manager 2>/dev/null || ls chart/templates`
Expected: identify the Deployment template file and its pod `template.metadata.labels` block.

- [ ] **Step 2: Add the three Gardener egress labels to the pod template**

Edit the Deployment template's `spec.template.metadata.labels` to include (literal, not gated on a value so they are always present):

```yaml
      labels:
        networking.gardener.cloud/to-dns: allowed
        networking.gardener.cloud/to-public-networks: allowed
        networking.gardener.cloud/to-private-networks: allowed
        # ...existing generated labels (control-plane, selector labels) preserved...
```

- [ ] **Step 3: Assert all three egress labels render on the pod template**

Run:
```bash
helm template dual-deployment-operator chart/ | \
  awk '/kind: Deployment/,/kind: /' | \
  grep -E "networking.gardener.cloud/to-(dns|public-networks|private-networks): allowed" | wc -l
```
Expected: `3`.

- [ ] **Step 4: Assert probes and bind-address flags are present**

Run:
```bash
helm template dual-deployment-operator chart/ | grep -E "path: /healthz|path: /readyz"
helm template dual-deployment-operator chart/ | grep -E -- "--health-probe-bind-address|--metrics-bind-address|--leader-elect=true"
```
Expected: `/healthz` and `/readyz` both present; all three flags present.

- [ ] **Step 5: Assert replicas default is 1 and NO chart-cache emptyDir**

Run:
```bash
helm template dual-deployment-operator chart/ | grep -E "replicas: 1"
! helm template dual-deployment-operator chart/ | grep -E "source-cache|/cache/source" && echo "no cache volume OK"
```
Expected: `replicas: 1` present; `no cache volume OK`.

- [ ] **Step 6: Re-lint after edits**

Run: `helm lint chart/`
Expected: `0 chart(s) failed`.

- [ ] **Step 7: Commit**

```bash
git add chart/
git commit -m "feat: Gardener egress labels on manager pod; confirm probes/leader-elect/replicas"
```

- [ ] **Task 5 complete**

---

## Task 6: Broad seed applier RBAC (verify scope + release-namespace binding)

**Files:**
- Verify/Modify: `chart/templates/rbac/*.yaml`

- [ ] **Step 1: Confirm a ClusterRole + ClusterRoleBinding are generated**

Run: `grep -rl "kind: ClusterRole\b" chart/templates/ ; grep -rl "kind: ClusterRoleBinding" chart/templates/`
Expected: at least one ClusterRole and one ClusterRoleBinding template.

- [ ] **Step 2: Confirm the applier grant covers CR watch + Secrets + seed-render kinds**

Run: `helm template dual-deployment-operator chart/ | awk '/kind: ClusterRole$/,/kind: ClusterRoleBinding/'`
Expected: rules include `dualdeploymentoperators` (get/list/watch), `secrets` (get/list), and the seed-render kinds (deployments, services, configmaps, serviceaccounts, roles, rolebindings, clusterroles, clusterrolebindings, networkpolicies) with create/update/delete/get/list/watch. If the generated grant is narrower than the spec (`config/rbac` markers on the controller only cover the CR), add the applier rules to `config/rbac/role.yaml` via `+kubebuilder:rbac` markers in `internal/controller/*_controller.go`, run `make manifests`, then re-run Task 2 Step 1 to regenerate the chart.

- [ ] **Step 3: Confirm the ClusterRoleBinding subject uses the release namespace**

Run: `grep -rn "namespace:" chart/templates/rbac/*.yaml | grep -i "Release.Namespace" || helm template dual-deployment-operator chart/ -n shoot--cp--test | awk '/kind: ClusterRoleBinding/,/roleRef/' | grep "namespace: shoot--cp--test"`
Expected: the binding subject namespace is `{{ .Release.Namespace }}` (renders to the install namespace), not hardcoded.

- [ ] **Step 4: Confirm cluster-scoped role names are static (release-independent)**

Run: `helm template dual-deployment-operator chart/ -n shoot--cp--a | grep -A2 "kind: ClusterRole$" | grep "name:" ; helm template dual-deployment-operator chart/ -n shoot--cp--b | grep -A2 "kind: ClusterRole$" | grep "name:"`
Expected: identical ClusterRole names across the two namespaces (seed-global names → single-install-per-seed contract).

- [ ] **Step 5: Commit (only if RBAC markers/chart changed)**

```bash
git add chart/ config/rbac/ internal/controller/ 2>/dev/null
git commit -m "feat: broad seed applier ClusterRole with release-namespace-bound subject" --allow-empty
```

- [ ] **Task 6 complete**

---

## Task 7: Per-shoot namespace-agnostic assertions + REUSE headers + drift gate

**Files:**
- Verify: `chart/templates/**`
- Add (if needed): REUSE/SPDX coverage for `chart/`

- [ ] **Step 1: Assert namespaced resources do NOT hardcode metadata.namespace**

Run:
```bash
helm template dual-deployment-operator chart/ -n shoot--cp--x | \
  grep -E "^\s+namespace:" | grep -v "shoot--cp--x" | grep -v "Release.Namespace" || echo "no hardcoded namespaces OK"
```
Expected: `no hardcoded namespaces OK` (every rendered namespace is the release namespace).

- [ ] **Step 2: Assert two releases land in independent namespaces**

Run:
```bash
helm template dual-deployment-operator chart/ -n shoot--cp--m-a | grep -m1 "namespace: shoot--cp--m-a"
helm template dual-deployment-operator chart/ -n shoot--cp--m-b | grep -m1 "namespace: shoot--cp--m-b"
```
Expected: each render's Deployment/SA namespace matches its own `-n` value.

- [ ] **Step 3: Ensure REUSE/SPDX compliance for the new chart files**

Run: `make check 2>&1 | grep -iE "reuse|license" || reuse lint 2>&1 | tail -5`
Expected: REUSE lint passes. If `chart/` files lack headers, add a `chart/**` paths block to the `reuse.annotations` in `Makefile.maker.yaml` (SPDX `Apache-2.0`, `SAP SE or an SAP affiliate company`), run `make` to regenerate `REUSE.toml`/`Makefile`, and re-run.

- [ ] **Step 4: Full gate — codegen drift + build + lint + helm**

Run:
```bash
make manifests generate && git diff --exit-code config/ api/ && \
go build ./... && \
make run-golangci-lint && \
helm lint chart/ && helm template dual-deployment-operator chart/ >/dev/null && \
echo "ALL GREEN"
```
Expected: `ALL GREEN` (no codegen drift, build clean, lint clean, chart lints + templates).

- [ ] **Step 5: Record the post-gen customization set (regeneration guard)**

Append a short "Chart customizations re-applied after any `kubebuilder edit --force`" note to `chart/README.md` (create if absent) listing: keppel-free image default (Task 4), the three Gardener egress pod labels (Task 5), `--leader-elect=true` (Task 1/5), and the broad applier RBAC (Task 6). This is the documented set an implementer re-applies after regeneration.

- [ ] **Step 6: Commit**

```bash
git add chart/ Makefile.maker.yaml REUSE.toml Makefile 2>/dev/null
git commit -m "chore: REUSE headers for chart, per-shoot namespace assertions, regeneration guard notes"
```

- [ ] **Task 7 complete**

---

## Self-Review

**Spec coverage:**
- `controller-helm-chart` — "generated from config/*" → Task 2; "regeneration confined to chart/" → Task 2 Step 4; "lints/templates cleanly" → Task 2 Step 5 + Task 7 Step 4; "CRD ships as toggled template with keep" → Task 3.
- `controller-chart-image` — "keppel-free ghcr default + AppVersion tag" → Task 4 Steps 2–4; "overridable / subchart override" → Task 4 Step 5.
- `controller-chart-rbac` — "broad ClusterRole/Binding" → Task 6 Steps 1–2; "leader-election lease" → covered by generated leader-election Role (Task 6 Step 1 scope) + Task 1; "ClusterRoleBinding subject release namespace + static names" → Task 6 Steps 3–4.
- `controller-chart-deployment` — "per-shoot release namespace" → Task 7 Steps 1–2; "Gardener egress labels" → Task 5 Steps 2–3; "leader election + replicas 1" → Task 1 + Task 5 Steps 4–5; "probes + metrics + no cache volume" → Task 5 Steps 4–5.

**Placeholder scan:** No TBD/TODO; every code/edit step shows exact content or an exact command with expected output. Fallback branches (e.g. Task 6 Step 2 "if narrower, add markers") give the concrete remediation, not a vague "handle it".

**Type/name consistency:** `manager.image.repository`/`manager.image.tag` used consistently (Tasks 4). CRD filename `dual-deployment-operator.cc.sap_dualdeploymentoperators.yaml` matches `config/crd/bases/`. `chart/` output dir and `helm template dual-deployment-operator chart/` release name used consistently throughout.

**Open dependency:** Task 6 may require adding `+kubebuilder:rbac` applier markers if the current controller markers only grant CR access — the plan gives the exact remediation path (markers → `make manifests` → regenerate chart) rather than assuming the generated RBAC is already broad.
