package controller

import (
	"context"
	"fmt"

	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	apimeta "k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/apimachinery/pkg/types"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/log"

	v1 "github.com/abexamir/app-operator/api/v1"
)

// Scopes of spec.secretStore. A Namespace-scoped store and its ServiceAccount (named after the
// namespace) are shared by every AppDefinition in the namespace that declares the same store, so
// they carry no owner reference: deleting one app must not garbage-collect a store the others
// still use. An App-scoped store and its ServiceAccount (named after the AppDefinition) are 1:1
// with the app and owner-referenced to it.
const (
	secretStoreScopeNamespace = "Namespace"
	secretStoreScopeApp       = "App"
)

var secretStoreGroupKind = schema.GroupKind{Group: "external-secrets.io", Kind: "SecretStore"}

// secretStoreAPIVersions mirrors externalSecretAPIVersions' newest-first preference order —
// see that var's comment for why the list is ordered this way and how to confirm it against
// a real cluster's installed CRD.
var secretStoreAPIVersions = []string{"v1", "v1beta1", "v1alpha1"}

func resolveSecretStoreGVK(mapper apimeta.RESTMapper) (schema.GroupVersionKind, error) {
	return resolvePreferredGVK(mapper, secretStoreGroupKind, secretStoreAPIVersions)
}

// secretStoreScope returns spec.secretStore's effective scope. The CRD defaults it, but objects
// built in Go (tests, fake clients) skip API server defaulting.
func secretStoreScope(store *v1.SecretStoreSpec) string {
	if store.Scope == "" {
		return secretStoreScopeNamespace
	}
	return store.Scope
}

// secretStoreServiceAccountName is the ServiceAccount the store authenticates to Vault as. The
// Vault-side Kubernetes auth role bound to it is provisioned outside this operator.
func secretStoreServiceAccountName(appDef *v1.AppDefinition) string {
	if secretStoreScope(appDef.Spec.SecretStore) == secretStoreScopeApp {
		return appDef.Name
	}
	return appDef.Namespace
}

// buildSecretStore renders spec.secretStore as a Vault SecretStore.
func buildSecretStore(appDef *v1.AppDefinition, gvk schema.GroupVersionKind) *unstructured.Unstructured {
	store := appDef.Spec.SecretStore
	saName := secretStoreServiceAccountName(appDef)
	vault := store.Vault
	path, version, mountPath, role := vault.Path, vault.Version, vault.AuthMountPath, vault.Role
	if path == "" {
		path = "secret"
	}
	if version == "" {
		version = "v2"
	}
	if mountPath == "" {
		mountPath = "kubernetes"
	}
	if role == "" {
		role = appDef.Namespace + "-" + saName
	}
	return &unstructured.Unstructured{
		Object: map[string]interface{}{
			"apiVersion": gvk.GroupVersion().String(),
			"kind":       gvk.Kind,
			"metadata": map[string]interface{}{
				"name":      store.Name,
				"namespace": appDef.Namespace,
			},
			"spec": map[string]interface{}{
				"provider": map[string]interface{}{
					"vault": map[string]interface{}{
						"server":  vault.Server,
						"path":    path,
						"version": version,
						"auth": map[string]interface{}{
							"kubernetes": map[string]interface{}{
								"mountPath": mountPath,
								"role":      role,
								"serviceAccountRef": map[string]interface{}{
									"name": saName,
								},
							},
						},
					},
				},
			},
		},
	}
}

// reconcileSecretStore get-or-creates the ServiceAccount and SecretStore that spec.secretStore
// declares, and does nothing when it is unset. Both are get-or-create only: an existing object,
// possibly hand-customized, is never overwritten.
func (r *AppDefinitionReconciler) reconcileSecretStore(ctx context.Context, appDef *v1.AppDefinition) error {
	store := appDef.Spec.SecretStore
	if store == nil {
		return nil
	}
	logger := log.FromContext(ctx)
	owned := secretStoreScope(store) == secretStoreScopeApp

	if err := r.ensureSecretStoreServiceAccount(ctx, appDef, owned); err != nil {
		return err
	}

	secretStoreGVK, err := resolveSecretStoreGVK(r.RESTMapper())
	if err != nil {
		if apimeta.IsNoMatchError(err) {
			logger.V(1).Info("SecretStore CRD not installed, skipping SecretStore provisioning")
			return nil
		}
		return fmt.Errorf("resolving SecretStore API version: %w", err)
	}

	// r.APIReader (bypasses the informer cache) rather than r.Get: SecretStore is an
	// unstructured/optional type with no scheme-registered informer, same reasoning as
	// reconcileExternalSecrets' existing existence checks.
	existing := &unstructured.Unstructured{}
	existing.SetGroupVersionKind(secretStoreGVK)
	getErr := r.APIReader.Get(ctx, types.NamespacedName{Name: store.Name, Namespace: appDef.Namespace}, existing)
	if getErr == nil {
		return nil
	}
	if apimeta.IsNoMatchError(getErr) {
		logger.V(1).Info("SecretStore CRD not installed, skipping SecretStore provisioning")
		return nil
	}
	if !apierrors.IsNotFound(getErr) {
		return fmt.Errorf("getting SecretStore %s: %w", store.Name, getErr)
	}

	desired := buildSecretStore(appDef, secretStoreGVK)
	if owned {
		if err := ctrl.SetControllerReference(appDef, desired, r.Scheme); err != nil {
			return fmt.Errorf("setting owner reference on SecretStore %s: %w", store.Name, err)
		}
	}

	logger.Info("creating SecretStore", "name", store.Name, "namespace", appDef.Namespace, "scope", secretStoreScope(store))
	if err := r.Create(ctx, desired); err != nil {
		if apierrors.IsAlreadyExists(err) {
			return nil
		}
		if apimeta.IsNoMatchError(err) {
			logger.V(1).Info("SecretStore CRD not installed, skipping SecretStore provisioning")
			return nil
		}
		return fmt.Errorf("creating SecretStore %s: %w", store.Name, err)
	}
	return nil
}

// ensureSecretStoreServiceAccount get-or-creates the ServiceAccount the store authenticates as.
// corev1.ServiceAccount is a scheme-registered built-in type, so unlike SecretStore this uses the
// regular cached r.Get, consistent with every other core-type reconciler in this package.
func (r *AppDefinitionReconciler) ensureSecretStoreServiceAccount(ctx context.Context, appDef *v1.AppDefinition, owned bool) error {
	name := secretStoreServiceAccountName(appDef)
	sa := &corev1.ServiceAccount{}
	if err := r.Get(ctx, types.NamespacedName{Name: name, Namespace: appDef.Namespace}, sa); err == nil {
		return nil
	} else if !apierrors.IsNotFound(err) {
		return fmt.Errorf("getting SecretStore ServiceAccount %s: %w", name, err)
	}

	sa = &corev1.ServiceAccount{ObjectMeta: metav1.ObjectMeta{Name: name, Namespace: appDef.Namespace}}
	if owned {
		if err := ctrl.SetControllerReference(appDef, sa, r.Scheme); err != nil {
			return fmt.Errorf("setting owner reference on ServiceAccount %s: %w", name, err)
		}
	}
	log.FromContext(ctx).Info("creating SecretStore ServiceAccount", "name", name, "namespace", appDef.Namespace)
	if err := r.Create(ctx, sa); err != nil && !apierrors.IsAlreadyExists(err) {
		return fmt.Errorf("creating SecretStore ServiceAccount %s: %w", name, err)
	}
	return nil
}
