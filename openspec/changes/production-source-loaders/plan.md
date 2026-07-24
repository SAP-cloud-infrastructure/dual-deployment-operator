<!--
SPDX-FileCopyrightText: 2026 SAP SE or an SAP affiliate company
SPDX-License-Identifier: Apache-2.0
-->

# Production Source Loaders (Phase 7) Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking. Trailing `- [ ]` "Task N complete" checkboxes mark task-group completion — check them off AFTER the task's final commit lands, not before.

**Goal:** Replace the test-only `source.Deps{}` fakes and the `TODO(production-loaders)` stub in `cmd/main.go` with production loaders — a Helm `ChartLoader` (OCI + classic HTTP(S), pull-to-temp-dir, no cache) and a go-git kustomize `RootResolver` (fail-closed pinned-ref fetch) — with per-source `authSecretRef` credentials, so the operator renders real sources end-to-end. Caching is out of scope (Phase 7.5).

**Architecture:** Two independent implementations behind the existing `internal/source` seams (`ChartLoader.Load`, `RootResolver.Resolve`) plus a credential resolver keyed off a new optional CRD `authSecretRef` on `HelmSource`/`KustomizeSource`. Both loaders fetch fresh each reconcile (temp dir + cleanup). Wiring happens in `cmd/main.go`. The renderer logic and interface signatures do not change. A final, independent task migrates `GetEventRecorderFor → GetEventRecorder`.

**Tech Stack:** Go, controller-runtime, `helm.sh/helm/v3` (`action`, `registry`, `chart/loader`), `github.com/go-git/go-git/v5`, Ginkgo+Gomega tests. Online tests gated behind `DDO_ONLINE_TESTS`; offline tests use local fixtures + a local on-disk git repo.

---

## Task 1: CRD `SecretReference` type + `authSecretRef` fields

**Files:**
- Modify: `api/v1alpha1/dualdeploymentoperator_types.go` (add `SecretReference`; add `AuthSecretRef` to `HelmSource` and `KustomizeSource`)
- Test: `api/v1alpha1/dualdeploymentoperator_types_test.go`
- Regenerate: `config/crd/bases/*.yaml`, `config/rbac/role.yaml`, `api/v1alpha1/zz_generated.deepcopy.go` (via `make manifests generate` — DO NOT hand-edit)

- [ ] **Step 1: Write the failing test** (append to `api/v1alpha1/dualdeploymentoperator_types_test.go`)

> This file uses plain Go `testing` (table/round-trip style, e.g. `TestHelmSourceRoundTrip`), NOT Ginkgo. Match that style.

```go
func TestAuthSecretRefFields(t *testing.T) {
	hs := HelmSource{
		Repo: "oci://r", Name: "n", Version: "1",
		AuthSecretRef: &SecretReference{Name: "creds"},
	}
	if hs.AuthSecretRef == nil || hs.AuthSecretRef.Name != "creds" {
		t.Fatalf("HelmSource.AuthSecretRef not set: %+v", hs.AuthSecretRef)
	}

	ks := KustomizeSource{
		URL: "https://x/y//p?ref=v1", SeedPath: "seed", ShootPath: "shoot",
		AuthSecretRef: &SecretReference{Name: "git-creds"},
	}
	if ks.AuthSecretRef == nil || ks.AuthSecretRef.Name != "git-creds" {
		t.Fatalf("KustomizeSource.AuthSecretRef not set: %+v", ks.AuthSecretRef)
	}

	// unset => nil (anonymous)
	if (HelmSource{Repo: "oci://r", Name: "n", Version: "1"}).AuthSecretRef != nil {
		t.Fatal("expected nil AuthSecretRef when unset")
	}
}
```

- [ ] **Step 2: Run test to verify it fails**

Run: `go test ./api/v1alpha1/... -run TestAuthSecretRefFields -v 2>&1 | tail -20`
Expected: FAIL — compile error, `SecretReference` / `AuthSecretRef` undefined.

- [ ] **Step 3: Add the `SecretReference` type and fields** (`api/v1alpha1/dualdeploymentoperator_types.go`)

Add the type near `ShootAccessRef`:

```go
// SecretReference names a Secret in the CR's own namespace holding source-pull
// credentials (keys: username/password/token). No namespace field — the Secret is
// always resolved in the CR's namespace, matching the shootAccess convention.
type SecretReference struct {
	// +kubebuilder:validation:MinLength=1
	Name string `json:"name"`
}
```

