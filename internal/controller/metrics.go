package controller

import (
	"time"

	"github.com/prometheus/client_golang/prometheus"
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
	appReady = prometheus.NewGaugeVec(prometheus.GaugeOpts{
		Name: "appoperator_app_ready",
		Help: "Whether an AppDefinition is currently ready (1 ready, 0 not ready).",
	}, []string{"namespace", "name"})
	appObservedGeneration = prometheus.NewGaugeVec(prometheus.GaugeOpts{
		Name: "appoperator_app_observed_generation",
		Help: "Latest AppDefinition generation observed by the controller.",
	}, []string{"namespace", "name"})
)

func init() {
	crmetrics.Registry.MustRegister(
		reconcileStepDuration,
		reconcileStepErrors,
		managedResourcePrunes,
		appReady,
		appObservedGeneration,
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

func recordAppStatus(namespace, name string, ready bool, observedGeneration int64) {
	readyValue := 0.0
	if ready {
		readyValue = 1
	}
	appReady.WithLabelValues(namespace, name).Set(readyValue)
	appObservedGeneration.WithLabelValues(namespace, name).Set(float64(observedGeneration))
}

func forgetAppMetrics(namespace, name string) {
	appReady.DeleteLabelValues(namespace, name)
	appObservedGeneration.DeleteLabelValues(namespace, name)
}
