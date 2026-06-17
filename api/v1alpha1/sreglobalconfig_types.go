package v1alpha1

import (
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
)

type RetentionSpec struct {
	MaxConfigMapCopies  int32 `json:"maxConfigMapCopies,omitempty"`
	MaxConfigMapAgeDays int32 `json:"maxConfigMapAgeDays,omitempty"`
}

type DefaultsSpec struct {
	VMNamespace        string   `json:"vmNamespace,omitempty"`
	ExcludedNamespaces []string `json:"excludedNamespaces,omitempty"`
}

type GlobalPrometheusSpec struct {
	PrometheusURL        string                    `json:"prometheusUrl,omitempty"`
	AlertManagerURL      string                    `json:"alertManagerUrl,omitempty"`
	InsecureSkipVerify   bool                      `json:"insecureSkipVerify,omitempty"`
	BearerTokenSecretRef *corev1.SecretKeySelector `json:"bearerTokenSecretRef,omitempty"`
}

type SREGlobalConfigSpec struct {
	Observability *ObservabilitySpec    `json:"observability,omitempty"`
	Retention     *RetentionSpec        `json:"retention,omitempty"`
	Defaults      *DefaultsSpec         `json:"defaults,omitempty"`
	Prometheus    *GlobalPrometheusSpec `json:"prometheus,omitempty"`
}

type SREGlobalConfigStatus struct {
	Phase      string             `json:"phase,omitempty"`
	Conditions []metav1.Condition `json:"conditions,omitempty"`
}

// +kubebuilder:object:root=true
// +kubebuilder:subresource:status
// +kubebuilder:resource:scope=Cluster

type SREGlobalConfig struct {
	metav1.TypeMeta   `json:",inline"`
	metav1.ObjectMeta `json:"metadata,omitempty"`

	Spec   SREGlobalConfigSpec   `json:"spec,omitempty"`
	Status SREGlobalConfigStatus `json:"status,omitempty"`
}

// +kubebuilder:object:root=true

type SREGlobalConfigList struct {
	metav1.TypeMeta `json:",inline"`
	metav1.ListMeta `json:"metadata,omitempty"`
	Items           []SREGlobalConfig `json:"items"`
}

func init() {
	SchemeBuilder.Register(&SREGlobalConfig{}, &SREGlobalConfigList{})
}

// DeepCopyObject implementations for runtime.Object

func (in *SREGlobalConfig) DeepCopyObject() runtime.Object {
	if c := in.DeepCopy(); c != nil {
		return c
	}
	return nil
}

func (in *SREGlobalConfig) DeepCopy() *SREGlobalConfig {
	if in == nil {
		return nil
	}
	out := new(SREGlobalConfig)
	in.DeepCopyInto(out)
	return out
}

func (in *SREGlobalConfig) DeepCopyInto(out *SREGlobalConfig) {
	*out = *in
	out.TypeMeta = in.TypeMeta
	in.ObjectMeta.DeepCopyInto(&out.ObjectMeta)
	// Add proper deepcopy of spec below when make generate is used
	out.Spec = in.Spec
	out.Status = in.Status
}

func (in *SREGlobalConfigList) DeepCopyObject() runtime.Object {
	if c := in.DeepCopy(); c != nil {
		return c
	}
	return nil
}

func (in *SREGlobalConfigList) DeepCopy() *SREGlobalConfigList {
	if in == nil {
		return nil
	}
	out := new(SREGlobalConfigList)
	in.DeepCopyInto(out)
	return out
}

func (in *SREGlobalConfigList) DeepCopyInto(out *SREGlobalConfigList) {
	*out = *in
	out.TypeMeta = in.TypeMeta
	in.ListMeta.DeepCopyInto(&out.ListMeta)
	if in.Items != nil {
		in, out := &in.Items, &out.Items
		*out = make([]SREGlobalConfig, len(*in))
		for i := range *in {
			(*in)[i].DeepCopyInto(&(*out)[i])
		}
	}
}