Add the field to `HelmSource` (after `ShootValues`):

```go
	// AuthSecretRef optionally names a Secret (in the CR's namespace) holding
	// chart-pull credentials. When unset, the chart is pulled anonymously.
	// +optional
	AuthSecretRef *SecretReference `json:"authSecretRef,omitempty"`
```

Add the same field to `KustomizeSource` (after `ShootPath`):

```go
	// AuthSecretRef optionally names a Secret (in the CR's namespace) holding
	// git HTTPS credentials. When unset, the root is fetched anonymously.
	// +optional
	AuthSecretRef *SecretReference `json:"authSecretRef,omitempty"`
```

- [ ] **Step 4: Regenerate deepcopy + manifests**

Run: `make generate manifests`
Expected: `zz_generated.deepcopy.go` gains `SecretReference.DeepCopy*` and updated `HelmSource`/`KustomizeSource` deepcopy; `config/crd/bases/*.yaml` gains the `authSecretRef` schema. No errors.

- [ ] **Step 5: Run test to verify it passes**

Run: `go test ./api/v1alpha1/... -run TestAuthSecretRefFields -v 2>&1 | tail -20`
Expected: PASS.

- [ ] **Step 6: Commit**

```bash
git add api/v1alpha1/ config/crd/bases/ config/rbac/
git commit -m "feat(crd)!: add optional authSecretRef to HelmSource and KustomizeSource"
```

- [x] Task 1 complete

---

## Task 2: `RegistryCredentials` + `GitCredentials` resolvers from `authSecretRef`

**Files:**
- Create: `internal/source/credentials.go`
- Test: `internal/source/credentials_test.go`

- [ ] **Step 1: Write the failing test** (`internal/source/credentials_test.go`)

```go
package source

import "testing"

func TestCredentialsFromSecretData(t *testing.T) {
	// token wins over password
	c := credsFromSecretData(map[string][]byte{
		"username": []byte("u"), "password": []byte("p"), "token": []byte("tok"),
	})
	if c.user != "u" || c.pass != "tok" {
		t.Fatalf("token must win: got user=%q pass=%q", c.user, c.pass)
	}
	// token only => username defaults to "git" (for git HTTPS PAT)
	c = credsFromSecretData(map[string][]byte{"token": []byte("tok")})
	if c.user != "git" || c.pass != "tok" {
		t.Fatalf("token-only: got user=%q pass=%q", c.user, c.pass)
	}
	// basic auth
	c = credsFromSecretData(map[string][]byte{"username": []byte("u"), "password": []byte("p")})
	if c.user != "u" || c.pass != "p" || !c.ok {
		t.Fatalf("basic: got %+v", c)
	}
	// empty => not ok (anonymous)
	if credsFromSecretData(map[string][]byte{}).ok {
		t.Fatal("empty secret data must resolve to anonymous (ok=false)")
	}
}
```

- [ ] **Step 2: Run test to verify it fails**

Run: `go test ./internal/source/ -run TestCredentials -v`
Expected: FAIL — `credsFromSecretData` / `creds` undefined.

- [ ] **Step 3: Implement credential resolution** (`internal/source/credentials.go`)

```go
package source

import (
	"context"

	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/types"
	"sigs.k8s.io/controller-runtime/pkg/client"

	v1alpha1 "github.com/SAP-cloud-infrastructure/dual-deployment-operator/api/v1alpha1"
)

// creds is a resolved credential. ok=false means anonymous.
type creds struct {
	user string
	pass string
	ok   bool
}

// credsFromSecretData maps fixed keys to a credential. token wins over password;
// a token with no username defaults username to "git" (git HTTPS PAT convention).
func credsFromSecretData(data map[string][]byte) creds {
	user := string(data["username"])
	pass := string(data["password"])
	if tok := string(data["token"]); tok != "" {
		pass = tok
		if user == "" {
			user = "git"
		}
	}
	return creds{user: user, pass: pass, ok: user != "" || pass != ""}
}

// CredentialResolver reads an authSecretRef Secret from the CR's namespace.
type CredentialResolver struct {
	Client    client.Client
	Namespace string
}

// Resolve reads the Secret named by ref (nil ref => anonymous).
func (r *CredentialResolver) Resolve(ctx context.Context, ref *v1alpha1.SecretReference) (creds, error) {
	if ref == nil || ref.Name == "" {
		return creds{}, nil
	}
	var s corev1.Secret
	if err := r.Client.Get(ctx, types.NamespacedName{Namespace: r.Namespace, Name: ref.Name}, &s); err != nil {
		return creds{}, err
	}
	return credsFromSecretData(s.Data), nil
}
```

