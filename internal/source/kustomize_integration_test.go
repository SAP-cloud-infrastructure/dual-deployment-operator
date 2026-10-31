// SPDX-FileCopyrightText: 2026 SAP SE or an SAP affiliate company
// SPDX-License-Identifier: Apache-2.0

package source

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"

	git "github.com/go-git/go-git/v5"
	"github.com/go-git/go-git/v5/plumbing"
	"github.com/go-git/go-git/v5/plumbing/object"
	gitclient "github.com/go-git/go-git/v5/plumbing/transport/client"
	githttp "github.com/go-git/go-git/v5/plumbing/transport/http"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"
	"sigs.k8s.io/kustomize/api/krusty"
	"sigs.k8s.io/kustomize/api/types"
	"sigs.k8s.io/kustomize/kyaml/filesys"

	v1alpha1 "github.com/SAP-cloud-infrastructure/dual-deployment-operator/api/v1alpha1"
	"github.com/SAP-cloud-infrastructure/dual-deployment-operator/internal/manifest"
)

type integrationRoot func(context.Context, string, string) (string, func(), error)

func (f integrationRoot) Resolve(ctx context.Context, url, sub string) (root string, cleanup func(), err error) {
	return f(ctx, url, sub)
}

func integrationFiles(t *testing.T, dir string, files map[string]string) {
	t.Helper()
	for name, text := range files {
		p := filepath.Join(dir, name)
		if err := os.MkdirAll(filepath.Dir(p), 0o700); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(p, []byte(text), 0o600); err != nil {
			t.Fatal(err)
		}
	}
}

func integrationCM(name string) string {
	return "apiVersion: v1\nkind: ConfigMap\nmetadata:\n  name: " + name + "\n"
}

func integrationSource(t *testing.T, spec *v1alpha1.KustomizeSource, deps Deps) Source {
	t.Helper()
	s, err := From(v1alpha1.Source{Kustomize: spec}, deps)
	if err != nil {
		t.Fatal(err)
	}
	return s
}

func integrationRender(t *testing.T, s Source, mode Mode, namespace string) []manifest.Manifest {
	t.Helper()
	m, err := s.Render(context.Background(), mode, namespace)
	if err != nil {
		t.Fatal(err)
	}
	return m
}

func integrationManifestSet(t *testing.T, manifests []manifest.Manifest) string {
	t.Helper()
	objects := map[string]manifest.Manifest{}
	for _, m := range manifests {
		u := m.Unstructured
		objects[u.GetAPIVersion()+"/"+u.GetKind()+"/"+u.GetNamespace()+"/"+u.GetName()] = m
	}
	b, err := json.Marshal(objects)
	if err != nil {
		t.Fatal(err)
	}
	return string(b)
}

func TestKustomizeIntegrationLocalEquivalence(t *testing.T) {
	root := t.TempDir()
	integrationFiles(t, root, map[string]string{
		"kustomization.yaml":           "resources: [upstream.yaml, additions]\ngenerators:\n- |\n  apiVersion: builtin\n  kind: ConfigMapGenerator\n  metadata:\n    name: generated\n  literals: [key=value]\n",
		"upstream.yaml":                integrationCM("upstream"),
		"additions/kustomization.yaml": "resources: [cm.yaml]\ncommonAnnotations:\n  dual-deployment-operator.cc.sap/origin: additions\n",
		"additions/cm.yaml":            integrationCM("addition"),
	})
	opts := krusty.MakeDefaultOptions()
	opts.LoadRestrictions = types.LoadRestrictionsNone
	opts.PluginConfig = types.DisabledPluginConfig()
	baseline, err := krusty.MakeKustomizer(opts).Run(filesys.MakeFsOnDisk(), root)
	if err != nil {
		t.Fatal(err)
	}
	want, err := baseline.AsYaml()
	if err != nil {
		t.Fatal(err)
	}
	for _, cache := range []*renderCache{nil, mustCache(t, 8)} {
		cleaned := 0
		rr := integrationRoot(func(context.Context, string, string) (string, func(), error) {
			return root, func() { cleaned++ }, nil
		})
		s := integrationSource(t, &v1alpha1.KustomizeSource{URL: "injected"}, Deps{RootResolver: rr, RenderCache: cache})
		got := integrationRender(t, s, ModeSeed, "")
		// Serialize the actual integrated manifests back to the same krusty YAML format.
		var objects []string
		for _, m := range got {
			b, err := json.Marshal(m.Unstructured.Object)
			if err != nil {
				t.Fatal(err)
			}
			objects = append(objects, string(b))
		}
		output := t.TempDir()
		integrationFiles(t, output, map[string]string{"kustomization.yaml": "resources: [objects.yaml]\n", "objects.yaml": strings.Join(objects, "\n---\n")})
		rendered, err := krusty.MakeKustomizer(opts).Run(filesys.MakeFsOnDisk(), output)
		if err != nil {
			t.Fatal(err)
		}
		actual, err := rendered.AsYaml()
		if err != nil || string(actual) != string(want) {
			t.Fatalf("local-only output differs:\n%s\nwant:\n%s\n%v", actual, want, err)
		}
		if cleaned != 1 {
			t.Fatalf("cleanup calls=%d", cleaned)
		}
		if kinds(got)["ConfigMap/addition"] != manifest.OriginAdditions || kinds(got)["ConfigMap/upstream"] != manifest.OriginUpstream {
			t.Fatal("origins lost")
		}
	}
}

