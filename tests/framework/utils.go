package framework

import (
	"context"
	"encoding/base64"
	"fmt"
	"log"
	"os/exec"
	"regexp"
	"strings"
	"time"

	configv1 "github.com/openshift/api/config/v1"
	tlspkg "github.com/openshift/controller-runtime-common/pkg/tls"
	"golang.org/x/mod/semver"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/apimachinery/pkg/util/wait"
)

// GetClusterAPIServer fetches the APIServer "cluster" resource.
func (f *Framework) GetClusterAPIServer() (*configv1.APIServer, error) {
	apiServer := &configv1.APIServer{}
	key := types.NamespacedName{Name: "cluster"}
	if err := f.Client.Get(context.TODO(), key, apiServer); err != nil {
		return nil, fmt.Errorf("failed to get APIServer cluster resource: %w", err)
	}
	return apiServer, nil
}

// GetExpectedMinTLSVersion returns the expected minimum TLS version string
// (e.g., "TLSv1.2", "TLSv1.3") for the metrics endpoint based on the
// cluster's APIServer TLS configuration and adherence policy.
func (f *Framework) GetExpectedMinTLSVersion(apiServer *configv1.APIServer) string {
	profile := extractTLSProfileForTest(apiServer)
	spec, err := tlspkg.GetTLSProfileSpec(profile)
	if err != nil {
		// Fall back to Intermediate defaults if profile resolution fails.
		spec = *configv1.TLSProfiles[configv1.TLSProfileIntermediateType]
	}
	switch spec.MinTLSVersion {
	case configv1.VersionTLS10:
		return "TLSv1.0"
	case configv1.VersionTLS11:
		return "TLSv1.1"
	case configv1.VersionTLS12:
		return "TLSv1.2"
	case configv1.VersionTLS13:
		return "TLSv1.3"
	default:
		return "TLSv1.2"
	}
}

// tlsVersionNumber maps a TLS version string (e.g. "TLSv1.2") to a numeric
// value for comparison. Higher values mean newer TLS versions.
var tlsVersionNumber = map[string]int{
	"TLSv1.0": 10,
	"TLSv1.1": 11,
	"TLSv1.2": 12,
	"TLSv1.3": 13,
}

// parseTLSVersionFromCurlOutput extracts the TLS version string from curl
// verbose output containing an "SSL connection using TLSvX.Y" line.
func parseTLSVersionFromCurlOutput(output string) string {
	re := regexp.MustCompile(`TLSv1\.[0-3]`)
	return re.FindString(output)
}

// tlsVersionAtLeast returns true if actual >= minimum using the TLS version
// ordering. Both arguments should be strings like "TLSv1.2".
func tlsVersionAtLeast(actual, minimum string) bool {
	a, aOK := tlsVersionNumber[actual]
	m, mOK := tlsVersionNumber[minimum]
	if !aOK || !mOK {
		return false
	}
	return a >= m
}

// AssertMetricsEndpointMinTLSVersion uses curl to connect to the metrics
// endpoint and verifies the negotiated TLS version is at least the expected
// minimum. The server may negotiate a higher version than the minimum (e.g.
// TLS 1.3 when the minimum is 1.2), which is correct behavior.
func (f *Framework) AssertMetricsEndpointMinTLSVersion(expectedMinTLSVersion string) error {
	endpoint := fmt.Sprintf("https://metrics.%s.svc:8585/metrics-co", f.OperatorNamespace)
	curlCMD := fmt.Sprintf("curl -vks %s 2>&1 | grep 'SSL connection'", endpoint)

	ocPath, err := exec.LookPath("oc")
	if err != nil {
		return fmt.Errorf("oc not found: %w", err)
	}

	var lastErr error
	timeouterr := wait.Poll(RetryInterval, Timeout, func() (bool, error) {
		// #nosec G204
		cmd := exec.Command(ocPath,
			"run", "--rm", "-i", "--restart=Never",
			"--image=registry.fedoraproject.org/fedora-minimal:latest",
			"-n", f.OperatorNamespace, "tls-version-test",
			"--", "bash", "-c", curlCMD,
		)
		out, err := cmd.CombinedOutput()
		if err != nil {
			lastErr = fmt.Errorf("curl command failed: %v, output: %s", err, string(out))
			log.Printf("%v... retrying\n", lastErr)
			return false, nil
		}

		output := string(out)
		actual := parseTLSVersionFromCurlOutput(output)
		if actual == "" {
			lastErr = fmt.Errorf("could not parse TLS version from output: %s", output)
			log.Printf("%v... retrying\n", lastErr)
			return false, nil
		}
		if !tlsVersionAtLeast(actual, expectedMinTLSVersion) {
			lastErr = fmt.Errorf("negotiated TLS version %s is below minimum %s", actual, expectedMinTLSVersion)
			log.Printf("%v... retrying\n", lastErr)
			return false, nil
		}
		log.Printf("metrics endpoint using %s (minimum: %s)\n", actual, expectedMinTLSVersion)
		return true, nil
	})
	if timeouterr != nil {
		if lastErr != nil {
			return lastErr
		}
		return timeouterr
	}
	return nil
}

