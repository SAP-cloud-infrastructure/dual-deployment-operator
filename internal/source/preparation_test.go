// SPDX-FileCopyrightText: 2026 SAP SE or an SAP affiliate company
// SPDX-License-Identifier: Apache-2.0

package source

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"reflect"
	"runtime"
	"strings"
	"sync/atomic"
	"testing"

	"sigs.k8s.io/kustomize/api/konfig"
	"sigs.k8s.io/kustomize/api/krusty"
	"sigs.k8s.io/kustomize/api/types"
	"sigs.k8s.io/kustomize/kyaml/filesys"
)

func TestPreparationClassify(t *testing.T) {
	for _, tt := range []struct {
		input, repo, sub, ref string
		bad                   bool
	}{
		{"https://github.com/org/repo//config/default?ref=v1", "https://github.com/org/repo", "config/default", "v1", false},
		{"github.com/org/repo//base?ref=main", "https://github.com/org/repo", "base", "main", false},
		{"github.com/org/repo/base?ref=main", "https://github.com/org/repo", "base", "main", false},
		{"https://example.com/org/repo.git?ref=abc", "https://example.com/org/repo.git", ".", "abc", false},
		{"https://example.com/org/repo//a/../base?ref=refs%2Fheads%2Fmain", "https://example.com/org/repo", "base", "refs/heads/main", false},
		{"../local", "", "", "", false},
		{"data/https://literal.yaml", "", "", "", false},
		{"./https://literal.yaml", "", "", "", false},
		{"https://example.com/file.yaml", "", "", "", true},
		{"http://example.com/file.yaml", "", "", "", true},
		{"ssh://git@example.com/org/repo?ref=v1", "", "", "", true},
		{"git@example.com:org/repo?ref=v1", "", "", "", true},
		{"github.com:org/repo?ref=v1", "", "", "", true},
		{"git::https://example.com/org/repo?ref=v1", "", "", "", true},
		{"file:///repo?ref=v1", "", "", "", true},
		{"https://example.com/org/repo?ref=", "", "", "", true},
		{"https://example.com/org/repo?ref=v1&ref=v2", "", "", "", true},
		{"https://example.com/org/repo?ref=v1&timeout=1", "", "", "", true},
		{"https://example.com/org/repo//../../outside?ref=v1", "", "", "", true},
		{"https://user:secret@example.com/org/repo?ref=v1", "", "", "", true},
		{"https://example.com/org/repo?ref=v1#fragment", "", "", "", true},
	} {
		t.Run(tt.input, func(t *testing.T) {
			got, remote, err := classifyGitBase(tt.input)
			if (err != nil) != tt.bad {
				t.Fatalf("error = %v, want bad=%v", err, tt.bad)
			}
			if tt.bad {
				return
			}
			if remote != (tt.repo != "") || got.Repository != tt.repo || got.SubPath != tt.sub || got.Ref != tt.ref {
				t.Fatalf("got %+v remote=%v", got, remote)
			}
		})
	}
}

func prepFixture(t *testing.T, files map[string]string) string {
	t.Helper()
	dir := t.TempDir()
	for name, data := range files {
		p := filepath.Join(dir, name)
		if err := os.MkdirAll(filepath.Dir(p), 0700); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(p, []byte(data), 0600); err != nil {
			t.Fatal(err)
		}
	}
	return dir
}

const prepCM = "apiVersion: v1\nkind: ConfigMap\nmetadata:\n  name: example\ndata:\n  hello: world\n"

func prepRender(t *testing.T, root string) string {
	t.Helper()
	opts := krusty.MakeDefaultOptions()
	opts.LoadRestrictions = types.LoadRestrictionsNone
	opts.PluginConfig = types.DisabledPluginConfig()
	rm, err := krusty.MakeKustomizer(opts).Run(filesys.MakeFsOnDisk(), root)
	if err != nil {
		t.Fatal(err)
	}
	b, err := rm.AsYaml()
	if err != nil {
		t.Fatal(err)
	}
	return string(b)
}

