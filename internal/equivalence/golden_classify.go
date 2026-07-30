// SPDX-FileCopyrightText: 2026 SAP SE or an SAP affiliate company
// Copyright 2026.
//
// SPDX-License-Identifier: Apache-2.0

package equivalence

import (
	"bytes"
	"encoding/base64"
	"errors"
	"fmt"
	"io"

	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	utilyaml "k8s.io/apimachinery/pkg/util/yaml"
)

// GoldenOpts drives identity-gated classification.
type GoldenOpts struct {
	ChartFullname string           // e.g. "metal-operator-remote"
	Exclusions    []ExclusionEntry // enumerated kind+name to drop (render emits no equivalent)
}

// ExclusionEntry identifies an object to exclude by kind AND name (never substring).
type ExclusionEntry struct {
	Kind string
	Name string
}

// Classified is the golden set partitioned after unwrap/exclude.
type Classified struct {
	Seed  ObjectSet
	Shoot ObjectSet
}

// ClassifyGolden buckets rendered docs into seed vs shoot sets, mirroring today's
// chart delivery: ManagedResource-wrapped content (managedresources/*) and the
// injector webhook-config are shoot-destined; chart templates are seed-destined.
// shootFromMR carries the bare objects already unwrapped from ManagedResource
// Secrets by UnwrapManagedResources — those are shoot-destined by construction.
// Every manipulation is identity-gated; unrecognized docs default to seed
// (keep-and-compare) and are never silently dropped.
func ClassifyGolden(docs, shootFromMR []*unstructured.Unstructured, opts GoldenOpts) (Classified, error) {
	out := Classified{Seed: ObjectSet{}, Shoot: ObjectSet{}}
	for _, d := range shootFromMR {
		if isExcluded(d, opts.Exclusions) {
			continue
		}
		out.Shoot[KeyOf(d)] = d
	}
	for _, d := range docs {
		switch {
		case isExcluded(d, opts.Exclusions):
			continue
		case isManagedResource(d):
			continue
		case isInjectorConfigMap(d, opts.ChartFullname):
			whs, err := decodeWebhooks(d)
			if err != nil {
				return Classified{}, fmt.Errorf("equivalence: unwrap injector ConfigMap %q: %w", d.GetName(), err)
			}
			for _, wh := range whs {
				out.Shoot[KeyOf(wh)] = wh
			}
		default:
			out.Seed[KeyOf(d)] = d
		}
	}
	return out, nil
}

func isExcluded(u *unstructured.Unstructured, ex []ExclusionEntry) bool {
	for _, e := range ex {
		if u.GetKind() == e.Kind && u.GetName() == e.Name {
			return true
		}
	}
	return false
}

func isManagedResource(u *unstructured.Unstructured) bool {
	return u.GetKind() == "ManagedResource" &&
		u.GroupVersionKind().Group == "resources.gardener.cloud"
}

// isInjectorConfigMap matches ONLY name==<fullname>-webhook-config AND sole data
// key "webhooks.yaml". Never matches on kind==ConfigMap alone.
func isInjectorConfigMap(u *unstructured.Unstructured, fullname string) bool {
	if u.GetKind() != "ConfigMap" || u.GetName() != fullname+"-webhook-config" {
		return false
	}
	data, found, err := unstructured.NestedMap(u.Object, "data")
	if err != nil || !found || len(data) != 1 {
		return false
	}
	_, ok := data["webhooks.yaml"]
	return ok
}

func decodeWebhooks(u *unstructured.Unstructured) ([]*unstructured.Unstructured, error) {
	raw, _, err := unstructured.NestedString(u.Object, "data", "webhooks.yaml")
	if err != nil {
		return nil, err
	}
	return splitYAMLDocs([]byte(raw))
}

// splitYAMLDocs parses a multi-doc YAML stream into objects. It handles leading
// "---", CRLF, and trailing-space separators via a real streaming decoder, and
// surfaces decode errors rather than silently dropping documents.
func splitYAMLDocs(b []byte) ([]*unstructured.Unstructured, error) {
	dec := utilyaml.NewYAMLOrJSONDecoder(bytes.NewReader(b), 4096)
	var out []*unstructured.Unstructured
	for {
		m := map[string]any{}
		err := dec.Decode(&m)
		if errors.Is(err, io.EOF) {
			break
		}
		if err != nil {
			return nil, fmt.Errorf("equivalence: decode YAML doc: %w", err)
		}
		if len(m) == 0 { // skip empty documents (e.g. leading ---, trailing ---)
			continue
		}
		out = append(out, &unstructured.Unstructured{Object: m})
	}
	return out, nil
}

// UnwrapManagedResources separates a rendered doc stream into the bare objects
// carried inside ManagedResource Secrets (fromMR — shoot-destined by
// construction) and the remaining pass-through docs (chart templates). The
// wrapper ManagedResource + its referenced Secret(s) are dropped. Errors from
// base64 or YAML decoding are surfaced, never silently dropped.
func UnwrapManagedResources(docs []*unstructured.Unstructured) (fromMR, passthrough []*unstructured.Unstructured, err error) {
	secretsByName := map[string]*unstructured.Unstructured{}
	for _, d := range docs {
		if d.GetKind() == "Secret" {
			secretsByName[d.GetName()] = d
		}
	}
	referenced := map[string]bool{}

	for _, d := range docs {
		if !isManagedResource(d) {
			continue
		}
		refs, _, err := unstructured.NestedSlice(d.Object, "spec", "secretRefs")
		if err != nil {
			return nil, nil, err
		}
		for _, r := range refs {
			m, ok := r.(map[string]any)
			if !ok {
				continue
			}
			name, _ := m["name"].(string)
			sec, ok := secretsByName[name]
			if !ok {
				continue
			}
			referenced[name] = true
			enc, _, err := unstructured.NestedString(sec.Object, "data", "objects.yaml")
			if err != nil {
				return nil, nil, err
			}
			raw, decErr := base64.StdEncoding.DecodeString(enc)
			if decErr != nil {
				return nil, nil, fmt.Errorf("equivalence: decode MR secret %q objects.yaml: %w", name, decErr)
			}
			objs, splitErr := splitYAMLDocs(raw)
			if splitErr != nil {
				return nil, nil, fmt.Errorf("equivalence: split MR secret %q payload: %w", name, splitErr)
			}
			fromMR = append(fromMR, objs...)
		}
	}

	for _, d := range docs {
		if isManagedResource(d) {
			continue
		}
		if d.GetKind() == "Secret" && referenced[d.GetName()] {
			continue
		}
		passthrough = append(passthrough, d)
	}
	return fromMR, passthrough, nil
}