func TestKustomizeIntegrationPreflight(t *testing.T) {
	var requests atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		requests.Add(1)
		if _, err := w.Write([]byte(integrationCM("network"))); err != nil {
			t.Errorf("write fixture response: %v", err)
		}
	}))
	defer server.Close()
	url := server.URL + "/object.yaml"
	for name, kustomization := range map[string]string{
		"resource":      "resources: [" + url + "]\n",
		"patch":         "resources: [cm.yaml]\npatches:\n- path: " + url + "\n",
		"openapi":       "openapi:\n  path: " + url + "\n",
		"configuration": "configurations: [" + url + "]\n",
		"generator":     "configMapGenerator:\n- name: bad\n  files: [" + url + "]\n",
		"supplemental":  "generators:\n- |\n  apiVersion: builtin\n  kind: ConfigMapGenerator\n  metadata:\n    name: bad\n  files: [" + url + "]\n",
		"plugin":        "transformers:\n- |\n  apiVersion: example.com/v1\n  kind: External\n  metadata:\n    name: no\n",
		"helm":          "helmCharts:\n- name: bad\n  repo: " + server.URL + "\n",
		"git":           "resources: [" + server.URL + "/repo.git?ref=v1]\n",
	} {
		t.Run(name, func(t *testing.T) {
			for _, cache := range []*renderCache{nil, mustCache(t, 4)} {
				root := t.TempDir()
				integrationFiles(t, root, map[string]string{"kustomization.yaml": kustomization, "cm.yaml": integrationCM("local")})
				cleaned := false
				s := integrationSource(t, &v1alpha1.KustomizeSource{URL: "injected"}, Deps{RenderCache: cache, RootResolver: integrationRoot(func(context.Context, string, string) (string, func(), error) {
					return root, func() { cleaned = true }, nil
				})})
				before := requests.Load()
				if _, err := s.Render(context.Background(), ModeSeed, ""); err == nil {
					t.Error("unsupported input reached build")
				}
				if requests.Load() != before {
					t.Error("krusty made an upstream request")
				}
				if !cleaned {
					t.Error("root cleanup skipped")
				}
			}
		})
	}
}

func TestKustomizeIntegrationInjectedBoundary(t *testing.T) {
	parent := t.TempDir()
	integrationFiles(t, parent, map[string]string{"selected/kustomization.yaml": "resources: [../outside.yaml]\n", "outside.yaml": integrationCM("outside")})
	s := integrationSource(t, &v1alpha1.KustomizeSource{URL: "ignored", SeedPath: "selected"}, Deps{RootResolver: fakeRootResolver{baseDir: parent}})
	if _, err := s.Render(context.Background(), ModeSeed, ""); err == nil {
		t.Fatal("injected resolver boundary was guessed by climbing its parents")
	}
}

func TestKustomizeIntegrationRejectedHTTPSNeverConnects(t *testing.T) {
	var connections atomic.Int32
	server := httptest.NewUnstartedServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		if _, err := w.Write([]byte(integrationCM("upstream"))); err != nil {
			t.Errorf("write fixture response: %v", err)
		}
	}))
	server.Config.ConnState = func(_ net.Conn, state http.ConnState) {
		if state == http.StateNew {
			connections.Add(1)
		}
	}
	server.StartTLS()
	defer server.Close()
	for name, kustomization := range map[string]string{
		"resource": "resources:\n- " + server.URL + "/object.yaml\n",
		"patch":    "resources: [cm.yaml]\npatches:\n- path: " + server.URL + "/patch.yaml\n",
		"git":      "resources:\n- " + server.URL + "/repo.git?ref=v1\n",
	} {
		t.Run(name, func(t *testing.T) {
			root := t.TempDir()
			integrationFiles(t, root, map[string]string{"kustomization.yaml": kustomization, "cm.yaml": integrationCM("local")})
			s := integrationSource(t, &v1alpha1.KustomizeSource{URL: "injected"}, Deps{RootResolver: integrationRoot(func(context.Context, string, string) (string, func(), error) { return root, func() {}, nil })})
			if _, err := s.Render(context.Background(), ModeSeed, ""); err == nil {
				t.Fatal("unsupported loading reached krusty")
			}
		})
	}
	if connections.Load() != 0 {
		t.Fatal("preflight allowed an HTTPS/Git connection attempt")
	}
}

