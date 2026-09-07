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

// usesPerAppSecretStore is a pure function — tested directly here, same reasoning as
// usesDefaultSecretStore's own test doc comment (the full get-or-create-SecretStore path needs a
// live external-secrets.io CRD this suite's envtest environment does not install).
var _ = Describe("usesPerAppSecretStore", func() {
	appDefWithExternalSecrets := func(entries ...appdefinitionv1.ExternalSecretMount) *appdefinitionv1.AppDefinition {
		return &appdefinitionv1.AppDefinition{
			ObjectMeta: metav1.ObjectMeta{Name: "my-app"},
			Spec:       appdefinitionv1.AppDefinitionSpec{ExternalSecrets: entries},
		}
	}

	It("returns false when there are no externalSecrets at all", func() {
		Expect(usesPerAppSecretStore(appDefWithExternalSecrets())).To(BeFalse())
	})

	It("returns true for store: vault-zarrino-<appName> with explicit storeKind: SecretStore", func() {
		app := appDefWithExternalSecrets(appdefinitionv1.ExternalSecretMount{
			Name: "app-secrets", Store: "vault-zarrino-my-app", StoreKind: "SecretStore",
		})
		Expect(usesPerAppSecretStore(app)).To(BeTrue())
	})

	It("returns false for another app's per-app store name — never claim a sibling's identity", func() {
		app := appDefWithExternalSecrets(appdefinitionv1.ExternalSecretMount{
			Name: "app-secrets", Store: "vault-zarrino-some-other-app", StoreKind: "SecretStore",
		})
		Expect(usesPerAppSecretStore(app)).To(BeFalse())
	})

	It("returns false for the shared default store name — that's usesDefaultSecretStore's job", func() {
		app := appDefWithExternalSecrets(appdefinitionv1.ExternalSecretMount{
			Name: "app-secrets", Store: defaultSecretStoreName, StoreKind: "SecretStore",
		})
		Expect(usesPerAppSecretStore(app)).To(BeFalse())
	})

	It("returns false when storeKind is omitted, since only ClusterSecretStore is the implicit default", func() {
		app := appDefWithExternalSecrets(appdefinitionv1.ExternalSecretMount{
			Name: "app-secrets", Store: "vault-zarrino-my-app",
		})
		Expect(usesPerAppSecretStore(app)).To(BeFalse())
	})

	It("returns true when at least one of several entries references its own per-app store", func() {
		app := appDefWithExternalSecrets(
			appdefinitionv1.ExternalSecretMount{Name: "a", Store: "some-other-store", StoreKind: "SecretStore"},
			appdefinitionv1.ExternalSecretMount{Name: "b", Store: "vault-zarrino-my-app", StoreKind: "SecretStore"},
		)
		Expect(usesPerAppSecretStore(app)).To(BeTrue())
	})
})

