package main

import (
	"encoding/base64"
	"fmt"
	"log"
	"os"
	"path/filepath"
	"strings"

	"gopkg.in/yaml.v3"
)

func readCSV(csvFilename string, csv *map[string]interface{}) {
	yamlFile, err := os.ReadFile(csvFilename)
	if err != nil {
		log.Fatal(fmt.Sprintf("Error: Failed to read file '%s'", csvFilename))
	}

	err = yaml.Unmarshal(yamlFile, csv)
	if err != nil {
		log.Fatal(fmt.Sprintf("Error: Failed to unmarshal yaml file '%s'", csvFilename))
	}
}

func replaceCSV(csvFilename string, outputCSVFilename string, csv map[string]interface{}) {
	err := os.Remove(csvFilename)
	if err != nil {
		log.Fatal(fmt.Sprintf("Error: Failed to remofe file '%s'", csvFilename))
	}

	f, err := os.Create(outputCSVFilename)
	if err != nil {
		log.Fatal(fmt.Sprintf("Error: Failed to create file '%s'", outputCSVFilename))
	}

	enc := yaml.NewEncoder(f)
	defer enc.Close()
	enc.SetIndent(2)

	err = enc.Encode(csv)
	if err != nil {
		log.Fatal("Error: Failed encode the CSV into yaml")
	}
}

func getInputCSVFilePath(dir string) string {
	filenames, err := os.ReadDir(dir)
	if err != nil {
		log.Fatal("Failed to find manifest dir")
	}

	for _, filename := range filenames {
		if strings.HasSuffix(filename.Name(), "clusterserviceversion.yaml") {
			return filepath.Join(dir, filename.Name())
		}
	}

	log.Fatal("Failed to find CSV file in manifest dir")
	return ""
}

func getOutputCSVFilePath(dir string, version string) string {
	return filepath.Join(dir, fmt.Sprintf("file-integrity-operator.v%s.clusterserviceversion.yaml", version))
}

func addRequiredAnnotations(csv map[string]interface{}) {
	requiredAnnotations := map[string]string{
		"features.operators.openshift.io/cnf":              "false",
		"features.operators.openshift.io/cni":              "false",
		"features.operators.openshift.io/csi":              "false",
		"features.operators.openshift.io/disconnected":     "true",
		"features.operators.openshift.io/fips-compliant":   "true",
		"features.operators.openshift.io/proxy-aware":      "false",
		"features.operators.openshift.io/tls-profiles":     "false",
		"features.operators.openshift.io/token-auth-aws":   "false",
		"features.operators.openshift.io/token-auth-azure": "false",
		"features.operators.openshift.io/token-auth-gcp":   "false",
	}

	annotations, ok := csv["metadata"].(map[string]interface{})["annotations"].(map[string]interface{})
	if !ok {
		log.Fatal("Error: 'annotations' does not exist within 'metadata' in the CSV content")
	}

	for key, value := range requiredAnnotations {
		annotations[key] = value
	}
	fmt.Println("Added required annotations")
}

func replaceVersion(oldVersion, newVersion string, csv map[string]interface{}) {
	spec, ok := csv["spec"].(map[string]interface{})
	metadata, ok := csv["metadata"].(map[string]interface{})
	if !ok {
		log.Fatal("Error: 'spec' does not exist in the CSV content")
	}

	fmt.Println(fmt.Sprintf("Updating version references from %s to %s", oldVersion, newVersion))

	spec["version"] = newVersion
	spec["replaces"] = "file-integrity-operator.v" + oldVersion

	metadata["name"] = strings.Replace(metadata["name"].(string), oldVersion, newVersion, 1)

	annotations := metadata["annotations"].(map[string]interface{})
	annotations["olm.skipRange"] = strings.Replace(annotations["olm.skipRange"].(string), oldVersion, newVersion, 1)

	fmt.Println(fmt.Sprintf("Updated version references from %s to %s", oldVersion, newVersion))
}