- [ ] **Step 4: Run test to verify it passes**

Run: `go test ./internal/source/ -run TestCredentials -v`
Expected: PASS.

- [ ] **Step 5: Commit**

```bash
git add internal/source/credentials.go internal/source/credentials_test.go
git commit -m "feat(source): per-source authSecretRef credential resolution"
```

- [x] Task 2 complete

---

## Task 3: Production `helmLoader` — OCI + classic HTTP(S), pull-to-temp-dir, auth

**Files:**
- Create: `internal/source/helmloader.go`
- Test: `internal/source/helmloader_test.go` (offline: scheme dispatch; online: `DDO_ONLINE_TESTS`)

- [ ] **Step 1: Write the failing test** (`internal/source/helmloader_test.go`)

```go
package source

import (
	"context"
	"strings"
	"testing"
)

func TestHelmLoaderRejectsUnknownScheme(t *testing.T) {
	l := newHelmLoader(nil)
	_, err := l.Load(context.Background(), "ftp://nope", "n", "1")
	if err == nil || !strings.Contains(err.Error(), "scheme") {
		t.Fatalf("expected scheme error, got %v", err)
	}
}

func TestHelmLoaderOnlineOCIAnonymous(t *testing.T) {
	requireOnline(t) // skips unless DDO_ONLINE_TESTS=1
	l := newHelmLoader(nil)
	ch, err := l.Load(context.Background(),
		"oci://keppel.global.cloud.sap/ccloud-helm", "metal-operator-remote", "0.6.2")
	if err != nil {
		t.Fatalf("anonymous OCI pull: %v", err)
	}
	if ch == nil || ch.Metadata == nil || ch.Metadata.Name == "" {
		t.Fatal("expected a parsed chart")
	}
}
```

Add the online gate helper (`internal/source/online_test.go`):

```go
package source

import (
	"os"
	"testing"
)

func requireOnline(t *testing.T) {
	t.Helper()
	if os.Getenv("DDO_ONLINE_TESTS") != "1" {
		t.Skip("set DDO_ONLINE_TESTS=1 to run online tests")
	}
}
```

- [ ] **Step 2: Run test to verify it fails**

Run: `go test ./internal/source/ -run TestHelmLoader -v`
Expected: FAIL — `newHelmLoader` undefined.

- [ ] **Step 3: Implement `helmLoader`** (`internal/source/helmloader.go`)