var _ = Describe("reconcilePerAppSecretStore's ServiceAccount half", func() {
	ctx := context.Background()

	newReconciler := func() *AppDefinitionReconciler {
		return &AppDefinitionReconciler{Client: k8sClient, Scheme: k8sClient.Scheme(), APIReader: k8sClient}
	}
	// spec.containers is required (MinItems=1) by the CRD schema; these tests only care about the
	// ExternalSecrets/ServiceAccount behavior, so a minimal placeholder suffices.
	minimalContainers := []appdefinitionv1.ContainerSpec{{Name: "app", Image: "busybox:1.36"}}

	It("creates a ServiceAccount named after the AppDefinition, owner-referenced to it", func() {
		const ns = "default"
		app := &appdefinitionv1.AppDefinition{
			ObjectMeta: metav1.ObjectMeta{Name: "per-app-store-app", Namespace: ns},
			Spec: appdefinitionv1.AppDefinitionSpec{
				Containers: minimalContainers,
				ExternalSecrets: []appdefinitionv1.ExternalSecretMount{
					{Name: "app-secrets", Store: "vault-zarrino-per-app-store-app", StoreKind: "SecretStore", DataFrom: []appdefinitionv1.ExternalSecretDataFrom{{Key: ns + "/per-app-store-app/app"}}},
				},
			},
		}
		Expect(k8sClient.Create(ctx, app)).To(Succeed())
		DeferCleanup(func() { _ = k8sClient.Delete(ctx, app) })

		Expect(newReconciler().reconcilePerAppSecretStore(ctx, app)).To(Succeed())

		sa := &corev1.ServiceAccount{}
		Expect(k8sClient.Get(ctx, types.NamespacedName{Name: app.Name, Namespace: ns}, sa)).To(Succeed())
		// Owner-referenced to this specific AppDefinition — unlike the shared default's
		// namespace-wide singleton, this one is 1:1 by construction (the store name is
		// parameterized by this exact AppDefinition's own name) and safe to garbage-collect.
		Expect(sa.OwnerReferences).To(HaveLen(1))
		Expect(sa.OwnerReferences[0].Name).To(Equal(app.Name))
		Expect(sa.OwnerReferences[0].Kind).To(Equal("AppDefinition"))
	})

	It("does not create a ServiceAccount when no externalSecrets entry references its own per-app store", func() {
		const ns = "default"
		app := &appdefinitionv1.AppDefinition{
			ObjectMeta: metav1.ObjectMeta{Name: "no-per-app-store-app", Namespace: ns},
			Spec: appdefinitionv1.AppDefinitionSpec{
				Containers: minimalContainers,
				ExternalSecrets: []appdefinitionv1.ExternalSecretMount{
					{Name: "app-secrets", Store: defaultSecretStoreName, StoreKind: "SecretStore", DataFrom: []appdefinitionv1.ExternalSecretDataFrom{{Key: ns + "/app"}}},
				},
			},
		}
		Expect(k8sClient.Create(ctx, app)).To(Succeed())
		DeferCleanup(func() { _ = k8sClient.Delete(ctx, app) })

		Expect(newReconciler().reconcilePerAppSecretStore(ctx, app)).To(Succeed())

		sa := &corev1.ServiceAccount{}
		err := k8sClient.Get(ctx, types.NamespacedName{Name: app.Name, Namespace: ns}, sa)
		Expect(errors.IsNotFound(err)).To(BeTrue())
	})

	It("does not error or overwrite an already-existing ServiceAccount", func() {
		const ns = "default"
		app := &appdefinitionv1.AppDefinition{
			ObjectMeta: metav1.ObjectMeta{Name: "reuse-per-app-store-app", Namespace: ns},
			Spec: appdefinitionv1.AppDefinitionSpec{
				Containers: minimalContainers,
				ExternalSecrets: []appdefinitionv1.ExternalSecretMount{
					{Name: "app-secrets", Store: "vault-zarrino-reuse-per-app-store-app", StoreKind: "SecretStore", DataFrom: []appdefinitionv1.ExternalSecretDataFrom{{Key: ns + "/reuse-per-app-store-app/app"}}},
				},
			},
		}
		Expect(k8sClient.Create(ctx, app)).To(Succeed())
		DeferCleanup(func() { _ = k8sClient.Delete(ctx, app) })

		reconciler := newReconciler()
		Expect(reconciler.reconcilePerAppSecretStore(ctx, app)).To(Succeed())

		first := &corev1.ServiceAccount{}
		Expect(k8sClient.Get(ctx, types.NamespacedName{Name: app.Name, Namespace: ns}, first)).To(Succeed())
		resourceVersionBefore := first.ResourceVersion

		Expect(reconciler.reconcilePerAppSecretStore(ctx, app)).To(Succeed())

		after := &corev1.ServiceAccount{}
		Expect(k8sClient.Get(ctx, types.NamespacedName{Name: app.Name, Namespace: ns}, after)).To(Succeed())
		Expect(after.ResourceVersion).To(Equal(resourceVersionBefore))
	})
})
