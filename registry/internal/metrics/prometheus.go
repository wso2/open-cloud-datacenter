package metrics

import (
	"net/http"

	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/promauto"
	"github.com/prometheus/client_golang/prometheus/promhttp"
)

type Registry struct {
	provisionDuration *prometheus.HistogramVec
	provisionTotal    *prometheus.CounterVec
	activeDeployments prometheus.Gauge
	credentialsFetched *prometheus.CounterVec
	deleteTotal       *prometheus.CounterVec
	deleteDuration    *prometheus.HistogramVec
}

func NewRegistry() *Registry {
	return &Registry{
		provisionDuration: promauto.NewHistogramVec(prometheus.HistogramOpts{
			Namespace: "registry_provisioner",
			Name:      "provision_duration_seconds",
			Help:      "Time to fully provision a Harbor instance",
			Buckets:   []float64{30, 60, 90, 120, 180, 300, 480},
		}, []string{"tenant_id"}),

		provisionTotal: promauto.NewCounterVec(prometheus.CounterOpts{
			Namespace: "registry_provisioner",
			Name:      "provision_total",
			Help:      "Total Harbor provisioning attempts",
		}, []string{"result"}), // success | failure

		activeDeployments: promauto.NewGauge(prometheus.GaugeOpts{
			Namespace: "registry_provisioner",
			Name:      "active_deployments",
			Help:      "Number of registries currently in DEPLOYING state",
		}),

		credentialsFetched: promauto.NewCounterVec(prometheus.CounterOpts{
			Namespace: "registry_provisioner",
			Name:      "credentials_fetched_total",
			Help:      "Total credential fetch requests (audit metric)",
		}, []string{"tenant_id"}),

		deleteTotal: promauto.NewCounterVec(prometheus.CounterOpts{
			Namespace: "registry_provisioner",
			Name:      "delete_total",
			Help:      "Total Harbor deletion attempts",
		}, []string{"result", "mode"}), // result: success|failure, mode: soft|hard

		deleteDuration: promauto.NewHistogramVec(prometheus.HistogramOpts{
			Namespace: "registry_provisioner",
			Name:      "delete_duration_seconds",
			Help:      "Time to fully delete a Harbor instance",
			Buckets:   []float64{5, 15, 30, 60, 120, 300},
		}, []string{"mode"}),
	}
}

func (r *Registry) StartProvisionTimer(tenantID string) *prometheus.Timer {
	r.activeDeployments.Inc()
	return prometheus.NewTimer(r.provisionDuration.WithLabelValues(tenantID))
}

func (r *Registry) ProvisionResult(tenantID, result string) {
	r.activeDeployments.Dec()
	r.provisionTotal.WithLabelValues(result).Inc()
}

func (r *Registry) TrackCredentialFetch(tenantID string) {
	r.credentialsFetched.WithLabelValues(tenantID).Inc()
}

func (r *Registry) StartDeleteTimer(mode string) *prometheus.Timer {
	return prometheus.NewTimer(r.deleteDuration.WithLabelValues(mode))
}

func (r *Registry) DeleteResult(result, mode string) {
	r.deleteTotal.WithLabelValues(result, mode).Inc()
}

// Handler returns the Prometheus metrics HTTP handler.
func Handler() http.Handler {
	return promhttp.Handler()
}