```go
package source

import (
	"context"
	"fmt"
	"net/url"
	"os"
	"path/filepath"
	"strings"

	"helm.sh/helm/v3/pkg/action"
	"helm.sh/helm/v3/pkg/chart"
	"helm.sh/helm/v3/pkg/chart/loader"
	"helm.sh/helm/v3/pkg/cli"
	"helm.sh/helm/v3/pkg/registry"

	"sigs.k8s.io/controller-runtime/pkg/log"
)

// helmLoader is the production ChartLoader. It pulls charts from OCI (oci://) and
// classic HTTP(S) Helm repositories into a temp dir, loads them, and cleans up.
// No caching in this change (Phase 7.5).
type helmLoader struct {
	settings *cli.EnvSettings
	resolve  func(ctx context.Context, host string) (creds, error) // nil => anonymous
}

func newHelmLoader(resolve func(context.Context, string) (creds, error)) *helmLoader {
	return &helmLoader{settings: cli.New(), resolve: resolve}
}

func (l *helmLoader) Load(ctx context.Context, repo, name, version string) (*chart.Chart, error) {
	tmp, err := os.MkdirTemp("", "ddo-helm-")
	if err != nil {
		return nil, err
	}
	defer func() { _ = os.RemoveAll(tmp) }()

	var chartPath string
	switch {
	case strings.HasPrefix(repo, "oci://"):
		chartPath, err = l.pullOCI(ctx, repo, name, version, tmp)
	case strings.HasPrefix(repo, "http://"), strings.HasPrefix(repo, "https://"):
		chartPath, err = l.pullHTTP(ctx, repo, name, version, tmp)
	default:
		return nil, fmt.Errorf("source: unsupported chart repo scheme %q (only oci:// and http(s):// are supported)", repo)
	}
	if err != nil {
		return nil, err
	}
	return loader.Load(chartPath)
}

func (l *helmLoader) credFor(ctx context.Context, host string) creds {
	if l.resolve == nil {
		return creds{}
	}
	c, err := l.resolve(ctx, host)
	if err != nil {
		log.FromContext(ctx).Info(fmt.Sprintf("credential resolve failed for %s (proceeding anonymously): %v", host, err))
		return creds{}
	}
	return c
}

func (l *helmLoader) pullOCI(ctx context.Context, repo, name, version, dest string) (string, error) {
	c := l.credFor(ctx, ociHost(repo))

	opts := []registry.ClientOption{registry.ClientOptEnableCache(true)}
	if c.ok {
		opts = append(opts, registry.ClientOptBasicAuth(c.user, c.pass)) // inline, no on-disk Login
	}
	rc, err := registry.NewClient(opts...) // MUST be non-nil even anonymously
	if err != nil {
		return "", err
	}

	cfg := &action.Configuration{RegistryClient: rc}
	pull := action.NewPullWithOpts(action.WithConfig(cfg))
	pull.Settings = l.settings
	pull.Version = version
	pull.DestDir = dest
	ref := "oci://" + strings.TrimSuffix(strings.TrimPrefix(repo, "oci://"), "/") + "/" + name
	if _, err := pull.Run(ref); err != nil {
		return "", fmt.Errorf("source: oci pull %s@%s: %w", name, version, err)
	}
	return findTGZ(dest)
}

func (l *helmLoader) pullHTTP(ctx context.Context, repo, name, version, dest string) (string, error) {
	c := l.credFor(ctx, hostOf(repo))
	rc, err := registry.NewClient(registry.ClientOptEnableCache(true))
	if err != nil {
		return "", err
	}
	cfg := &action.Configuration{RegistryClient: rc}
	pull := action.NewPullWithOpts(action.WithConfig(cfg))
	pull.Settings = l.settings
	pull.RepoURL = repo
	pull.Version = version
	pull.DestDir = dest
	if c.ok {
		pull.Username = c.user
		pull.Password = c.pass
	}
	if _, err := pull.Run(name); err != nil {
		return "", fmt.Errorf("source: http repo pull %s@%s: %w", name, version, err)
	}
	return findTGZ(dest)
}

func ociHost(repo string) string { return hostOf(strings.Replace(repo, "oci://", "https://", 1)) }

func hostOf(raw string) string {
	if u, err := url.Parse(raw); err == nil && u.Host != "" {
		return u.Host
	}
	return raw
}

// findTGZ locates the pulled chart tgz in dest (helm writes <name>-<version>.tgz).
func findTGZ(dest string) (string, error) {
	entries, _ := os.ReadDir(dest)
	for _, e := range entries {
		if strings.HasSuffix(e.Name(), ".tgz") {
			return filepath.Join(dest, e.Name()), nil
		}
	}
	return "", fmt.Errorf("source: no chart tgz found in %s", dest)
}
```

- [ ] **Step 4: Run test to verify it passes (offline)**

Run: `go test ./internal/source/ -run TestHelmLoader -v`
Expected: PASS for `RejectsUnknownScheme`; `OnlineOCIAnonymous` SKIPPED.

- [ ] **Step 5: Run the online tier once to prove the real pull**

Run: `DDO_ONLINE_TESTS=1 go test ./internal/source/ -run TestHelmLoaderOnline -v`
Expected: PASS — anonymous keppel pull returns a parsed chart.

- [ ] **Step 6: Commit**

```bash
git add internal/source/helmloader.go internal/source/helmloader_test.go internal/source/online_test.go
git commit -m "feat(source): production Helm ChartLoader (OCI + HTTP repo, authed, no cache)"
```

- [ ] Task 3 complete

---

## Task 4: Production `gitResolver` — fail-closed pinned-ref fetch via go-git

**Files:**
- Create: `internal/source/gitresolver.go`
- Test: `internal/source/gitresolver_test.go` (offline: a local on-disk repo for tag/SHA + fail-closed)

- [ ] **Step 1: Write the failing test** (`internal/source/gitresolver_test.go`)

