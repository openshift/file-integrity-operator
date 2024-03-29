//go:build !ignore_autogenerated
// +build !ignore_autogenerated

/*
Copyright © 2019 - 2022 Red Hat Inc.

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

// Code generated by controller-gen. DO NOT EDIT.

package v1alpha1

import (
	"k8s.io/api/core/v1"
	runtime "k8s.io/apimachinery/pkg/runtime"
)

// DeepCopyInto is an autogenerated deepcopy function, copying the receiver, writing into out. in must be non-nil.
func (in *FileIntegrity) DeepCopyInto(out *FileIntegrity) {
	*out = *in
	out.TypeMeta = in.TypeMeta
	in.ObjectMeta.DeepCopyInto(&out.ObjectMeta)
	in.Spec.DeepCopyInto(&out.Spec)
	out.Status = in.Status
}

// DeepCopy is an autogenerated deepcopy function, copying the receiver, creating a new FileIntegrity.
func (in *FileIntegrity) DeepCopy() *FileIntegrity {
	if in == nil {
		return nil
	}
	out := new(FileIntegrity)
	in.DeepCopyInto(out)
	return out
}

// DeepCopyObject is an autogenerated deepcopy function, copying the receiver, creating a new runtime.Object.
func (in *FileIntegrity) DeepCopyObject() runtime.Object {
	if c := in.DeepCopy(); c != nil {
		return c
	}
	return nil
}

// DeepCopyInto is an autogenerated deepcopy function, copying the receiver, writing into out. in must be non-nil.
func (in *FileIntegrityConfig) DeepCopyInto(out *FileIntegrityConfig) {
	*out = *in
}

// DeepCopy is an autogenerated deepcopy function, copying the receiver, creating a new FileIntegrityConfig.
func (in *FileIntegrityConfig) DeepCopy() *FileIntegrityConfig {
	if in == nil {
		return nil
	}
	out := new(FileIntegrityConfig)
	in.DeepCopyInto(out)
	return out
}

// DeepCopyInto is an autogenerated deepcopy function, copying the receiver, writing into out. in must be non-nil.
func (in *FileIntegrityList) DeepCopyInto(out *FileIntegrityList) {
	*out = *in
	out.TypeMeta = in.TypeMeta
	in.ListMeta.DeepCopyInto(&out.ListMeta)
	if in.Items != nil {
		in, out := &in.Items, &out.Items
		*out = make([]FileIntegrity, len(*in))
		for i := range *in {
			(*in)[i].DeepCopyInto(&(*out)[i])
		}
	}
}

// DeepCopy is an autogenerated deepcopy function, copying the receiver, creating a new FileIntegrityList.
func (in *FileIntegrityList) DeepCopy() *FileIntegrityList {
	if in == nil {
		return nil
	}
	out := new(FileIntegrityList)
	in.DeepCopyInto(out)
	return out
}

// DeepCopyObject is an autogenerated deepcopy function, copying the receiver, creating a new runtime.Object.
func (in *FileIntegrityList) DeepCopyObject() runtime.Object {
	if c := in.DeepCopy(); c != nil {
		return c
	}
	return nil
}

// DeepCopyInto is an autogenerated deepcopy function, copying the receiver, writing into out. in must be non-nil.
func (in *FileIntegrityNodeStatus) DeepCopyInto(out *FileIntegrityNodeStatus) {
	*out = *in
	out.TypeMeta = in.TypeMeta
	in.ObjectMeta.DeepCopyInto(&out.ObjectMeta)
	if in.Results != nil {
		in, out := &in.Results, &out.Results
		*out = make([]FileIntegrityScanResult, len(*in))
		for i := range *in {
			(*in)[i].DeepCopyInto(&(*out)[i])
		}
	}
	in.LastResult.DeepCopyInto(&out.LastResult)
}

// DeepCopy is an autogenerated deepcopy function, copying the receiver, creating a new FileIntegrityNodeStatus.
func (in *FileIntegrityNodeStatus) DeepCopy() *FileIntegrityNodeStatus {
	if in == nil {
		return nil
	}
	out := new(FileIntegrityNodeStatus)
	in.DeepCopyInto(out)
	return out
}

// DeepCopyObject is an autogenerated deepcopy function, copying the receiver, creating a new runtime.Object.
func (in *FileIntegrityNodeStatus) DeepCopyObject() runtime.Object {
	if c := in.DeepCopy(); c != nil {
		return c
	}
	return nil
}

// DeepCopyInto is an autogenerated deepcopy function, copying the receiver, writing into out. in must be non-nil.
func (in *FileIntegrityNodeStatusList) DeepCopyInto(out *FileIntegrityNodeStatusList) {
	*out = *in
	out.TypeMeta = in.TypeMeta
	in.ListMeta.DeepCopyInto(&out.ListMeta)
	if in.Items != nil {
		in, out := &in.Items, &out.Items
		*out = make([]FileIntegrityNodeStatus, len(*in))
		for i := range *in {
			(*in)[i].DeepCopyInto(&(*out)[i])
		}
	}
}

// DeepCopy is an autogenerated deepcopy function, copying the receiver, creating a new FileIntegrityNodeStatusList.
func (in *FileIntegrityNodeStatusList) DeepCopy() *FileIntegrityNodeStatusList {
	if in == nil {
		return nil
	}
	out := new(FileIntegrityNodeStatusList)
	in.DeepCopyInto(out)
	return out
}

// DeepCopyObject is an autogenerated deepcopy function, copying the receiver, creating a new runtime.Object.
func (in *FileIntegrityNodeStatusList) DeepCopyObject() runtime.Object {
	if c := in.DeepCopy(); c != nil {
		return c
	}
	return nil
}

// DeepCopyInto is an autogenerated deepcopy function, copying the receiver, writing into out. in must be non-nil.
func (in *FileIntegrityScanResult) DeepCopyInto(out *FileIntegrityScanResult) {
	*out = *in
	in.LastProbeTime.DeepCopyInto(&out.LastProbeTime)
}

// DeepCopy is an autogenerated deepcopy function, copying the receiver, creating a new FileIntegrityScanResult.
func (in *FileIntegrityScanResult) DeepCopy() *FileIntegrityScanResult {
	if in == nil {
		return nil
	}
	out := new(FileIntegrityScanResult)
	in.DeepCopyInto(out)
	return out
}

// DeepCopyInto is an autogenerated deepcopy function, copying the receiver, writing into out. in must be non-nil.
func (in *FileIntegritySpec) DeepCopyInto(out *FileIntegritySpec) {
	*out = *in
	if in.NodeSelector != nil {
		in, out := &in.NodeSelector, &out.NodeSelector
		*out = make(map[string]string, len(*in))
		for key, val := range *in {
			(*out)[key] = val
		}
	}
	out.Config = in.Config
	if in.Tolerations != nil {
		in, out := &in.Tolerations, &out.Tolerations
		*out = make([]v1.Toleration, len(*in))
		for i := range *in {
			(*in)[i].DeepCopyInto(&(*out)[i])
		}
	}
}

// DeepCopy is an autogenerated deepcopy function, copying the receiver, creating a new FileIntegritySpec.
func (in *FileIntegritySpec) DeepCopy() *FileIntegritySpec {
	if in == nil {
		return nil
	}
	out := new(FileIntegritySpec)
	in.DeepCopyInto(out)
	return out
}

// DeepCopyInto is an autogenerated deepcopy function, copying the receiver, writing into out. in must be non-nil.
func (in *FileIntegrityStatus) DeepCopyInto(out *FileIntegrityStatus) {
	*out = *in
}

// DeepCopy is an autogenerated deepcopy function, copying the receiver, creating a new FileIntegrityStatus.
func (in *FileIntegrityStatus) DeepCopy() *FileIntegrityStatus {
	if in == nil {
		return nil
	}
	out := new(FileIntegrityStatus)
	in.DeepCopyInto(out)
	return out
}
