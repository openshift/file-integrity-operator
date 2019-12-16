package common

import (
	appsv1 "k8s.io/api/apps/v1"
)

// IsAideConfig returns whether the given map contains a
// label that indicates that this is an AIDE config.
func IsAideConfig(labels map[string]string) bool {
	_, ok := labels[AideConfigLabelKey]
	return ok
}

func DaemonSetIsReady(ds *appsv1.DaemonSet) bool {
	return ds.Status.DesiredNumberScheduled == ds.Status.NumberAvailable
}

func DaemonSetIsUpdating(ds *appsv1.DaemonSet) bool {
	return ds.Status.UpdatedNumberScheduled > 0 &&
		(ds.Status.UpdatedNumberScheduled < ds.Status.DesiredNumberScheduled || ds.Status.NumberUnavailable > 0)
}
