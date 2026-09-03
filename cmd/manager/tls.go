package manager

import (
	"context"
	"crypto/tls"
	"os"
	"time"

	configv1 "github.com/openshift/api/config/v1"
	tlspkg "github.com/openshift/controller-runtime-common/pkg/tls"
	"github.com/openshift/file-integrity-operator/pkg/controller/metrics"
	libgocrypto "github.com/openshift/library-go/pkg/crypto"
	"k8s.io/client-go/rest"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/webhook"
)

// tlsLookupTimeout bounds the cluster TLS profile/adherence lookups so an
// unresponsive API server cannot block operator startup or the result server
// indefinitely; on timeout the caller falls back to secure defaults.
const tlsLookupTimeout = 30 * time.Second

// clusterTLSSettings captures the cluster TLS profile and adherence policy
// used to configure the manager's TLS endpoints and watch for future changes.
type clusterTLSSettings struct {
	profile   configv1.TLSProfileSpec
	adherence configv1.TLSAdherencePolicy
}

// fetchClusterTLSSettings fetches the cluster's TLS security profile and adherence
// policy from the APIServer resource using c, bounding both lookups with
// tlsLookupTimeout (a child of ctx). On any lookup error it falls back to
// secure defaults (default ciphers/min version and a "no opinion" adherence
// policy) so callers always receive a usable pair.
func fetchClusterTLSSettings(ctx context.Context, c client.Client) (configv1.TLSProfileSpec, configv1.TLSAdherencePolicy) {
	lookupCtx, cancel := context.WithTimeout(ctx, tlsLookupTimeout)
	defer cancel()

	profile, err := tlspkg.FetchAPIServerTLSProfile(lookupCtx, c)
	if err != nil {
		log.Info("Could not fetch APIServer TLS profile, using defaults", "error", err)
		profile = configv1.TLSProfileSpec{
			Ciphers:       tlspkg.DefaultTLSCiphers,
			MinTLSVersion: tlspkg.DefaultMinTLSVersion,
		}
	}

	adherence, err := tlspkg.FetchAPIServerTLSAdherencePolicy(lookupCtx, c)
	if err != nil {
		log.Info("Could not fetch APIServer TLS adherence policy, using defaults", "error", err)
		adherence = configv1.TLSAdherencePolicyNoOpinion
	}

	return profile, adherence
}

// applyClusterTLSSettings conditionally applies the cluster TLS security
// settings to c. It is a no-op when the adherence policy does not require
// strict adherence. Returns any cipher suites unsupported by Go.
func applyClusterTLSSettings(c *tls.Config, s *clusterTLSSettings) []string {
	if !libgocrypto.ShouldHonorClusterTLSProfile(s.adherence) {
		return nil
	}
	fn, unsupported := tlspkg.NewTLSConfigFromProfile(s.profile)
	fn(c)
	return unsupported
}

// makeClusterTLSSettings returns the cluster TLS security profile and adherence
// policy. These are used to configure all TLS endpoints at startup and to
// detect changes later via the SecurityProfileWatcher. The lookups are bounded
// by a timeout and fall back to secure defaults on error.
func makeClusterTLSSettings(ctx context.Context, cfg *rest.Config) *clusterTLSSettings {
	// Build a client for fetching the cluster TLS profile and adherence
	// policy before the manager is started.
	preStartClient, err := client.New(cfg, client.Options{Scheme: scheme})
	if err != nil {
		log.Error(err, "Failed to create pre-start client for TLS profile lookup")
		os.Exit(1)
	}

	profile, adherence := fetchClusterTLSSettings(ctx, preStartClient)
	return &clusterTLSSettings{profile, adherence}
}

// makeWebhookServer returns a webhook.Server configured with the cluster TLS
// security profile and adherence policy. It logs any cipher suites unsupported
// by Go.
func makeWebhookServer(state *clusterTLSSettings) webhook.Server {
	opts := []func(config *tls.Config){
		func(c *tls.Config) {
			c.NextProtos = []string{"http/1.1"}
		},
		func(c *tls.Config) {
			if unsupported := applyClusterTLSSettings(c, state); len(unsupported) > 0 {
				log.Info("TLS profile contains ciphers unsupported by Go", "unsupported", unsupported)
			}
		},
	}
	return webhook.NewServer(webhook.Options{Port: 9443, TLSOpts: opts})
}

// configureMetricsTLSProfile configures the metrics server with the cluster TLS
// security profile and adherence policy. It is a no-op when the adherence policy
// does not require strict adherence.
func configureMetricsTLSProfile(met *metrics.Metrics, s *clusterTLSSettings) {
	// If the tlsAdherence policy requires strict adherence, configure the
	// metrics server to use the cluster's TLS security profile.
	if libgocrypto.ShouldHonorClusterTLSProfile(s.adherence) {
		met.SetTLSProfileSpec(s.profile)
	}
}

// makeSecurityProfileWatcher returns a SecurityProfileWatcher configured with
// the cluster TLS security profile and adherence policy. It sets up callbacks
// to cancel the manager context on any changes, so the pod restarts with the
// new TLS configuration applied.
func makeSecurityProfileWatcher(client client.Client, s *clusterTLSSettings, cancel func()) *tlspkg.SecurityProfileWatcher {
	// Set up the SecurityProfileWatcher to detect APIServer TLS profile
	// and adherence policy changes. On change, cancel the context so the
	// manager shuts down gracefully and the pod restarts with the new
	// TLS configuration applied.
	return &tlspkg.SecurityProfileWatcher{
		Client:                    client,
		InitialTLSProfileSpec:     s.profile,
		InitialTLSAdherencePolicy: s.adherence,
		OnProfileChange: func(_ context.Context, oldProfile, newProfile configv1.TLSProfileSpec) {
			log.Info("Cluster TLS profile changed, initiating graceful shutdown to reload",
				"oldMinTLSVersion", oldProfile.MinTLSVersion,
				"newMinTLSVersion", newProfile.MinTLSVersion,
			)
			cancel()
		},
		OnAdherencePolicyChange: func(_ context.Context, oldPolicy, newPolicy configv1.TLSAdherencePolicy) {
			log.Info("Cluster TLS adherence policy changed, initiating graceful shutdown to reload",
				"oldPolicy", oldPolicy,
				"newPolicy", newPolicy,
			)
			cancel()
		},
	}
}
