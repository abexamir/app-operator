package controller

import (
	"context"
	"fmt"

	apimeta "k8s.io/apimachinery/pkg/api/meta"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/apimachinery/pkg/types"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/log"

	v1 "github.com/abexamir/app-operator/api/v1"
)

var externalSecretGVK = schema.GroupVersionKind{
	Group:   "external-secrets.io",
	Version: "v1",
	Kind:    "ExternalSecret",
}

// reconcileExternalSecrets creates or updates an ExternalSecret for each entry in
// spec.externalSecrets. ESO syncs each ExternalSecret into a Kubernetes Secret named
// "<app>-<name>", which the Deployment then mounts or injects like any other Secret.
//
// The function is a no-op when the external-secrets.io CRDs are not installed — the
// same graceful-skip pattern used for ServiceMonitor.
func (r *AppDefinitionReconciler) reconcileExternalSecrets(ctx context.Context, appDef *v1.AppDefinition) error {
	if len(appDef.Spec.ExternalSecrets) == 0 {
		return nil
	}
	logger := log.FromContext(ctx)

	for _, es := range appDef.Spec.ExternalSecrets {
		name := appDef.Name + "-" + es.Name

		storeKind := es.StoreKind
		if storeKind == "" {
			storeKind = "ClusterSecretStore"
		}
		refreshInterval := es.RefreshInterval
		if refreshInterval == "" {
			refreshInterval = "1h"
		}

		// Build the data array.
		data := make([]interface{}, 0, len(es.Data))
		for _, d := range es.Data {
			ref := map[string]interface{}{
				"key": d.RemoteRef.Key,
			}
			if d.RemoteRef.Property != "" {
				ref["property"] = d.RemoteRef.Property
			}
			if d.RemoteRef.Version != "" {
				ref["version"] = d.RemoteRef.Version
			}
			data = append(data, map[string]interface{}{
				"secretKey": d.SecretKey,
				"remoteRef": ref,
			})
		}

		// Build the dataFrom array.
		dataFrom := make([]interface{}, 0, len(es.DataFrom))
		for _, df := range es.DataFrom {
			extract := map[string]interface{}{"key": df.Key}
			if df.Version != "" {
				extract["version"] = df.Version
			}
			dataFrom = append(dataFrom, map[string]interface{}{"extract": extract})
		}

		desired := &unstructured.Unstructured{
			Object: map[string]interface{}{
				"apiVersion": "external-secrets.io/v1",
				"kind":       "ExternalSecret",
				"metadata": map[string]interface{}{
					"name":      name,
					"namespace": appDef.Namespace,
					"labels":    labelsToInterface(standardLabels(appDef.Name)),
				},
				"spec": map[string]interface{}{
					"refreshInterval": refreshInterval,
					"secretStoreRef": map[string]interface{}{
						"name": es.Store,
						"kind": storeKind,
					},
					"target": map[string]interface{}{
						"name":           name,
						"creationPolicy": "Owner",
					},
					"data":     data,
					"dataFrom": dataFrom,
				},
			},
		}

		if err := ctrl.SetControllerReference(appDef, desired, r.Scheme); err != nil {
			return fmt.Errorf("setting owner reference on ExternalSecret %s: %w", name, err)
		}

		key := types.NamespacedName{Name: name, Namespace: appDef.Namespace}
		existing := &unstructured.Unstructured{}
		existing.SetGroupVersionKind(externalSecretGVK)

		err := r.APIReader.Get(ctx, key, existing)
		if err != nil {
			if apimeta.IsNoMatchError(err) {
				logger.V(1).Info("ExternalSecret CRD not installed, skipping")
				return nil
			}
			if apierrors.IsNotFound(err) {
				logger.Info("creating ExternalSecret", "name", name)
				if createErr := r.Create(ctx, desired); createErr != nil {
					if apimeta.IsNoMatchError(createErr) {
						logger.V(1).Info("ExternalSecret CRD not installed, skipping")
						return nil
					}
					return fmt.Errorf("creating ExternalSecret %s: %w", name, createErr)
				}
				continue
			}
			return fmt.Errorf("getting ExternalSecret %s: %w", name, err)
		}

		desired.SetResourceVersion(existing.GetResourceVersion())
		logger.Info("updating ExternalSecret", "name", name)
		if err := r.Update(ctx, desired); err != nil {
			return fmt.Errorf("updating ExternalSecret %s: %w", name, err)
		}
	}
	return nil
}

// labelsToInterface converts string labels to map[string]interface{} for unstructured objects.
func labelsToInterface(labels map[string]string) map[string]interface{} {
	out := make(map[string]interface{}, len(labels))
	for k, v := range labels {
		out[k] = v
	}
	return out
}
