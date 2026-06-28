package controller

import (
	"context"
	"fmt"

	apierrors "k8s.io/apimachinery/pkg/api/errors"
	apimeta "k8s.io/apimachinery/pkg/api/meta"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/apimachinery/pkg/types"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/log"

	v1 "github.com/abexamir/app-operator/api/v1"
)

var serviceMonitorGVK = schema.GroupVersionKind{
	Group:   "monitoring.coreos.com",
	Version: "v1",
	Kind:    "ServiceMonitor",
}

// reconcileServiceMonitor creates or updates a ServiceMonitor when metrics.enabled is true.
// When the prometheus-operator CRDs are not installed the call is silently skipped — the app
// still works, just without Prometheus scraping.
func (r *AppDefinitionReconciler) reconcileServiceMonitor(ctx context.Context, appDef *v1.AppDefinition) error {
	logger := log.FromContext(ctx)
	smKey := types.NamespacedName{Name: serviceMonitorName(appDef.Name), Namespace: appDef.Namespace}

	enabled := hasMetricsEnabled(appDef) ||
		(appDef.Spec.MonitoringConfig != nil && appDef.Spec.MonitoringConfig.Enabled)
	endpoints := buildSMEndpoints(appDef)

	existing := &unstructured.Unstructured{}
	existing.SetGroupVersionKind(serviceMonitorGVK)

	if !enabled || len(endpoints) == 0 {
		err := r.Get(ctx, smKey, existing)
		if err == nil {
			logger.Info("deleting ServiceMonitor")
			return r.Delete(ctx, existing)
		}
		if apimeta.IsNoMatchError(err) || apierrors.IsNotFound(err) {
			return nil
		}
		return err
	}

	labels := make(map[string]interface{})
	for k, v := range standardLabels(appDef.Name) {
		labels[k] = v
	}
	if appDef.Spec.MonitoringConfig != nil {
		for k, v := range appDef.Spec.MonitoringConfig.Labels {
			labels[k] = v
		}
	}

	desired := &unstructured.Unstructured{
		Object: map[string]interface{}{
			"apiVersion": "monitoring.coreos.com/v1",
			"kind":       "ServiceMonitor",
			"metadata": map[string]interface{}{
				"name":      serviceMonitorName(appDef.Name),
				"namespace": appDef.Namespace,
				"labels":    labels,
			},
			"spec": map[string]interface{}{
				"selector": map[string]interface{}{
					"matchLabels": map[string]interface{}{
						"app.kubernetes.io/name": appDef.Name,
					},
				},
				"endpoints": endpoints,
				"namespaceSelector": map[string]interface{}{
					"matchNames": []interface{}{appDef.Namespace},
				},
			},
		},
	}

	if err := ctrl.SetControllerReference(appDef, desired, r.Scheme); err != nil {
		return fmt.Errorf("setting owner reference on ServiceMonitor: %w", err)
	}

	err := r.Get(ctx, smKey, existing)
	if err != nil {
		if apimeta.IsNoMatchError(err) {
			logger.V(1).Info("ServiceMonitor CRD not installed, skipping")
			return nil
		}
		if apierrors.IsNotFound(err) {
			logger.Info("creating ServiceMonitor")
			if createErr := r.Create(ctx, desired); createErr != nil {
				if apimeta.IsNoMatchError(createErr) {
					logger.V(1).Info("ServiceMonitor CRD not installed, skipping")
					return nil
				}
				return createErr
			}
			return nil
		}
		return err
	}

	desired.SetResourceVersion(existing.GetResourceVersion())
	logger.Info("updating ServiceMonitor")
	return r.Update(ctx, desired)
}

func hasMetricsEnabled(appDef *v1.AppDefinition) bool {
	for _, c := range appDef.Spec.Containers {
		for _, p := range c.Ports {
			if p.Metrics != nil && p.Metrics.Enabled {
				return true
			}
		}
	}
	return false
}

func buildSMEndpoints(appDef *v1.AppDefinition) []interface{} {
	if hasMetricsEnabled(appDef) {
		var endpoints []interface{}
		seen := map[string]bool{}
		for _, c := range appDef.Spec.Containers {
			for _, p := range c.Ports {
				if p.Metrics == nil || !p.Metrics.Enabled || seen[p.Name] {
					continue
				}
				seen[p.Name] = true
				path := p.Metrics.Path
				if path == "" {
					path = "/metrics"
				}
				endpoints = append(endpoints, map[string]interface{}{
					"port": p.Name,
					"path": path,
				})
			}
		}
		return endpoints
	}

	if appDef.Spec.MonitoringConfig == nil || !appDef.Spec.MonitoringConfig.Enabled {
		return nil
	}
	var endpoints []interface{}
	seen := map[string]bool{}
	for _, c := range appDef.Spec.Containers {
		for _, p := range c.Ports {
			if p.MetricsPath == "" || seen[p.Name] {
				continue
			}
			seen[p.Name] = true
			ep := map[string]interface{}{
				"port": p.Name,
				"path": p.MetricsPath,
			}
			if appDef.Spec.MonitoringConfig.ScrapeInterval != "" {
				ep["interval"] = appDef.Spec.MonitoringConfig.ScrapeInterval
			}
			endpoints = append(endpoints, ep)
		}
	}
	return endpoints
}
