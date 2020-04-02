package common

import (
	"github.com/operator-framework/operator-sdk/pkg/k8sutil"
)

// FileIntegrityNamespace defines the namespace in which the operator is active on.
// When this package is imported, the namespace will be determined. If it can't be
// determined, it'll default to this set value.
var FileIntegrityNamespace = "openshift-file-integrity"

func init() {
	ns, err := k8sutil.GetOperatorNamespace()
	if err == nil {
		FileIntegrityNamespace = ns
	}
}