// The only transport override is in this nonparallel test harness: trust the
// local TLS server's certificate. Production still enforces HTTPS authoring, and
// real go-git listing/fetch/snapshots and real krusty rendering are exercised.
type integrationGit struct {
	server            *httptest.Server
	parent            string
	requests, fetches atomic.Int32
	authorization     atomic.Int32
	deny              atomic.Bool
	afterRequest      atomic.Pointer[func(*http.Request)]
}

func newIntegrationGit(t *testing.T) *integrationGit {
	t.Helper()
	out, err := exec.Command("git", "--exec-path").Output()
	if err != nil {
		t.Skip("git-http-backend unavailable")
	}
	backend := filepath.Join(strings.TrimSpace(string(out)), "git-http-backend")
	if _, err := os.Stat(backend); err != nil {
		t.Skip("git-http-backend unavailable")
	}
	g := &integrationGit{parent: t.TempDir()}
	authed := authedGitServer(t, backend, g.parent, "git", "good")
	g.server = httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		g.requests.Add(1)
		if r.Header.Get("Authorization") != "" {
			g.authorization.Add(1)
		}
		if g.deny.Load() {
			http.Error(w, "denied", http.StatusUnauthorized)
			return
		}
		if r.Method == http.MethodPost {
			g.fetches.Add(1)
		}
		// Anonymous fixtures are public; credentialed requests must be valid.
		if r.Header.Get("Authorization") == "" {
			r.SetBasicAuth("git", "good")
		}
		if user, password, ok := r.BasicAuth(); ok && user == "git" && password == "rotated" {
			r.SetBasicAuth("git", "good") // this fixture accepts both known credential versions
		}
		authed.Config.Handler.ServeHTTP(w, r)
		if hook := g.afterRequest.Load(); hook != nil {
			(*hook)(r)
		}
	}))
	previous := gitclient.Protocols["https"]
	gitclient.InstallProtocol("https", githttp.NewClient(g.server.Client()))
	t.Cleanup(func() { gitclient.InstallProtocol("https", previous); g.server.Close() })
	return g
}

func (g *integrationGit) repo(t *testing.T, name string, files map[string]string) (url, revision string) {
	t.Helper()
	work := t.TempDir()
	r, err := git.PlainInit(work, false)
	if err != nil {
		t.Fatal(err)
	}
	integrationFiles(t, work, files)
	w, err := r.Worktree()
	if err != nil {
		t.Fatal(err)
	}
	if _, err := w.Add("."); err != nil {
		t.Fatal(err)
	}
	sha, err := w.Commit("fixture", &git.CommitOptions{Author: &object.Signature{Name: "test", Email: "test@example.com"}})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := r.CreateTag("moving", sha, nil); err != nil {
		t.Fatal(err)
	}
	if _, err := git.PlainClone(filepath.Join(g.parent, name), true, &git.CloneOptions{URL: "file://" + work}); err != nil {
		t.Fatal(err)
	}
	return g.server.URL + "/" + name, sha.String()
}

func (g *integrationGit) moveTag(t *testing.T, name, sha string) {
	t.Helper()
	r, err := git.PlainOpen(filepath.Join(g.parent, name))
	if err != nil {
		t.Fatal(err)
	}
	if err := r.Storer.SetReference(plumbing.NewHashReference(plumbing.NewTagReferenceName("moving"), plumbing.NewHash(sha))); err != nil {
		t.Fatal(err)
	}
}

func (g *integrationGit) advance(t *testing.T, name string, files map[string]string) string {
	t.Helper()
	work := t.TempDir()
	r, err := git.PlainClone(work, false, &git.CloneOptions{URL: "file://" + filepath.Join(g.parent, name)})
	if err != nil {
		t.Fatal(err)
	}
	integrationFiles(t, work, files)
	w, err := r.Worktree()
	if err != nil {
		t.Fatal(err)
	}
	if _, err := w.Add("."); err != nil {
		t.Fatal(err)
	}
	sha, err := w.Commit("advance", &git.CommitOptions{Author: &object.Signature{Name: "test", Email: "test@example.com"}})
	if err != nil {
		t.Fatal(err)
	}
	if err := r.Push(&git.PushOptions{}); err != nil {
		t.Fatal(err)
	}
	return sha.String()
}

