package controller

import (
	"context"
	"fmt"

	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	apimeta "k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/types"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/log"

	v1 "github.com/abexamir/app-operator/api/v1"
)

// perAppSecretStoreName is the well-known SecretStore name pattern an AppDefinition's
// externalSecrets entries reference to get a *dedicated* Vault identity instead of sharing the
// namespace-wide default (see reconcile_defaultsecretstore.go) — parameterized by the
// AppDefinition's own name, so multiple AppDefinitions in the same namespace (namespace:AppProject
// stays 1:1, but a namespace can and does hold more than one app) each get an isolated
// ServiceAccount + SecretStore + Vault role instead of being able to read each other's secrets
// under the shared store. reconcilePerAppSecretStore auto-provisions both — this time
// owner-referenced to the AppDefinition, since unlike the shared store this one is 1:1 by
// construction and safe to garbage-collect when the app is deleted:
//
//	apiVersion: v1
//	kind: ServiceAccount
//	metadata: {name: <appName>, namespace: <namespace>, ownerReferences: [<appDef>]}
//	---
//	apiVersion: external-secrets.io/v1beta1
//	kind: SecretStore
//	metadata: {name: vault-zarrino-<appName>, namespace: <namespace>, ownerReferences: [<appDef>]}
//	spec:
//	  provider:
//	    vault:
//	      server: https://vault.zarrino.tech
//	      path: secret
//	      version: v2
//	      auth:
//	        kubernetes:
//	          mountPath: kubernetes-zarrino
//	          role: <namespace>-<appName>
//	          serviceAccountRef: {name: <appName>}
//
// The Vault-side kubernetes-auth role itself is provisioned separately (outside this operator, in
// vault-iac) — this reconciler only ensures the Kubernetes-side objects that role expects to find
// exist. A human scopes the role's actual Vault policy to this app's own secret path (rather than
// the whole namespace) by adding a nested `paths/secret/<namespace>/<appName>/owners` file in
// vault-iac — vault-iac's existing (namespace, sa_name)-generic machinery needs no changes for
// this; see vault/iac's own README/CLAUDE.md.
func perAppSecretStoreName(appName string) string {
	return "vault-zarrino-" + appName
}

// usesPerAppSecretStore reports whether appDef has at least one externalSecrets entry pointing at
// its own well-known per-app store name — same storeKind-must-be-explicit-SecretStore reasoning
// as usesDefaultSecretStore.
func usesPerAppSecretStore(appDef *v1.AppDefinition) bool {
	want := perAppSecretStoreName(appDef.Name)
	for _, es := range appDef.Spec.ExternalSecrets {
		storeKind := es.StoreKind
		if storeKind == "" {
			storeKind = implicitStoreKind
		}
		if es.Store == want && storeKind == "SecretStore" {
			return true
		}
	}
	return false
}

