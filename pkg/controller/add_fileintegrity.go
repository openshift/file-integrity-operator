package controller

import (
	"github.com/openshift/file-integrity-operator/pkg/controller/fileintegrity"
)

func init() {
	// AddToManagerFuncs is a list of functions to create controllers and add them to a manager.
	AddToManagerFuncs = append(AddToManagerFuncs, fileintegrity.Add)
}
