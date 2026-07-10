/*
Copyright 2026.

Licensed under the Apache License, Version 2.0 (the "License");
you may not use this file except in compliance with the License.
You may obtain a copy of the License at

    http://www.apache.org/licenses/LICENSE-2.0

Unless required by applicable law or agreed to in writing, software
distributed under the License is distributed on an "AS IS" BASIS,
WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
See the License for the specific language governing permissions and
limitations under the License.
*/

package v1alpha1

import (
	apiextensionsv1 "k8s.io/apiextensions-apiserver/pkg/apis/apiextensions/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
)

// DualDeploymentOperatorSpec defines the desired state of DualDeploymentOperator.
type DualDeploymentOperatorSpec struct {
	Source           Source              `json:"source"`
	RemoteKubeconfig RemoteKubeconfigRef `json:"remoteKubeconfig"`
	// +optional
	Transformations []Transformation `json:"transformations,omitempty"`
	// +optional
	// +kubebuilder:default={crds:Retain}
	DeletionPolicy DeletionPolicy `json:"deletionPolicy,omitempty"`
}

// Source is a discriminated union — exactly one of Helm or Kustomize.
// +kubebuilder:validation:XValidation:rule="has(self.helm) != has(self.kustomize)",message="exactly one of source.helm or source.kustomize must be set"
type Source struct {
	// +optional
	Helm *HelmSource `json:"helm,omitempty"`
	// +optional
	Kustomize *KustomizeSource `json:"kustomize,omitempty"`
}

type HelmSource struct {
	// +kubebuilder:validation:MinLength=1
	Repo string `json:"repo"`
	// +kubebuilder:validation:MinLength=1
	Name string `json:"name"`
	// +kubebuilder:validation:MinLength=1
	Version string `json:"version"`
	// +optional
	// +kubebuilder:validation:Schemaless
	// +kubebuilder:pruning:PreserveUnknownFields
	Values *apiextensionsv1.JSON `json:"values,omitempty"`
	// +optional
	// +kubebuilder:validation:Schemaless
	// +kubebuilder:pruning:PreserveUnknownFields
	HostValues *apiextensionsv1.JSON `json:"hostValues,omitempty"`
	// +optional
	// +kubebuilder:validation:Schemaless
	// +kubebuilder:pruning:PreserveUnknownFields
	RemoteValues *apiextensionsv1.JSON `json:"remoteValues,omitempty"`
}

// KustomizeSource references a kustomize root plus its two overlay subpaths.
// +kubebuilder:validation:XValidation:rule="self.url.matches('.*[?&]ref=.+')",message="kustomize url must include a pinned ref= query parameter"
type KustomizeSource struct {
	// +kubebuilder:validation:MinLength=1
	URL string `json:"url"`
	// +kubebuilder:validation:MinLength=1
	HostPath string `json:"hostPath"`
	// +kubebuilder:validation:MinLength=1
	RemotePath string `json:"remotePath"`
}

type RemoteKubeconfigRef struct {
	// +kubebuilder:validation:MinLength=1
	SecretName string `json:"secretName"`
	// +kubebuilder:validation:MinLength=1
	Key string `json:"key"`
}

// Transformation is a discriminated union — exactly one field set per entry.
// +kubebuilder:validation:XValidation:rule="(has(self.patch) ? 1 : 0) + (has(self.rewriteWebhookURL) ? 1 : 0) + (has(self.filterKinds) ? 1 : 0) + (has(self.packageWebhookConfigsForInjector) ? 1 : 0) == 1",message="exactly one transformation type must be set per entry"
type Transformation struct {
	// +optional
	Patch *PatchSpec `json:"patch,omitempty"`
	// +optional
	RewriteWebhookURL *RewriteWebhookURLSpec `json:"rewriteWebhookURL,omitempty"`
	// +optional
	FilterKinds *FilterKindsSpec `json:"filterKinds,omitempty"`
	// +optional
	PackageWebhookConfigsForInjector *PackageWebhookConfigsForInjectorSpec `json:"packageWebhookConfigsForInjector,omitempty"`
}

