// Copyright The prometheus-operator Authors
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// You may obtain a copy of the License at
//
//     http://www.apache.org/licenses/LICENSE-2.0
//
// Unless required by applicable law or agreed to in writing, software
// distributed under the License is distributed on an "AS IS" BASIS,
// WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
// See the License for the specific language governing permissions and
// limitations under the License.

// Code generated by client-gen. DO NOT EDIT.

package fake

import (
	"context"
	json "encoding/json"
	"fmt"

	v1beta1 "github.com/prometheus-operator/prometheus-operator/pkg/apis/monitoring/v1beta1"
	monitoringv1beta1 "github.com/prometheus-operator/prometheus-operator/pkg/client/applyconfiguration/monitoring/v1beta1"
	v1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	labels "k8s.io/apimachinery/pkg/labels"
	types "k8s.io/apimachinery/pkg/types"
	watch "k8s.io/apimachinery/pkg/watch"
	testing "k8s.io/client-go/testing"
)

// FakeAlertmanagerConfigs implements AlertmanagerConfigInterface
type FakeAlertmanagerConfigs struct {
	Fake *FakeMonitoringV1beta1
	ns   string
}

var alertmanagerconfigsResource = v1beta1.SchemeGroupVersion.WithResource("alertmanagerconfigs")

var alertmanagerconfigsKind = v1beta1.SchemeGroupVersion.WithKind("AlertmanagerConfig")

// Get takes name of the alertmanagerConfig, and returns the corresponding alertmanagerConfig object, and an error if there is any.
func (c *FakeAlertmanagerConfigs) Get(ctx context.Context, name string, options v1.GetOptions) (result *v1beta1.AlertmanagerConfig, err error) {
	obj, err := c.Fake.
		Invokes(testing.NewGetAction(alertmanagerconfigsResource, c.ns, name), &v1beta1.AlertmanagerConfig{})

	if obj == nil {
		return nil, err
	}
	return obj.(*v1beta1.AlertmanagerConfig), err
}

// List takes label and field selectors, and returns the list of AlertmanagerConfigs that match those selectors.
func (c *FakeAlertmanagerConfigs) List(ctx context.Context, opts v1.ListOptions) (result *v1beta1.AlertmanagerConfigList, err error) {
	obj, err := c.Fake.
		Invokes(testing.NewListAction(alertmanagerconfigsResource, alertmanagerconfigsKind, c.ns, opts), &v1beta1.AlertmanagerConfigList{})

	if obj == nil {
		return nil, err
	}

	label, _, _ := testing.ExtractFromListOptions(opts)
	if label == nil {
		label = labels.Everything()
	}
	list := &v1beta1.AlertmanagerConfigList{ListMeta: obj.(*v1beta1.AlertmanagerConfigList).ListMeta}
	for _, item := range obj.(*v1beta1.AlertmanagerConfigList).Items {
		if label.Matches(labels.Set(item.Labels)) {
			list.Items = append(list.Items, item)
		}
	}
	return list, err
}

// Watch returns a watch.Interface that watches the requested alertmanagerConfigs.
func (c *FakeAlertmanagerConfigs) Watch(ctx context.Context, opts v1.ListOptions) (watch.Interface, error) {
	return c.Fake.
		InvokesWatch(testing.NewWatchAction(alertmanagerconfigsResource, c.ns, opts))

}

// Create takes the representation of a alertmanagerConfig and creates it.  Returns the server's representation of the alertmanagerConfig, and an error, if there is any.
func (c *FakeAlertmanagerConfigs) Create(ctx context.Context, alertmanagerConfig *v1beta1.AlertmanagerConfig, opts v1.CreateOptions) (result *v1beta1.AlertmanagerConfig, err error) {
	obj, err := c.Fake.
		Invokes(testing.NewCreateAction(alertmanagerconfigsResource, c.ns, alertmanagerConfig), &v1beta1.AlertmanagerConfig{})

	if obj == nil {
		return nil, err
	}
	return obj.(*v1beta1.AlertmanagerConfig), err
}

// Update takes the representation of a alertmanagerConfig and updates it. Returns the server's representation of the alertmanagerConfig, and an error, if there is any.
func (c *FakeAlertmanagerConfigs) Update(ctx context.Context, alertmanagerConfig *v1beta1.AlertmanagerConfig, opts v1.UpdateOptions) (result *v1beta1.AlertmanagerConfig, err error) {
	obj, err := c.Fake.
		Invokes(testing.NewUpdateAction(alertmanagerconfigsResource, c.ns, alertmanagerConfig), &v1beta1.AlertmanagerConfig{})

	if obj == nil {
		return nil, err
	}
	return obj.(*v1beta1.AlertmanagerConfig), err
}

// Delete takes name of the alertmanagerConfig and deletes it. Returns an error if one occurs.
func (c *FakeAlertmanagerConfigs) Delete(ctx context.Context, name string, opts v1.DeleteOptions) error {
	_, err := c.Fake.
		Invokes(testing.NewDeleteActionWithOptions(alertmanagerconfigsResource, c.ns, name, opts), &v1beta1.AlertmanagerConfig{})

	return err
}

// DeleteCollection deletes a collection of objects.
func (c *FakeAlertmanagerConfigs) DeleteCollection(ctx context.Context, opts v1.DeleteOptions, listOpts v1.ListOptions) error {
	action := testing.NewDeleteCollectionAction(alertmanagerconfigsResource, c.ns, listOpts)

	_, err := c.Fake.Invokes(action, &v1beta1.AlertmanagerConfigList{})
	return err
}

// Patch applies the patch and returns the patched alertmanagerConfig.
func (c *FakeAlertmanagerConfigs) Patch(ctx context.Context, name string, pt types.PatchType, data []byte, opts v1.PatchOptions, subresources ...string) (result *v1beta1.AlertmanagerConfig, err error) {
	obj, err := c.Fake.
		Invokes(testing.NewPatchSubresourceAction(alertmanagerconfigsResource, c.ns, name, pt, data, subresources...), &v1beta1.AlertmanagerConfig{})

	if obj == nil {
		return nil, err
	}
	return obj.(*v1beta1.AlertmanagerConfig), err
}

// Apply takes the given apply declarative configuration, applies it and returns the applied alertmanagerConfig.
func (c *FakeAlertmanagerConfigs) Apply(ctx context.Context, alertmanagerConfig *monitoringv1beta1.AlertmanagerConfigApplyConfiguration, opts v1.ApplyOptions) (result *v1beta1.AlertmanagerConfig, err error) {
	if alertmanagerConfig == nil {
		return nil, fmt.Errorf("alertmanagerConfig provided to Apply must not be nil")
	}
	data, err := json.Marshal(alertmanagerConfig)
	if err != nil {
		return nil, err
	}
	name := alertmanagerConfig.Name
	if name == nil {
		return nil, fmt.Errorf("alertmanagerConfig.Name must be provided to Apply")
	}
	obj, err := c.Fake.
		Invokes(testing.NewPatchSubresourceAction(alertmanagerconfigsResource, c.ns, *name, types.ApplyPatchType, data), &v1beta1.AlertmanagerConfig{})

	if obj == nil {
		return nil, err
	}
	return obj.(*v1beta1.AlertmanagerConfig), err
}