// AssertResultServerMinTLSVersion verifies that the result server created for
// the given scan uses the expected minimum TLS version. It fetches client
// certificates from the scan's Kubernetes secret and uses curl with mTLS to
// connect to the result server endpoint.
func (f *Framework) AssertResultServerMinTLSVersion(scanName, expectedMinTLSVersion string) error {
	ocPath, err := exec.LookPath("oc")
	if err != nil {
		return fmt.Errorf("oc not found: %w", err)
	}

	var lastErr error
	timeouterr := wait.Poll(RetryInterval, Timeout, func() (bool, error) {
		clientCertSecret, err := f.KubeClient.CoreV1().Secrets(f.OperatorNamespace).Get(
			context.TODO(), "result-client-cert-"+scanName, metav1.GetOptions{},
		)
		if err != nil {
			lastErr = fmt.Errorf("failed to get client cert secret: %v", err)
			log.Printf("%v... retrying\n", lastErr)
			return false, nil
		}

		certB64 := base64.StdEncoding.EncodeToString(clientCertSecret.Data["tls.crt"])
		keyB64 := base64.StdEncoding.EncodeToString(clientCertSecret.Data["tls.key"])

		endpoint := fmt.Sprintf("https://%s-rs:8443/", scanName)
		curlCMD := fmt.Sprintf(
			"echo '%s' | base64 -d > /tmp/client.crt && "+
				"echo '%s' | base64 -d > /tmp/client.key && "+
				"curl -vks --cert /tmp/client.crt --key /tmp/client.key %s 2>&1 | grep 'SSL connection'",
			certB64, keyB64, endpoint,
		)

		// #nosec G204
		cmd := exec.Command(ocPath,
			"run", "--rm", "-i", "--restart=Never",
			"--image=registry.fedoraproject.org/fedora-minimal:latest",
			"-n", f.OperatorNamespace, "rs-tls-version-test",
			"--", "bash", "-c", curlCMD,
		)
		out, err := cmd.CombinedOutput()
		if err != nil {
			lastErr = fmt.Errorf("curl command failed: %v, output: %s", err, string(out))
			log.Printf("%v... retrying\n", lastErr)
			return false, nil
		}

		output := string(out)
		actual := parseTLSVersionFromCurlOutput(output)
		if actual == "" {
			lastErr = fmt.Errorf("could not parse TLS version from result server output: %s", output)
			log.Printf("%v... retrying\n", lastErr)
			return false, nil
		}
		if !tlsVersionAtLeast(actual, expectedMinTLSVersion) {
			lastErr = fmt.Errorf("result server negotiated TLS version %s is below minimum %s", actual, expectedMinTLSVersion)
			log.Printf("%v... retrying\n", lastErr)
			return false, nil
		}
		log.Printf("result server using %s (minimum: %s)\n", actual, expectedMinTLSVersion)
		return true, nil
	})
	if timeouterr != nil {
		if lastErr != nil {
			return lastErr
		}
		return timeouterr
	}
	return nil
}

