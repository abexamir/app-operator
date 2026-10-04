package controller

import (
	"time"

	"github.com/prometheus/client_golang/prometheus"
	"k8s.io/apimachinery/pkg/types"
	crmetrics "sigs.k8s.io/controller-runtime/pkg/metrics"
)

var (
	reconcileStepDuration = prometheus.NewHistogramVec(prometheus.HistogramOpts{
		Name:    "appoperator_reconcile_step_duration_seconds",
		Help:    "Duration of AppDefinition reconciliation steps.",
		Buckets: prometheus.DefBuckets,
	}, []string{"step"})
	reconcileStepErrors = prometheus.NewCounterVec(prometheus.CounterOpts{
		Name: "appoperator_reconcile_step_errors_total",
		Help: "Total AppDefinition reconciliation step errors.",
	}, []string{"step"})
	managedResourcePrunes = prometheus.NewCounterVec(prometheus.CounterOpts{
		Name: "appoperator_managed_resource_prunes_total",
		Help: "Total stale managed resources deleted by kind.",
	}, []string{"kind"})
)

func init() {
	crmetrics.Registry.MustRegister(
		reconcileStepDuration,
		reconcileStepErrors,
		managedResourcePrunes,
	)
}

func observeReconcileStep(step string, reconcile func() error) error {
	started := time.Now()
	err := reconcile()
	reconcileStepDuration.WithLabelValues(step).Observe(time.Since(started).Seconds())
	if err != nil {
		reconcileStepErrors.WithLabelValues(step).Inc()
	}
	return err
}

func recordManagedResourcePrune(kind string) {
	managedResourcePrunes.WithLabelValues(kind).Inc()
}

// forgetAppMetrics drops the in-memory reconcile stats of a deleted AppDefinition. Every other
// per-app metric is read from the cache at scrape time (see app_metrics.go) and disappears
// with the object.
func forgetAppMetrics(key types.NamespacedName) {
	appReconcileStats.forget(key)
}