func TestPreparationStagesDepthFirstAndPreservesSources(t *testing.T) {
	rootText := "resources:\n- local\n- https://example.com/org/a//overlay?ref=v1\n"
	root := prepFixture(t, map[string]string{"selected/kustomization.yaml": rootText, "selected/local/kustomization.yml": "resources: [cm.yaml]\nnamePrefix: local-\n", "selected/local/cm.yaml": prepCM})
	a := prepFixture(t, map[string]string{"overlay/Kustomization": "bases: [../base]\ncomponents: ['https://example.com/org/b//component?ref=main']\n", "base/kustomization.yaml": "resources: [cm.yaml]\n", "base/cm.yaml": prepCM})
	b := prepFixture(t, map[string]string{"component/kustomization.yml": "apiVersion: kustomize.config.k8s.io/v1alpha1\nkind: Component\nnamePrefix: remote-\n"})
	var order []string
	cleaned := 0
	acquire := func(_ context.Context, ref gitBaseReference) (acquiredBase, error) {
		order = append(order, ref.Repository)
		dir, id := a, "repo-a/auth-public/sha-a"
		if strings.HasSuffix(ref.Repository, "/b") {
			dir, id = b, "repo-b/auth-public/sha-b"
		}
		return acquiredBase{RepositoryDir: dir, SubPath: ref.SubPath, Identity: id, Cleanup: func() { cleaned++ }}, nil
	}
	scratch := t.TempDir()
	p, err := prepareKustomize(context.Background(), acquiredBase{RepositoryDir: root, SubPath: "selected", Identity: "root"}, scratch, acquire)
	if err != nil {
		t.Fatal(err)
	}
	defer p.Cleanup()
	canonicalScratch, err := filepath.EvalSymlinks(scratch)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.HasPrefix(p.Root, canonicalScratch+string(filepath.Separator)) {
		t.Fatalf("outside scratch: %s", p.Root)
	}
	if !reflect.DeepEqual(order, []string{"https://example.com/org/a", "https://example.com/org/b"}) {
		t.Fatal(order)
	}
	if cleaned != 2 || len(p.Identities) != 2 || p.Identities[0].Identity != "repo-a/auth-public/sha-a" {
		t.Fatalf("cleanup=%d identities=%+v", cleaned, p.Identities)
	}
	out := prepRender(t, p.Root)
	if !strings.Contains(out, "name: local-example") || !strings.Contains(out, "name: remote-example") {
		t.Fatal(out)
	}
	original, err := os.ReadFile(filepath.Join(root, "selected/kustomization.yaml"))
	if err != nil || string(original) != rootText {
		t.Fatalf("source changed: %s, %v", original, err)
	}
	p.Cleanup()
	entries, err := os.ReadDir(scratch)
	if err != nil || len(entries) != 0 {
		t.Fatalf("scratch leaked: %v, %v", entries, err)
	}
}

func TestPreparationLocalOnlyAliasesAndRepeatedReferences(t *testing.T) {
	for _, filename := range konfig.RecognizedKustomizationFileNames() {
		t.Run(filename, func(t *testing.T) {
			root := prepFixture(t, map[string]string{filename: "resources: [left, right]\n", "left/kustomization.yaml": "resources: [../shared]\nnamePrefix: left-\n", "right/kustomization.yaml": "resources: [../shared]\nnamePrefix: right-\n", "shared/Kustomization": "resources: [cm.yaml]\n", "shared/cm.yaml": prepCM})
			want := prepRender(t, root)
			p, err := prepareKustomize(context.Background(), acquiredBase{RepositoryDir: root, Identity: "root"}, t.TempDir(), nil)
			if err != nil {
				t.Fatal(err)
			}
			defer p.Cleanup()
			if got := prepRender(t, p.Root); got != want {
				t.Fatalf("output changed:\n%s\nwant:\n%s", got, want)
			}
			if len(p.Identities) != 0 {
				t.Fatal(p.Identities)
			}
		})
	}
}