func TestKustomizeIntegrationGraphAndCache(t *testing.T) {
	g := newIntegrationGit(t)
	deployment := "apiVersion: apps/v1\nkind: Deployment\nmetadata:\n  name: ipam-capi-controller\nspec:\n  selector:\n    matchLabels: {app: ipam-capi}\n  template:\n    metadata:\n      labels: {app: ipam-capi}\n    spec:\n      containers:\n      - name: manager\n        image: example.invalid/ipam-capi:v1\n"
	base, sha := g.repo(t, "base.git", map[string]string{
		"config/default/kustomization.yaml": "resources: [../manager]\n",
		"config/manager/kustomization.yaml": "resources: [deployment.yaml]\n",
		"config/manager/deployment.yaml":    deployment,
		"wrong/kustomization.yaml":          "resources: [cm.yaml]\n", "wrong/cm.yaml": integrationCM("wrong-subdir"),
	})
	middle, middleSHA := g.repo(t, "middle.git", map[string]string{"nested/kustomization.yaml": fmt.Sprintf("resources:\n- %s//config/default?ref=%s\n", base, sha)})
	root, rootSHA := g.repo(t, "root.git", map[string]string{
		"operand/seed/kustomization.yaml":  fmt.Sprintf("resources:\n- %s//nested?ref=%s\n- ../../additions\n", middle, middleSHA),
		"operand/shoot/kustomization.yaml": "resources: [../../additions]\n",
		"additions/kustomization.yaml":     "resources: [cm.yaml]\ncommonAnnotations:\n  dual-deployment-operator.cc.sap/origin: additions\n", "additions/cm.yaml": integrationCM("addition"),
	})
	scratch, cache := t.TempDir(), mustCache(t, 16)
	rr := NewGitResolver(scratch)
	var sawBaseScratch atomic.Bool
	hook := func(req *http.Request) {
		if req.Method != http.MethodPost || !strings.HasPrefix(req.URL.Path, "/base.git/") {
			return
		}
		entries, err := os.ReadDir(scratch)
		if err != nil {
			return
		}
		checkouts, stages := 0, 0
		for _, entry := range entries {
			switch {
			case strings.HasPrefix(entry.Name(), "ddo-kustomize-stage-"):
				stages++
			case strings.HasPrefix(entry.Name(), "ddo-kustomize-"):
				checkouts++
			}
		}
		sawBaseScratch.Store(stages == 1 && checkouts >= 2)
	}
	g.afterRequest.Store(&hook)
	spec := &v1alpha1.KustomizeSource{URL: root + "//operand?ref=" + rootSHA, SeedPath: "./seed", ShootPath: "shoot"}
	s := integrationSource(t, spec, Deps{RootResolver: rr, RenderCache: cache})
	got := integrationRender(t, s, ModeSeed, "target")
	want, err := manifest.Parse([]byte(deployment+"\n---\n"+integrationCM("addition")+"  annotations:\n    dual-deployment-operator.cc.sap/origin: additions\n"), manifest.OriginUpstream)
	if err != nil {
		t.Fatal(err)
	}
	manifest.ApplyNamespace(want, "target")
	if integrationManifestSet(t, got) != integrationManifestSet(t, want) {
		t.Fatalf("ipam-capi-shaped expected manifest set differs:\ngot %s\nwant %s", integrationManifestSet(t, got), integrationManifestSet(t, want))
	}
	if len(got) != 2 || kinds(got)["Deployment/ipam-capi-controller"] != manifest.OriginUpstream || kinds(got)["ConfigMap/addition"] != manifest.OriginAdditions {
		t.Fatalf("unexpected ipam-capi-shaped fixture render: %v", kinds(got))
	}
	for _, m := range got {
		if m.Unstructured.GetNamespace() != "target" {
			t.Fatal("target namespace lost")
		}
	}
	if g.fetches.Load() != 3 {
		t.Fatalf("fetches=%d, want root + middle + base", g.fetches.Load())
	}
	if !sawBaseScratch.Load() {
		t.Fatal("root, base and preparation stage were not observed together under configured scratch")
	}
	assertScratchEmpty(t, scratch)
	// All refs are SHAs. A prepared cache hit needs no upstream traffic.
	requests := g.requests.Load()
	for _, key := range cache.lru.Keys() {
		marker := cloneManifests(got)
		marker[0].Unstructured.SetName("cached-marker")
		cache.put(key, marker)
	}
	hit := integrationRender(t, s, ModeSeed, "target")
	if hit[0].Unstructured.GetName() != "cached-marker" {
		t.Fatal("render-cache hit did not skip krusty")
	}
	if g.requests.Load() != requests {
		t.Fatal("localized SHA-pinned render made upstream requests after acquisition")
	}
	assertScratchEmpty(t, scratch)
	// A distinct root/operand still shares the middle/base snapshots.
	other, otherSHA := g.repo(t, "other.git", map[string]string{"kustomization.yaml": fmt.Sprintf("resources:\n- %s//nested?ref=%s\nnamePrefix: other-\n", middle, middleSHA)})
	otherSource := integrationSource(t, &v1alpha1.KustomizeSource{URL: other + "?ref=" + otherSHA}, Deps{RootResolver: rr, RenderCache: cache})
	otherGot := integrationRender(t, otherSource, ModeSeed, "other")
	if len(otherGot) != 1 || otherGot[0].Unstructured.GetName() != "other-ipam-capi-controller" || g.fetches.Load() != 4 {
		t.Fatal("shared-base snapshot reuse or operand isolation failed")
	}
	// The same source without a render cache must still prepare and render offline.
	g.deny.Store(true)
	uncached := integrationSource(t, spec, Deps{RootResolver: rr})
	if len(integrationRender(t, uncached, ModeSeed, "target")) != 2 || g.requests.Load() != requests+2 {
		t.Fatal("nil render cache skipped safe localization or fetched resident snapshots")
	}
	shoot := integrationRender(t, s, ModeShoot, "shoot")
	if len(shoot) != 1 || shoot[0].Unstructured.GetName() != "addition" {
		t.Fatal("mode selection/cache isolation failed")
	}
	assertScratchEmpty(t, scratch)
}

