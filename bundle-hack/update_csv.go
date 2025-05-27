package main

import (
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"

	"gopkg.in/yaml.v3"
)

// QuayTagResponse defines the structure for parsing the JSON response from the Quay API.
type QuayTagResponse struct {
	Tags []struct {
		ManifestDigest string `json:"manifest_digest"`
	} `json:"tags"`
}

// readCSV reads and unmarshals a YAML file into a map.
func readCSV(csvFilename string, csv *map[string]interface{}) {
	yamlFile, err := os.ReadFile(csvFilename)
	if err != nil {
		log.Fatalf("Error: Failed to read file '%s': %v", csvFilename, err)
	}

	err = yaml.Unmarshal(yamlFile, csv)
	if err != nil {
		log.Fatalf("Error: Failed to unmarshal yaml file '%s': %v", csvFilename, err)
	}
}

// replaceCSV writes a map to a new YAML file.
func replaceCSV(csvFilename string, outputCSVFilename string, csv map[string]interface{}) {
	err := os.Remove(csvFilename)
	if err != nil {
		log.Fatalf("Error: Failed to remove file '%s': %v", csvFilename, err)
	}

	f, err := os.Create(outputCSVFilename)
	if err != nil {
		log.Fatalf("Error: Failed to create file '%s': %v", outputCSVFilename, err)
	}
	defer f.Close()

	enc := yaml.NewEncoder(f)
	enc.SetIndent(2)
	defer enc.Close()

	err = enc.Encode(csv)
	if err != nil {
		log.Fatalf("Error: Failed to encode the CSV into yaml: %v", err)
	}
}

// getInputCSVFilePath finds the ClusterServiceVersion YAML file in a directory.
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

// getOutputCSVFilePath constructs the output file path for the new CSV.
func getOutputCSVFilePath(dir string, version string) string {
	return filepath.Join(dir, fmt.Sprintf("file-integrity-operator.v%s.clusterserviceversion.yaml", version))
}