func TestPreparationBlocksLoadingPathsWithoutHTTP(t *testing.T) {
	var requests atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		requests.Add(1)
		if _, err := w.Write([]byte(prepCM)); err != nil {
			t.Errorf("write fixture response: %v", err)
		}
	}))
	defer server.Close()
	u := server.URL + "/input.yaml"
	fields := []string{
		"resources: [%s]", "components: [%s]", "bases: [%s]",
		"patches: [{path: %s}]", "patchesJson6902: [{path: %s}]", "patchesStrategicMerge: [%s]",
		"openapi: {path: %s}", "configurations: [%s]", "crds: [%s]", "replacements: [{path: %s}]",
		"configMapGenerator: [{name: x, files: [key=%s]}]", "secretGenerator: [{name: x, envs: [%s]}]", "configMapGenerator: [{name: x, env: %s}]",
		"generators: [%s]", "transformers: [%s]", "validators: [%s]",
	}
	for _, field := range fields {
		t.Run(field, func(t *testing.T) {
			root := prepFixture(t, map[string]string{"kustomization.yaml": fmt.Sprintf(field, u)})
			scratch := t.TempDir()
			p, err := prepareKustomize(context.Background(), acquiredBase{RepositoryDir: root, Identity: "root"}, scratch, nil)
			if err == nil {
				p.Cleanup()
				t.Fatal("accepted network load")
			}
			entries, err := os.ReadDir(scratch)
			if err != nil {
				t.Fatal(err)
			}
			if len(entries) != 0 {
				t.Fatal("leaked scratch")
			}
		})
	}
	for _, field := range []string{"generators", "transformers", "validators"} {
		for _, config := range []string{
			"apiVersion: builtin\nkind: ConfigMapGenerator\nmetadata: {name: x}\nfiles: [key=%s]\n",
			"apiVersion: builtin\nkind: SecretGenerator\nmetadata: {name: x}\nenvs: [%s]\n",
			"apiVersion: builtin\nkind: PatchTransformer\nmetadata: {name: x}\npath: %s\n",
			"apiVersion: builtin\nkind: PatchJson6902Transformer\nmetadata: {name: x}\npath: %s\n",
			"apiVersion: builtin\nkind: PatchStrategicMergeTransformer\nmetadata: {name: x}\npaths: [%s]\n",
			"apiVersion: builtin\nkind: ReplacementTransformer\nmetadata: {name: x}\nreplacements: [{path: %s}]\n",
			"apiVersion: builtin\nkind: ValueAddTransformer\nmetadata: {name: x}\ntargetFilePath: %s\n",
		} {
			root := prepFixture(t, map[string]string{"Kustomization": field + ": [plugin.yaml]\n", "plugin.yaml": fmt.Sprintf(config, u)})
			p, err := prepareKustomize(context.Background(), acquiredBase{RepositoryDir: root, Identity: "root"}, t.TempDir(), nil)
			if err == nil {
				p.Cleanup()
				t.Fatalf("accepted %s %s", field, config)
			}
		}
	}
	if requests.Load() != 0 {
		t.Fatalf("made %d requests", requests.Load())
	}
}

func TestPreparationRejectsCyclesEscapesAndMalformed(t *testing.T) {
	for _, text := range []string{
		"resources: [. ]\n", "resources: [../outside]\n", "resources: [/etc/passwd]\n",
		"resources: [cm.yaml]\nunknownLoader: https://example.com/x\n", "resources: [\n",
		"resources: [cm.yaml]\nresources: [other.yaml]\n", "resources: [cm.yaml]\n---\nresources: [other.yaml]\n",
		"helmCharts: [{name: chart, repo: https://example.com}]\n",
		"generators: [plugin-dir]\n",
	} {
		root := prepFixture(t, map[string]string{"kustomization.yaml": text, "cm.yaml": prepCM, "plugin-dir/Kustomization": "resources: []"})
		p, err := prepareKustomize(context.Background(), acquiredBase{RepositoryDir: root, Identity: "root"}, t.TempDir(), nil)
		if err == nil {
			p.Cleanup()
			t.Fatalf("accepted %q", text)
		}
	}
	root := prepFixture(t, map[string]string{"kustomization.yaml": "resources: [escape.yaml]\n"})
	outside := prepFixture(t, map[string]string{"cm.yaml": prepCM})
	if err := os.Symlink(filepath.Join(outside, "cm.yaml"), filepath.Join(root, "escape.yaml")); err != nil {
		t.Fatal(err)
	}
	p, err := prepareKustomize(context.Background(), acquiredBase{RepositoryDir: root, Identity: "root"}, t.TempDir(), nil)
	if err == nil {
		p.Cleanup()
		t.Fatal("accepted symlink escape")
	}
}

