package equivalence

import (
	"bytes"
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

// ClassifyGolden buckets rendered docs. Every manipulation is identity-gated:
// unrecognized docs default to keep-and-compare (seed) and are never silently dropped.
// NOTE: ManagedResource docs are discarded here; their Secret payloads are unwrapped
// by UnwrapManagedResources (Task 6), which runs on the doc stream BEFORE this.
func ClassifyGolden(docs []*unstructured.Unstructured, opts GoldenOpts) (Classified, error) {
	out := Classified{Seed: ObjectSet{}, Shoot: ObjectSet{}}
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
	data, found, _ := unstructured.NestedMap(u.Object, "data")
	if !found || len(data) != 1 {
		return false
	}
	_, ok := data["webhooks.yaml"]
	return ok
}

func decodeWebhooks(u *unstructured.Unstructured) ([]*unstructured.Unstructured, error) {
	raw, _, _ := unstructured.NestedString(u.Object, "data", "webhooks.yaml")
	return splitYAMLDocs([]byte(raw))
}

// splitYAMLDocs parses a multi-doc YAML stream into objects. It handles leading
// "---", CRLF, and trailing-space separators via a real streaming decoder, and
// surfaces decode errors rather than silently dropping documents.
func splitYAMLDocs(b []byte) ([]*unstructured.Unstructured, error) {
	dec := utilyaml.NewYAMLOrJSONDecoder(bytes.NewReader(b), 4096)
	var out []*unstructured.Unstructured
	for {
		m := map[string]interface{}{}
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
