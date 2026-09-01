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

type tlsState struct {
	profile   configv1.TLSProfileSpec
	adherence configv1.TLSAdherencePolicy
}

// fetchClusterTLSState fetches the cluster's TLS security profile and adherence
// policy from the APIServer resource using c, bounding both lookups with
// tlsLookupTimeout (a child of ctx). On any lookup error it falls back to
// secure defaults (default ciphers/min version and a "no opinion" adherence
// policy) so callers always receive a usable pair.
func fetchClusterTLSState(ctx context.Context, c client.Client) *tlsState {
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

	return &tlsState{profile, adherence}
}

// applyClusterTLSProfile conditionally applies the cluster TLS security
// profile to c. It is a no-op when the adherence policy does not require
// strict adherence. Returns any cipher suites unsupported by Go.
func applyClusterTLSProfile(c *tls.Config, cfg *tlsState) []string {
	if !libgocrypto.ShouldHonorClusterTLSProfile(cfg.adherence) {
		return nil
	}
	fn, unsupported := tlspkg.NewTLSConfigFromProfile(cfg.profile)
	fn(c)
	return unsupported
}

func makeTLSConfig(ctx context.Context, cfg *rest.Config) *tlsState {
	// Build a client for fetching the cluster TLS profile and adherence
	// policy before the manager is started.
	preStartClient, err := client.New(cfg, client.Options{Scheme: scheme})
	if err != nil {
		log.Error(err, "Failed to create pre-start client for TLS profile lookup")
		os.Exit(1)
	}

	// Fetch the initial TLS profile and adherence policy from the APIServer
	// resource. These are used to configure all TLS endpoints at startup and
	// to detect changes later via the SecurityProfileWatcher. The lookups are
	// bounded by a timeout and fall back to secure defaults on error.
	return fetchClusterTLSState(ctx, preStartClient)
}

func (cfg *tlsState) makeWebhookServer() webhook.Server {
	opts := []func(config *tls.Config){
		func(c *tls.Config) {
			c.NextProtos = []string{"http/1.1"}
		},
		func(c *tls.Config) {
			if unsupported := applyClusterTLSProfile(c, cfg); len(unsupported) > 0 {
				log.Info("TLS profile contains ciphers unsupported by Go", "unsupported", unsupported)
			}
		},
	}
	return webhook.NewServer(webhook.Options{Port: 9443, TLSOpts: opts})
}

func (cfg *tlsState) makeSecurityProfileWatcher(client client.Client, met *metrics.Metrics, cancel func()) *tlspkg.SecurityProfileWatcher {
	// If the tlsAdherence policy requires strict adherence, configure the
	// metrics server to use the cluster's TLS security profile.
	if libgocrypto.ShouldHonorClusterTLSProfile(cfg.adherence) {
		met.SetTLSProfileSpec(cfg.profile)
	}

	// Set up the SecurityProfileWatcher to detect APIServer TLS profile
	// and adherence policy changes. On change, cancel the context so the
	// manager shuts down gracefully and the pod restarts with the new
	// TLS configuration applied.
	return &tlspkg.SecurityProfileWatcher{
		Client:                    client,
		InitialTLSProfileSpec:     cfg.profile,
		InitialTLSAdherencePolicy: cfg.adherence,
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