```go
package source

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	git "github.com/go-git/go-git/v5"
	"github.com/go-git/go-git/v5/plumbing/object"
)

// makeLocalRepo builds a repo on disk with one commit + tag "v1"; returns file:// URL and SHA.
func makeLocalRepo(t *testing.T) (fileURL, sha string) {
	t.Helper()
	work := t.TempDir()
	r, err := git.PlainInit(work, false)
	if err != nil { t.Fatal(err) }
	if err := os.MkdirAll(filepath.Join(work, "seed"), 0o700); err != nil { t.Fatal(err) }
	if err := os.WriteFile(filepath.Join(work, "seed", "kustomization.yaml"), []byte("resources: []\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	w, _ := r.Worktree()
	_, _ = w.Add(".")
	h, err := w.Commit("init", &git.CommitOptions{Author: &object.Signature{Name: "t", Email: "t@t"}})
	if err != nil { t.Fatal(err) }
	if _, err := r.CreateTag("v1", h, nil); err != nil { t.Fatal(err) }
	return "file://" + work, h.String()
}

func TestGitResolverTagAndSHA(t *testing.T) {
	base, sha := makeLocalRepo(t)
	res := &gitResolver{} // anonymous

	for _, ref := range []string{"v1", sha} {
		path, cleanup, err := res.Resolve(context.Background(), base+"?ref="+ref, "seed")
		if err != nil { t.Fatalf("resolve ref %s: %v", ref, err) }
		if _, err := os.Stat(filepath.Join(path, "kustomization.yaml")); err != nil {
			cleanup()
			t.Fatalf("ref %s: expected seed/kustomization.yaml at %s: %v", ref, path, err)
		}
		cleanup()
	}
}

func TestGitResolverFailsClosedOnBadRef(t *testing.T) {
	base, _ := makeLocalRepo(t)
	res := &gitResolver{}
	_, cleanup, err := res.Resolve(context.Background(), base+"?ref=does-not-exist", "seed")
	if cleanup != nil { cleanup() }
	if err == nil {
		t.Fatal("expected fail-closed error for a nonexistent ref")
	}
}
```

- [ ] **Step 2: Run test to verify it fails**

Run: `go test ./internal/source/ -run TestGitResolver -v`
Expected: FAIL — `gitResolver` undefined.

- [ ] **Step 3: Implement `gitResolver`** (`internal/source/gitresolver.go`)

