package metrics

import (
	"context"
	"crypto/tls"
	"fmt"
	"net/http"

	"github.com/go-logr/logr"
	"github.com/pkg/errors"
	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/promhttp"
	dto "github.com/prometheus/client_model/go"

	ctrllog "sigs.k8s.io/controller-runtime/pkg/log"

	libgocrypto "github.com/openshift/library-go/pkg/crypto"
)

const (
	metricNamespace = "file_integrity_operator"

	// FileIntegrity states
	metricNameFileIntegrityPhase   = "phase_total"
	metricNameFileIntegrityError   = "error_total"
	metricNameFileIntegrityPause   = "pause_total"
	metricNameFileIntegrityUnpause = "unpause_total"
	// Reinit
	metricNameFileIntegrityReinit = "reinit_total"
	// Node status
	metricNameFileIntegrityNodeStatus      = "node_status_total"
	metricNameFileIntegrityNodeStatusGauge = "node_failed"
	metricNameFileIntegrityNodeStatusError = "node_status_error_total"
	// DaemonSets
	metricNameFileIntegrityDaemonsetUpdate       = "daemonset_update_total"
	metricNameFileIntegrityReinitDaemonsetUpdate = "reinit_daemonset_update_total"

	// Defined metric values
	metricLabelPhase         = "phase"
	metricLabelError         = "error"
	metricLabelOperation     = "operation"
	metricLabelNodeCondition = "condition"
	metricLabelReinitBy      = "by"
	metricLabelNode          = "node"

	metricLabelValuePhaseInit    = "Initializing"
	metricLabelValuePhaseActive  = "Active"
	metricLabelValuePhasePending = "Pending"
	metricLabelValuePhaseError   = "Error"

	metricLabelValueReinitNode   = "node"
	metricLabelValueReinitDemand = "demand"
	metricLabelValueReinitConfig = "config"

	metricLabelValueOperatorDaemonsetUpdate  = "update"
	metricLabelValueOperatorDaemonsetDelete  = "delete"
	metricLabelValueOperatorDaemonsetPodKill = "podkill"

	// HandlerPath is the default path for serving metrics.
	HandlerPath                        = "/metrics-fio"
	ControllerMetricsServiceName       = "metrics-fio"
	ControllerMetricsPort        int32 = 8585
)

var (
	ErrNoMetrics = errors.New("metric value is nil")
)

// Metrics is the main structure of this package.
type Metrics struct {
	impl                                     impl
	log                                      logr.Logger
	metricFileIntegrityPhase                 *prometheus.CounterVec
	metricFileIntegrityError                 *prometheus.CounterVec
	metricFileIntegrityPause                 *prometheus.CounterVec
	metricFileIntegrityUnpause               *prometheus.CounterVec
	metricFileIntegrityReinit                *prometheus.CounterVec
	metricFileIntegrityNodeStatus            *prometheus.CounterVec
	metricFileIntegrityNodeStatusError       *prometheus.CounterVec
	metricFileIntegrityDaemonsetUpdate       *prometheus.CounterVec
	metricFileIntegrityReinitDaemonsetUpdate *prometheus.CounterVec
	metricFileIntegrityNodeStatusGauge       *prometheus.GaugeVec
}