func TestKustomizeIntegrationMovedTagAndAuthorization(t *testing.T) {
	g := newIntegrationGit(t)
	base, _ := g.repo(t, "base.git", map[string]string{"selected/kustomization.yaml": "resources: [cm.yaml]\n", "selected/cm.yaml": integrationCM("old")})
	// Publish another commit into this same repository and move its tag later.
	work := t.TempDir()
	r, err := git.PlainClone(work, false, &git.CloneOptions{URL: "file://" + filepath.Join(g.parent, "base.git")})
	if err != nil {
		t.Fatal(err)
	}
	integrationFiles(t, work, map[string]string{"selected/cm.yaml": integrationCM("new")})
	w, err := r.Worktree()
	if err != nil {
		t.Fatal(err)
	}
	if _, err := w.Add("."); err != nil {
		t.Fatal(err)
	}
	newSHA, err := w.Commit("move", &git.CommitOptions{Author: &object.Signature{Name: "test", Email: "test@example.com"}})
	if err != nil {
		t.Fatal(err)
	}
	if err := r.Push(&git.PushOptions{}); err != nil {
		t.Fatal(err)
	}
	root, rootSHA := g.repo(t, "root.git", map[string]string{"kustomization.yaml": fmt.Sprintf("resources:\n- %s//selected?ref=moving\n", base)})
	scheme := runtime.NewScheme()
	if err := corev1.AddToScheme(scheme); err != nil {
		t.Fatal(err)
	}
	secret := &corev1.Secret{ObjectMeta: metav1.ObjectMeta{Name: "auth", Namespace: "ns"}, Data: map[string][]byte{"username": []byte("git"), "password": []byte("good")}}
	cl := fake.NewClientBuilder().WithScheme(scheme).WithObjects(secret).Build()
	scratch, cache := t.TempDir(), mustCache(t, 16)
	deps := Deps{RootResolver: NewGitResolver(scratch), RenderCache: cache, CredentialResolver: &CredentialResolver{Client: cl, Namespace: "ns"}}
	spec := &v1alpha1.KustomizeSource{URL: root + "?ref=" + rootSHA, AuthSecretRef: &v1alpha1.SecretReference{Name: "auth"}}
	s := integrationSource(t, spec, deps)
	if got := integrationRender(t, s, ModeSeed, "ns"); got[0].Unstructured.GetName() != "old" {
		t.Fatal("initial base content wrong")
	}
	before := g.requests.Load()
	if got := integrationRender(t, s, ModeSeed, "ns"); got[0].Unstructured.GetName() != "old" || g.requests.Load() <= before {
		t.Fatal("cache hit skipped mutable transitive ref resolution")
	}
	g.moveTag(t, "base.git", newSHA.String())
	if got := integrationRender(t, s, ModeSeed, "ns"); got[0].Unstructured.GetName() != "new" {
		t.Fatal("root-stable render served stale transitive tag")
	}
	if cache.lru.Len() != 2 {
		t.Fatalf("render cache entries=%d want 2", cache.lru.Len())
	}
	fetches := g.fetches.Load()
	secret.Data["password"] = []byte("rotated")
	if err := cl.Update(context.Background(), secret); err != nil {
		t.Fatal(err)
	}
	if got := integrationRender(t, s, ModeSeed, "ns"); got[0].Unstructured.GetName() != "new" || g.fetches.Load() != fetches+2 || cache.lru.Len() != 3 {
		t.Fatal("successful credential rotation reused old authorization identity")
	}
	secret.Data["password"] = []byte("wrong")
	if err := cl.Update(context.Background(), secret); err != nil {
		t.Fatal(err)
	}
	if _, err := s.Render(context.Background(), ModeSeed, "ns"); err == nil {
		t.Fatal("rotated denied credentials read cached private content")
	}
	if err := cl.Delete(context.Background(), secret); err != nil {
		t.Fatal(err)
	}
	if _, err := s.Render(context.Background(), ModeSeed, "ns"); err == nil {
		t.Fatal("removed Secret read cached private content")
	}
	// Removing authSecretRef changes identity even when the server permits public access.
	anonSpec := *spec
	anonSpec.AuthSecretRef = nil
	anon := integrationSource(t, &anonSpec, deps)
	fetches = g.fetches.Load()
	if got := integrationRender(t, anon, ModeSeed, "ns"); got[0].Unstructured.GetName() != "new" || g.fetches.Load() != fetches+2 || cache.lru.Len() != 4 {
		t.Fatal("anonymous context reused private cache entries")
	}
	g.deny.Store(true)
	if _, err := anon.Render(context.Background(), ModeSeed, "ns"); err == nil {
		t.Fatal("cache hit skipped transitive authorization/ref check")
	}
	assertScratchEmpty(t, scratch)
}