func TestPreparationAcquisitionCycleFailureAndCancellation(t *testing.T) {
	root := prepFixture(t, map[string]string{"kustomization.yaml": "resources: ['https://example.com/org/repo?ref=main']\n"})
	for _, mode := range []string{"cycle", "failure", "cancel"} {
		t.Run(mode, func(t *testing.T) {
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			cleaned := false
			acquire := func(context.Context, gitBaseReference) (acquiredBase, error) {
				base := acquiredBase{RepositoryDir: root, Identity: "root", Cleanup: func() { cleaned = true }}
				if mode == "failure" {
					return base, errors.New("fetch failed")
				}
				if mode == "cancel" {
					cancel()
				}
				return base, nil
			}
			scratch := t.TempDir()
			p, err := prepareKustomize(ctx, acquiredBase{RepositoryDir: root, Identity: "root"}, scratch, acquire)
			if err == nil {
				p.Cleanup()
				t.Fatal("unexpected success")
			}
			if !cleaned {
				t.Fatal("acquisition cleanup not called")
			}
			entries, err := os.ReadDir(scratch)
			if err != nil {
				t.Fatal(err)
			}
			if len(entries) != 0 {
				t.Fatal("leaked scratch")
			}
		})
	}
}

func TestPreparationLocalSupplementalOutputAndSymlinks(t *testing.T) {
	root := prepFixture(t, map[string]string{
		"kustomization.yaml":        "resources: [data/https://literal.yaml]\ngenerators: [config/generator.yaml]\ntransformers: [config/value.yaml]\n",
		"data/https://literal.yaml": prepCM,
		"config/generator.yaml":     "apiVersion: builtin\nkind: ConfigMapGenerator\nmetadata: {name: generated}\nfiles: [hello=link.txt]\n",
		"config/value.yaml":         "apiVersion: builtin\nkind: ValueAddTransformer\nmetadata: {name: dirname}\ntargets:\n- fieldPath: metadata/labels/directory\n",
		"content.txt":               "hello from disk",
	})
	if err := os.Symlink("content.txt", filepath.Join(root, "link.txt")); err != nil {
		t.Fatal(err)
	}
	want := prepRender(t, root)
	p, err := prepareKustomize(context.Background(), acquiredBase{RepositoryDir: root, Identity: "root"}, t.TempDir(), nil)
	if err != nil {
		t.Fatal(err)
	}
	defer p.Cleanup()
	if got := prepRender(t, p.Root); got != want {
		t.Fatalf("local-only output changed:\n%s\nwant:\n%s", got, want)
	}
}

