package common

import (
	"context"
	"errors"
	"fmt"
	"io/ioutil"
	"os"
	"os/exec"
	"strconv"

	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	kerr "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/labels"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/client-go/discovery"
	"sigs.k8s.io/controller-runtime/pkg/client"

	monitoringv1 "github.com/prometheus-operator/prometheus-operator/pkg/apis/monitoring/v1"
)

type FileIntegrityComponent uint

const (
	OPERATOR = iota
)

const AIDE_RETFAIL = 255

const AIDE_IO_ERROR = 18

var componentDefaults = []struct {
	defaultImage string
	envVar       string
}{
	{"quay.io/file-integrity-operator/file-integrity-operator:latest", "RELATED_IMAGE_OPERATOR"},
}

// GetComponentImage returns a full image pull spec for a given component
// based on the component type, if override is set then we always use that.
func GetComponentImage(override string, component FileIntegrityComponent) string {
	comp := componentDefaults[component]

	imageTag := os.Getenv(comp.envVar)
	if len(override) > 0 {
		imageTag = override
	} else if imageTag == "" {
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
	owner, ok := cm.Labels[IntegrityOwnerLabelKey]
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

// DaemonSetName returns a friendly name for the AIDE daemonSet
func DaemonSetName(name string) string {
	return DNSLengthName(DaemonSetPrefix, "%s-%s", DaemonSetPrefix, name)
}

// ReinitDaemonSetName returns a friendly name for the re-init daemonSet
func ReinitDaemonSetName(name string) string {
	return DNSLengthName(ReinitDaemonSetPrefix, "%s-%s", ReinitDaemonSetPrefix, name)
}

// ReinitDaemonSetNodeName returns a friendly name for the re-init daemonSet for one node.
func ReinitDaemonSetNodeName(name, node string) string {
	if len(node) == 0 {
		return ReinitDaemonSetName(name)
	}
	return DNSLengthName(ReinitDaemonSetPrefix, "%s-%s-%s", ReinitDaemonSetPrefix, name, node)
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

	for podIdx := range pods.Items {
		pod := pods.Items[podIdx]
		err := c.Delete(context.TODO(), &pod, &client.DeleteOptions{})
		if err != nil {
			return err
		}
	}

	return nil
}

// getExitCode examines the error interface as returned from exec.Command().Run()
// and returns 0 if the command succeeded, the command's return code if it ran to completion
// but failed and a default error otherwise (e.g. on exit because of a signal)
//
// requires go 1.13 or newer due to using errors.As() and ExitCode
func getExitCode(runCmdErr error, defaultError int) int {
	// No error, awesome, the command exited successfully
	if runCmdErr == nil {
		return 0
	}

	var exitError *exec.ExitError
	if errors.As(runCmdErr, &exitError) {
		// ExitError.ExitCode() return the error code if the command produced any or -1
		// if the command exited for another reason (signal, ...)
		rv := exitError.ExitCode()
		if rv != -1 {
			return rv
		}
		// fall back to returning defaultError
	}
	// if the error is something else then exec.ExitError, just return the defaultError

	return defaultError
}

func fipsModeEnabled() (bool, error) {
	f, err := os.Open("/proc/sys/crypto/fips_enabled")
	if err != nil {
		return false, err
	}

	d, err := ioutil.ReadAll(f)
	if err != nil {
		if closeErr := f.Close(); closeErr != nil {
			return false, closeErr
		}
		return false, err
	}

	if len(d) == 0 {
		if closeErr := f.Close(); closeErr != nil {
			return false, closeErr
		}
		return false, fmt.Errorf("/proc/sys/crypto/fips_enabled has no contents")
	}

	fipsStatus, err := strconv.Atoi(string(d[0]))
	if err != nil {
		if closeErr := f.Close(); closeErr != nil {
			return false, closeErr
		}
		return false, err
	}

	if closeErr := f.Close(); closeErr != nil {
		return false, closeErr
	}

	return fipsStatus == 1, nil
}

func GetAideExitCode(runCmdError error) int {
	ecode := getExitCode(runCmdError, aideErrSentinel)
	if ecode == AIDE_RETFAIL {
		// This error varies in meaning depending on the FIPS mode, so we'll check for it here, and convert to the FIPS error.
		fipsEnabled, err := fipsModeEnabled()
		if err != nil {
			// We couldn't rely on figuring out the FIPS status, so return a "possible FIPS error" status.
			ecode = aideErrPossibleFips
		}
		if fipsEnabled {
			// We know this is a FIPS error.
			ecode = aideErrFips
		}
	}
	return ecode
}

const aideErrBase = 14

const (
	aideErrWriteErr = iota + aideErrBase
	aideErrEinval
	aideErrNotImplemented
	aideErrConfig
	aideErrIO
	aideErrVersionMismatch
	// These are FIO additions
	aideErrFips
	aideErrPossibleFips
	aideErrSentinel
)

var aideErrLookup = []struct {
	errCode   int
	errString string
}{
	{aideErrWriteErr, "Error writing error"},
	{aideErrEinval, "Invalid argument error"},
	{aideErrNotImplemented, "Unimplemented function error"},
	{aideErrConfig, "Invalid configureline error"},
	{aideErrIO, "IO error"},
	{aideErrVersionMismatch, "Version mismatch error"},
	{aideErrFips, "Use of FIPS disallowed algorithm under FIPS mode"},
	{aideErrPossibleFips, "Possible use of FIPS disallowed algorithm"},
	{aideErrSentinel, "Unexpected error"},
}

func GetAideErrorMessage(rv int) string {
	if rv < aideErrBase || rv > aideErrSentinel {
		// default to the sentinel error message for unknown or unexpected errors
		rv = aideErrSentinel
	}

	rv -= aideErrBase // the array index still starts at zero...
	return aideErrLookup[rv].errString
}

// ResourceExists returns true if the given resource kind exists
// in the given api groupversion
func ResourceExists(dc discovery.DiscoveryInterface, apiGroupVersion, kind string) (bool, error) {

	_, apiLists, err := dc.ServerGroupsAndResources()
	if err != nil {
		return false, err
	}
	for _, apiList := range apiLists {
		if apiList.GroupVersion == apiGroupVersion {
			for _, r := range apiList.APIResources {
				if r.Kind == kind {
					return true, nil
				}
			}
		}
	}
	return false, nil
}

var ErrServiceMonitorNotPresent = fmt.Errorf("no ServiceMonitor registered with the API")

type ServiceMonitorUpdater func(*monitoringv1.ServiceMonitor) error

// GenerateServiceMonitor generates a prometheus-operator ServiceMonitor object
// based on the passed Service object.
func GenerateServiceMonitor(s *corev1.Service) *monitoringv1.ServiceMonitor {
	labels := make(map[string]string)
	for k, v := range s.ObjectMeta.Labels {
		labels[k] = v
	}
	endpoints := populateEndpointsFromServicePorts(s)
	boolTrue := true

	return &monitoringv1.ServiceMonitor{
		ObjectMeta: metav1.ObjectMeta{
			Name:      s.ObjectMeta.Name,
			Namespace: s.ObjectMeta.Namespace,
			Labels:    labels,
			OwnerReferences: []metav1.OwnerReference{
				{
					APIVersion:         "v1",
					BlockOwnerDeletion: &boolTrue,
					Controller:         &boolTrue,
					Kind:               "Service",
					Name:               s.Name,
					UID:                s.UID,
				},
			},
		},
		Spec: monitoringv1.ServiceMonitorSpec{
			Selector: metav1.LabelSelector{
				MatchLabels: labels,
			},
			Endpoints: endpoints,
		},
	}
}

func populateEndpointsFromServicePorts(s *corev1.Service) []monitoringv1.Endpoint {
	var endpoints []monitoringv1.Endpoint
	for _, port := range s.Spec.Ports {
		endpoints = append(endpoints, monitoringv1.Endpoint{Port: port.Name})
	}
	return endpoints
}