func TestKustomizeIntegrationIdentityAssociations(t *testing.T) {
	g := newIntegrationGit(t)
	a, old := g.repo(t, "a.git", map[string]string{"kustomization.yaml": "resources: [cm.yaml]\n", "cm.yaml": integrationCM("old")})
	next := g.advance(t, "a.git", map[string]string{"cm.yaml": integrationCM("new")})
	if _, err := git.PlainClone(filepath.Join(g.parent, "b.git"), true, &git.CloneOptions{URL: "file://" + filepath.Join(g.parent, "a.git")}); err != nil {
		t.Fatal(err)
	}
	b := g.server.URL + "/b.git"
	g.moveTag(t, "b.git", next)
	root, sha := g.repo(t, "root.git", map[string]string{
		"kustomization.yaml":   "resources: [a, b]\n",
		"a/kustomization.yaml": fmt.Sprintf("namePrefix: a-\nresources:\n- %s?ref=moving\n", a),
		"b/kustomization.yaml": fmt.Sprintf("namePrefix: b-\nresources:\n- %s?ref=moving\n", b),
	})
	scratch, cache := t.TempDir(), mustCache(t, 8)
	rr := NewGitResolver(scratch)
	s := integrationSource(t, &v1alpha1.KustomizeSource{URL: root + "?ref=" + sha}, Deps{RootResolver: rr, RenderCache: cache})
	first := kinds(integrationRender(t, s, ModeSeed, "ns"))
	if len(first) != 2 || first["ConfigMap/a-old"] != manifest.OriginUpstream || first["ConfigMap/b-new"] != manifest.OriginUpstream {
		t.Fatalf("initial associations: %v", first)
	}
	g.moveTag(t, "a.git", next)
	g.moveTag(t, "b.git", old)
	second := kinds(integrationRender(t, s, ModeSeed, "ns"))
	if len(second) != 2 || second["ConfigMap/a-new"] != manifest.OriginUpstream || second["ConfigMap/b-old"] != manifest.OriginUpstream || cache.lru.Len() != 2 {
		t.Fatalf("SHA set unchanged but association swap was missed: %v", second)
	}
	// Same SHA and hostname in another root repository must not reuse a render.
	if _, err := git.PlainClone(filepath.Join(g.parent, "other.git"), true, &git.CloneOptions{URL: "file://" + filepath.Join(g.parent, "root.git")}); err != nil {
		t.Fatal(err)
	}
	other := integrationSource(t, &v1alpha1.KustomizeSource{URL: g.server.URL + "/other.git?ref=" + sha}, Deps{RootResolver: rr, RenderCache: cache})
	integrationRender(t, other, ModeSeed, "ns")
	if cache.lru.Len() != 3 {
		t.Fatal("same-SHA repositories shared a render identity")
	}
	assertScratchEmpty(t, scratch)
}

func TestKustomizeIntegrationRootMovesDuringAcquisition(t *testing.T) {
	g := newIntegrationGit(t)
	root, old := g.repo(t, "root.git", map[string]string{"kustomization.yaml": "resources: [cm.yaml]\n", "cm.yaml": integrationCM("old")})
	next := g.advance(t, "root.git", map[string]string{"cm.yaml": integrationCM("new")})
	var changed atomic.Bool
	hook := func(r *http.Request) {
		if r.Method == http.MethodGet && changed.CompareAndSwap(false, true) {
			g.moveTag(t, "root.git", next)
		}
	}
	g.afterRequest.Store(&hook)
	scratch, cache := t.TempDir(), mustCache(t, 8)
	s := integrationSource(t, &v1alpha1.KustomizeSource{URL: root + "?ref=moving"}, Deps{RootResolver: NewGitResolver(scratch), RenderCache: cache})
	if got := integrationRender(t, s, ModeSeed, "ns"); got[0].Unstructured.GetName() != "old" {
		t.Fatal("render fetched second mutable root rather than the resolved SHA " + old)
	}
	if got := integrationRender(t, s, ModeSeed, "ns"); got[0].Unstructured.GetName() != "new" {
		t.Fatal("next render failed to resolve moved root")
	}
	if cache.lru.Len() != 2 {
		t.Fatal("resolved root identities not reflected in render cache")
	}
	assertScratchEmpty(t, scratch)
}