func TestPreparationPinnedKustomizeCompatibility(t *testing.T) {
	_, filename, _, ok := runtime.Caller(0)
	if !ok {
		t.Fatal("could not locate go.mod")
	}
	mod, err := os.ReadFile(filepath.Join(filepath.Dir(filename), "../../go.mod"))
	if err != nil {
		t.Fatal(err)
	}
	for _, module := range []string{"sigs.k8s.io/kustomize/api v0.21.1", "sigs.k8s.io/kustomize/kyaml v0.21.1"} {
		if !strings.Contains(string(mod), module+"\n") {
			t.Fatalf("dependency changed: review preflight loading inventory for %s", module)
		}
	}
	wantFields := "TypeMeta MetaData OpenAPI NamePrefix NameSuffix Namespace CommonLabels Labels CommonAnnotations PatchesStrategicMerge PatchesJson6902 Patches Images ImageTags Replacements Replicas Vars SortOptions Resources Components Crds Bases ConfigMapGenerator SecretGenerator HelmGlobals HelmCharts HelmChartInflationGenerator GeneratorOptions Configurations Generators Transformers Validators BuildMetadata"
	typ := reflect.TypeFor[types.Kustomization]()
	var fields []string
	for field := range typ.Fields() {
		fields = append(fields, field.Name)
	}
	if strings.Join(fields, " ") != wantFields {
		t.Fatalf("typed loading surface changed: %v", fields)
	}
	if !reflect.DeepEqual(konfig.RecognizedKustomizationFileNames(), []string{"kustomization.yaml", "kustomization.yml", "Kustomization"}) {
		t.Fatal("filename compatibility changed")
	}
	opts := krusty.MakeDefaultOptions()
	if opts.PluginConfig.PluginRestrictions != types.PluginRestrictionsBuiltinsOnly || opts.PluginConfig.HelmConfig.Enabled {
		t.Fatal("default execution restrictions changed")
	}
}

func TestPreparationDeterministicRepeatedRemoteAndOfflineRender(t *testing.T) {
	var requests atomic.Int32
	server := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		requests.Add(1)
		if _, err := w.Write([]byte(prepCM)); err != nil {
			t.Errorf("write fixture response: %v", err)
		}
	}))
	defer server.Close()
	url := server.URL + "/org/repo//base?ref=main"
	root := prepFixture(t, map[string]string{
		"kustomization.yaml":      "resources: [left, right]\n",
		"left/Kustomization":      fmt.Sprintf("resources: [%q]\nnamePrefix: left-\n", url),
		"right/kustomization.yml": fmt.Sprintf("resources: [%q]\nnamePrefix: right-\n", url),
	})
	base := prepFixture(t, map[string]string{"base/kustomization.yaml": "resources: [../shared/cm.yaml]\n", "shared/cm.yaml": prepCM})
	acquire := func(_ context.Context, ref gitBaseReference) (acquiredBase, error) {
		return acquiredBase{RepositoryDir: base, SubPath: ref.SubPath, Identity: "repository/auth/sha"}, nil
	}
	var prior string
	for i := range 2 {
		p, err := prepareKustomize(context.Background(), acquiredBase{RepositoryDir: root, Identity: "root"}, t.TempDir(), acquire)
		if err != nil {
			t.Fatal(err)
		}
		defer p.Cleanup()
		b, err := os.ReadFile(filepath.Join(p.Root, "left/Kustomization"))
		if err != nil {
			t.Fatal(err)
		}
		if i > 0 && string(b) != prior {
			t.Fatalf("nondeterministic rewrite: %s versus %s", b, prior)
		}
		prior = string(b)
		if len(p.Identities) != 2 || p.Identities[0] != p.Identities[1] {
			t.Fatalf("repeated references not associated: %+v", p.Identities)
		}
		out := prepRender(t, p.Root)
		if !strings.Contains(out, "name: left-example") || !strings.Contains(out, "name: right-example") {
			t.Fatal(out)
		}
	}
	if requests.Load() != 0 {
		t.Fatalf("krusty made %d requests", requests.Load())
	}
}

func TestPreparationNestedPreflightAndAliasesFailClosed(t *testing.T) {
	for _, files := range []map[string]string{
		{"kustomization.yaml": "resources: [nested]\n", "nested/Kustomization": "configMapGenerator: [{name: x, files: ['https://example.com/data']}]\n"},
		{"kustomization.yaml": "resources: [nested]\n", "nested/kustomization.yml": "resources: []\n", "nested/Kustomization": "resources: []\n"},
		{"kustomization.yaml": "transformers: [plugin.yaml]\n", "plugin.yaml": "apiVersion: example.com/v1\nkind: NetworkPlugin\nmetadata: {name: bad}\n"},
		{"kustomization.yaml": "transformers: [plugin.yaml]\n", "plugin.yaml": "apiVersion: builtin\nkind: UnreviewedBuiltin\nmetadata: {name: bad}\n"},
		{"kustomization.yaml": "transformers:\n- |\n  apiVersion: builtin\n  kind: PatchTransformer\n  metadata: {name: patch}\n  path: https://example.com/patch\n"},
	} {
		root := prepFixture(t, files)
		p, err := prepareKustomize(context.Background(), acquiredBase{RepositoryDir: root, Identity: "root"}, t.TempDir(), nil)
		if err == nil {
			p.Cleanup()
			t.Fatalf("accepted unsafe tree: %v", files)
		}
	}
}

