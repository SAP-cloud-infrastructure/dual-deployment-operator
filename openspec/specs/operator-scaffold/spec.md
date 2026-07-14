# Spec: Operator Scaffold

## Purpose

Defines the kubebuilder project initialization, repository directory layout, Go module dependencies, infrastructure manifests (manager Deployment, RBAC, Makefile, Dockerfile), and boilerplate header requirements for the `dual-deployment-operator` scaffold.

## Requirements

### Requirement: Kubebuilder project initialization

The repository MUST be initialized as a kubebuilder v3+ project targeting Go 1.22+ with:

- `--domain cc.sap`
- `--repo github.com/SAP-cloud-infrastructure/dual-deployment-operator`
- `--project-name dual-deployment-operator`

The `PROJECT` file MUST record group `dual-deployment-operator`, kind `DualDeploymentOperator`, and version `v1alpha1`.

#### Scenario: Project file records correct scaffold identity

- **WHEN** the maintainer runs the kubebuilder init and create-api commands per `docs/implementation.md` §Phase 0
- **THEN** the file `PROJECT` at the repository root exists
- **AND** it contains `domain: cc.sap`
- **AND** it contains `repo: github.com/SAP-cloud-infrastructure/dual-deployment-operator`
- **AND** its `resources` list includes exactly one entry with `group: dual-deployment-operator`, `kind: DualDeploymentOperator`, `version: v1alpha1`

#### Scenario: Go module path matches scaffold flag

- **WHEN** the repository is initialized
- **THEN** `go.mod` line 1 is `module github.com/SAP-cloud-infrastructure/dual-deployment-operator`

---

### Requirement: Repository directory layout

The repository MUST contain the following directories at the paths specified in `docs/implementation.md` §Phase 0, populated by the kubebuilder scaffold and this change's additions:

```
api/v1alpha1/                              # CRD Go types
  dualdeploymentoperator_types.go
  groupversion_info.go
  zz_generated.deepcopy.go
cmd/main.go                                # entrypoint
config/
  crd/bases/                               # generated CRD manifest
  crd/patches/                             # (scaffolded, empty by default)
  manager/                                 # Deployment + Service manifests
  rbac/                                    # ServiceAccount, Role, RoleBinding
  default/                                 # kustomize entrypoint
  certmanager/                             # scaffolded, not enabled
  webhook/                                 # scaffolded, not enabled
internal/
  controller/
    dualdeploymentoperator_controller.go   # no-op reconciler
    suite_test.go                          # envtest scaffold
  webhook/v1alpha1/
    dualdeploymentoperator_webhook.go      # no-op CustomValidator methods
    webhook_suite_test.go                  # envtest scaffold
hack/
  boilerplate.go.txt                       # SPDX header for generated files
Makefile
Dockerfile
go.mod
go.sum
```

Files not listed above and not scaffolded by kubebuilder MUST NOT be added to the repository by this change.

#### Scenario: Directory layout matches implementation.md

- **WHEN** the repository is checked out after this change lands
- **THEN** every path in the list above exists
- **AND** paths not in the list are either kubebuilder scaffold outputs or excluded by `.gitignore` (`bin/`, `.vscode/`, etc.)

---

### Requirement: Go module dependencies

`go.mod` MUST declare the following top-level dependencies, using latest stable versions verified at implementation time:

- `sigs.k8s.io/controller-runtime` — imported by the reconciler and manager
- `k8s.io/api`, `k8s.io/apimachinery`, `k8s.io/client-go` — imported by types and reconciler
- `k8s.io/apiextensions-apiserver` — imported for `apiextensionsv1.JSON`
- `github.com/onsi/ginkgo/v2`, `github.com/onsi/gomega` — imported by envtest
- `helm.sh/helm/v3` — declared but not imported in this change; reserved for Phase 2
- `sigs.k8s.io/kustomize/api` — declared but not imported in this change; reserved for Phase 2
- `sigs.k8s.io/kustomize/kyaml` — declared alongside kustomize/api

Phase 2 dependencies (Helm, kustomize) are pulled in early so `go.sum` settles once and future changes do not drag them in. They MUST NOT be imported by any Go source in this change; presence in `go.mod` `require` alone is sufficient.

#### Scenario: Required imports are present

- **WHEN** the maintainer runs `go mod graph | grep -E '(helm|kustomize|controller-runtime|apiextensions)'`
- **THEN** all listed dependencies appear at the top level

#### Scenario: Helm and kustomize are not imported yet

- **WHEN** the maintainer runs `go list -deps ./... | grep -E '(helm.sh/helm|sigs.k8s.io/kustomize)'`
- **THEN** the output is empty
- **AND** neither package appears in the transitive import graph of any file under `api/`, `cmd/`, or `internal/`

---

