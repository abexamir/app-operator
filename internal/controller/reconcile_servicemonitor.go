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

// reconcileServiceMonitor creates or updates a ServiceMonitor when any port has metrics.enabled.
// When the prometheus-operator CRDs are not installed the call is silently skipped — the app
// still works, just without Prometheus scraping.
func (r *AppDefinitionReconciler) reconcileServiceMonitor(ctx context.Context, appDef *v1.AppDefinition) error {
	logger := log.FromContext(ctx)
	smKey := types.NamespacedName{Name: serviceMonitorName(appDef.Name), Namespace: appDef.Namespace}

	endpoints := buildSMEndpoints(appDef)

	existing := &unstructured.Unstructured{}
	existing.SetGroupVersionKind(serviceMonitorGVK)

	if len(endpoints) == 0 {
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

	desired := &unstructured.Unstructured{
		Object: map[string]interface{}{
			"apiVersion": "monitoring.coreos.com/v1",
			"kind":       "ServiceMonitor",
			"metadata": map[string]interface{}{
				"name":      serviceMonitorName(appDef.Name),
				"namespace": appDef.Namespace,
				"labels":    buildSMLabels(appDef),
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

func buildSMLabels(appDef *v1.AppDefinition) map[string]interface{} {
	labels := make(map[string]interface{})
	for k, v := range standardLabels(appDef.Name) {
		labels[k] = v
	}
	for _, c := range appDef.Spec.Containers {
		for _, p := range c.Ports {
			if p.Metrics == nil || !p.Metrics.Enabled {
				continue
			}
			for k, v := range p.Metrics.Labels {
				labels[k] = v
			}
		}
	}
	return labels
}

func buildSMEndpoints(appDef *v1.AppDefinition) []interface{} {
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
			ep := map[string]interface{}{
				"port": p.Name,
				"path": path,
			}
			if p.Metrics.Interval != "" {
				ep["interval"] = p.Metrics.Interval
			}
			endpoints = append(endpoints, ep)
		}
	}
	return endpoints
}
