# IPAM guarded migration comparison — exact reviewed source

The fixture is pinned to reviewed, published candidate
`463740c01f7466f360847ae6507ff06570903aac`. The test serves that exact commit
without source adaptation. Final independent DDO review and the full repository
gate remain orchestrator-owned; this is not a live/runtime equivalence claim.

Enabled result for this exact pin: **PASS**, 15 seed / 25 shoot identities on
both sides, all guarded comparisons and mutation checks passed (`85.702s`
package). One real public provider fetch was observed. Source was unmodified;
all three vendored byte hashes were verified. Raw migration differences are
still logged, not confused with byte-for-byte equality of the two delivery modes.

## Inputs and provenance

- Independent legacy: `https://github.com/sapcc/helm-charts.git` at
  `97d52c25ae067c00fd5f5b3e7a66771fdcbc10db`,
  `system/ipam-capi-remote`, chart **1.2.37**.
- Candidate: PR13218, `system/kustomize/ipam-capi-remote-ddo/{seed,shoot}`.
  Exact commit `463740c01f7466f360847ae6507ff06570903aac`.
  Candidate fingerprints are never the legacy oracle.
- Provider stays a real remote Git directory:
  `https://github.com/kubernetes-sigs/cluster-api-ipam-provider-in-cluster//config/crd?ref=7933583138cf1b5b16b6a1dd82a804729f83afab`.
- Three core CRD files originate from `kubernetes-sigs/cluster-api` at
  `5f7d58c9ad647f31bd342bf8de33092c27f7fc38`, `core/config/crd/bases/`:

  | File | Exact byte SHA-256 |
  | --- | --- |
  | `cluster.x-k8s.io_clusters.yaml` | `39768d1dbe14c7932e4fc4aacd90fcc08395f5922f74886e1a3d87868ce8d997` |
  | `ipam.cluster.x-k8s.io_ipaddresses.yaml` | `d5068e3d0031f0cca9514492712384d4325b2481d3e6854da1192dc6d88c28cd` |
  | `ipam.cluster.x-k8s.io_ipaddressclaims.yaml` | `28b61b0fc4c4a66ed2d9ca70c488eae210251662a7d2bcb5cc3ad93c46e6fa19` |

The published candidate already vendors these exact bytes. The staging helper
only reads/verifies them and checks that the Git checkout remains clean; it does
not download raw files, replace references, write manifests, or create a new
commit. A temporary branch advertises the same published SHA over the local TLS
Git seam. The actual `strategy: None` patch and remote provider reference are
unchanged. Production raw HTTP loading is neither enabled nor required.

## Verified placement and nonsecret values

The pinned kube-secrets PR5683 contract
`53cf84a1cea65d23307866b7fd2436ebccf6f164` establishes seed
**`shoot--cp--m-qa-de-200`**, shoot **`kube-system`**, for both releases. Pipeline
provenance: DDO `77ac02903baf231c23678f2cf4072caaaaa85d2e`; legacy
`727ae856ce93bcdcbdc7c196ce652bc62c632635`. This supersedes the earlier
`equivalence-seed` portability-only scenario.

The repaired source's seed ClusterRoleBinding subject must omit namespace. The
required CR JSON patch adds `/subjects/0/namespace` to the exact
`ipam-capi-remote-webhook-injector` ClusterRoleBinding with `origin: additions`.
It must equal both CR
namespace and delivered seed ServiceAccount namespace. The offline alternate
namespace case uses this same patch mechanism; comparison never canonicalizes
subjects. **Downstream kube-secrets must preserve the complete transformation
array when adding this instance patch** (instance arrays replace defaults).
This fixture does not edit that repository.

Both sides use the neutral `api.equivalence.invalid` host and matching image
references/security settings from `overlay-values.yaml`; no images or real
credentials are fetched. Existing exact admission-label patches remain
idempotent alongside the required six source labels. Helm's explicit
`monitoring.coreos.com/v1` capability retains ServiceMonitor in the oracle.

## Guard before comparing remaining fields

`inventory.yaml` is the **same complete expected identity list on both sides**:
15 seed / 25 shoot. No missing-object exemptions exist. All three NetworkPolicies,
manager ConfigMap, shoot leader-election role/binding, and metrics Service stay
in scope. Both-sided removals fail too.

The comparison deep-copies raw evidence and first asserts exact old/new values:

1. Three core CRDs: absent legacy conversion → exactly `{strategy: None}`;
   no target label or webhook fields. Only the guarded strategy leaf is removed.