// addRequiredAnnotations adds a set of required annotations to the CSV.
func addRequiredAnnotations(csv map[string]interface{}) {
	requiredAnnotations := map[string]string{
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

// replaceVersion updates version strings within the CSV.
func replaceVersion(oldVersion, newVersion string, csv map[string]interface{}) {
	spec, ok := csv["spec"].(map[string]interface{})
	if !ok {
		log.Fatal("Error: 'spec' does not exist in the CSV content")
	}
	metadata, ok := csv["metadata"].(map[string]interface{})
	if !ok {
		log.Fatal("Error: 'metadata' does not exist in the CSV content")
	}

	fmt.Printf("Updating version references from %s to %s\n", oldVersion, newVersion)

	spec["version"] = newVersion
	spec["replaces"] = "file-integrity-operator.v" + oldVersion

	metadata["name"] = strings.Replace(metadata["name"].(string), oldVersion, newVersion, 1)

	annotations := metadata["annotations"].(map[string]interface{})
	annotations["olm.skipRange"] = fmt.Sprintf(">=%s", newVersion)

	fmt.Printf("Updated version references from %s to %s\n", oldVersion, newVersion)
}

// replaceIcon updates the operator icon in the CSV.
func replaceIcon(csv map[string]interface{}) {
	spec, ok := csv["spec"].(map[string]interface{})
	if !ok {
		log.Fatal("Error: 'spec' does not exist in the CSV content")
	}

	iconPath := "../bundle/icons/icon.png"
	iconData, err := os.ReadFile(iconPath)
	if err != nil {
		log.Fatalf("Error: Failed to read icon file '%s': %v", iconPath, err)
	}

	icon := make(map[string]string)
	icon["base64data"] = base64.StdEncoding.EncodeToString(iconData)
	icon["mediatype"] = "image/png"

	var icons = make([]map[string]string, 1)
	icons[0] = icon

	spec["icon"] = icons

	fmt.Printf("Updated the operator image to use icon in %s\n", iconPath)
}

// recoverFromReplaceImages handles panics during image replacement.
func recoverFromReplaceImages() {
	if r := recover(); r != nil {
		log.Fatalf("Error: It was not possible to replace RELATED_IMAGE_OPERATOR: %v", r)
	}
}

// getLatestGitCommitSha retrieves the latest git commit SHA.
func getLatestGitCommitSha() string {
	cmd := exec.Command("git", "rev-parse", "HEAD")
	out, err := cmd.Output()
	if err != nil {
		log.Fatalf("Error getting latest git commit SHA: %v", err)
	}
	return strings.TrimSpace(string(out))
}

// getDigestFromQuay fetches the manifest digest for a specific tag from Quay.io,
// retrying for up to 30 minutes.
func getDigestFromQuay(tag string) string {
	const timeout = 30 * time.Minute
	const retryInterval = 1 * time.Minute

	startTime := time.Now()
	fmt.Printf("Attempting to find manifest digest for tag '%s'. Will retry for up to %v.\n", tag, timeout)

	for {
		// Check for timeout at the beginning of each iteration
		if time.Since(startTime) >= timeout {
			log.Fatalf("Timeout: Failed to find manifest digest for tag '%s' after %v.", tag, timeout)
		}

		var digestFound string = ""

		// Create and send the request
		url := fmt.Sprintf("https://quay.io/api/v1/repository/redhat-user-workloads/ocp-isc-tenant/file-integrity-operator/tag/?specificTag=%s", tag)
		resp, err := http.Get(url)

		if err != nil {
			log.Printf("Warning: Error fetching from Quay.io: %v.", err)
		} else {
			if resp.StatusCode == http.StatusOK {
				body, readErr := io.ReadAll(resp.Body)
				if readErr != nil {
					log.Printf("Warning: Error reading response body: %v.", readErr)
				} else {
					var quayResponse QuayTagResponse
					if jsonErr := json.Unmarshal(body, &quayResponse); jsonErr != nil {
						log.Printf("Warning: Error unmarshaling JSON: %v.", jsonErr)
					} else if len(quayResponse.Tags) > 0 && quayResponse.Tags[0].ManifestDigest != "" {
						digest := quayResponse.Tags[0].ManifestDigest
						fmt.Printf("Success: Found manifest digest '%s' after %v.\n", digest, time.Since(startTime).Round(time.Second))
						digestFound = digest // Store the digest to return later
					}
				}
			} else {
				bodyBytes, _ := io.ReadAll(resp.Body)
				log.Printf("Warning: Received non-200 status from Quay.io: %s. Body: %s.", resp.Status, string(bodyBytes))
			}
			// IMPORTANT: Close the body inside the loop to prevent resource leaks
			resp.Body.Close()
		}

		// If we found the digest, exit the loop and return it.
		if digestFound != "" {
			return digestFound
		}

		// Wait before the next retry
		log.Printf("Manifest digest not yet found. Retrying in %v...", retryInterval)
		time.Sleep(retryInterval)
	}
}

// replaceImages updates the operator and related images in the CSV.
func replaceImages(csv map[string]interface{}) {
	defer recoverFromReplaceImages()

	gitCommitSha := getLatestGitCommitSha()
	fmt.Printf("Using latest git commit SHA: %s\n", gitCommitSha)

	imageSha := getDigestFromQuay(gitCommitSha)
	fmt.Printf("Found manifest digest: %s\n", imageSha)

	registry := "registry.redhat.io/compliance/openshift-file-integrity-rhel8-operator"
	redHatPullSpec := registry + "@" + imageSha

	installSpec, ok := csv["spec"].(map[string]interface{})["install"].(map[string]interface{})["spec"].(map[string]interface{})
	if !ok {
		log.Fatal("Error: 'spec.install.spec' does not exist in the CSV content")
	}

	deployments, ok := installSpec["deployments"].([]interface{})
	if !ok || len(deployments) == 0 {
		log.Fatal("Error: 'deployments' not found in the CSV content")
	}
	deployment, ok := deployments[0].(map[string]interface{})
	if !ok {
		log.Fatal("Error: Could not process deployment in the CSV content")
	}

	podSpec, ok := deployment["spec"].(map[string]interface{})["template"].(map[string]interface{})["spec"].(map[string]interface{})
	if !ok {
		log.Fatal("Error: 'pod spec' not found in the CSV content")
	}

	containers, ok := podSpec["containers"].([]interface{})
	if !ok || len(containers) == 0 {
		log.Fatal("Error: 'containers' not found in the CSV content")
	}
	container, ok := containers[0].(map[string]interface{})
	if !ok {
		log.Fatal("Error: Could not process container in the CSV content")
	}

	// Update container image
	container["image"] = redHatPullSpec

	// Update RELATED_IMAGE_OPERATOR environment variable
	env, ok := container["env"].([]interface{})
	if !ok {
		log.Println("Warning: 'env' for RELATED_IMAGE_OPERATOR not found, creating it.")
		env = []interface{}{}
	}

	found := false
	for _, item := range env {
		variable, ok := item.(map[string]interface{})
		if !ok {
			continue
		}
		if name, ok := variable["name"].(string); ok && name == "RELATED_IMAGE_OPERATOR" {
			variable["value"] = redHatPullSpec
			found = true
			break
		}
	}

	if !found {
		env = append(env, map[string]interface{}{
			"name":  "RELATED_IMAGE_OPERATOR",
			"value": redHatPullSpec,
		})
		container["env"] = env
	}

	fmt.Println("Updated the deployment manifest to use downstream builds")
}

// removeRelated removes the 'relatedImages' section from the CSV.
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
	fmt.Printf("Found manifest in %s\n", csvFilename)

	readCSV(csvFilename, &csv)

	addRequiredAnnotations(csv)
	replaceVersion(oldVersion, newVersion, csv)
	replaceIcon(csv)
	replaceImages(csv)
	removeRelated(csv)

	outputCSVFilename := getOutputCSVFilePath(manifestsDir, newVersion)
	replaceCSV(csvFilename, outputCSVFilename, csv)
	fmt.Printf("Replaced CSV manifest for %s\n", newVersion)
}
