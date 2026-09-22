package framework

import (
	"context"
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

// testPodSecurityOverrides is the `oc run --overrides` securityContext
// required for the ephemeral curl pods below to be admitted under the
// cluster's restricted PodSecurity/SCC; without it the pod is admitted and
// immediately terminated as Error before curl ever runs, so every poll
// attempt fails identically and the caller retries until Timeout. Kept
// identical to the pre-existing, proven metricsTestPodOverrides in
// tests/e2e/helpers.go - duplicated here (not imported) because package
// e2e imports package framework, not the other way around.
const testPodSecurityOverrides = `--overrides={"spec":{"securityContext":{"runAsNonRoot":true,"runAsUser":65534,"seccompProfile":{"type":"RuntimeDefault"}}}}`

// AssertMetricsEndpointMinTLSVersion uses curl to connect to the metrics
// endpoint and verifies the negotiated TLS version is at least the expected
// minimum. The server may negotiate a higher version than the minimum (e.g.
// TLS 1.3 when the minimum is 1.2), which is correct behavior.
func (f *Framework) AssertMetricsEndpointMinTLSVersion(expectedMinTLSVersion string) error {
	endpoint := fmt.Sprintf("https://metrics.%s.svc:8585/metrics-fio", f.OperatorNamespace)
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
			"-n", f.OperatorNamespace, testPodSecurityOverrides, fmt.Sprintf("tls-version-test-%d", time.Now().UnixNano()),
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

// AssertMetricsEndpointRejectsTLSVersion verifies that the metrics endpoint
// rejects connections limited to the given TLS version. This is the inverse of
// AssertMetricsEndpointMinTLSVersion: it proves the server's floor is actually
// above the given version by confirming the handshake fails.
func (f *Framework) AssertMetricsEndpointRejectsTLSVersion(rejectedTLSVersion string) error {
	endpoint := fmt.Sprintf("https://metrics.%s.svc:8585/metrics-fio", f.OperatorNamespace)
	// The exit code is the source of truth, not the presence of "SSL"/"alert"
	// in verbose output: curl -v prints "SSL connection using TLSvX.Y" on a
	// *successful* handshake too, so text-matching those substrings can't
	// tell a rejection from an acceptance. Exit 0 means curl completed the
	// request at the capped version, i.e. the server wrongly accepted it.
	curlCMD := fmt.Sprintf("curl -vks --tls-max %s -o /dev/null %s; echo REJECT_TEST_EXIT:$?", rejectedTLSVersion, endpoint)

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
			"-n", f.OperatorNamespace, testPodSecurityOverrides, fmt.Sprintf("tls-reject-test-%d", time.Now().UnixNano()),
			"--", "bash", "-c", curlCMD,
		)
		out, _ := cmd.CombinedOutput()
		output := string(out)

		exitCode, ok := parseRejectTestExitCode(output)
		if !ok {
			// oc run/exec never got far enough to run curl at all (image
			// pull, pod scheduling issue, etc.) - retry; this is not a TLS
			// result either way.
			lastErr = fmt.Errorf("could not determine curl exit code, possible infra issue: %s", output)
			log.Printf("%v... retrying\n", lastErr)
			return false, nil
		}
		if exitCode == "0" {
			lastErr = fmt.Errorf("expected connection with --tls-max %s to be rejected, but curl succeeded: %s", rejectedTLSVersion, output)
			log.Printf("%v... retrying\n", lastErr)
			return false, nil
		}
		log.Printf("metrics endpoint correctly rejected connection capped at %s (curl exit %s)\n", rejectedTLSVersion, exitCode)
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

var rejectTestExitCodeRE = regexp.MustCompile(`REJECT_TEST_EXIT:(\d+)`)

// parseRejectTestExitCode extracts the curl exit code appended by
// AssertMetricsEndpointRejectsTLSVersion's shell command. ok is false if the
// marker is missing, which happens when the command never actually ran
// (e.g. the pod failed to start), as opposed to curl running and exiting
// non-zero.
func parseRejectTestExitCode(output string) (code string, ok bool) {
	m := rejectTestExitCodeRE.FindStringSubmatch(output)
	if m == nil {
		return "", false
	}
	return m[1], true
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
// which TLS profile to use based on the APIServer adherence policy: like
// libgocrypto.ShouldHonorClusterTLSProfile, only NoOpinion and
// LegacyAdheringComponentsOnly fall back to secure defaults, so an
// unknown/future adherence value is treated the same as StrictAllComponents
// (forward compatibility, per the APIServer API's own doc comment on
// TLSAdherence).
func extractTLSProfileForTest(apiServer *configv1.APIServer) *configv1.TLSSecurityProfile {
	switch apiServer.Spec.TLSAdherence {
	case configv1.TLSAdherencePolicyNoOpinion, configv1.TLSAdherencePolicyLegacyAdheringComponentsOnly:
		// The operator uses secure defaults (Intermediate profile from
		// library-go's SecureTLSConfig) when it doesn't honor the cluster
		// profile.
		return &configv1.TLSSecurityProfile{
			Type: configv1.TLSProfileIntermediateType,
		}
	default:
		if apiServer.Spec.TLSSecurityProfile != nil {
			return apiServer.Spec.TLSSecurityProfile
		}
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
		if strings.Contains(pod.GetName(), "file-integrity-operator") {
			operatorPods = append(operatorPods, pod)
		}
	}
	return operatorPods, nil
}

// WaitForOperatorPodRestart waits until the operator has restarted since
// original was captured. A graceful `mgr.Start(ctx)` exit (triggered by the
// TLS profile change detector) causes kubelet to restart the container
// in place under restartPolicy: Always - the Pod object and its UID do NOT
// change in that case, only the container's restart count does. This also
// detects a full pod replacement (different UID), in case that ever becomes
// the restart mechanism instead.
func (f *Framework) WaitForOperatorPodRestart(original corev1.Pod) error {
	originalRestarts := totalContainerRestarts(original)
	return wait.Poll(RetryInterval, Timeout, func() (bool, error) {
		pods, err := f.GetOperatorPods()
		if err != nil {
			log.Printf("Error getting operator pods: %v... retrying\n", err)
			return false, nil
		}
		for _, pod := range pods {
			if pod.Status.Phase != corev1.PodRunning {
				continue
			}
			if pod.UID != original.UID {
				log.Printf("Operator pod restarted: new pod UID %s\n", pod.UID)
				return true, nil
			}
			if restarts := totalContainerRestarts(pod); restarts > originalRestarts {
				log.Printf("Operator pod restarted: container restart count %d -> %d\n", originalRestarts, restarts)
				return true, nil
			}
		}
		log.Println("Waiting for operator pod to restart...")
		return false, nil
	})
}

func totalContainerRestarts(pod corev1.Pod) int32 {
	var total int32
	for _, cs := range pod.Status.ContainerStatuses {
		total += cs.RestartCount
	}
	return total
}

// IsOCPVersionAtLeast checks whether the cluster is running at least the
// specified OCP version (e.g. 4, 22 for OCP 4.22). Returns false if the
// ClusterVersion resource cannot be fetched or has no Completed history
// entry. History[0] is not used directly: mid-upgrade it can be a
// still-Partial entry for a version the cluster hasn't actually finished
// rolling out to yet.
func (f *Framework) IsOCPVersionAtLeast(major, minor int) (bool, error) {
	clusterVersion := &configv1.ClusterVersion{}
	key := types.NamespacedName{Name: "version"}
	if err := f.Client.Get(context.TODO(), key, clusterVersion); err != nil {
		return false, fmt.Errorf("failed to get ClusterVersion: %w", err)
	}
	for _, entry := range clusterVersion.Status.History {
		if entry.State != configv1.CompletedUpdate {
			continue
		}
		if !semver.IsValid("v" + entry.Version) {
			return false, fmt.Errorf("unexpected version format: %s", entry.Version)
		}
		return semver.Compare("v"+entry.Version, fmt.Sprintf("v%d.%d.0", major, minor)) >= 0, nil
	}
	return false, fmt.Errorf("ClusterVersion has no Completed history entries")
}
