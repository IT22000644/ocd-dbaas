package v1alpha1

import (
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
)

func (in *DBInstance) DeepCopyInto(out *DBInstance) {
	*out = *in
	out.TypeMeta = in.TypeMeta
	in.ObjectMeta.DeepCopyInto(&out.ObjectMeta)
	in.Spec.DeepCopyInto(&out.Spec)
	in.Status.DeepCopyInto(&out.Status)
}

func (in *DBInstance) DeepCopy() *DBInstance {
	if in == nil {
		return nil
	}
	out := new(DBInstance)
	in.DeepCopyInto(out)
	return out
}

func (in *DBInstance) DeepCopyObject() runtime.Object {
	if c := in.DeepCopy(); c != nil {
		return c
	}
	return nil
}

func (in *DBInstanceList) DeepCopyInto(out *DBInstanceList) {
	*out = *in
	out.TypeMeta = in.TypeMeta
	in.ListMeta.DeepCopyInto(&out.ListMeta)
	if in.Items != nil {
		in, out := &in.Items, &out.Items
		*out = make([]DBInstance, len(*in))
		for i := range *in {
			(*in)[i].DeepCopyInto(&(*out)[i])
		}
	}
}

func (in *DBInstanceList) DeepCopy() *DBInstanceList {
	if in == nil {
		return nil
	}
	out := new(DBInstanceList)
	in.DeepCopyInto(out)
	return out
}

func (in *DBInstanceList) DeepCopyObject() runtime.Object {
	if c := in.DeepCopy(); c != nil {
		return c
	}
	return nil
}

func (in *DBInstanceSpec) DeepCopyInto(out *DBInstanceSpec) {
	*out = *in
	if in.MasterUserPasswordRef != nil {
		in, out := &in.MasterUserPasswordRef, &out.MasterUserPasswordRef
		*out = new(SecretKeyRef)
		**out = **in
	}
	if in.Running != nil {
		in, out := &in.Running, &out.Running
		*out = new(bool)
		**out = **in
	}
	if in.S3BackupConfig != nil {
		in, out := &in.S3BackupConfig, &out.S3BackupConfig
		*out = new(S3BackupConfig)
		**out = **in
	}
	if in.Tags != nil {
		in, out := &in.Tags, &out.Tags
		*out = make(map[string]string, len(*in))
		for key, val := range *in {
			(*out)[key] = val
		}
	}
}

func (in *DBInstanceSpec) DeepCopy() *DBInstanceSpec {
	if in == nil {
		return nil
	}
	out := new(DBInstanceSpec)
	in.DeepCopyInto(out)
	return out
}

func (in *DBInstanceStatus) DeepCopyInto(out *DBInstanceStatus) {
	*out = *in
	if in.Conditions != nil {
		in, out := &in.Conditions, &out.Conditions
		*out = make([]metav1.Condition, len(*in))
		for i := range *in {
			(*in)[i].DeepCopyInto(&(*out)[i])
		}
	}
	if in.Endpoint != nil {
		in, out := &in.Endpoint, &out.Endpoint
		*out = new(Endpoint)
		**out = **in
	}
	if in.MasterUserSecret != nil {
		in, out := &in.MasterUserSecret, &out.MasterUserSecret
		*out = new(MasterUserSecretRef)
		**out = **in
	}
	if in.ReadReplicas != nil {
		in, out := &in.ReadReplicas, &out.ReadReplicas
		*out = make([]string, len(*in))
		copy(*out, *in)
	}
}

func (in *DBInstanceStatus) DeepCopy() *DBInstanceStatus {
	if in == nil {
		return nil
	}
	out := new(DBInstanceStatus)
	in.DeepCopyInto(out)
	return out
}

func (in *DBSnapshot) DeepCopyInto(out *DBSnapshot) {
	*out = *in
	out.TypeMeta = in.TypeMeta
	in.ObjectMeta.DeepCopyInto(&out.ObjectMeta)
	out.Spec = in.Spec
	in.Status.DeepCopyInto(&out.Status)
}