// reconcilePerAppSecretStore get-or-creates this AppDefinition's own dedicated ServiceAccount and
// SecretStore when needed (usesPerAppSecretStore) and does nothing otherwise. Unlike
// reconcileDefaultSecretStore's namespace-wide singleton, both are owner-referenced to appDef —
// safe, since nothing else can reference a store name parameterized by this exact AppDefinition's
// own name.
func (r *AppDefinitionReconciler) reconcilePerAppSecretStore(ctx context.Context, appDef *v1.AppDefinition) error {
	if !usesPerAppSecretStore(appDef) {
		return nil
	}
	logger := log.FromContext(ctx)
	namespace := appDef.Namespace
	storeName := perAppSecretStoreName(appDef.Name)

	if err := r.ensurePerAppServiceAccount(ctx, appDef); err != nil {
		return err
	}

	secretStoreGVK, err := resolveSecretStoreGVK(r.RESTMapper())
	if err != nil {
		if apimeta.IsNoMatchError(err) {
			logger.V(1).Info("SecretStore CRD not installed, skipping per-app SecretStore provisioning")
			return nil
		}
		return fmt.Errorf("resolving SecretStore API version: %w", err)
	}

	// r.APIReader (bypasses the informer cache) rather than r.Get: SecretStore is an
	// unstructured/optional type with no scheme-registered informer, same reasoning as
	// reconcileDefaultSecretStore's existing existence check.
	existing := &unstructured.Unstructured{}
	existing.SetGroupVersionKind(secretStoreGVK)
	storeKey := types.NamespacedName{Name: storeName, Namespace: namespace}
	getErr := r.APIReader.Get(ctx, storeKey, existing)
	if getErr == nil {
		return nil // already exists — get-or-create only, never updated.
	}
	if apimeta.IsNoMatchError(getErr) {
		logger.V(1).Info("SecretStore CRD not installed, skipping per-app SecretStore provisioning")
		return nil
	}
	if !apierrors.IsNotFound(getErr) {
		return fmt.Errorf("getting per-app SecretStore: %w", getErr)
	}

	desired := &unstructured.Unstructured{
		Object: map[string]interface{}{
			"apiVersion": secretStoreGVK.GroupVersion().String(),
			"kind":       secretStoreGVK.Kind,
			"metadata": map[string]interface{}{
				"name":      storeName,
				"namespace": namespace,
			},
			"spec": map[string]interface{}{
				"provider": map[string]interface{}{
					"vault": map[string]interface{}{
						"server":  "https://vault.zarrino.tech",
						"path":    "secret",
						"version": "v2",
						"auth": map[string]interface{}{
							"kubernetes": map[string]interface{}{
								"mountPath": "kubernetes-zarrino",
								"role":      namespace + "-" + appDef.Name,
								"serviceAccountRef": map[string]interface{}{
									"name": appDef.Name,
								},
							},
						},
					},
				},
			},
		},
	}

	if err := ctrl.SetControllerReference(appDef, desired, r.Scheme); err != nil {
		return fmt.Errorf("setting owner reference on per-app SecretStore %s: %w", storeName, err)
	}

	logger.Info("creating per-app SecretStore", "name", storeName, "namespace", namespace)
	if err := r.Create(ctx, desired); err != nil {
		if apierrors.IsAlreadyExists(err) {
			return nil
		}
		if apimeta.IsNoMatchError(err) {
			logger.V(1).Info("SecretStore CRD not installed, skipping per-app SecretStore provisioning")
			return nil
		}
		return fmt.Errorf("creating per-app SecretStore: %w", err)
	}
	return nil
}

// ensurePerAppServiceAccount get-or-creates a ServiceAccount named after the AppDefinition itself
// — the naming convention the Vault-side kubernetes-auth role (provisioned outside this operator)
// expects to bind to. Owner-referenced to appDef, unlike ensureDefaultServiceAccount's
// namespace-wide singleton: this one is 1:1 scoped, so garbage-collecting it when the app is
// deleted is correct, not a shared-resource hazard.
func (r *AppDefinitionReconciler) ensurePerAppServiceAccount(ctx context.Context, appDef *v1.AppDefinition) error {
	namespace := appDef.Namespace
	sa := &corev1.ServiceAccount{}
	saKey := types.NamespacedName{Name: appDef.Name, Namespace: namespace}
	if err := r.Get(ctx, saKey, sa); err == nil {
		return nil // already exists — get-or-create only, never updated.
	} else if !apierrors.IsNotFound(err) {
		return fmt.Errorf("getting per-app ServiceAccount: %w", err)
	}

	logger := log.FromContext(ctx)
	logger.Info("creating per-app ServiceAccount", "name", appDef.Name, "namespace", namespace)
	sa = &corev1.ServiceAccount{
		ObjectMeta: metav1.ObjectMeta{Name: appDef.Name, Namespace: namespace},
	}
	if err := ctrl.SetControllerReference(appDef, sa, r.Scheme); err != nil {
		return fmt.Errorf("setting owner reference on per-app ServiceAccount %s: %w", appDef.Name, err)
	}
	if err := r.Create(ctx, sa); err != nil && !apierrors.IsAlreadyExists(err) {
		return fmt.Errorf("creating per-app ServiceAccount: %w", err)
	}
	return nil
}