```go
package source

import (
	"context"
	"fmt"
	"net/url"
	"os"
	"path/filepath"
	"regexp"

	git "github.com/go-git/go-git/v5"
	"github.com/go-git/go-git/v5/config"
	"github.com/go-git/go-git/v5/plumbing"
	"github.com/go-git/go-git/v5/plumbing/transport"
	httpauth "github.com/go-git/go-git/v5/plumbing/transport/http"
)

var shaRe = regexp.MustCompile(`^[0-9a-f]{7,40}$`)

// gitResolver is the production RootResolver. It fetches a ?ref=-pinned remote git
// root into a temp dir (fail-closed) and returns <tmp>/<subPath> + cleanup.
type gitResolver struct {
	resolve func(ctx context.Context, host string) (creds, error) // nil => anonymous
}

func (r *gitResolver) Resolve(ctx context.Context, rawURL, subPath string) (string, func(), error) {
	base, ref, err := splitRef(rawURL)
	if err != nil {
		return "", nil, err
	}
	dir, err := os.MkdirTemp("", "ddo-kustomize-")
	if err != nil {
		return "", nil, err
	}
	cleanup := func() { _ = os.RemoveAll(dir) }

	auth := r.authFor(ctx, base)
	if err := fetchPinned(ctx, dir, base, ref, auth); err != nil {
		cleanup()
		return "", nil, err
	}
	return filepath.Join(dir, subPath), cleanup, nil
}

func (r *gitResolver) authFor(ctx context.Context, base string) transport.AuthMethod {
	if r.resolve == nil {
		return nil
	}
	c, err := r.resolve(ctx, hostOf(base))
	if err != nil || !c.ok {
		return nil
	}
	return &httpauth.BasicAuth{Username: c.user, Password: c.pass}
}

// splitRef extracts the base repo URL and the pinned ref from a ?ref= URL.
func splitRef(rawURL string) (base, ref string, err error) {
	u, err := url.Parse(rawURL)
	if err != nil {
		return "", "", fmt.Errorf("source: parse kustomize url: %w", err)
	}
	ref = u.Query().Get("ref")
	if ref == "" {
		return "", "", fmt.Errorf("source: kustomize url missing pinned ?ref=")
	}
	u.RawQuery = ""
	return u.String(), ref, nil
}

// fetchPinned fetches ref (tag/branch OR sha) at Depth 1, fail-closed.
func fetchPinned(ctx context.Context, dir, base, ref string, auth transport.AuthMethod) error {
	if shaRe.MatchString(ref) {
		return fetchSHA(ctx, dir, base, ref, auth)
	}
	for _, rn := range []plumbing.ReferenceName{
		plumbing.NewTagReferenceName(ref), plumbing.NewBranchReferenceName(ref),
	} {
		_, err := git.PlainCloneContext(ctx, dir, false, &git.CloneOptions{
			URL: base, Auth: auth, ReferenceName: rn, SingleBranch: true, Depth: 1,
		})
		if err == nil {
			return nil
		}
	}
	return fmt.Errorf("source: could not resolve ref %q on %s (fail-closed)", ref, base)
}

func fetchSHA(ctx context.Context, dir, base, sha string, auth transport.AuthMethod) error {
	repo, err := git.PlainInit(dir, false)
	if err != nil {
		return err
	}
	remote, err := repo.CreateRemote(&config.RemoteConfig{Name: "origin", URLs: []string{base}})
	if err != nil {
		return err
	}
	tmpRef := plumbing.NewBranchReferenceName("ddo-pin")
	if err := remote.FetchContext(ctx, &git.FetchOptions{
		RefSpecs: []config.RefSpec{config.RefSpec(sha + ":" + tmpRef.String())},
		Depth:    1, Auth: auth,
	}); err != nil {
		return fmt.Errorf("source: fetch sha %s: %w (fail-closed)", sha, err)
	}
	w, err := repo.Worktree()
	if err != nil {
		return err
	}
	if err := w.Checkout(&git.CheckoutOptions{Hash: plumbing.NewHash(sha)}); err != nil {
		return fmt.Errorf("source: checkout sha %s: %w (fail-closed)", sha, err)
	}
	return nil
}
```

- [ ] **Step 4: Run test to verify it passes**

Run: `go test ./internal/source/ -run TestGitResolver -v`
Expected: PASS — tag + SHA resolve from the local repo; bad ref fails closed.

- [ ] **Step 5: Commit**

```bash
git add internal/source/gitresolver.go internal/source/gitresolver_test.go
git commit -m "feat(source): production git RootResolver via go-git (fail-closed pinned ref)"
```

- [ ] Task 4 complete

---

## Task 5: Exported constructors + `CredentialResolver` in `Deps`

**Files:**
- Modify: `internal/source/source.go` (add exported constructors; add `CredentialResolver` to `Deps`; thread per-source `authSecretRef` into the loaders)
- Test: `internal/source/source_test.go` (authed path with a fake client + Secret)

- [ ] **Step 1: Write the failing test** (append to `internal/source/source_test.go`)

> This file uses plain Go `testing` (e.g. `TestFromSelectsHelm`), NOT Ginkgo. Match that style. The `controller-runtime` fake client + core scheme are needed; add these imports to the test file: `corev1 "k8s.io/api/core/v1"`, `metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"`, `"k8s.io/apimachinery/pkg/runtime"`, `"sigs.k8s.io/controller-runtime/pkg/client/fake"`, and the project `v1alpha1` alias.

```go
func TestFromWiresAuthSecretRefCredentials(t *testing.T) {
	scheme := runtime.NewScheme()
	if err := corev1.AddToScheme(scheme); err != nil {
		t.Fatal(err)
	}
	cl := fake.NewClientBuilder().WithScheme(scheme).WithObjects(&corev1.Secret{
		ObjectMeta: metav1.ObjectMeta{Name: "creds", Namespace: "ns"},
		Data:       map[string][]byte{"token": []byte("tok")},
	}).Build()

	deps := Deps{
		ChartLoader:        NewHelmLoader(),
		RootResolver:       NewGitResolver(),
		CredentialResolver: &CredentialResolver{Client: cl, Namespace: "ns"},
	}
	spec := v1alpha1.Source{Helm: &v1alpha1.HelmSource{
		Repo: "oci://r", Name: "n", Version: "1",
		AuthSecretRef: &v1alpha1.SecretReference{Name: "creds"},
	}}
	if _, err := From(spec, deps); err != nil {
		t.Fatalf("From with authSecretRef: %v", err)
	}
}
```

