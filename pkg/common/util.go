package common

import (
	"context"
	"errors"
	"fmt"
	"io/ioutil"
	"os"
	"os/exec"
	"strconv"

	"k8s.io/apimachinery/pkg/labels"
	"k8s.io/apimachinery/pkg/types"

	"sigs.k8s.io/controller-runtime/pkg/client"

	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	kerr "k8s.io/apimachinery/pkg/api/errors"
)

type FileIntegrityComponent uint

const (
	OPERATOR = iota
)

const AIDE_RETFAIL = 255

var componentDefaults = []struct {
	defaultImage string
	envVar       string
}{
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
