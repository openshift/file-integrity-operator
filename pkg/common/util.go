package common

import (
	"context"
	"fmt"
	"os"

	"k8s.io/apimachinery/pkg/labels"
	"k8s.io/apimachinery/pkg/types"

	"sigs.k8s.io/controller-runtime/pkg/client"

	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	kerr "k8s.io/apimachinery/pkg/api/errors"
)

type FileIntegrityComponent uint

const (
	AIDE = iota
	OPERATOR
)

var componentDefaults = []struct {
	defaultImage string
	envVar       string
}{
	{"quay.io/file-integrity-operator/aide:latest", "RELATED_IMAGE_AIDE"},
	{"quay.io/file-integrity-operator/file-integrity-operator:latest", "RELATED_IMAGE_OPERATOR"},
}

// GetComponentImage returns a full image pull spec for a given component
// based on the component type
func GetComponentImage(component FileIntegrityComponent) string {
	comp := componentDefaults[component]

	imageTag := os.Getenv(comp.envVar)
	if imageTag == "" {
		imageTag = comp.defaultImage
	}
	return imageTag
}

// IsAideConfig returns whether the given map contains a
// label that indicates that this is an AIDE config.
func IsAideConfig(labels map[string]string) bool {
	_, ok := labels[AideConfigLabelKey]
	return ok
}

// IsIntegrityLog returns whether the given map contains a
// log from the integrity check
func IsIntegrityLog(labels map[string]string) bool {
	_, ok := labels[IntegrityLogLabelKey]
	return ok
}

// IsIntegrityLogAnError returns whether the given map coming
// from an integrity check logcollector contains an error
func IsIntegrityLogAnError(cm *corev1.ConfigMap) bool {
	_, containsErrorAnnotation := cm.Annotations[IntegrityLogErrorAnnotationKey]
	return containsErrorAnnotation
}

// IsIntegrityLogAFailure returns whether the given map coming
// from an integrity check logcollector contains an failure
func IsIntegrityLogAFailure(cm *corev1.ConfigMap) bool {
	return cm.Data[IntegrityLogContentKey] != ""
}

// GetConfigMapOwnerName gets the name of the FileIntegrity that owns
// the config map from the Labels
func GetConfigMapOwnerName(cm *corev1.ConfigMap) (string, error) {
	owner, ok := cm.Labels[IntegrityConfigMapOwnerLabelKey]
	if !ok {
		return "", fmt.Errorf("ConfigMap '%s' had no owner label", cm.Name)
	}
	return owner, nil
}

// GetConfigMapNodeName gets the name of the node where
// the config map was generated from
func GetConfigMapNodeName(cm *corev1.ConfigMap) (string, error) {
	owner, ok := cm.Labels[IntegrityConfigMapNodeLabelKey]
	if !ok {
		return "", fmt.Errorf("ConfigMap '%s' had no node label", cm.Name)
	}
	return owner, nil
}

func DaemonSetIsReady(ds *appsv1.DaemonSet) bool {
	return ds.Status.DesiredNumberScheduled > 0 && ds.Status.DesiredNumberScheduled == ds.Status.NumberAvailable
}

func DaemonSetIsUpdating(ds *appsv1.DaemonSet) bool {
	return ds.Status.UpdatedNumberScheduled > 0 &&
		(ds.Status.UpdatedNumberScheduled < ds.Status.DesiredNumberScheduled || ds.Status.NumberUnavailable > 0)
}

// IgnoreAlreadyExists will not return an error if the error is that the resource
// already exists.
func IgnoreAlreadyExists(err error) error {
	if kerr.IsAlreadyExists(err) {
		return nil
	}
	return err
}

// GetScriptName returns the name of a configMap for a FI object with a
// given name
func GetScriptName(fiName string) string {
	return AideScriptConfigMapPrefix + "-" + fiName
}

// GetDaemonSetName gets the name of the owner (usually a FileIntegrity CR) and
// returns an appropriate name for the DaemonSet that's owned by it.
func GetDaemonSetName(name string) string {
	return DaemonSetPrefix + "-" + name
}

// GetReinitDaemonSetName gets the name of the owner (usually a FileIntegrity CR) and
// returns an appropriate name for the DaemonSet that's owned by it.
func GetReinitDaemonSetName(name string) string {
	return ReinitDaemonSetPrefix + "-" + name
}

// RestartFileIntegrityDs restarts all pods that belong to a given DaemonSet. This can be
// used to e.g. remount a configMap after it had changed or restart a FI DS after a re-init
// had happened
func RestartFileIntegrityDs(c client.Client, dsName string) error {
	ds := &appsv1.DaemonSet{}
	err := c.Get(context.TODO(), types.NamespacedName{Name: dsName, Namespace: FileIntegrityNamespace}, ds)
	if err != nil {
		return err
	}

	if err := deleteDaemonSetPods(c, ds); err != nil {
		return err
	}

	return nil
}

func deleteDaemonSetPods(c client.Client, ds *appsv1.DaemonSet) error {
	var pods corev1.PodList

	if err := c.List(context.TODO(), &pods, &client.ListOptions{
		LabelSelector: labels.SelectorFromSet(labels.Set{"app": ds.Name}),
		Namespace:     FileIntegrityNamespace,
	}); err != nil {
		return err
	}

	for _, pod := range pods.Items {
		err := c.Delete(context.TODO(), &pod, &client.DeleteOptions{})
		if err != nil {
			return err
		}
	}

	return nil
}