- [ ] **Step 2: Run test to verify it fails**

Run: `go test ./internal/source/ -run TestFromWiresAuthSecretRefCredentials -v`
Expected: FAIL — `NewHelmLoader` / `NewGitResolver` / `Deps.CredentialResolver` undefined.

- [ ] **Step 3: Add constructors + `CredentialResolver` field + threading** (`internal/source/source.go`)

Add exported constructors:

```go
// NewHelmLoader returns a production OCI+HTTP ChartLoader (no cache). The
// per-source credential resolver is injected via Deps.CredentialResolver.
func NewHelmLoader() ChartLoader { return newHelmLoader(nil) }

// NewGitResolver returns a production git RootResolver.
func NewGitResolver() RootResolver { return &gitResolver{} }
```

Add the field to `Deps`:

```go
type Deps struct {
	ChartLoader        ChartLoader
	RootResolver       RootResolver
	CredentialResolver *CredentialResolver // optional; nil => anonymous
}
```

In `From`, when `deps.CredentialResolver != nil`, bind a per-source `resolve` func (closing over the source's `AuthSecretRef`) onto the concrete `*helmLoader` / `*gitResolver` via an unexported setter that does NOT change the public interface signatures:

```go
func bindHelmCreds(cl ChartLoader, cr *CredentialResolver, ref *v1alpha1.SecretReference) {
	if hl, ok := cl.(*helmLoader); ok && cr != nil {
		hl.resolve = func(ctx context.Context, _ string) (creds, error) { return cr.Resolve(ctx, ref) }
	}
}
func bindGitCreds(rr RootResolver, cr *CredentialResolver, ref *v1alpha1.SecretReference) {
	if gr, ok := rr.(*gitResolver); ok && cr != nil {
		gr.resolve = func(ctx context.Context, _ string) (creds, error) { return cr.Resolve(ctx, ref) }
	}
}
```

Call `bindHelmCreds` in the `spec.Helm` branch (with `spec.Helm.AuthSecretRef`) and `bindGitCreds` in the `spec.Kustomize` branch (with `spec.Kustomize.AuthSecretRef`) before returning the source. (The `resolve` func ignores its `host` arg — the `authSecretRef` already scopes credentials to this source.)

- [ ] **Step 4: Run test to verify it passes**

Run: `go test ./internal/source/ -run TestFromWiresAuthSecretRefCredentials -v`
Expected: PASS.

- [ ] **Step 5: Commit**

```bash
git add internal/source/source.go internal/source/source_test.go
git commit -m "feat(source): exported loader constructors + authSecretRef credential threading"
```

- [ ] Task 5 complete

---

## Task 6: Wire production loaders into `cmd/main.go` + reconciler

**Files:**
- Modify: `cmd/main.go` (construct loaders; replace `source.Deps{}`)
- Modify: `internal/controller/dualdeploymentoperator_controller.go` (set per-CR `CredentialResolver` namespace on the `From` call)

- [ ] **Step 1: Replace the `source.Deps{}` stub** (`cmd/main.go`)

Replace the `SourceDeps: source.Deps{}` block (removing the `TODO(production-loaders)` comment):

```go
		SourceDeps: source.Deps{
			ChartLoader:  source.NewHelmLoader(),
			RootResolver: source.NewGitResolver(),
		},
```

- [ ] **Step 2: Set the per-CR credential resolver in the reconciler**

Where the reconciler calls `source.From(cr.Spec.Source, r.SourceDeps)`, inject the CR namespace:

```go
deps := r.SourceDeps
deps.CredentialResolver = &source.CredentialResolver{Client: r.Client, Namespace: cr.Namespace}
src, err := source.From(cr.Spec.Source, deps)
```

- [ ] **Step 3: Build to verify wiring compiles**

Run: `make build`
Expected: exit 0; `TODO(production-loaders)` line removed.

- [ ] **Step 4: Run the full unit suite (offline)**

Run: `make test`
Expected: PASS; no network hit (online tests skipped).

- [ ] **Step 5: Commit**

```bash
git add cmd/main.go internal/controller/
git commit -m "feat: wire production source loaders into manager (remove TODO stub)"
```

- [ ] Task 6 complete

---

## Task 7: Gated online auth integration tests

**Files:**
- Test: `internal/source/helmloader_online_test.go` (self-hosted authed OCI registry); `internal/source/gitresolver_online_test.go` (local authed git server) — both `//go:build online`

- [ ] **Step 1: Write the authed OCI online test** (`internal/source/helmloader_online_test.go`)

```go
//go:build online

package source

// Spin up a local authed OCI registry (registry:2 / zot via testcontainers OR a
// pre-provisioned DDO_TEST_OCI_URL + DDO_TEST_OCI_USER/PASS), push a tiny fixture
// chart, then Load with matching creds and assert the chart parses. Load with a
// wrong credential must return an error whose text contains neither user nor pass.
```

- [ ] **Step 2: Write the authed git online test** (`internal/source/gitresolver_online_test.go`)

```go
//go:build online

package source

// Serve a local repo over HTTP requiring basic auth; Resolve with a matching
// http.BasicAuth-derived creds succeeds; wrong creds => error with no secret text.
```

- [ ] **Step 3: Run offline suite (build-tagged tests excluded)**

Run: `make test`
Expected: PASS; online tests not compiled.

- [ ] **Step 4: Run the gated tier once**

Run: `DDO_ONLINE_TESTS=1 go test -tags online ./internal/source/... -v 2>&1 | tail -40`
Expected: PASS — authed OCI + authed git succeed; wrong-cred errors are secret-free.

- [ ] **Step 5: Commit**

```bash
git add internal/source/*online_test.go
git commit -m "test(source): gated online auth integration tests (OCI + git HTTPS)"
```

- [ ] Task 7 complete

---

## Task 8: Migrate `GetEventRecorderFor` → `GetEventRecorder` (distinct final task)

**Files:**
- Modify: `cmd/main.go:185` (recorder construction; drop `//nolint:staticcheck`)
- Modify: `internal/controller/dualdeploymentoperator_controller.go` (`Recorder` field type + every `Recorder.Event(...)` call site)
- Modify: controller test suite fake recorder (events-API equivalent)

- [ ] **Step 1: Change the reconciler `Recorder` field type**

In `internal/controller/dualdeploymentoperator_controller.go`, change the field from `record.EventRecorder` to the events API recorder:

```go
import "k8s.io/client-go/tools/events"
// ...
Recorder events.EventRecorder
```

Update every call site from `r.Recorder.Event(obj, eventtype, reason, message)` to:

```go
r.Recorder.Eventf(obj, nil, eventtype, reason, "", message)
```

- [ ] **Step 2: Update `cmd/main.go` recorder construction**

Replace line 185:

```go
recorder := mgr.GetEventRecorder()
```

Remove the `//nolint:staticcheck` comment.

- [ ] **Step 3: Update the controller test fake recorder**

In the controller suite, replace `record.FakeRecorder` with an events-API fake (or a minimal `events.EventRecorder` stub whose `Eventf` records calls) and update assertions.

- [ ] **Step 4: Build + lint + test**

Run: `make build && golangci-lint run ./... 2>&1 | tail -20 && make test`
Expected: build exit 0; lint clean with NO `//nolint:staticcheck` for the recorder; tests PASS; events still emitted (asserted by the suite).

- [ ] **Step 5: Commit**

```bash
git add cmd/main.go internal/controller/
git commit -m "refactor: migrate GetEventRecorderFor to GetEventRecorder (events API)"
```

- [ ] Task 8 complete

---

## Task 9: Full verification gate

**Files:** none (verification only)

- [ ] **Step 1: Regenerate + build + lint + offline tests**

Run: `make manifests generate && make build && make lint && make test`
Expected: no manifest drift (git diff clean after generate), build exit 0, lint clean, all offline tests PASS.

- [ ] **Step 2: Run the online tier once**

Run: `DDO_ONLINE_TESTS=1 go test -tags online ./internal/source/... -v 2>&1 | tail -40`
Expected: anonymous keppel OCI pull + public-github kustomize resolve + authed variants all PASS.

- [ ] **Step 3: Confirm no stub remains**

Run: `grep -rn "TODO(production-loaders)\|source.Deps{}" cmd/ internal/ || echo "clean"`
Expected: `clean`.

- [ ] **Step 4: Commit any generated drift**

```bash
git add -A && git commit -m "chore: regenerate manifests for production-source-loaders" || echo "nothing to commit"
```

- [ ] Task 9 complete