func TestKustomizeIntegrationPreflightBeforeResidentRender(t *testing.T) {
	g := newIntegrationGit(t)
	root, sha := g.repo(t, "root.git", map[string]string{"kustomization.yaml": "resources: [http://127.0.0.1:1/no.yaml]\n"})
	scratch, cache := t.TempDir(), mustCache(t, 4)
	rr := NewGitResolver(scratch).(*gitResolver)
	checkout, err := rr.acquire(context.Background(), root, sha)
	if err != nil {
		t.Fatal(err)
	}
	checkout.Cleanup()
	spec := &v1alpha1.KustomizeSource{URL: root + "?ref=" + sha}
	key, err := kustomizeCacheKey(spec, checkout.Identity, nil, ModeSeed, "ns")
	if err != nil {
		t.Fatal(err)
	}
	cache.put(key, nil)
	s := integrationSource(t, spec, Deps{RootResolver: rr, RenderCache: cache})
	if _, err := s.Render(context.Background(), ModeSeed, "ns"); err == nil || !strings.Contains(err.Error(), "prepare kustomize") {
		t.Fatalf("local-only resident render bypassed preflight: %v", err)
	}
	assertScratchEmpty(t, scratch)
}

func TestKustomizeIntegrationCancellationDuringPreparation(t *testing.T) {
	g := newIntegrationGit(t)
	base, baseSHA := g.repo(t, "base.git", map[string]string{"kustomization.yaml": "resources: [cm.yaml]\n", "cm.yaml": integrationCM("base")})
	root, sha := g.repo(t, "root.git", map[string]string{"kustomization.yaml": fmt.Sprintf("resources:\n- %s?ref=%s\n", base, baseSHA)})
	scratch, cache := t.TempDir(), mustCache(t, 4)
	rr := NewGitResolver(scratch).(*gitResolver)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	calls := 0
	rr.resolve = func(context.Context, gitOrigin) (creds, error) {
		calls++
		if calls == 2 {
			entries, err := os.ReadDir(scratch)
			if err != nil {
				t.Fatal(err)
			}
			var rootPresent, stagePresent bool
			for _, entry := range entries {
				rootPresent = rootPresent || strings.HasPrefix(entry.Name(), "ddo-kustomize-") && !strings.HasPrefix(entry.Name(), "ddo-kustomize-stage-")
				stagePresent = stagePresent || strings.HasPrefix(entry.Name(), "ddo-kustomize-stage-")
			}
			if !rootPresent || !stagePresent {
				t.Fatalf("root/stage not under configured scratch: %v", entries)
			}
			cancel()
		}
		return creds{}, nil
	}
	s := integrationSource(t, &v1alpha1.KustomizeSource{URL: root + "?ref=" + sha}, Deps{RootResolver: rr, RenderCache: cache})
	if _, err := s.Render(ctx, ModeSeed, "ns"); !errors.Is(err, context.Canceled) {
		t.Fatalf("preparation cancellation lost: %v", err)
	}
	if calls != 2 || cache.lru.Len() != 0 {
		t.Fatal("did not cancel during transitive preparation")
	}
	assertScratchEmpty(t, scratch)
}

func TestKustomizeIntegrationShorthandCredentials(t *testing.T) {
	scheme := runtime.NewScheme()
	if err := corev1.AddToScheme(scheme); err != nil {
		t.Fatal(err)
	}
	secret := &corev1.Secret{ObjectMeta: metav1.ObjectMeta{Name: "auth", Namespace: "ns"}, Data: map[string][]byte{"token": []byte("test-token")}}
	cl := fake.NewClientBuilder().WithScheme(scheme).WithObjects(secret).Build()
	s := integrationSource(t, &v1alpha1.KustomizeSource{URL: "github.com/org/repo/subdir?ref=v1", AuthSecretRef: &v1alpha1.SecretReference{Name: "auth"}}, Deps{RootResolver: NewGitResolver(t.TempDir()), CredentialResolver: &CredentialResolver{Client: cl, Namespace: "ns"}})
	rr := s.(*kustomizeSource).resolver.(*gitResolver)
	auth, err := rr.authFor(context.Background(), "https://github.com/org/repo")
	if err != nil || auth == nil {
		t.Fatalf("normalized shorthand lost root-origin credentials: %v", err)
	}
}

func TestKustomizeIntegrationOtherOriginIsAnonymous(t *testing.T) {
	rootServer, baseServer := newIntegrationGit(t), newIntegrationGit(t)
	base, baseSHA := baseServer.repo(t, "base.git", map[string]string{"kustomization.yaml": "resources: [cm.yaml]\n", "cm.yaml": integrationCM("public-base")})
	root, sha := rootServer.repo(t, "root.git", map[string]string{"kustomization.yaml": fmt.Sprintf("resources:\n- %s?ref=%s\n", base, baseSHA)})
	scheme := runtime.NewScheme()
	if err := corev1.AddToScheme(scheme); err != nil {
		t.Fatal(err)
	}
	secret := &corev1.Secret{ObjectMeta: metav1.ObjectMeta{Name: "auth", Namespace: "ns"}, Data: map[string][]byte{"username": []byte("git"), "password": []byte("good")}}
	cl := fake.NewClientBuilder().WithScheme(scheme).WithObjects(secret).Build()
	scratch := t.TempDir()
	s := integrationSource(t, &v1alpha1.KustomizeSource{URL: root + "?ref=" + sha, AuthSecretRef: &v1alpha1.SecretReference{Name: "auth"}}, Deps{RootResolver: NewGitResolver(scratch), RenderCache: mustCache(t, 4), CredentialResolver: &CredentialResolver{Client: cl, Namespace: "ns"}})
	if got := integrationRender(t, s, ModeSeed, "ns"); len(got) != 1 || got[0].Unstructured.GetName() != "public-base" {
		t.Fatal("cross-origin base did not render")
	}
	if rootServer.authorization.Load() == 0 || baseServer.authorization.Load() != 0 {
		t.Fatal("root credentials crossed HTTPS effective-port boundary")
	}
	assertScratchEmpty(t, scratch)
}