func replaceIcon(csv map[string]interface{}) {

	s, ok := csv["spec"]
	if !ok {
		log.Fatal("Error: 'spec' does not exist in the CSV content")
	}
	spec := s.(map[string]interface{})

	iconPath := "../bundle/icons/icon.png"
	iconData, err := os.ReadFile(iconPath)
	if err != nil {
		log.Fatal(fmt.Sprintf("Error: Failed to read icon file '%s'", iconPath))
	}
	icon := make(map[string]string)
	icon["base64data"] = base64.StdEncoding.EncodeToString(iconData)
	icon["media"] = "image/png"

	var icons = make([]map[string]string, 1)
	icons[0] = icon

	spec["icon"] = icons

	fmt.Println(fmt.Sprintf("Updated the operator image to use icon in %s", iconPath))
}

func recoverFromReplaceImages() {
	if r := recover(); r != nil {
		log.Fatal("Error: It was not possible to replace RELATED_IMAGE_OPERATOR")
	}
}

func replaceImages(csv map[string]interface{}) {
	defer recoverFromReplaceImages()

	// Konflux will automatically update the image sha based on the most
	// recent builds. We want to peel off the SHA and append it to the Red
	// Hat registry so that the bundle image will work when it's available
	// there.
	konfluxPullSpec := "quay.io/redhat-user-workloads/ocp-isc-tenant/file-integrity-operator-release@sha256:b350fbfa98e1b3199f791e360d90a34c0b3745dfc68425f3b593a5f0291b0169"
	delimiter := "@"
	parts := strings.Split(konfluxPullSpec, delimiter)
	if len(parts) > 2 {
		log.Fatalf("Error: Failed to safely determine image SHA from Konflux pull spec: %s", konfluxPullSpec)
	}
	imageSha := parts[1]
	registry := "registry.redhat.io/compliance/openshift-file-integrity-rhel8-operator"
	redHatPullSpec := registry + delimiter + imageSha

	env, ok := csv["spec"].(map[string]interface{})["install"].(map[string]interface{})["spec"].(map[string]interface{})["deployments"].([]interface{})[0].(map[string]interface{})["spec"].(map[string]interface{})["template"].(map[string]interface{})["spec"].(map[string]interface{})["containers"].([]interface{})[0].(map[string]interface{})["env"].([]interface{})
	if !ok {
		log.Fatal("Error: 'env' with RELATED_IMAGE_OPERATOR does not exist in the CSV content")
	}

	for _, item := range env {
		variable := item.(map[string]interface{})
		if variable["name"] == "RELATED_IMAGE_OPERATOR" {
			variable["value"] = redHatPullSpec
		}
	}

	containersMap := csv["spec"].(map[string]interface{})["install"].(map[string]interface{})["spec"].(map[string]interface{})["deployments"].([]interface{})[0].(map[string]interface{})["spec"].(map[string]interface{})["template"].(map[string]interface{})["spec"].(map[string]interface{})["containers"].([]interface{})[0].(map[string]interface{})
	containersMap["image"] = redHatPullSpec

	fmt.Println("Updated the deployment manifest to use downstream builds")
}

func removeRelated(csv map[string]interface{}) {
	spec, ok := csv["spec"].(map[string]interface{})
	if !ok {
		log.Fatal("Error: 'spec' does not exist in the CSV content")
	}

	delete(spec, "relatedImages")
	fmt.Println("Removed the operator from operator manifest")
}

func main() {
	var csv map[string]interface{}

	manifestsDir := os.Args[1]
	oldVersion := os.Args[2]
	newVersion := os.Args[3]

	csvFilename := getInputCSVFilePath(manifestsDir)
	fmt.Println(fmt.Sprintf("Found manifest in %s", csvFilename))

	readCSV(csvFilename, &csv)

	addRequiredAnnotations(csv)
	replaceVersion(oldVersion, newVersion, csv)
	replaceIcon(csv)
	replaceImages(csv)
	removeRelated(csv)

	outputCSVFilename := getOutputCSVFilePath(manifestsDir, newVersion)
	replaceCSV(csvFilename, outputCSVFilename, csv)
	fmt.Println(fmt.Sprintf("Replaced CSV manifest for %s", newVersion))
}