2. Four pool CRDs: legacy Service `webhook-service` / `kube-system` / `/convert`
   (port omitted, CA absent) → exact
   `https://ipam-capi-remote-webhook-service:443/convert`, no Service or static CA,
   exact target label, `Webhook` strategy and `[v1]` review versions. Only the
   guarded Service/URL and label leaves are discounted; schemas remain whole.
3. Both admission configurations: exact added target label, no static CA;
   all eight entries, URLs, paths, rules, policies and other metadata compare.
4. Seed Deployment: exact legacy injector arguments → exact target-only
   arguments. SAN, image, cert name, kubeconfig, leader ID and rotation settings
   remain constrained. Only that guarded argument list and the independently
   guarded absent → `origin: additions` pod annotation differ.
5. Shoot injector ClusterRole: exact legacy rules → exact reviewed named
   `get,patch` for four pools and two admission targets, plus unrestricted
   `list,watch`. No `create/update`, broad patch rights, or other RBAC exemptions.
   Shoot binding remains the controller ServiceAccount in `kube-system`.
6. Seed binding must actually match after the CR placement patch; no exception.
7. ServiceMonitor: exact legacy own-namespace selector → absent candidate only
   when monitor and selected Service are co-located. Other fields still compare.
8. Exact chart provenance: the seed Deployment, both seed Services and
   ServiceMonitor lose only `helm.sh/chart: ipam-capi-remote-1.2.37`,
   `app.kubernetes.io/version: 0.1.0` and `app.kubernetes.io/managed-by: Helm`.
   The restored manager ConfigMap loses **only managed-by**; its chart/version
   and application labels remain compared. Each old value and new absence is
   checked before removing that leaf. Every seed object's additions origin is
   independently checked as absent → exactly `additions`.

Exactly six source resources must match exactly one injector selector argument;
core CRDs stay unlabelled. Captured pool CA is **not** accepted trust material.
Absent static CA plus exact labels, permissions and bindings establish the
reviewed static ownership contract, **not runtime initialization or rotation
safety**. Production CA stripping is unchanged.

Unrelated pod fields, annotations, RBAC, schemas, versions, NetworkPolicies,
Services and all other objects remain under the existing full comparison.
Remaining root labels/annotations are compared strictly, including keys the
generic harness normally allowlists; only the explicit guarded deltas above
are accepted. The generic allowlist itself is unchanged. IPAM canonicalizes
numbers only: unguarded empty maps and null fields retain their presence. Only
guarded leaves and ancestors newly emptied by a successful guard removal may
disappear. In particular, adding `namespaceSelector: {}` to an apiserver peer
must fail rather than silently broaden access from own-namespace to all
namespaces. Ingress, egress, schema/null presence and path-local cleanup have
dedicated regressions. Other operators retain their existing normalization.
Guard failure remains a failure; raw differences are also logged by the enabled
case before guarded comparison.

## Offline historical snapshot and sensitivity

`legacy-render.json.gz` is the complete configured **legacy Helm render**, not a
candidate output or fingerprint approximation. Generated locally from the exact
clean legacy chart using the existing pinned Helm v3 Go API, fixture values,
verified seed namespace and explicit monitoring capability. Loading it uses the
same MR Secret / injector ConfigMap unwrapping and namespace-placement helpers
as the online oracle. Size: 80,567 bytes. SHA-256:
`af953c4e6620feda1ef7885c5c0e73eafe58e2a8670280f8a9cfce7f2b2765f0`.
The loader checks this digest; never regenerate it to make a candidate pass.

Offline positives model only the **approved proposal** on a copy of that
historical render. Negative cases run through the final guarded comparator:
missing/wrong target labels, CA/URL/path/review-version drift, SAN/argument/image
drift, broadened RBAC, wrong subjects, policy/schema/selector/port changes, and
every single-sided/both-sided object removal. The comparator must not mutate its
raw inputs. These tests prove the discriminator, not repaired-source conformance.

```sh
RUN_EQUIVALENCE= go test ./internal/equivalence -run '^(TestIPAM.*|TestNormalize.*|TestGoldenRenderAPIVersions|TestReview.*)$' -count=1
golangci-lint run ./internal/equivalence/...
```

Run the actual pinned-source case (public GitHub network and local Git backend required):

```sh
RUN_EQUIVALENCE=1 go test ./internal/equivalence -run '^TestEquivalenceIPAMCapiRemote$' -count=1 -v
```

That path still uses real `NewGitResolver`, `source.From`, both renders,
`transform.Build` and capture. Only the exact local Git endpoint uses the test
TLS transport; public sessions delegate the saved production transport and
redirect policy. Existing delegation/rejection tests remain intact. No live
cluster, Kind, cutover, connectivity, or runtime CA-rotation claim is made.