func TestKustomizeIntegrationRootGrammarAndPaths(t *testing.T) {
	g := newIntegrationGit(t)
	root, sha := g.repo(t, "root.git", map[string]string{"kustomization.yaml": "resources: [cm.yaml]\n", "cm.yaml": integrationCM("local")})
	for _, suffix := range []string{"&timeout=1", "&submodules=false", "&ref=other"} {
		scratch := t.TempDir()
		s := integrationSource(t, &v1alpha1.KustomizeSource{URL: root + "?ref=" + sha + suffix}, Deps{RootResolver: NewGitResolver(scratch)})
		if _, err := s.Render(context.Background(), ModeSeed, "ns"); err == nil || !strings.Contains(err.Error(), "no other URL options") {
			t.Fatalf("runtime silently changed query semantics for %s: %v", suffix, err)
		}
		assertScratchEmpty(t, scratch)
	}
	for _, selected := range []string{"../outside", "/absolute", "https://example.invalid/path", `bad\path`} {
		scratch := t.TempDir()
		s := integrationSource(t, &v1alpha1.KustomizeSource{URL: root + "?ref=" + sha, SeedPath: selected}, Deps{RootResolver: NewGitResolver(scratch)})
		if _, err := s.Render(context.Background(), ModeSeed, "ns"); err == nil {
			t.Fatalf("accepted mode path %s", selected)
		}
		assertScratchEmpty(t, scratch)
	}
	if g.requests.Load() != 0 {
		t.Fatal("invalid root syntax/path reached acquisition")
	}
}

func TestKustomizeIntegrationProductionRejection(t *testing.T) {
	var requests atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		requests.Add(1)
		http.Error(w, "no", http.StatusForbidden)
	}))
	defer srv.Close()
	for _, raw := range []string{srv.URL + "/repo.git?ref=v1", "file:///not-a-production-repo?ref=v1", "ssh://127.0.0.1:1/repo?ref=v1", "https://127.0.0.1:1/repo.git?ref=v1&timeout=1", "https://127.0.0.1:1/repo.git?ref=v1&submodules=false"} {
		scratch := t.TempDir()
		s := integrationSource(t, &v1alpha1.KustomizeSource{URL: raw}, Deps{RootResolver: NewGitResolver(scratch), RenderCache: mustCache(t, 4)})
		if _, err := s.Render(context.Background(), ModeSeed, ""); err == nil {
			t.Errorf("accepted unsupported authoring %s", raw)
		}
		assertScratchEmpty(t, scratch)
	}
	if requests.Load() != 0 {
		t.Fatal("unsupported root transport reached acquisition")
	}
}

func TestKustomizeIntegrationCleanup(t *testing.T) {
	g := newIntegrationGit(t)
	for name, files := range map[string]map[string]string{
		"preflight": {"kustomization.yaml": "resources: [http://127.0.0.1:1/no.yaml]\n"},
		"build":     {"kustomization.yaml": "resources: [one.yaml, two.yaml]\n", "one.yaml": integrationCM("duplicate"), "two.yaml": integrationCM("duplicate")},
		"base":      {"kustomization.yaml": fmt.Sprintf("resources:\n- %s/missing.git?ref=moving\n", g.server.URL)},
	} {
		t.Run(name, func(t *testing.T) {
			url, sha := g.repo(t, name+".git", files)
			scratch, cache := t.TempDir(), mustCache(t, 4)
			s := integrationSource(t, &v1alpha1.KustomizeSource{URL: url + "?ref=" + sha}, Deps{RootResolver: NewGitResolver(scratch), RenderCache: cache})
			if _, err := s.Render(context.Background(), ModeSeed, ""); err == nil {
				t.Fatal("expected failure")
			}
			assertScratchEmpty(t, scratch)
			if cache.lru.Len() != 0 {
				t.Fatal("cached failed render")
			}
			ctx, cancel := context.WithCancel(context.Background())
			cancel()
			if _, err := s.Render(ctx, ModeSeed, ""); !errors.Is(err, context.Canceled) {
				t.Fatalf("cancellation lost: %v", err)
			}
			assertScratchEmpty(t, scratch)
		})
	}
}
