package controller

import (
	"context"
	"fmt"

	apierrors "k8s.io/apimachinery/pkg/api/errors"
	apimeta "k8s.io/apimachinery/pkg/api/meta"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/client-go/util/retry"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/log"

	v1 "github.com/abexamir/app-operator/api/v1"
)

// externalSecretGroupKind identifies ExternalSecret independent of API version — ESO promoted
// this kind v1alpha1 -> v1beta1 -> v1 across its own releases, and different clusters this
// operator targets run different ESO versions (this repo's dev cluster has v1; Zarrino's
// production cluster, running ESO since 2023, only serves v1alpha1/v1beta1 — v1 404s there with a
// NoMatchError). See resolveExternalSecretGVK.
var externalSecretGroupKind = schema.GroupKind{Group: "external-secrets.io", Kind: "ExternalSecret"}

// externalSecretAPIVersions is the preference order resolveExternalSecretGVK tries. Was
// previously hardcoded to v1 alone, which silently no-op'd ExternalSecret creation on any cluster
// that hadn't upgraded ESO past v1beta1 — indistinguishable from "ESO not installed at all" (both
// surface as apimeta.IsNoMatchError), so this went unnoticed until checked against a real
// cluster's installed CRDs (`kubectl get crd externalsecrets.external-secrets.io -o
// jsonpath='{.spec.versions[*].name}'` — confirm this if the CRD ever adds/drops a version).
var externalSecretAPIVersions = []string{"v1", "v1beta1", "v1alpha1"}

// resolveExternalSecretGVK finds the first ExternalSecret API version the cluster's RESTMapper
// actually recognizes, trying externalSecretAPIVersions in order. Returns the same
// apimeta-recognizable NoMatchError the mapper itself returns when none of them exist, so
// callers' existing apimeta.IsNoMatchError graceful-skip handling doesn't need to change.
func resolveExternalSecretGVK(mapper apimeta.RESTMapper) (schema.GroupVersionKind, error) {
	return resolvePreferredGVK(mapper, externalSecretGroupKind, externalSecretAPIVersions)
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

	externalSecretGVK, err := resolveExternalSecretGVK(r.RESTMapper())
	if err != nil {
		if apimeta.IsNoMatchError(err) {
			logger.V(1).Info("ExternalSecret CRD not installed, skipping")
			return nil
		}
		return fmt.Errorf("resolving ExternalSecret API version: %w", err)
	}

	for _, es := range appDef.Spec.ExternalSecrets {
		name := appDef.Name + "-" + es.Name

		storeKind := es.StoreKind
		if storeKind == "" {
			storeKind = "ClusterSecretStore"
		}
		refreshInterval := es.RefreshInterval
		if refreshInterval == "" {
			refreshInterval = "1m"
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
				"apiVersion": externalSecretGVK.GroupVersion().String(),
				"kind":       externalSecretGVK.Kind,
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
						// Labels the synced Secret with our standard labels so the
						// controller's label-based Secret watch (see SetupWithManager)
						// picks it up — this Secret is owned by the ExternalSecret, not
						// the AppDefinition, so it falls outside any owner-based watch.
						"template": map[string]interface{}{
							"metadata": map[string]interface{}{
								"labels": labelsToInterface(standardLabels(appDef.Name)),
							},
						},
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

		if err := retry.RetryOnConflict(retry.DefaultBackoff, func() error {
			if err := r.APIReader.Get(ctx, key, existing); err != nil {
				return err
			}
			desired.SetResourceVersion(existing.GetResourceVersion())
			return r.Update(ctx, desired)
		}); err != nil {
			return fmt.Errorf("updating ExternalSecret %s: %w", name, err)
		}
		logger.Info("updating ExternalSecret", "name", name)
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
