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
	"sigs.k8s.io/controller-runtime/pkg/log"

	v1 "github.com/abexamir/app-operator/api/v1"
)

// defaultSecretStoreName is the well-known SecretStore name apps across this operator's
// clusters reference when they want Vault-backed ExternalSecrets without standing up their
// own store. reconcileDefaultSecretStore auto-provisions it — plus its backing
// ServiceAccount — the first time any AppDefinition in a namespace references it, so app
// repos (and anything driving them, e.g. a self-service onboarding tool) never need to
// hand-commit this boilerplate again:
//
//	apiVersion: v1
//	kind: ServiceAccount
//	metadata: {name: <namespace>, namespace: <namespace>}
//	---
//	apiVersion: external-secrets.io/v1beta1
//	kind: SecretStore
//	metadata: {name: vault-zarrino, namespace: <namespace>}
//	spec:
//	  provider:
//	    vault:
//	      server: https://vault.zarrino.tech
//	      path: secret
//	      version: v2
//	      auth:
//	        kubernetes:
//	          mountPath: kubernetes-zarrino
//	          role: <namespace>-<namespace>
//	          serviceAccountRef: {name: <namespace>}
//
// The Vault-side kubernetes-auth role itself (bound to a ServiceAccount named exactly
// <namespace>, in that namespace) is provisioned separately (outside this operator) — this
// reconciler only ensures the Kubernetes-side objects that role expects to find exist.
const defaultSecretStoreName = "vault-zarrino"

var secretStoreGroupKind = schema.GroupKind{Group: "external-secrets.io", Kind: "SecretStore"}

// secretStoreAPIVersions mirrors externalSecretAPIVersions' newest-first preference order —
// see that var's comment for why the list is ordered this way and how to confirm it against
// a real cluster's installed CRD.
var secretStoreAPIVersions = []string{"v1", "v1beta1", "v1alpha1"}

func resolveSecretStoreGVK(mapper apimeta.RESTMapper) (schema.GroupVersionKind, error) {
	return resolvePreferredGVK(mapper, secretStoreGroupKind, secretStoreAPIVersions)
}

// usesDefaultSecretStore reports whether appDef has at least one externalSecrets entry
// pointing at the well-known default store — storeKind must be the (explicit or defaulted)
// "SecretStore" kind, since a ClusterSecretStore named "vault-zarrino" would be a different,
// pre-existing resource this operator has no business touching. Pulled out as its own pure
// function so the decision logic is unit-testable without envtest or real CRDs (this
// cluster's envtest environment does not install external-secrets.io's CRDs — see
// suite_test.go's CRDDirectoryPaths — so anything requiring a live SecretStore/GVK
// resolution can only be exercised against a real cluster).
func usesDefaultSecretStore(appDef *v1.AppDefinition) bool {
	for _, es := range appDef.Spec.ExternalSecrets {
		storeKind := es.StoreKind
		if storeKind == "" {
			storeKind = "ClusterSecretStore"
		}
		if es.Store == defaultSecretStoreName && storeKind == "SecretStore" {
			return true
		}
	}
	return false
}

// reconcileDefaultSecretStore get-or-creates the namespace's default ServiceAccount and
// SecretStore when needed (usesDefaultSecretStore) and does nothing otherwise. Both are
// treated as namespace-scoped singletons, never overwritten once present (a hand-customized
// store must never be clobbered) and never owner-referenced to any single AppDefinition:
// multiple AppDefinitions can and do share one namespace's default store, so deleting one of
// them must never garbage-collect a SecretStore the others still depend on. This is a
// deliberate divergence from every other reconciler in this package, which all set
// ctrl.SetControllerReference on resources scoped 1:1 to the owning AppDefinition.
func (r *AppDefinitionReconciler) reconcileDefaultSecretStore(ctx context.Context, appDef *v1.AppDefinition) error {
	if !usesDefaultSecretStore(appDef) {
		return nil
	}
	logger := log.FromContext(ctx)
	namespace := appDef.Namespace

	if err := r.ensureDefaultServiceAccount(ctx, namespace); err != nil {
		return err
	}

	secretStoreGVK, err := resolveSecretStoreGVK(r.RESTMapper())
	if err != nil {
		if apimeta.IsNoMatchError(err) {
			logger.V(1).Info("SecretStore CRD not installed, skipping default SecretStore provisioning")
			return nil
		}
		return fmt.Errorf("resolving SecretStore API version: %w", err)
	}

	// r.APIReader (bypasses the informer cache) rather than r.Get: SecretStore is an
	// unstructured/optional type with no scheme-registered informer, same reasoning as
	// reconcileExternalSecrets' existing existence checks.
	existing := &unstructured.Unstructured{}
	existing.SetGroupVersionKind(secretStoreGVK)
	storeKey := types.NamespacedName{Name: defaultSecretStoreName, Namespace: namespace}
	getErr := r.APIReader.Get(ctx, storeKey, existing)
	if getErr == nil {
		return nil // already exists — get-or-create only, never updated.
	}
	if apimeta.IsNoMatchError(getErr) {
		logger.V(1).Info("SecretStore CRD not installed, skipping default SecretStore provisioning")
		return nil
	}
	if !apierrors.IsNotFound(getErr) {
		return fmt.Errorf("getting default SecretStore: %w", getErr)
	}

	desired := &unstructured.Unstructured{
		Object: map[string]interface{}{
			"apiVersion": secretStoreGVK.GroupVersion().String(),
			"kind":       secretStoreGVK.Kind,
			"metadata": map[string]interface{}{
				"name":      defaultSecretStoreName,
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
								"role":      namespace + "-" + namespace,
								"serviceAccountRef": map[string]interface{}{
									"name": namespace,
								},
							},
						},
					},
				},
			},
		},
	}

	logger.Info("creating default SecretStore", "name", defaultSecretStoreName, "namespace", namespace)
	if err := r.Create(ctx, desired); err != nil {
		if apierrors.IsAlreadyExists(err) {
			return nil
		}
		if apimeta.IsNoMatchError(err) {
			logger.V(1).Info("SecretStore CRD not installed, skipping default SecretStore provisioning")
			return nil
		}
		return fmt.Errorf("creating default SecretStore: %w", err)
	}
	return nil
}

// ensureDefaultServiceAccount get-or-creates a ServiceAccount named after the namespace
// itself — the naming convention the Vault-side kubernetes-auth role (provisioned outside
// this operator) expects to bind to. corev1.ServiceAccount is a scheme-registered built-in
// type, so unlike SecretStore this uses the regular cached r.Get, consistent with every
// other core-type reconciler in this package.
func (r *AppDefinitionReconciler) ensureDefaultServiceAccount(ctx context.Context, namespace string) error {
	sa := &corev1.ServiceAccount{}
	saKey := types.NamespacedName{Name: namespace, Namespace: namespace}
	if err := r.Get(ctx, saKey, sa); err == nil {
		return nil // already exists — get-or-create only, never updated.
	} else if !apierrors.IsNotFound(err) {
		return fmt.Errorf("getting default ServiceAccount: %w", err)
	}

	logger := log.FromContext(ctx)
	logger.Info("creating default ServiceAccount", "name", namespace, "namespace", namespace)
	sa = &corev1.ServiceAccount{
		ObjectMeta: metav1.ObjectMeta{Name: namespace, Namespace: namespace},
	}
	if err := r.Create(ctx, sa); err != nil && !apierrors.IsAlreadyExists(err) {
		return fmt.Errorf("creating default ServiceAccount: %w", err)
	}
	return nil
}