// NewControllerMetrics returns a new Metrics instance.
func NewControllerMetrics() *Metrics {
	return &Metrics{
		impl: &defaultImpl{},
		log:  ctrllog.Log.WithName("metrics"),
		metricFileIntegrityPhase: prometheus.NewCounterVec(
			prometheus.CounterOpts{
				Name:      metricNameFileIntegrityPhase,
				Namespace: metricNamespace,
				Help:      "The total number of transitions to the FileIntegrity phase",
			},
			[]string{metricLabelPhase},
		),
		metricFileIntegrityError: prometheus.NewCounterVec(
			prometheus.CounterOpts{
				Name:      metricNameFileIntegrityError,
				Namespace: metricNamespace,
				Help:      "The total number of FileIntegrity phase errors, per error",
			},
			[]string{metricLabelError},
		),
		metricFileIntegrityPause: prometheus.NewCounterVec(
			prometheus.CounterOpts{
				Name:      metricNameFileIntegrityPause,
				Namespace: metricNamespace,
				Help:      "The total number of FileIntegrity scan pause actions (during node updates)",
			}, []string{metricLabelNode},
		),
		metricFileIntegrityUnpause: prometheus.NewCounterVec(
			prometheus.CounterOpts{
				Name:      metricNameFileIntegrityUnpause,
				Namespace: metricNamespace,
				Help:      "The total number of FileIntegrity scan unpause actions (during node updates)",
			}, []string{metricLabelNode},
		),
		metricFileIntegrityReinit: prometheus.NewCounterVec(
			prometheus.CounterOpts{
				Name:      metricNameFileIntegrityReinit,
				Namespace: metricNamespace,
				Help:      "The total number of FileIntegrity database re-initialization triggers (annotation), per method and node",
			}, []string{metricLabelReinitBy, metricLabelNode},
		),
		metricFileIntegrityNodeStatus: prometheus.NewCounterVec(
			prometheus.CounterOpts{
				Name:      metricNameFileIntegrityNodeStatus,
				Namespace: metricNamespace,
				Help:      "The total number of FileIntegrityNodeStatus transitions, per condition and node",
			}, []string{metricLabelNodeCondition, metricLabelNode},
		),
		metricFileIntegrityNodeStatusError: prometheus.NewCounterVec(
			prometheus.CounterOpts{
				Name:      metricNameFileIntegrityNodeStatusError,
				Namespace: metricNamespace,
				Help:      "The total number of FileIntegrityNodeStatus errors, per error and node",
			}, []string{metricLabelError, metricLabelNode},
		),
		metricFileIntegrityDaemonsetUpdate: prometheus.NewCounterVec(
			prometheus.CounterOpts{
				Name:      metricNameFileIntegrityDaemonsetUpdate,
				Namespace: metricNamespace,
				Help:      "The total number of updates to the FileIntegrity AIDE daemonSet",
			},
			[]string{metricLabelOperation},
		),
		metricFileIntegrityReinitDaemonsetUpdate: prometheus.NewCounterVec(
			prometheus.CounterOpts{
				Name:      metricNameFileIntegrityReinitDaemonsetUpdate,
				Namespace: metricNamespace,
				Help:      "The total number of updates to the FileIntegrity re-init signaling daemonSet",
			},
			[]string{metricLabelOperation},
		),
		metricFileIntegrityNodeStatusGauge: prometheus.NewGaugeVec(
			prometheus.GaugeOpts{
				Name:      metricNameFileIntegrityNodeStatusGauge,
				Namespace: metricNamespace,
				Help:      "A gauge that is set to 1 when a node has unresolved integrity failures, and 0 when it is healthy",
			},
			[]string{metricLabelNode},
		),
	}
}

// Register iterates over all available Metrics and registers them.
func (m *Metrics) Register() error {
	for name, collector := range map[string]prometheus.Collector{
		metricNameFileIntegrityPhase:                 m.metricFileIntegrityPhase,
		metricNameFileIntegrityError:                 m.metricFileIntegrityError,
		metricNameFileIntegrityPause:                 m.metricFileIntegrityPause,
		metricNameFileIntegrityUnpause:               m.metricFileIntegrityUnpause,
		metricNameFileIntegrityReinit:                m.metricFileIntegrityReinit,
		metricNameFileIntegrityNodeStatus:            m.metricFileIntegrityNodeStatus,
		metricNameFileIntegrityNodeStatusError:       m.metricFileIntegrityNodeStatusError,
		metricNameFileIntegrityDaemonsetUpdate:       m.metricFileIntegrityDaemonsetUpdate,
		metricNameFileIntegrityReinitDaemonsetUpdate: m.metricFileIntegrityReinitDaemonsetUpdate,
		metricNameFileIntegrityNodeStatusGauge:       m.metricFileIntegrityNodeStatusGauge,
	} {
		m.log.Info(fmt.Sprintf("Registering metric: %s", name))
		if err := m.impl.Register(collector); err != nil {
			return errors.Wrapf(err, "register collector for %s metric", name)
		}
	}
	return nil
}