func (in *DBSnapshot) DeepCopy() *DBSnapshot {
	if in == nil {
		return nil
	}
	out := new(DBSnapshot)
	in.DeepCopyInto(out)
	return out
}

func (in *DBSnapshot) DeepCopyObject() runtime.Object {
	if c := in.DeepCopy(); c != nil {
		return c
	}
	return nil
}

func (in *DBSnapshotList) DeepCopyInto(out *DBSnapshotList) {
	*out = *in
	out.TypeMeta = in.TypeMeta
	in.ListMeta.DeepCopyInto(&out.ListMeta)
	if in.Items != nil {
		in, out := &in.Items, &out.Items
		*out = make([]DBSnapshot, len(*in))
		for i := range *in {
			(*in)[i].DeepCopyInto(&(*out)[i])
		}
	}
}

func (in *DBSnapshotList) DeepCopy() *DBSnapshotList {
	if in == nil {
		return nil
	}
	out := new(DBSnapshotList)
	in.DeepCopyInto(out)
	return out
}

func (in *DBSnapshotList) DeepCopyObject() runtime.Object {
	if c := in.DeepCopy(); c != nil {
		return c
	}
	return nil
}

func (in *DBSnapshotStatus) DeepCopyInto(out *DBSnapshotStatus) {
	*out = *in
	if in.Done != nil {
		in, out := &in.Done, &out.Done
		*out = (*in).DeepCopy()
	}
}

func (in *DBSnapshotStatus) DeepCopy() *DBSnapshotStatus {
	if in == nil {
		return nil
	}
	out := new(DBSnapshotStatus)
	in.DeepCopyInto(out)
	return out
}

func (in *DBParameterGroup) DeepCopyInto(out *DBParameterGroup) {
	*out = *in
	out.TypeMeta = in.TypeMeta
	in.ObjectMeta.DeepCopyInto(&out.ObjectMeta)
	in.Spec.DeepCopyInto(&out.Spec)
}

func (in *DBParameterGroup) DeepCopy() *DBParameterGroup {
	if in == nil {
		return nil
	}
	out := new(DBParameterGroup)
	in.DeepCopyInto(out)
	return out
}

func (in *DBParameterGroup) DeepCopyObject() runtime.Object {
	if c := in.DeepCopy(); c != nil {
		return c
	}
	return nil
}

func (in *DBParameterGroupList) DeepCopyInto(out *DBParameterGroupList) {
	*out = *in
	out.TypeMeta = in.TypeMeta
	in.ListMeta.DeepCopyInto(&out.ListMeta)
	if in.Items != nil {
		in, out := &in.Items, &out.Items
		*out = make([]DBParameterGroup, len(*in))
		for i := range *in {
			(*in)[i].DeepCopyInto(&(*out)[i])
		}
	}
}

func (in *DBParameterGroupList) DeepCopy() *DBParameterGroupList {
	if in == nil {
		return nil
	}
	out := new(DBParameterGroupList)
	in.DeepCopyInto(out)
	return out
}

func (in *DBParameterGroupList) DeepCopyObject() runtime.Object {
	if c := in.DeepCopy(); c != nil {
		return c
	}
	return nil
}

func (in *DBParameterGroupSpec) DeepCopyInto(out *DBParameterGroupSpec) {
	*out = *in
	if in.Parameters != nil {
		in, out := &in.Parameters, &out.Parameters
		*out = make(map[string]string, len(*in))
		for key, val := range *in {
			(*out)[key] = val
		}
	}
}

func (in *DBParameterGroupSpec) DeepCopy() *DBParameterGroupSpec {
	if in == nil {
		return nil
	}
	out := new(DBParameterGroupSpec)
	in.DeepCopyInto(out)
	return out
}
