package controller

import (
	"context"
	"fmt"
	"time"

	apierrors "k8s.io/apimachinery/pkg/api/errors"
	apimeta "k8s.io/apimachinery/pkg/api/meta"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/apimachinery/pkg/types"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/log"

	v1 "github.com/abexamir/app-operator/api/v1"
)

// revalidateAnnotation is patched onto a failing SecretStore or ExternalSecret to make ESO
// reconcile it immediately. ESO returns an error on a failed Vault login or sync, which puts the
// object on controller-runtime's exponential backoff (capped at ~16m). Granting Vault access
// afterwards produces no Kubernetes event, so without a nudge the store stays in
// InvalidProviderConfig until the backoff expires. Any SecretStore update and any ExternalSecret
// annotation change bypasses that backoff.
const revalidateAnnotation = "appdefinition.abexamir.me/revalidate-at"

// externalSecretsRetryInterval is both the minimum gap between nudges of the same object and the
// requeue interval while any of them is not Ready.
const externalSecretsRetryInterval = 30 * time.Second

// retryFailedExternalSecrets nudges every not-Ready SecretStore this operator provisions for
// appDef, and every not-Ready ExternalSecret it owns. It returns pending=true while any of them
// is not yet Ready, so the caller keeps requeueing until they recover.
func (r *AppDefinitionReconciler) retryFailedExternalSecrets(ctx context.Context, appDef *v1.AppDefinition) (bool, error) {
	if len(appDef.Spec.ExternalSecrets) == 0 {
		return false, nil
	}
	now := time.Now()
	pending := false

	var storeNames []string
	if usesDefaultSecretStore(appDef) {
		storeNames = append(storeNames, defaultSecretStoreName)
	}
	if usesPerAppSecretStore(appDef) {
		storeNames = append(storeNames, perAppSecretStoreName(appDef.Name))
	}
	if len(storeNames) > 0 {
		gvk, err := resolveSecretStoreGVK(r.RESTMapper())
		if err != nil && !apimeta.IsNoMatchError(err) {
			return false, fmt.Errorf("resolving SecretStore API version: %w", err)
		}
		if err == nil {
			for _, name := range storeNames {
				p, err := r.retryIfNotReady(ctx, gvk, types.NamespacedName{Name: name, Namespace: appDef.Namespace}, now)
				if err != nil {
					return false, err
				}
				pending = pending || p
			}
		}
	}

	gvk, err := resolveExternalSecretGVK(r.RESTMapper())
	if err != nil {
		if apimeta.IsNoMatchError(err) {
			return pending, nil
		}
		return false, fmt.Errorf("resolving ExternalSecret API version: %w", err)
	}
	for _, es := range appDef.Spec.ExternalSecrets {
		key := types.NamespacedName{Name: appDef.Name + "-" + es.Name, Namespace: appDef.Namespace}
		p, err := r.retryIfNotReady(ctx, gvk, key, now)
		if err != nil {
			return false, err
		}
		pending = pending || p
	}
	return pending, nil
}

// retryIfNotReady patches revalidateAnnotation onto the object when ESO reports it not Ready and
// it hasn't been nudged within externalSecretsRetryInterval. An object ESO hasn't processed yet
// (no Ready condition) counts as pending but isn't nudged.
func (r *AppDefinitionReconciler) retryIfNotReady(
	ctx context.Context, gvk schema.GroupVersionKind, key types.NamespacedName, now time.Time,
) (bool, error) {
	obj := &unstructured.Unstructured{}
	obj.SetGroupVersionKind(gvk)
	if err := r.APIReader.Get(ctx, key, obj); err != nil {
		if apierrors.IsNotFound(err) || apimeta.IsNoMatchError(err) {
			return false, nil
		}
		return false, fmt.Errorf("getting %s %s: %w", gvk.Kind, key.Name, err)
	}

	known, ready := esoReadyStatus(obj)
	if ready {
		return false, nil
	}
	if !known || !revalidationDue(obj, now) {
		return true, nil
	}

	log.FromContext(ctx).Info("nudging ESO to retry not-Ready resource", "kind", gvk.Kind, "name", key.Name)
	patch := client.MergeFrom(obj.DeepCopy())
	annotations := obj.GetAnnotations()
	if annotations == nil {
		annotations = map[string]string{}
	}
	annotations[revalidateAnnotation] = now.UTC().Format(time.RFC3339)
	obj.SetAnnotations(annotations)
	if err := r.Patch(ctx, obj, patch); err != nil && !apierrors.IsNotFound(err) {
		return false, fmt.Errorf("annotating %s %s for revalidation: %w", gvk.Kind, key.Name, err)
	}
	return true, nil
}

// esoReadyStatus reads the Ready condition ESO sets on SecretStores and ExternalSecrets. known is
// false when ESO hasn't written a Ready condition yet.
func esoReadyStatus(obj *unstructured.Unstructured) (known, ready bool) {
	conditions, found, err := unstructured.NestedSlice(obj.Object, "status", "conditions")
	if err != nil || !found {
		return false, false
	}
	for _, c := range conditions {
		cond, ok := c.(map[string]interface{})
		if !ok || cond["type"] != "Ready" {
			continue
		}
		return true, cond["status"] == "True"
	}
	return false, false
}

// revalidationDue reports whether the object was last nudged at least
// externalSecretsRetryInterval ago, or never. An unparseable timestamp counts as due.
func revalidationDue(obj *unstructured.Unstructured, now time.Time) bool {
	last, ok := obj.GetAnnotations()[revalidateAnnotation]
	if !ok {
		return true
	}
	t, err := time.Parse(time.RFC3339, last)
	if err != nil {
		return true
	}
	return now.Sub(t) >= externalSecretsRetryInterval
}