// AssertMetricsEndpointRejectsTLSVersion verifies that the metrics endpoint
// rejects connections limited to the given TLS version. This is the inverse of
// AssertMetricsEndpointMinTLSVersion: it proves the server's floor is actually
// above the given version by confirming the handshake fails.
func (f *Framework) AssertMetricsEndpointRejectsTLSVersion(rejectedTLSVersion string) error {
	endpoint := fmt.Sprintf("https://metrics.%s.svc:8585/metrics-co", f.OperatorNamespace)
	curlCMD := fmt.Sprintf("curl -vks --tls-max %s %s 2>&1", rejectedTLSVersion, endpoint)

	ocPath, err := exec.LookPath("oc")
	if err != nil {
		return fmt.Errorf("oc not found: %w", err)
	}

	var lastErr error
	timeouterr := wait.Poll(RetryInterval, Timeout, func() (bool, error) {
		// #nosec G204
		cmd := exec.Command(ocPath,
			"run", "--rm", "-i", "--restart=Never",
			"--image=registry.fedoraproject.org/fedora-minimal:latest",
			"-n", f.OperatorNamespace, "tls-reject-test",
			"--", "bash", "-c", curlCMD,
		)
		out, err := cmd.CombinedOutput()
		output := string(out)

		if err == nil && !strings.Contains(output, "SSL") && !strings.Contains(output, "alert") {
			lastErr = fmt.Errorf("expected connection with --tls-max %s to be rejected, but it succeeded: %s", rejectedTLSVersion, output)
			log.Printf("%v... retrying\n", lastErr)
			return false, nil
		}
		log.Printf("metrics endpoint correctly rejected connection capped at %s\n", rejectedTLSVersion)
		return true, nil
	})
	if timeouterr != nil {
		if lastErr != nil {
			return lastErr
		}
		return timeouterr
	}
	return nil
}

// AssertResultServerRejectsTLSVersion verifies that the result server for the
// given scan rejects connections limited to the given TLS version. This proves
// the server's TLS floor is above the capped version by confirming the
// handshake fails.
func (f *Framework) AssertResultServerRejectsTLSVersion(scanName, rejectedTLSVersion string) error {
	ocPath, err := exec.LookPath("oc")
	if err != nil {
		return fmt.Errorf("oc not found: %w", err)
	}

	var lastErr error
	timeouterr := wait.Poll(RetryInterval, Timeout, func() (bool, error) {
		clientCertSecret, err := f.KubeClient.CoreV1().Secrets(f.OperatorNamespace).Get(
			context.TODO(), "result-client-cert-"+scanName, metav1.GetOptions{},
		)
		if err != nil {
			lastErr = fmt.Errorf("failed to get client cert secret: %v", err)
			log.Printf("%v... retrying\n", lastErr)
			return false, nil
		}

		certB64 := base64.StdEncoding.EncodeToString(clientCertSecret.Data["tls.crt"])
		keyB64 := base64.StdEncoding.EncodeToString(clientCertSecret.Data["tls.key"])

		endpoint := fmt.Sprintf("https://%s-rs:8443/", scanName)
		curlCMD := fmt.Sprintf(
			"echo '%s' | base64 -d > /tmp/client.crt && "+
				"echo '%s' | base64 -d > /tmp/client.key && "+
				"curl -vks --tls-max %s --cert /tmp/client.crt --key /tmp/client.key %s 2>&1",
			certB64, keyB64, rejectedTLSVersion, endpoint,
		)

		// #nosec G204
		cmd := exec.Command(ocPath,
			"run", "--rm", "-i", "--restart=Never",
			"--image=registry.fedoraproject.org/fedora-minimal:latest",
			"-n", f.OperatorNamespace, "rs-tls-reject-test",
			"--", "bash", "-c", curlCMD,
		)
		out, err := cmd.CombinedOutput()
		output := string(out)

		if err == nil && !strings.Contains(output, "SSL") && !strings.Contains(output, "alert") {
			lastErr = fmt.Errorf("expected result server connection with --tls-max %s to be rejected, but it succeeded: %s", rejectedTLSVersion, output)
			log.Printf("%v... retrying\n", lastErr)
			return false, nil
		}
		log.Printf("result server correctly rejected connection capped at %s\n", rejectedTLSVersion)
		return true, nil
	})
	if timeouterr != nil {
		if lastErr != nil {
			return lastErr
		}
		return timeouterr
	}
	return nil
}

