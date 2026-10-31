// SPDX-FileCopyrightText: 2026 SAP SE or an SAP affiliate company
// SPDX-License-Identifier: Apache-2.0

package source

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"strings"

	yamlv3 "go.yaml.in/yaml/v3"
	"sigs.k8s.io/kustomize/api/resource"
	"sigs.k8s.io/kustomize/api/types"
	"sigs.k8s.io/yaml"
)

// This loading-path inventory is pinned to kustomize/api v0.21.1:
// target/kusttarget, krusty/kustomizer, kv/kv, accumulator/loadconfigfromcrds,
// plugins/builtinconfig, and builtins/{Patch*,Replacement,ValueAdd}Transformer.
// Helm and external plugins are deliberately unsupported. Supplemental config
// directories are rejected: their generated/transformed contents could introduce
// loads not visible in their source documents. This is preflight, not a sandbox.
func preflightKustomization(repo, dir string, k *types.Kustomization) error {
	if k.HelmGlobals != nil || len(k.HelmCharts) != 0 || len(k.HelmChartInflationGenerator) != 0 {
		return errors.New("helm execution is disabled for kustomize preparation")
	}
	paths := append([]string{}, k.Configurations...)
	paths = append(paths, k.Crds...)
	if p := k.OpenAPI["path"]; p != "" {
		paths = append(paths, p)
	}
	for _, patches := range [][]types.Patch{k.Patches, k.PatchesJson6902} { //nolint:staticcheck // Legacy JSON patches still load files and require preflight.
		for _, patch := range patches {
			if patch.Path != "" {
				paths = append(paths, patch.Path)
			}
		}
	}
	for _, r := range k.Replacements {
		if r.Path != "" {
			paths = append(paths, r.Path)
		}
	}
	if err := preflightFiles(repo, dir, paths); err != nil {
		return err
	}
	if err := preflightStrategicPatches(repo, dir, k.PatchesStrategicMerge); err != nil { //nolint:staticcheck // Legacy strategic patches still load files and require preflight.
		return err
	}
	for _, g := range k.ConfigMapGenerator {
		if err := preflightGenerator(repo, dir, g.KvPairSources); err != nil {
			return err
		}
	}
	for _, g := range k.SecretGenerator {
		if err := preflightGenerator(repo, dir, g.KvPairSources); err != nil {
			return err
		}
	}
	for _, name := range k.Generators {
		if err := preflightSupplemental(repo, dir, name, true); err != nil {
			return err
		}
	}
	for _, configs := range [][]string{k.Transformers, k.Validators} {
		for _, name := range configs {
			if err := preflightSupplemental(repo, dir, name, true); err != nil {
				return err
			}
		}
	}
	return nil
}

func preflightFiles(repo, dir string, paths []string) error {
	for _, name := range paths {
		p, err := preparationLocalPath(repo, dir, name)
		if err != nil {
			return err
		}
		info, err := os.Stat(p)
		if err != nil {
			return err
		}
		if !info.Mode().IsRegular() {
			return errors.New("loading input must be a regular file")
		}
	}
	return nil
}

func preflightGenerator(repo, dir string, sources types.KvPairSources) error {
	paths := append([]string{}, sources.EnvSources...)
	if sources.EnvSource != "" {
		paths = append(paths, sources.EnvSource)
	}
	for _, source := range sources.FileSources {
		// Matches the pinned internal generators.ParseFileSource grammar without
		// depending on that private API. Literals are data, never loading paths.
		if strings.Count(source, "=") > 1 {
			return errors.New("generator file source contains multiple '=' separators")
		}
		key, name, found := strings.Cut(source, "=")
		if !found {
			name = key
		} else if key == "" || name == "" {
			return errors.New("invalid generator file source")
		}
		paths = append(paths, name)
	}
	return preflightFiles(repo, dir, paths)
}

func preflightStrategicPatches(repo, dir string, patches []types.PatchStrategicMerge) error {
	for _, patch := range patches {
		// The pinned builtin tries resource parsing FIRST (including JSON and
		// single-line inline patches), and only loads a file if parsing fails.
		if _, err := resource.NewFactory(nil).SliceFromBytes([]byte(patch)); err == nil {
			continue
		}
		if err := preflightFiles(repo, dir, []string{string(patch)}); err != nil {
			return err
		}
	}
	return nil
}

