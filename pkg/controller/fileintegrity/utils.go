package fileintegrity

import (
	"os"
)

type FileIntegrityComponent uint

const (
	AIDE = iota
	LOGCOLLECTOR
)

var componentDefaults = []struct {
	defaultImage string
	envVar       string
}{
	{"quay.io/file-integrity-operator/aide:latest", "AIDE_IMAGE"},
	{"quay.io/file-integrity-operator/file-integrity-logcollector:latest", "LOGCOLLECTOR_IMAGE"},
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
