package v1alpha1

import (
	"k8s.io/apimachinery/pkg/runtime"
)

func (in *SREPolicy) DeepCopyInto(out *SREPolicy) {
	*out = *in
	out.TypeMeta = in.TypeMeta
	in.ObjectMeta.DeepCopyInto(&out.ObjectMeta)
}

func (in *SREPolicy) DeepCopy() *SREPolicy {
	if in == nil {
		return nil
	}
	out := new(SREPolicy)
	in.DeepCopyInto(out)
	return out
}

func (in *SREPolicy) DeepCopyObject() runtime.Object {
	if c := in.DeepCopy(); c != nil {
		return c
	}
	return nil
}

func (in *SREPolicyList) DeepCopyInto(out *SREPolicyList) {
	*out = *in
	out.TypeMeta = in.TypeMeta
	in.ListMeta.DeepCopyInto(&out.ListMeta)
	if in.Items != nil {
		in, out := &in.Items, &out.Items
		*out = make([]SREPolicy, len(*in))
		for i := range *in {
			(*in)[i].DeepCopyInto(&(*out)[i])
		}
	}
}

func (in *SREPolicyList) DeepCopy() *SREPolicyList {
	if in == nil {
		return nil
	}
	out := new(SREPolicyList)
	in.DeepCopyInto(out)
	return out
}

func (in *SREPolicyList) DeepCopyObject() runtime.Object {
	if c := in.DeepCopy(); c != nil {
		return c
	}
	return nil
}