// WaitForNodesToBeSchedulable waits until all nodes in the cluster are
// schedulable and ready. This is useful after changing the APIServer TLS
// profile, which triggers a kube-apiserver rollout that temporarily cordons
// nodes.
func (f *Framework) WaitForNodesToBeSchedulable() error {
	var lastErr error
	timeouterr := wait.Poll(RetryInterval, 20*time.Minute, func() (bool, error) {
		nodes, err := f.KubeClient.CoreV1().Nodes().List(context.TODO(), metav1.ListOptions{})
		if err != nil {
			lastErr = fmt.Errorf("failed to list nodes: %v", err)
			log.Printf("%v... retrying\n", lastErr)
			return false, nil
		}
		for _, node := range nodes.Items {
			if node.Spec.Unschedulable {
				lastErr = fmt.Errorf("node %s is unschedulable", node.Name)
				log.Printf("%v... retrying\n", lastErr)
				return false, nil
			}
			ready := false
			for _, cond := range node.Status.Conditions {
				if cond.Type == corev1.NodeReady && cond.Status == corev1.ConditionTrue {
					ready = true
					break
				}
			}
			if !ready {
				lastErr = fmt.Errorf("node %s is not ready", node.Name)
				log.Printf("%v... retrying\n", lastErr)
				return false, nil
			}
		}
		return true, nil
	})
	if timeouterr != nil {
		if lastErr != nil {
			return lastErr
		}
		return timeouterr
	}
	return nil
}

// extractTLSProfileForTest mirrors the operator's logic for determining
// which TLS profile to use based on the APIServer adherence policy.
func extractTLSProfileForTest(apiServer *configv1.APIServer) *configv1.TLSSecurityProfile {
	switch apiServer.Spec.TLSAdherence {
	case configv1.TLSAdherencePolicyStrictAllComponents:
		if apiServer.Spec.TLSSecurityProfile != nil {
			return apiServer.Spec.TLSSecurityProfile
		}
		return &configv1.TLSSecurityProfile{
			Type: configv1.TLSProfileIntermediateType,
		}
	default:
		// When adherence is not strict, the operator uses secure defaults
		// (Intermediate profile from library-go's SecureTLSConfig).
		return &configv1.TLSSecurityProfile{
			Type: configv1.TLSProfileIntermediateType,
		}
	}
}

// GetOperatorPods returns the operator pods in the operator namespace.
func (f *Framework) GetOperatorPods() ([]corev1.Pod, error) {
	podList, err := f.KubeClient.CoreV1().Pods(f.OperatorNamespace).List(context.TODO(), metav1.ListOptions{})
	if err != nil {
		return nil, fmt.Errorf("failed to list pods: %w", err)
	}
	var operatorPods []corev1.Pod
	for _, pod := range podList.Items {
		if strings.Contains(pod.Name, OperatorName) &&
			!strings.Contains(pod.Name, OperatorName+"-result") {
			operatorPods = append(operatorPods, pod)
		}
	}
	return operatorPods, nil
}

// WaitForOperatorPodRestart waits until the operator pod has a different UID
// than the one provided, indicating the pod has been restarted.
func (f *Framework) WaitForOperatorPodRestart(originalPodUID types.UID) error {
	return wait.Poll(RetryInterval, Timeout, func() (bool, error) {
		pods, err := f.GetOperatorPods()
		if err != nil {
			log.Printf("Error getting operator pods: %v... retrying\n", err)
			return false, nil
		}
		for _, pod := range pods {
			if pod.UID != originalPodUID && pod.Status.Phase == corev1.PodRunning {
				log.Printf("Operator pod restarted: new UID %s\n", pod.UID)
				return true, nil
			}
		}
		log.Println("Waiting for operator pod to restart...")
		return false, nil
	})
}

// IsOCPVersionAtLeast checks whether the cluster is running at least the
// specified OCP version (e.g. 4, 22 for OCP 4.22). Returns false if the
// ClusterVersion resource cannot be fetched or has no history.
func (f *Framework) IsOCPVersionAtLeast(major, minor int) (bool, error) {
	clusterVersion := &configv1.ClusterVersion{}
	key := types.NamespacedName{Name: "version"}
	if err := f.Client.Get(context.TODO(), key, clusterVersion); err != nil {
		return false, fmt.Errorf("failed to get ClusterVersion: %w", err)
	}
	if len(clusterVersion.Status.History) == 0 {
		return false, fmt.Errorf("ClusterVersion has no history entries")
	}
	version := clusterVersion.Status.History[0].Version
	if !semver.IsValid("v" + version) {
		return false, fmt.Errorf("unexpected version format: %s", version)
	}
	return semver.Compare("v"+version, fmt.Sprintf("v%d.%d.0", major, minor)) >= 0, nil
}