func (m *Metrics) Start(ctx context.Context) error {

	m.log.Info("Starting to serve controller metrics")
	http.Handle(HandlerPath, promhttp.Handler())

	tlsConfig := &tls.Config{
		MinVersion: tls.VersionTLS12,
		NextProtos: []string{"http/1.1"},
	}
	tlsConfig = libgocrypto.SecureTLSConfig(tlsConfig)
	server := &http.Server{
		Addr:      ":8585",
		TLSConfig: tlsConfig,
	}

	err := server.ListenAndServeTLS("/var/run/secrets/serving-cert/tls.crt", "/var/run/secrets/serving-cert/tls.key")
	if err != nil {
		m.log.Error(err, "Metrics service failed")
	}
	return nil
}

// IncFileIntegrityPhaseInit increments the FileIntegrity Phase init counter.
func (m *Metrics) IncFileIntegrityPhaseInit() {
	m.metricFileIntegrityPhase.
		WithLabelValues(metricLabelValuePhaseInit).Inc()
}

// IncFileIntegrityPhaseActive increments the FileIntegrity Phase active counter.
func (m *Metrics) IncFileIntegrityPhaseActive() {
	m.metricFileIntegrityPhase.
		WithLabelValues(metricLabelValuePhaseActive).Inc()
}

// IncFileIntegrityPhasePending increments the FileIntegrity Phase pending counter.
func (m *Metrics) IncFileIntegrityPhasePending() {
	m.metricFileIntegrityPhase.
		WithLabelValues(metricLabelValuePhasePending).Inc()
}

// IncFileIntegrityPhaseError increments the FileIntegrity Phase error counter.
func (m *Metrics) IncFileIntegrityPhaseError() {
	m.metricFileIntegrityPhase.
		WithLabelValues(metricLabelValuePhaseError).Inc()
}

// IncFileIntegrityError increments the FileIntegrity error counter per the given reason.
func (m *Metrics) IncFileIntegrityError(reason string) {
	m.metricFileIntegrityError.WithLabelValues(reason).Inc()
}

// IncFileIntegrityDaemonsetUpdate increments the counter for updates to the FileIntegrity daemonSet.
func (m *Metrics) IncFileIntegrityDaemonsetUpdate() {
	m.metricFileIntegrityDaemonsetUpdate.WithLabelValues(metricLabelValueOperatorDaemonsetUpdate).Inc()
}

// IncFileIntegrityDaemonsetDelete increments the counter for deletes of the FileIntegrity daemonSet.
func (m *Metrics) IncFileIntegrityDaemonsetDelete() {
	m.metricFileIntegrityDaemonsetUpdate.WithLabelValues(metricLabelValueOperatorDaemonsetDelete).Inc()
}

// IncFileIntegrityDaemonsetPodKill increments the counter for force-pod deletion of the FileIntegrity daemonSet pods.
func (m *Metrics) IncFileIntegrityDaemonsetPodKill() {
	m.metricFileIntegrityDaemonsetUpdate.WithLabelValues(metricLabelValueOperatorDaemonsetPodKill).Inc()
}

// IncFileIntegrityReinitDaemonsetUpdate increments the counter for updates to the FileIntegrity re-init daemonSet.
func (m *Metrics) IncFileIntegrityReinitDaemonsetUpdate() {
	m.metricFileIntegrityReinitDaemonsetUpdate.WithLabelValues(metricLabelValueOperatorDaemonsetUpdate).Inc()
}

// IncFileIntegrityReinitDaemonsetDelete increments the counter for deletes of the FileIntegrity re-init daemonSet.
func (m *Metrics) IncFileIntegrityReinitDaemonsetDelete() {
	m.metricFileIntegrityReinitDaemonsetUpdate.WithLabelValues(metricLabelValueOperatorDaemonsetDelete).Inc()
}

// IncFileIntegrityUnpause increments the FileIntegrity counter for pausing the scans (hold-off).
func (m *Metrics) IncFileIntegrityPause(node string) {
	m.metricFileIntegrityPause.WithLabelValues(node).Inc()
}

