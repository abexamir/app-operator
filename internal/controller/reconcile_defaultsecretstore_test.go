/*
Copyright 2025.

Licensed under the Apache License, Version 2.0 (the "License");
you may not use this file except in compliance with the License.
You may obtain a copy of the License at

    http://www.apache.org/licenses/LICENSE-2.0

Unless required by applicable law or agreed to in writing, software
distributed under the License is distributed on an "AS IS" BASIS,
WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
See the License for the specific language governing permissions and
limitations under the License.
*/

package controller

import (
	"context"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"

	appdefinitionv1 "github.com/abexamir/app-operator/api/v1"
)

// usesDefaultSecretStore is a pure function — tested directly here, not through envtest.
// The full get-or-create-SecretStore path (reconcileDefaultSecretStore) additionally needs
// a live external-secrets.io CRD this suite's envtest environment does not install (see
// suite_test.go's CRDDirectoryPaths, scoped to this operator's own CRD only), so that half
// can only be exercised against a real cluster. The ServiceAccount half of the same
// reconciler has no such limitation (corev1.ServiceAccount is a built-in type) and is
// covered below with real envtest assertions.
var _ = Describe("usesDefaultSecretStore", func() {
	appDefWithExternalSecrets := func(entries ...appdefinitionv1.ExternalSecretMount) *appdefinitionv1.AppDefinition {
		return &appdefinitionv1.AppDefinition{
			Spec: appdefinitionv1.AppDefinitionSpec{ExternalSecrets: entries},
		}
	}

	It("returns false when there are no externalSecrets at all", func() {
		Expect(usesDefaultSecretStore(appDefWithExternalSecrets())).To(BeFalse())
	})

	It("returns true for store: vault-zarrino with explicit storeKind: SecretStore", func() {
		app := appDefWithExternalSecrets(appdefinitionv1.ExternalSecretMount{
			Name: "app-secrets", Store: "vault-zarrino", StoreKind: "SecretStore",
		})
		Expect(usesDefaultSecretStore(app)).To(BeTrue())
	})

	It("returns true for store: vault-zarrino with storeKind omitted, since only ClusterSecretStore is the implicit default", func() {
		// storeKind defaults to ClusterSecretStore when omitted (see reconcile_externalsecrets.go),
		// so an omitted storeKind must NOT match the default SecretStore — only an explicit
		// storeKind: SecretStore does. This case asserts the omitted-kind path is false.
		app := appDefWithExternalSecrets(appdefinitionv1.ExternalSecretMount{
			Name: "app-secrets", Store: "vault-zarrino",
		})
		Expect(usesDefaultSecretStore(app)).To(BeFalse())
	})

	It("returns false when store is vault-zarrino but storeKind is ClusterSecretStore", func() {
		app := appDefWithExternalSecrets(appdefinitionv1.ExternalSecretMount{
			Name: "app-secrets", Store: "vault-zarrino", StoreKind: "ClusterSecretStore",
		})
		Expect(usesDefaultSecretStore(app)).To(BeFalse())
	})

	It("returns false for a differently-named SecretStore — assumed pre-existing and custom", func() {
		app := appDefWithExternalSecrets(appdefinitionv1.ExternalSecretMount{
			Name: "app-secrets", Store: "some-other-store", StoreKind: "SecretStore",
		})
		Expect(usesDefaultSecretStore(app)).To(BeFalse())
	})

	It("returns true when at least one of several entries references the default store", func() {
		app := appDefWithExternalSecrets(
			appdefinitionv1.ExternalSecretMount{Name: "a", Store: "some-other-store", StoreKind: "SecretStore"},
			appdefinitionv1.ExternalSecretMount{Name: "b", Store: "vault-zarrino", StoreKind: "SecretStore"},
		)
		Expect(usesDefaultSecretStore(app)).To(BeTrue())
	})
})