func TestPreparationScratchFallbackAndPreCancelled(t *testing.T) {
	root := prepFixture(t, map[string]string{"kustomization.yaml": "resources: [cm.yaml]\n", "cm.yaml": prepCM})
	p, err := prepareKustomize(context.Background(), acquiredBase{RepositoryDir: root, Identity: "root"}, "", nil)
	if err != nil {
		t.Fatal(err)
	}
	defer p.Cleanup()
	temp, err := filepath.EvalSymlinks(os.TempDir()) //nolint:usetesting // Assert the real empty-scratch fallback, not a test-owned directory.
	if err != nil {
		t.Fatal(err)
	}
	if !preparationWithin(temp, p.Root) {
		t.Fatal("empty scratch did not use os.TempDir")
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	scratch := t.TempDir()
	if p, err := prepareKustomize(ctx, acquiredBase{RepositoryDir: root, Identity: "root"}, scratch, nil); err == nil {
		p.Cleanup()
		t.Fatal("accepted cancelled context")
	}
	entries, err := os.ReadDir(scratch)
	if err != nil || len(entries) != 0 {
		t.Fatalf("cancelled preparation leaked: %v %v", entries, err)
	}
}

func TestPreparationRequiresExplicitRepositoryBoundary(t *testing.T) {
	root := prepFixture(t, map[string]string{"kustomization.yaml": "resources: [cm.yaml]\n", "cm.yaml": prepCM})
	t.Chdir(root)
	p, err := prepareKustomize(context.Background(), acquiredBase{Identity: "root"}, t.TempDir(), nil)
	if err == nil {
		p.Cleanup()
		t.Fatal("missing repository boundary silently used process working directory")
	}
}

func TestPreparationInlineGenerators(t *testing.T) {
	var requests atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		requests.Add(1)
		if _, err := w.Write([]byte("hello=world\n")); err != nil {
			t.Errorf("write fixture response: %v", err)
		}
	}))
	defer server.Close()
	for _, kind := range []string{"ConfigMapGenerator", "SecretGenerator"} {
		t.Run(kind, func(t *testing.T) {
			config := "apiVersion: builtin\nkind: " + kind + "\nmetadata: {name: inline}\nliterals: [hello=world]\n"
			root := prepFixture(t, map[string]string{"kustomization.yaml": fmt.Sprintf("generators: [%q]\n", config)})
			want := prepRender(t, root)
			p, err := prepareKustomize(context.Background(), acquiredBase{RepositoryDir: root, Identity: "root"}, t.TempDir(), nil)
			if err != nil {
				t.Fatal(err)
			}
			defer p.Cleanup()
			if got := prepRender(t, p.Root); got != want {
				t.Fatalf("inline generator output changed:\n%s\nwant:\n%s", got, want)
			}
		})
		for _, input := range []string{"files: [key=" + server.URL + "/input]", "envs: [" + server.URL + "/input]", "env: " + server.URL + "/input"} {
			t.Run(kind+"/"+input, func(t *testing.T) {
				config := "apiVersion: builtin\nkind: " + kind + "\nmetadata: {name: inline}\n" + input + "\n"
				root := prepFixture(t, map[string]string{"kustomization.yaml": fmt.Sprintf("generators: [%q]\n", config)})
				p, err := prepareKustomize(context.Background(), acquiredBase{RepositoryDir: root, Identity: "root"}, t.TempDir(), nil)
				if err == nil {
					p.Cleanup()
					t.Fatal("accepted remote input in inline generator")
				}
			})
		}
	}
	if requests.Load() != 0 {
		t.Fatalf("inline generators made %d requests", requests.Load())
	}
}