// IncFileIntegrityUnpause increments the FileIntegrity counter for unpausing the scan (removing hold-off).
func (m *Metrics) IncFileIntegrityUnpause(node string) {
	m.metricFileIntegrityUnpause.WithLabelValues(node).Inc()
}

// IncFileIntegrityReinitByDemand increments the FileIntegrity counter for the total number of database re-initializations
// triggered by the re-init annotation.
func (m *Metrics) IncFileIntegrityReinitByDemand() {
	m.metricFileIntegrityReinit.With(prometheus.Labels{
		metricLabelReinitBy: metricLabelValueReinitDemand,
		metricLabelNode:     "",
	}).Inc()
}

// IncFileIntegrityReinitByConfig increments the FileIntegrity counter for the total number of database re-initializations
// triggered by a configuration change.
func (m *Metrics) IncFileIntegrityReinitByConfig() {
	m.metricFileIntegrityReinit.With(prometheus.Labels{
		metricLabelReinitBy: metricLabelValueReinitConfig,
		metricLabelNode:     "",
	}).Inc()
}

// IncFileIntegrityReinitByNode increments the FileIntegrity counter for the total number of database re-initializations
// triggered by the node controller through the re-init annotation, per node.
func (m *Metrics) IncFileIntegrityReinitByNode(node string) {
	m.metricFileIntegrityReinit.With(prometheus.Labels{
		metricLabelReinitBy: metricLabelValueReinitNode,
		metricLabelNode:     node,
	}).Inc()
}

// IncFileIntegrityNodeStatus increments the FileIntegrity counter for FileIntegrityNodeStatus condition transitions.
func (m *Metrics) IncFileIntegrityNodeStatus(condition, node string) {
	m.metricFileIntegrityNodeStatus.With(prometheus.Labels{
		metricLabelNodeCondition: condition,
		metricLabelNode:          node,
	}).Inc()
}

// GetFileIntegrityNodeStatus retrieves the current value of the FileIntegrity counter for a specific node.
// Returns the current value of the counter, a boolean indicating if the counter was found, and an error if the metric
// was not found.
func (m *Metrics) GetFileIntegrityNodeStatus(node string) (float64, bool, error) {
	ctr := m.metricFileIntegrityNodeStatusGauge.WithLabelValues(node)
	if ctr == nil {
		return 0, false, fmt.Errorf("failed to get metric for node: %s", node)
	}
	c := make(chan prometheus.Metric, 1)
	ctr.Collect(c)
	// Ensure the channel is not empty before reading
	select {
	case metric := <-c:
		dto := dto.Metric{}
		if err := metric.Write(&dto); err != nil {
			return 0, false, fmt.Errorf("failed to write metric: %w", err)
		}
		if dto.Gauge == nil || dto.Gauge.Value == nil {
			return 0, false, ErrNoMetrics
		}
		return *dto.Gauge.Value, true, nil
	default:
		return 0, false, fmt.Errorf("no metric collected for node: %s", node)
	}

}

// IncFileIntegrityNodeStatusError increments the FileIntegrity counter for FileIntegrityNodeStatus errors, per errMsg.
func (m *Metrics) IncFileIntegrityNodeStatusError(errMsg string, node string) {
	m.metricFileIntegrityNodeStatusError.With(prometheus.Labels{
		metricLabelError: errMsg,
		metricLabelNode:  node,
	}).Inc()
}

// SetFileIntegrityNodeStatusGaugeBad sets the node_failed gauge to 1.
func (m *Metrics) SetFileIntegrityNodeStatusGaugeBad(node string) {
	m.metricFileIntegrityNodeStatusGauge.WithLabelValues(node).Set(1)
}

// SetFileIntegrityNodeStatusGaugeGood sets the node_failed gauge to 0.
func (m *Metrics) SetFileIntegrityNodeStatusGaugeGood(node string) {
	m.metricFileIntegrityNodeStatusGauge.WithLabelValues(node).Set(0)
}
