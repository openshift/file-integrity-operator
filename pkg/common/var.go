package common

import (
	"fmt"
	"io/ioutil"
	"os"
	"strings"
)

// ErrNoNamespace indicates that a namespace could not be found for the current
// environment
var ErrNoNamespace = fmt.Errorf("namespace not found for current environment")

var readSAFile = func() ([]byte, error) {
	return ioutil.ReadFile("/var/run/secrets/kubernetes.io/serviceaccount/namespace")
}

// GetOperatorNamespace returns the namespace the operator should be running in from
// the associated service account secret.
func GetOperatorNamespace() (string, error) {
	nsBytes, err := readSAFile()
	if err != nil {
		if os.IsNotExist(err) {
			return "", ErrNoNamespace
		}
		return "", err
	}
	ns := strings.TrimSpace(string(nsBytes))
	return ns, nil
}

// GetWatchNamespace returns the Namespace the operator should be watching for changes
func GetWatchNamespace() (string, error) {
	// WatchNamespaceEnvVar is the constant for env variable WATCH_NAMESPACE
	// which specifies the Namespace to watch.
	// An empty value means the operator is running with cluster scope.
	var watchNamespaceEnvVar = "WATCH_NAMESPACE"

	ns, found := os.LookupEnv(watchNamespaceEnvVar)
	if !found {
		return "", fmt.Errorf("%s must be set", watchNamespaceEnvVar)
	}
	return ns, nil
}

// FileIntegrityNamespace defines the namespace in which the operator is active on.
// When this package is imported, the namespace will be determined. If it can't be
// determined, it'll default to this set value.
var FileIntegrityNamespace = "openshift-file-integrity"

func init() {
	ns, err := GetOperatorNamespace()
	if err == nil {
		FileIntegrityNamespace = ns
	}
}