// PatchSpec is a Kubernetes-native patch (strategic-merge XOR JSON Patch) with a target selector.
// +kubebuilder:validation:XValidation:rule="has(self.strategicMerge) != has(self.jsonPatch)",message="exactly one of patch.strategicMerge or patch.jsonPatch must be set"
type PatchSpec struct {
	Target Selector `json:"target"`
	// +optional
	// +kubebuilder:validation:Schemaless
	// +kubebuilder:pruning:PreserveUnknownFields
	StrategicMerge *apiextensionsv1.JSON `json:"strategicMerge,omitempty"`
	// +optional
	JSONPatch []JSONPatchOp `json:"jsonPatch,omitempty"`
}

// JSONPatchOp is one RFC 6902 operation.
type JSONPatchOp struct {
	// +kubebuilder:validation:Enum=add;remove;replace;move;copy;test
	Op string `json:"op"`
	// +kubebuilder:validation:MinLength=1
	Path string `json:"path"`
	// +optional
	From string `json:"from,omitempty"`
	// +optional
	// +kubebuilder:validation:Schemaless
	// +kubebuilder:pruning:PreserveUnknownFields
	Value *apiextensionsv1.JSON `json:"value,omitempty"`
}

type RewriteWebhookURLSpec struct {
	// +kubebuilder:validation:MinLength=1
	URLPrefix string `json:"urlPrefix"`
	// +optional
	TargetKinds []string `json:"targetKinds,omitempty"`
}

type FilterKindsSpec struct {
	// +kubebuilder:validation:MinItems=1
	Kinds []string `json:"kinds"`
	// +optional
	Source string `json:"source,omitempty"`
}

type PackageWebhookConfigsForInjectorSpec struct {
	// +kubebuilder:validation:MinLength=1
	ConfigMapName string `json:"configMapName"`
	// +optional
	DataKey string `json:"dataKey,omitempty"`
}

type Selector struct {
	// +optional
	Kind string `json:"kind,omitempty"`
	// +optional
	Name string `json:"name,omitempty"`
	// +optional
	Origin string `json:"origin,omitempty"`
}

type DeletionPolicy struct {
	// +optional
	// +kubebuilder:validation:Enum=Retain;Delete
	// +kubebuilder:default=Retain
	CRDs string `json:"crds,omitempty"`
}

// DualDeploymentOperatorStatus defines the observed state of DualDeploymentOperator.
type DualDeploymentOperatorStatus struct {
	// +optional
	HostResources []ResourceStatus `json:"hostResources,omitempty"`
	// +optional
	RemoteResources []ResourceStatus `json:"remoteResources,omitempty"`
	// +optional
	Conditions []metav1.Condition `json:"conditions,omitempty"`
	// +optional
	LastReconcile *metav1.Time `json:"lastReconcile,omitempty"`
}

type ResourceStatus struct {
	Kind       string `json:"kind"`
	APIVersion string `json:"apiVersion"`
	// +optional
	Namespace string      `json:"namespace,omitempty"`
	Name      string      `json:"name"`
	Health    HealthState `json:"health"`
	// +optional
	LastApplied *metav1.Time `json:"lastApplied,omitempty"`
	// +optional
	Message string `json:"message,omitempty"`
}

// +kubebuilder:validation:Enum=Healthy;Progressing;Degraded;Unknown
type HealthState string

const (
	HealthHealthy     HealthState = "Healthy"
	HealthProgressing HealthState = "Progressing"
	HealthDegraded    HealthState = "Degraded"
	HealthUnknown     HealthState = "Unknown"
)

// +kubebuilder:object:root=true
// +kubebuilder:subresource:status
// +kubebuilder:resource:shortName=ddo
type DualDeploymentOperator struct {
	metav1.TypeMeta   `json:",inline"`
	metav1.ObjectMeta `json:"metadata,omitempty"`

	Spec   DualDeploymentOperatorSpec   `json:"spec,omitempty"`
	Status DualDeploymentOperatorStatus `json:"status,omitempty"`
}

// +kubebuilder:object:root=true
type DualDeploymentOperatorList struct {
	metav1.TypeMeta `json:",inline"`
	metav1.ListMeta `json:"metadata,omitempty"`
	Items           []DualDeploymentOperator `json:"items"`
}

func init() {
	SchemeBuilder.Register(func(s *runtime.Scheme) error {
		s.AddKnownTypes(SchemeGroupVersion, &DualDeploymentOperator{}, &DualDeploymentOperatorList{})
		return nil
	})
}
