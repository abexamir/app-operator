package controller

import (
	"context"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/apimachinery/pkg/types"

	appdefinitionv1 "github.com/abexamir/app-operator/api/v1"
)

var _ = Describe("buildSecretStore", func() {
	gvk := schema.GroupVersionKind{Group: "external-secrets.io", Version: "v1", Kind: "SecretStore"}
	vaultField := func(obj *unstructured.Unstructured, fields ...string) string {
		v, _, err := unstructured.NestedString(obj.Object, append([]string{"spec", "provider", "vault"}, fields...)...)
		Expect(err).NotTo(HaveOccurred())
		return v
	}
	appDef := func(store *appdefinitionv1.SecretStoreSpec) *appdefinitionv1.AppDefinition {
		return &appdefinitionv1.AppDefinition{
			ObjectMeta: metav1.ObjectMeta{Name: "web", Namespace: "team-a"},
			Spec:       appdefinitionv1.AppDefinitionSpec{SecretStore: store},
		}
	}

	It("uses the user's Vault server and fills defaults for a Namespace-scoped store", func() {
		obj := buildSecretStore(appDef(&appdefinitionv1.SecretStoreSpec{
			Name:  "vault",
			Vault: appdefinitionv1.VaultSecretStoreSpec{Server: "https://vault.example.com"},
		}), gvk)

		Expect(obj.GetName()).To(Equal("vault"))
		Expect(obj.GetNamespace()).To(Equal("team-a"))
		Expect(vaultField(obj, "server")).To(Equal("https://vault.example.com"))
		Expect(vaultField(obj, "path")).To(Equal("secret"))
		Expect(vaultField(obj, "version")).To(Equal("v2"))
		Expect(vaultField(obj, "auth", "kubernetes", "mountPath")).To(Equal("kubernetes"))
		Expect(vaultField(obj, "auth", "kubernetes", "role")).To(Equal("team-a-team-a"))
		Expect(vaultField(obj, "auth", "kubernetes", "serviceAccountRef", "name")).To(Equal("team-a"))
	})

	It("authenticates an App-scoped store as the app's own ServiceAccount", func() {
		obj := buildSecretStore(appDef(&appdefinitionv1.SecretStoreSpec{
			Name:  "vault-web",
			Scope: secretStoreScopeApp,
			Vault: appdefinitionv1.VaultSecretStoreSpec{Server: "https://vault.example.com"},
		}), gvk)

		Expect(vaultField(obj, "auth", "kubernetes", "role")).To(Equal("team-a-web"))
		Expect(vaultField(obj, "auth", "kubernetes", "serviceAccountRef", "name")).To(Equal("web"))
	})

	It("honors explicit path, version, auth mount and role", func() {
		obj := buildSecretStore(appDef(&appdefinitionv1.SecretStoreSpec{
			Name: "vault",
			Vault: appdefinitionv1.VaultSecretStoreSpec{
				Server: "https://vault.example.com", Path: "kv", Version: "v1",
				AuthMountPath: "kubernetes-prod", Role: "custom-role",
			},
		}), gvk)

		Expect(vaultField(obj, "path")).To(Equal("kv"))
		Expect(vaultField(obj, "version")).To(Equal("v1"))
		Expect(vaultField(obj, "auth", "kubernetes", "mountPath")).To(Equal("kubernetes-prod"))
		Expect(vaultField(obj, "auth", "kubernetes", "role")).To(Equal("custom-role"))
	})
})

// The envtest suite doesn't install external-secrets.io CRDs, so only the ServiceAccount half of
// reconcileSecretStore runs against the real API server; the SecretStore half is skipped as a
// missing CRD.
var _ = Describe("reconcileSecretStore's ServiceAccount half", func() {
	ctx := context.Background()
	const ns = "default"
	minimalContainers := []appdefinitionv1.ContainerSpec{{Name: "app", Image: "busybox:1.36"}}
	vault := appdefinitionv1.VaultSecretStoreSpec{Server: "https://vault.example.com"}

	newReconciler := func() *AppDefinitionReconciler {
		return &AppDefinitionReconciler{Client: k8sClient, Scheme: k8sClient.Scheme(), APIReader: k8sClient}
	}
	createApp := func(name string, store *appdefinitionv1.SecretStoreSpec) *appdefinitionv1.AppDefinition {
		app := &appdefinitionv1.AppDefinition{
			ObjectMeta: metav1.ObjectMeta{Name: name, Namespace: ns},
			Spec:       appdefinitionv1.AppDefinitionSpec{Containers: minimalContainers, SecretStore: store},
		}
		Expect(k8sClient.Create(ctx, app)).To(Succeed())
		DeferCleanup(func() { _ = k8sClient.Delete(ctx, app) })
		return app
	}

	It("creates an unowned ServiceAccount named after the namespace for a Namespace-scoped store", func() {
		app := createApp("ns-store-app", &appdefinitionv1.SecretStoreSpec{Name: "vault", Vault: vault})

		Expect(newReconciler().reconcileSecretStore(ctx, app)).To(Succeed())

		sa := &corev1.ServiceAccount{}
		Expect(k8sClient.Get(ctx, types.NamespacedName{Name: ns, Namespace: ns}, sa)).To(Succeed())
		Expect(sa.OwnerReferences).To(BeEmpty())
	})

	It("creates a ServiceAccount named after the app, owned by it, for an App-scoped store", func() {
		app := createApp("app-store-app", &appdefinitionv1.SecretStoreSpec{Name: "vault-app-store-app", Scope: secretStoreScopeApp, Vault: vault})

		Expect(newReconciler().reconcileSecretStore(ctx, app)).To(Succeed())

		sa := &corev1.ServiceAccount{}
		Expect(k8sClient.Get(ctx, types.NamespacedName{Name: app.Name, Namespace: ns}, sa)).To(Succeed())
		Expect(sa.OwnerReferences).To(HaveLen(1))
		Expect(sa.OwnerReferences[0].Name).To(Equal(app.Name))
		Expect(sa.OwnerReferences[0].Kind).To(Equal("AppDefinition"))
	})

	It("does nothing when spec.secretStore is unset", func() {
		app := createApp("no-store-app", nil)

		Expect(newReconciler().reconcileSecretStore(ctx, app)).To(Succeed())

		err := k8sClient.Get(ctx, types.NamespacedName{Name: app.Name, Namespace: ns}, &corev1.ServiceAccount{})
		Expect(errors.IsNotFound(err)).To(BeTrue())
	})

	It("does not overwrite an existing ServiceAccount", func() {
		app := createApp("reuse-store-app", &appdefinitionv1.SecretStoreSpec{Name: "vault-reuse", Scope: secretStoreScopeApp, Vault: vault})
		r := newReconciler()
		Expect(r.reconcileSecretStore(ctx, app)).To(Succeed())

		first := &corev1.ServiceAccount{}
		Expect(k8sClient.Get(ctx, types.NamespacedName{Name: app.Name, Namespace: ns}, first)).To(Succeed())
		Expect(r.reconcileSecretStore(ctx, app)).To(Succeed())

		after := &corev1.ServiceAccount{}
		Expect(k8sClient.Get(ctx, types.NamespacedName{Name: app.Name, Namespace: ns}, after)).To(Succeed())
		Expect(after.ResourceVersion).To(Equal(first.ResourceVersion))
	})
})