### Requirement: Manager Deployment scaffold

`config/manager/manager.yaml` MUST declare a `Deployment` named `dual-deployment-operator-controller-manager` in a namespace determined by the deployment chart (parameterized via kustomize), with:

- Single replica by default
- Container `manager` running the operator binary
- Resource requests: 100m CPU, 128Mi memory (kubebuilder defaults; tuned later)
- Liveness and readiness probes on `/healthz` and `/readyz` respectively
- `ServiceAccount` reference to `dual-deployment-operator-controller-manager`

Phase 8 (deployment chart) will consume this manifest via kustomize; this change lands the manifest content, not any cluster-side application.

#### Scenario: Manager manifest is kustomize-buildable

- **WHEN** the maintainer runs `kustomize build config/default`
- **THEN** the output contains exactly one `Deployment` named `dual-deployment-operator-controller-manager`
- **AND** the Deployment's ServiceAccount name matches the scaffolded `ServiceAccount`

---

### Requirement: RBAC scaffold with namespace-scoped roles

`config/rbac/` MUST contain kubebuilder-scaffolded `ServiceAccount`, `Role`, `RoleBinding`, and `ClusterRole`/`ClusterRoleBinding` resources appropriate for a namespace-scoped operator. In v1 the operator's RBAC grants MUST cover only:

- `get`, `list`, `watch`, `patch`, `update` on `DualDeploymentOperator` CRs in the operator's own namespace
- `patch`, `update` on `DualDeploymentOperator/status` and `DualDeploymentOperator/finalizers`
- `get`, `list`, `watch` on `Secrets` in the operator's own namespace (for future kubeconfig fetch, unused in v1 no-op reconciler)

RBAC scaffolds for applying arbitrary host or remote resources (Phases 4-6) MUST NOT be added in this change.

#### Scenario: RBAC covers only CRs and Secrets

- **WHEN** the maintainer runs `kustomize build config/rbac`
- **THEN** the output's `Role` grants verbs on `dualdeploymentoperators.dual-deployment-operator.cc.sap` and `secrets` (core)
- **AND** it does NOT grant permissions on `deployments`, `services`, `customresourcedefinitions`, `validatingwebhookconfigurations`, or any other cluster-scoped resources

---

### Requirement: Makefile targets

The `Makefile` MUST support the standard kubebuilder targets used in CI and development:

- `make manifests` — regenerates CRDs and RBAC via `controller-gen`
- `make generate` — regenerates `zz_generated.deepcopy.go`
- `make fmt` — runs `go fmt ./...`
- `make vet` — runs `go vet ./...`
- `make test` — runs unit tests and envtest suite
- `make build` — builds the operator binary at `bin/manager`
- `make run` — runs the operator locally against the current kubectl context
- `make docker-build`, `make docker-push` — container image lifecycle
- `make install` — applies the CRD to the current cluster
- `make uninstall` — removes the CRD from the current cluster
- `make deploy`, `make undeploy` — applies/removes full config via `kustomize build config/default | kubectl apply -f -`

The Makefile MUST additionally pin `ENVTEST_K8S_VERSION` to a value ≥ 1.29 as its own variable near the top of the file.

#### Scenario: Every listed target exists

- **WHEN** the maintainer runs `make -n <target>` for each target in the list
- **THEN** the dry-run succeeds without a "No rule to make target" error

#### Scenario: envtest version is pinned in Makefile

- **WHEN** the maintainer runs `grep -E '^ENVTEST_K8S_VERSION' Makefile`
- **THEN** exactly one line matches
- **AND** the version is 1.29 or higher

---

### Requirement: Dockerfile builds the operator binary

`Dockerfile` MUST be a two-stage build:

- Stage 1 (`builder`): from a `golang:1.22+` base image; runs `go mod download`, then `go build -o manager cmd/main.go`
- Stage 2 (runtime): from a distroless or scratch base image (`gcr.io/distroless/static:nonroot` per kubebuilder default); copies the `manager` binary; runs as non-root user; ENTRYPOINT is `/manager`

#### Scenario: Docker build produces the manager image

- **WHEN** the maintainer runs `make docker-build`
- **THEN** a container image is produced tagged as `IMG` (defaulted by the Makefile)
- **AND** running the image without arguments starts the `manager` binary

---

### Requirement: Boilerplate header on generated files

`hack/boilerplate.go.txt` MUST contain an SPDX license header appropriate for the SAP-cloud-infrastructure organization. `controller-gen` MUST use this file to prefix `zz_generated.deepcopy.go` and other generated Go files.

#### Scenario: Generated files carry the boilerplate

- **WHEN** the maintainer runs `make generate`
- **THEN** the first lines of `api/v1alpha1/zz_generated.deepcopy.go` match the content of `hack/boilerplate.go.txt`