func TestPreparationReachableKustomizationSymlinkTarget(t *testing.T) {
	for _, order := range []string{"left, right", "right, left"} {
		t.Run(order, func(t *testing.T) {
			shared := "resources: ['https://example.com/org/base//selected?ref=main']\n"
			files := map[string]string{
				"kustomization.yaml":        "resources: [" + order + "]\n",
				"left/kustomization.yaml":   "resources: [a]\nnamePrefix: left-\n",
				"left/a/kustomization.yaml": shared,
				"right/kustomization.yaml":  "resources: [deep/b]\nnamePrefix: right-\n",
			}
			root := prepFixture(t, files)
			alias := filepath.Join(root, "right/deep/b/kustomization.yaml")
			const link = "../../../left/a/kustomization.yaml"
			if err := os.MkdirAll(filepath.Dir(alias), 0700); err != nil {
				t.Fatal(err)
			}
			if err := os.Symlink(link, alias); err != nil {
				t.Fatal(err)
			}
			baseFiles := map[string]string{"selected/kustomization.yaml": "resources: [cm.yaml]\n", "selected/cm.yaml": prepCM}
			base := prepFixture(t, baseFiles)
			acquire := func(_ context.Context, ref gitBaseReference) (acquiredBase, error) {
				return acquiredBase{RepositoryDir: base, SubPath: ref.SubPath, Identity: "base/auth/sha"}, nil
			}
			p, err := prepareKustomize(context.Background(), acquiredBase{RepositoryDir: root, Identity: "root"}, t.TempDir(), acquire)
			if err != nil {
				t.Fatal(err)
			}
			defer p.Cleanup()
			wantRoot := prepFixture(t, map[string]string{
				"kustomization.yaml": "resources: [" + strings.NewReplacer("left", "left.yaml", "right", "right.yaml").Replace(order) + "]\n",
				"left.yaml":          strings.Replace(prepCM, "name: example", "name: left-example", 1),
				"right.yaml":         strings.Replace(prepCM, "name: example", "name: right-example", 1),
			})
			if got, want := prepRender(t, p.Root), prepRender(t, wantRoot); got != want {
				t.Fatalf("expected exactly two prefixed ConfigMaps:\ngot %s\nwant %s", got, want)
			}
			if len(p.Identities) != 2 || p.Identities[0] != p.Identities[1] {
				t.Fatalf("lost alias reference associations: %+v", p.Identities)
			}
			for dir, originals := range map[string]map[string]string{root: files, base: baseFiles} {
				for name, want := range originals {
					got, err := os.ReadFile(filepath.Join(dir, name))
					if err != nil || string(got) != want {
						t.Fatalf("original %s changed: %s, %v", name, got, err)
					}
				}
			}
			if got, err := os.Readlink(alias); err != nil || got != link {
				t.Fatalf("original alias changed: %q, %v", got, err)
			}
		})
	}
}

