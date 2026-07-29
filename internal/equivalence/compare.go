package equivalence

import (
	"fmt"
	"reflect"
	"strings"

	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
)

// Report accumulates per-resource equivalence findings.
type Report struct {
	Mismatched []string // displayKey + reason
	MissingOp  []string // in golden, absent on operator side
	ExtraOp    []string // on operator side, absent in golden
}

// Equal reports whether the two sides are equivalent.
func (r Report) Equal() bool {
	return len(r.Mismatched) == 0 && len(r.MissingOp) == 0 && len(r.ExtraOp) == 0
}

func (r Report) String() string {
	var b strings.Builder
	for _, m := range r.Mismatched {
		fmt.Fprintf(&b, "MISMATCH %s\n", m)
	}
	for _, m := range r.MissingOp {
		fmt.Fprintf(&b, "MISSING on operator side: %s\n", m)
	}
	for _, e := range r.ExtraOp {
		fmt.Fprintf(&b, "EXTRA on operator side: %s\n", e)
	}
	return b.String()
}

// displayKey renders a human-readable identity including apiVersion.
func displayKey(u *unstructured.Unstructured) string {
	return fmt.Sprintf("%s/%s/%s/%s", u.GetAPIVersion(), u.GetKind(), u.GetNamespace(), u.GetName())
}

// Compare normalizes + allowlist-strips both sides and deep-equals per resource.
func Compare(golden, op ObjectSet) Report {
	var r Report
	prep := func(u *unstructured.Unstructured) *unstructured.Unstructured {
		StripAllowlist(u)
		Normalize(u)
		return u
	}
	for k, gobj := range golden {
		oobj, ok := op[k]
		if !ok {
			r.MissingOp = append(r.MissingOp, displayKey(gobj))
			continue
		}
		if !reflect.DeepEqual(prep(gobj).Object, prep(oobj).Object) {
			r.Mismatched = append(r.Mismatched, fmt.Sprintf("%s (fields differ)", displayKey(gobj)))
		}
	}
	for k, oobj := range op {
		if _, ok := golden[k]; !ok {
			r.ExtraOp = append(r.ExtraOp, displayKey(oobj))
		}
	}
	return r
}
