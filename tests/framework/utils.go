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
	libgocrypto "github.com/openshift/library-go/pkg/crypto"
	"golang.org/x/mod/semver"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/apimachinery/pkg/util/wait"
)

func (f *Framework) GetClusterAPIServer() (*configv1.APIServer, error) {
	apiServer := &configv1.APIServer{}
	key := types.NamespacedName{Name: "cluster"}
	if err := f.Client.Get(context.TODO(), key, apiServer); err != nil {
		return nil, fmt.Errorf("failed to get APIServer cluster resource: %w", err)
	}
	return apiServer, nil
}

func (f *Framework) GetExpectedMinTLSVersion(apiServer *configv1.APIServer) string {
	profile := extractTLSProfileForTest(apiServer)
	spec, err := tlspkg.GetTLSProfileSpec(profile)
	if err != nil {
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

// Higher value = newer TLS version.
var tlsVersionNumber = map[string]int{
	"TLSv1.0": 10,
	"TLSv1.1": 11,
	"TLSv1.2": 12,
	"TLSv1.3": 13,
}

func parseTLSVersionFromCurlOutput(output string) string {
	re := regexp.MustCompile(`TLSv1\.[0-3]`)
	return re.FindString(output)
}

func tlsVersionAtLeast(actual, minimum string) bool {
	a, aOK := tlsVersionNumber[actual]
	m, mOK := tlsVersionNumber[minimum]
	if !aOK || !mOK {
		return false
	}
	return a >= m
}

// Required for the ephemeral curl pods below to be admitted under the
// cluster's restricted PodSecurity/SCC; without it the pod errors out
// before curl runs. Duplicated from tests/e2e/helpers.go's
// metricsTestPodOverrides since package e2e imports framework, not the
// reverse.
const testPodSecurityOverrides = `--overrides={"spec":{"securityContext":{"runAsNonRoot":true,"runAsUser":65534,"seccompProfile":{"type":"RuntimeDefault"}}}}`

// A higher negotiated version than expectedMinTLSVersion is still correct.
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

// Inverse of AssertMetricsEndpointMinTLSVersion: proves the floor is above
// rejectedTLSVersion via a failed handshake.
func (f *Framework) AssertMetricsEndpointRejectsTLSVersion(rejectedTLSVersion string) error {
	endpoint := fmt.Sprintf("https://metrics.%s.svc:8585/metrics-fio", f.OperatorNamespace)
	// Exit code is the source of truth, not the verbose output text: curl -v
	// prints "SSL connection using TLSvX.Y" on success too, so it can't
	// distinguish a rejection from an acceptance.
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

// ok is false if the marker is missing, meaning the command never ran
// (e.g. the pod failed to start), not that curl exited non-zero.
func parseRejectTestExitCode(output string) (code string, ok bool) {
	m := rejectTestExitCodeRE.FindStringSubmatch(output)
	if m == nil {
		return "", false
	}
	return m[1], true
}

// Inverse of AssertMetricsEndpointRejectsTLSVersion: proves the floor was
// NOT raised above maxTLSVersion. AssertMetricsEndpointMinTLSVersion alone
// can't show this, since an uncapped connection always negotiates the
// highest mutually supported version regardless of the floor.
func (f *Framework) AssertMetricsEndpointAcceptsTLSVersion(maxTLSVersion string) error {
	endpoint := fmt.Sprintf("https://metrics.%s.svc:8585/metrics-fio", f.OperatorNamespace)
	curlCMD := fmt.Sprintf("curl -vks --tls-max %s -o /dev/null %s; echo ACCEPT_TEST_EXIT:$?", maxTLSVersion, endpoint)

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
			"-n", f.OperatorNamespace, testPodSecurityOverrides, fmt.Sprintf("tls-accept-test-%d", time.Now().UnixNano()),
			"--", "bash", "-c", curlCMD,
		)
		out, _ := cmd.CombinedOutput()
		output := string(out)

		exitCode, ok := parseAcceptTestExitCode(output)
		if !ok {
			lastErr = fmt.Errorf("could not determine curl exit code, possible infra issue: %s", output)
			log.Printf("%v... retrying\n", lastErr)
			return false, nil
		}
		if exitCode != "0" {
			lastErr = fmt.Errorf("expected connection with --tls-max %s to succeed, but curl failed with exit %s: %s", maxTLSVersion, exitCode, output)
			log.Printf("%v... retrying\n", lastErr)
			return false, nil
		}
		log.Printf("metrics endpoint correctly accepted connection capped at %s\n", maxTLSVersion)
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

var acceptTestExitCodeRE = regexp.MustCompile(`ACCEPT_TEST_EXIT:(\d+)`)

// Mirrors parseRejectTestExitCode; kept separate since the two functions'
// success conditions are inverted.
func parseAcceptTestExitCode(output string) (code string, ok bool) {
	m := acceptTestExitCodeRE.FindStringSubmatch(output)
	if m == nil {
		return "", false
	}
	return m[1], true
}

// Needed after an APIServer TLS profile change, which triggers a
// kube-apiserver rollout that temporarily cordons nodes.
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

// Delegates to the real libgocrypto.ShouldHonorClusterTLSProfile instead of
// reimplementing it, so this test expectation can't drift from
// cmd/manager/operator.go's actual gating logic.
func extractTLSProfileForTest(apiServer *configv1.APIServer) *configv1.TLSSecurityProfile {
	if libgocrypto.ShouldHonorClusterTLSProfile(apiServer.Spec.TLSAdherence) && apiServer.Spec.TLSSecurityProfile != nil {
		return apiServer.Spec.TLSSecurityProfile
	}
	// Falls back to the operator's own Intermediate default (library-go's
	// SecureTLSConfig).
	return &configv1.TLSSecurityProfile{
		Type: configv1.TLSProfileIntermediateType,
	}
}

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

// A graceful mgr.Start(ctx) exit causes kubelet to restart the container in
// place (UID unchanged, only the restart count increases); this also
// detects a full pod replacement, in case that ever becomes the mechanism.
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

// History[0] isn't used directly: mid-upgrade it can be a still-Partial
// entry for a version not yet fully rolled out.
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

// Without this gate, tlsAdherence is absent from the APIServer CRD schema:
// writes to it are silently dropped and it always reads back as
// TLSAdherencePolicyNoOpinion, so StrictAllComponents is unreachable.
func (f *Framework) IsTLSAdherenceFeatureGateEnabled() (bool, error) {
	featureGate := &configv1.FeatureGate{}
	key := types.NamespacedName{Name: "cluster"}
	if err := f.Client.Get(context.TODO(), key, featureGate); err != nil {
		return false, fmt.Errorf("failed to get FeatureGate cluster resource: %w", err)
	}
	for _, versionGates := range featureGate.Status.FeatureGates {
		for _, enabled := range versionGates.Enabled {
			if enabled.Name == "TLSAdherence" {
				return true, nil
			}
		}
	}
	return false, nil
}