func preflightSupplemental(repo, dir, name string, allowInline bool) error {
	factory := resource.NewFactory(nil)
	factory.IncludeLocalConfigs = true
	var configs []*resource.Resource
	if allowInline {
		// Match the pinned loader: failed inline parsing means this is a file
		// candidate, not a partially parsed plugin configuration.
		if parsed, err := factory.SliceFromBytes([]byte(name)); err == nil {
			configs = parsed
		}
	}
	if configs == nil {
		if err := preflightFiles(repo, dir, []string{name}); err != nil {
			return err
		}
		p, err := preparationLocalPath(repo, dir, name)
		if err != nil {
			return err
		}
		data, err := os.ReadFile(p)
		if err != nil {
			return err
		}
		configs, err = factory.SliceFromBytes(data)
		if err != nil {
			return err
		}
	}
	for _, config := range configs {
		if config.GetApiVersion() != "builtin" {
			return errors.New("external kustomize plugins are disabled")
		}
		data, err := config.AsYAML()
		if err != nil {
			return err
		}
		if err := preflightBuiltin(repo, dir, config.GetKind(), data); err != nil {
			return err
		}
	}
	return nil
}

func preflightBuiltin(repo, dir, kind string, data []byte) error {
	// Supplemental configurations are NEVER rewritten. Unknown fields remain
	// intact, just as in the pinned builtins' permissive YAML decoders. A closed
	// kind allowlist prevents unreviewed plugins from adding loading behavior.
	switch kind {
	case "ConfigMapGenerator", "SecretGenerator":
		var g types.KvPairSources
		if err := yaml.Unmarshal(data, &g); err != nil {
			return err
		}
		return preflightGenerator(repo, dir, g)
	case "PatchTransformer", "PatchJson6902Transformer":
		var p types.Patch
		if err := yaml.Unmarshal(data, &p); err != nil {
			return err
		}
		if p.Path != "" {
			return preflightFiles(repo, dir, []string{p.Path})
		}
	case "PatchStrategicMergeTransformer":
		var p struct {
			Paths []types.PatchStrategicMerge `json:"paths"`
		}
		if err := yaml.Unmarshal(data, &p); err != nil {
			return err
		}
		return preflightStrategicPatches(repo, dir, p.Paths)
	case "ReplacementTransformer":
		var r struct {
			Replacements []types.ReplacementField `json:"replacements"`
		}
		if err := yaml.Unmarshal(data, &r); err != nil {
			return err
		}
		for _, replacement := range r.Replacements {
			if replacement.Path != "" {
				if err := preflightFiles(repo, dir, []string{replacement.Path}); err != nil {
					return err
				}
			}
		}
	case "ValueAddTransformer":
		var v struct {
			TargetFilePath string `json:"targetFilePath"`
		}
		if err := yaml.Unmarshal(data, &v); err != nil {
			return err
		}
		if v.TargetFilePath != "" {
			return preflightFiles(repo, dir, []string{v.TargetFilePath})
		}
	case "AnnotationsTransformer", "HashTransformer", "ImageTagTransformer", "LabelTransformer",
		"NamespaceTransformer", "PrefixTransformer", "SuffixTransformer", "ReplicaCountTransformer", "SortOrderTransformer":
		// No filesystem or URL-loading fields in these pinned builtins.
	default:
		return fmt.Errorf("unsupported supplemental builtin %q", kind)
	}
	return nil
}

// Reject duplicate keys, multiple documents and unknown typed fields. Ordinary
// YAMLToJSON alone would silently retain only the first YAML document.
func preparationDecode(data []byte, target any) error {
	d := yamlv3.NewDecoder(bytes.NewReader(data))
	var node yamlv3.Node
	if err := d.Decode(&node); err != nil {
		return err
	}
	if err := d.Decode(&yamlv3.Node{}); !errors.Is(err, io.EOF) {
		return errors.New("expected exactly one kustomization document")
	}
	j, err := yaml.YAMLToJSONStrict(data)
	if err != nil {
		return err
	}
	decoder := json.NewDecoder(bytes.NewReader(j))
	decoder.DisallowUnknownFields()
	return decoder.Decode(target)
}
