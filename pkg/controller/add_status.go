package controller

import (
	"github.com/openshift/file-integrity-operator/pkg/controller/status"
)

func init() {
	// AddToManagerFuncs is a list of functions to create controllers and add them to a manager.
	AddToManagerFuncs = append(AddToManagerFuncs, status.Add)
}