func TestPreparationSharedKustomizationSymlinks(t *testing.T) {
	shared := "resources: ['https://example.com/org/base//selected?ref=main']\n"
	root := prepFixture(t, map[string]string{
		"kustomization.yaml":       "resources: [left, right]\n",
		"shared.yaml":              shared,
		"left/kustomization.yaml":  "resources: [a]\nnamePrefix: left-\n",
		"right/kustomization.yaml": "resources: [deep/b]\nnamePrefix: right-\n",
	})
	aliases := map[string]string{"left/a/kustomization.yaml": "../../shared.yaml", "right/deep/b/kustomization.yaml": "../../../shared.yaml"}
	for name, target := range aliases {
		p := filepath.Join(root, name)
		if err := os.MkdirAll(filepath.Dir(p), 0700); err != nil {
			t.Fatal(err)
		}
		if err := os.Symlink(target, p); err != nil {
			t.Fatal(err)
		}
	}
	base := prepFixture(t, map[string]string{"selected/kustomization.yaml": "resources: [cm.yaml]\n", "selected/cm.yaml": prepCM})
	acquire := func(_ context.Context, ref gitBaseReference) (acquiredBase, error) {
		return acquiredBase{RepositoryDir: base, SubPath: ref.SubPath, Identity: "base/auth/sha"}, nil
	}
	p, err := prepareKustomize(context.Background(), acquiredBase{RepositoryDir: root, Identity: "root"}, t.TempDir(), acquire)
	if err != nil {
		t.Fatal(err)
	}
	defer p.Cleanup()
	out := prepRender(t, p.Root)
	if !strings.Contains(out, "name: left-example") || !strings.Contains(out, "name: right-example") {
		t.Fatal(out)
	}
	if len(p.Identities) != 2 {
		t.Fatalf("aliases lost reference associations: %+v", p.Identities)
	}
	for name := range aliases {
		staged, err := os.Lstat(filepath.Join(p.Root, name))
		if err != nil || !staged.Mode().IsRegular() {
			t.Fatalf("staged alias not materialized: %s, %v", name, err)
		}
		original, err := os.Lstat(filepath.Join(root, name))
		if err != nil || original.Mode()&os.ModeSymlink == 0 {
			t.Fatalf("source alias changed: %s, %v", name, err)
		}
	}
	for _, dir := range []string{root, p.Root} {
		data, err := os.ReadFile(filepath.Join(dir, "shared.yaml"))
		if err != nil || string(data) != shared {
			t.Fatalf("shared target changed: %s, %v", data, err)
		}
	}
}

// Bound the overlap regression independently of wall-clock timing: if the guard
// regresses, walking the recursively copied scratch tree stops after 12 checks.
type preparationBoundedContext struct {
	context.Context //nolint:containedctx // This test context decorator implements context.Context, forwarding everything except the bounded Err check.
	cancel          context.CancelFunc
	checks          int
}

func (c *preparationBoundedContext) Err() error {
	c.checks++
	if c.checks >= 12 {
		c.cancel()
	}
	return c.Context.Err()
}

func TestPreparationRelativeScratch(t *testing.T) {
	for _, inside := range []bool{true, false} {
		t.Run(fmt.Sprintf("inside=%v", inside), func(t *testing.T) {
			root := prepFixture(t, map[string]string{"kustomization.yaml": "resources: [cm.yaml]\n", "cm.yaml": prepCM})
			want := prepRender(t, root)
			t.Chdir(root)
			scratch := "scratch"
			if inside {
				if err := os.Mkdir(scratch, 0700); err != nil {
					t.Fatal(err)
				}
			} else {
				var err error
				scratch, err = filepath.Rel(root, t.TempDir())
				if err != nil {
					t.Fatal(err)
				}
			}
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			bounded := &preparationBoundedContext{Context: ctx, cancel: cancel}
			p, err := prepareKustomize(bounded, acquiredBase{RepositoryDir: root, Identity: "root"}, scratch, nil)
			if inside {
				if err == nil {
					p.Cleanup()
					t.Fatal("accepted scratch inside input repository")
				}
				if !strings.Contains(err.Error(), "scratch directory must not be inside") || bounded.checks > 2 {
					t.Fatalf("overlap not rejected before copying: err=%v, context checks=%d", err, bounded.checks)
				}
			} else {
				if err != nil {
					t.Fatal(err)
				}
				defer p.Cleanup()
				if !filepath.IsAbs(p.Root) {
					t.Fatalf("staged root is relative: %s", p.Root)
				}
				if got := prepRender(t, p.Root); got != want {
					t.Fatalf("relative-scratch output changed:\n%s\nwant:\n%s", got, want)
				}
				p.Cleanup()
			}
			entries, err := os.ReadDir(scratch)
			if err != nil || len(entries) != 0 {
				t.Fatalf("scratch leaked: %v, %v", entries, err)
			}
		})
	}
}