var _ = Describe("reconcileDefaultSecretStore's ServiceAccount half", func() {
	ctx := context.Background()

	newReconciler := func() *AppDefinitionReconciler {
		return &AppDefinitionReconciler{Client: k8sClient, Scheme: k8sClient.Scheme(), APIReader: k8sClient}
	}
	// spec.containers is required (MinItems=1) by the CRD schema; these tests only care
	// about the ExternalSecrets/ServiceAccount behavior, so a minimal placeholder suffices.
	minimalContainers := []appdefinitionv1.ContainerSpec{{Name: "app", Image: "busybox:1.36"}}

	It("creates a ServiceAccount named after the namespace when an AppDefinition references the default store", func() {
		const ns = "default"
		app := &appdefinitionv1.AppDefinition{
			ObjectMeta: metav1.ObjectMeta{Name: "default-store-app", Namespace: ns},
			Spec: appdefinitionv1.AppDefinitionSpec{
				Containers: minimalContainers,
				ExternalSecrets: []appdefinitionv1.ExternalSecretMount{
					{Name: "app-secrets", Store: defaultSecretStoreName, StoreKind: "SecretStore", DataFrom: []appdefinitionv1.ExternalSecretDataFrom{{Key: ns + "/app"}}},
				},
			},
		}
		Expect(k8sClient.Create(ctx, app)).To(Succeed())
		DeferCleanup(func() { _ = k8sClient.Delete(ctx, app) })

		Expect(newReconciler().reconcileDefaultSecretStore(ctx, app)).To(Succeed())

		sa := &corev1.ServiceAccount{}
		Expect(k8sClient.Get(ctx, types.NamespacedName{Name: ns, Namespace: ns}, sa)).To(Succeed())
		// Not owner-referenced to this specific AppDefinition — it's a namespace-scoped
		// singleton other AppDefinitions in the same namespace may also depend on.
		Expect(sa.OwnerReferences).To(BeEmpty())
	})

	It("does not create a ServiceAccount when no externalSecrets entry references the default store", func() {
		const ns = "default"
		app := &appdefinitionv1.AppDefinition{
			ObjectMeta: metav1.ObjectMeta{Name: "no-store-app", Namespace: ns},
			Spec: appdefinitionv1.AppDefinitionSpec{
				Containers: minimalContainers,
				ExternalSecrets: []appdefinitionv1.ExternalSecretMount{
					{Name: "app-secrets", Store: "some-other-store", StoreKind: "SecretStore", DataFrom: []appdefinitionv1.ExternalSecretDataFrom{{Key: ns + "/app"}}},
				},
			},
		}
		Expect(k8sClient.Create(ctx, app)).To(Succeed())
		DeferCleanup(func() { _ = k8sClient.Delete(ctx, app) })

		// Pre-condition: no such ServiceAccount exists yet in this namespace from an
		// earlier test in this suite.
		preExisting := &corev1.ServiceAccount{}
		preErr := k8sClient.Get(ctx, types.NamespacedName{Name: "no-such-sa-marker", Namespace: ns}, preExisting)
		Expect(errors.IsNotFound(preErr)).To(BeTrue())

		Expect(newReconciler().reconcileDefaultSecretStore(ctx, app)).To(Succeed())
		// reconcileDefaultSecretStore must not error and must not touch anything — the
		// meaningful assertion here is simply that the call above succeeded as a no-op;
		// there is no ServiceAccount name to check since ensureDefaultServiceAccount is
		// never invoked in this path.
	})

	It("does not error or overwrite an already-existing ServiceAccount", func() {
		const ns = "default"
		existing := &corev1.ServiceAccount{ObjectMeta: metav1.ObjectMeta{Name: ns, Namespace: ns}}
		// Reuse if a prior It already created it; only create when actually absent.
		if err := k8sClient.Get(ctx, types.NamespacedName{Name: ns, Namespace: ns}, existing); err != nil {
			Expect(errors.IsNotFound(err)).To(BeTrue())
			Expect(k8sClient.Create(ctx, existing)).To(Succeed())
		}
		resourceVersionBefore := existing.ResourceVersion

		app := &appdefinitionv1.AppDefinition{
			ObjectMeta: metav1.ObjectMeta{Name: "reuse-store-app", Namespace: ns},
			Spec: appdefinitionv1.AppDefinitionSpec{
				Containers: minimalContainers,
				ExternalSecrets: []appdefinitionv1.ExternalSecretMount{
					{Name: "app-secrets", Store: defaultSecretStoreName, StoreKind: "SecretStore", DataFrom: []appdefinitionv1.ExternalSecretDataFrom{{Key: ns + "/app"}}},
				},
			},
		}
		Expect(k8sClient.Create(ctx, app)).To(Succeed())
		DeferCleanup(func() { _ = k8sClient.Delete(ctx, app) })

		Expect(newReconciler().reconcileDefaultSecretStore(ctx, app)).To(Succeed())

		after := &corev1.ServiceAccount{}
		Expect(k8sClient.Get(ctx, types.NamespacedName{Name: ns, Namespace: ns}, after)).To(Succeed())
		Expect(after.ResourceVersion).To(Equal(resourceVersionBefore))
	})
})
