package controller

import (
	"context"
	"time"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
	corev1 "k8s.io/api/core/v1"
	apimeta "k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/apimachinery/pkg/types"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"
	"sigs.k8s.io/controller-runtime/pkg/event"
	"sigs.k8s.io/controller-runtime/pkg/reconcile"

	appdefinitionv1 "github.com/abexamir/app-operator/api/v1"
)

// The envtest suite doesn't install external-secrets.io CRDs, so these tests use a fake client
// whose RESTMapper knows the ESO kinds.
var _ = Describe("ExternalSecret retry", func() {
	const namespace = "team-a"
	storeGVK := schema.GroupVersionKind{Group: "external-secrets.io", Version: "v1", Kind: "SecretStore"}
	esGVK := schema.GroupVersionKind{Group: "external-secrets.io", Version: "v1", Kind: "ExternalSecret"}

	esoObject := func(gvk schema.GroupVersionKind, name, readyStatus string) *unstructured.Unstructured {
		obj := &unstructured.Unstructured{}
		obj.SetGroupVersionKind(gvk)
		obj.SetName(name)
		obj.SetNamespace(namespace)
		if readyStatus != "" {
			Expect(unstructured.SetNestedSlice(obj.Object, []interface{}{
				map[string]interface{}{"type": "Ready", "status": readyStatus},
			}, "status", "conditions")).To(Succeed())
		}
		return obj
	}

	app := &appdefinitionv1.AppDefinition{
		ObjectMeta: metav1.ObjectMeta{Name: "web", Namespace: namespace},
		Spec: appdefinitionv1.AppDefinitionSpec{ExternalSecrets: []appdefinitionv1.ExternalSecretMount{{
			Name: "creds", Store: defaultSecretStoreName, StoreKind: "SecretStore",
		}}},
	}

	newReconciler := func(objs ...*unstructured.Unstructured) *AppDefinitionReconciler {
		scheme := runtime.NewScheme()
		Expect(corev1.AddToScheme(scheme)).To(Succeed())
		Expect(appdefinitionv1.AddToScheme(scheme)).To(Succeed())
		mapper := apimeta.NewDefaultRESTMapper(nil)
		mapper.Add(storeGVK, apimeta.RESTScopeNamespace)
		mapper.Add(esGVK, apimeta.RESTScopeNamespace)
		b := fake.NewClientBuilder().WithScheme(scheme).WithRESTMapper(mapper)
		for _, o := range objs {
			b = b.WithObjects(o)
		}
		c := b.Build()
		return &AppDefinitionReconciler{Client: c, APIReader: c, Scheme: scheme}
	}

	annotationOf := func(r *AppDefinitionReconciler, gvk schema.GroupVersionKind, name string) string {
		obj := &unstructured.Unstructured{}
		obj.SetGroupVersionKind(gvk)
		Expect(r.Get(context.Background(), types.NamespacedName{Name: name, Namespace: namespace}, obj)).To(Succeed())
		return obj.GetAnnotations()[revalidateAnnotation]
	}

	It("annotates a not-Ready store and ExternalSecret, then throttles repeat nudges", func() {
		r := newReconciler(
			esoObject(storeGVK, defaultSecretStoreName, "False"),
			esoObject(esGVK, "web-creds", "False"),
		)
		pending, err := r.retryFailedExternalSecrets(context.Background(), app)
		Expect(err).NotTo(HaveOccurred())
		Expect(pending).To(BeTrue())
		storeStamp := annotationOf(r, storeGVK, defaultSecretStoreName)
		Expect(storeStamp).NotTo(BeEmpty())
		Expect(annotationOf(r, esGVK, "web-creds")).NotTo(BeEmpty())

		pending, err = r.retryFailedExternalSecrets(context.Background(), app)
		Expect(err).NotTo(HaveOccurred())
		Expect(pending).To(BeTrue())
		Expect(annotationOf(r, storeGVK, defaultSecretStoreName)).To(Equal(storeStamp))
	})

	It("leaves Ready resources alone and reports nothing pending", func() {
		r := newReconciler(
			esoObject(storeGVK, defaultSecretStoreName, "True"),
			esoObject(esGVK, "web-creds", "True"),
		)
		pending, err := r.retryFailedExternalSecrets(context.Background(), app)
		Expect(err).NotTo(HaveOccurred())
		Expect(pending).To(BeFalse())
		Expect(annotationOf(r, storeGVK, defaultSecretStoreName)).To(BeEmpty())
		Expect(annotationOf(r, esGVK, "web-creds")).To(BeEmpty())
	})

	It("waits for ESO to report status before nudging", func() {
		r := newReconciler(
			esoObject(storeGVK, defaultSecretStoreName, ""),
			esoObject(esGVK, "web-creds", "True"),
		)
		pending, err := r.retryFailedExternalSecrets(context.Background(), app)
		Expect(err).NotTo(HaveOccurred())
		Expect(pending).To(BeTrue())
		Expect(annotationOf(r, storeGVK, defaultSecretStoreName)).To(BeEmpty())
	})

	It("treats a nudge older than the retry interval as due", func() {
		obj := esoObject(storeGVK, defaultSecretStoreName, "False")
		now := time.Now()
		obj.SetAnnotations(map[string]string{revalidateAnnotation: now.Add(-externalSecretsRetryInterval).UTC().Format(time.RFC3339)})
		Expect(revalidationDue(obj, now)).To(BeTrue())
		obj.SetAnnotations(map[string]string{revalidateAnnotation: now.UTC().Format(time.RFC3339)})
		Expect(revalidationDue(obj, now)).To(BeFalse())
	})

	Describe("esoDependencyPredicate", func() {
		It("fires on Ready transitions and deletes, not on status churn or creates", func() {
			notReady := esoObject(storeGVK, defaultSecretStoreName, "False")
			ready := esoObject(storeGVK, defaultSecretStoreName, "True")
			Expect(esoDependencyPredicate.Update(event.UpdateEvent{ObjectOld: notReady, ObjectNew: ready})).To(BeTrue())
			Expect(esoDependencyPredicate.Update(event.UpdateEvent{ObjectOld: ready, ObjectNew: ready.DeepCopy()})).To(BeFalse())
			Expect(esoDependencyPredicate.Delete(event.DeleteEvent{Object: ready})).To(BeTrue())
			Expect(esoDependencyPredicate.Create(event.CreateEvent{Object: ready})).To(BeFalse())
		})
	})

	Describe("mapSecretStoreDependencyToAppDefinitions", func() {
		It("maps an owned per-app store to its AppDefinition", func() {
			r := newReconciler()
			store := esoObject(storeGVK, perAppSecretStoreName("web"), "")
			owner := app.DeepCopy()
			owner.UID = "uid-1"
			Expect(ctrl.SetControllerReference(owner, store, r.Scheme)).To(Succeed())
			Expect(r.mapSecretStoreDependencyToAppDefinitions(context.Background(), store)).To(ConsistOf(
				reconcile.Request{NamespacedName: types.NamespacedName{Name: "web", Namespace: namespace}},
			))
		})

		It("maps the shared default store and its ServiceAccount to every app using it", func() {
			r := newReconciler()
			other := &appdefinitionv1.AppDefinition{ObjectMeta: metav1.ObjectMeta{Name: "worker", Namespace: namespace}}
			Expect(r.Create(context.Background(), app.DeepCopy())).To(Succeed())
			Expect(r.Create(context.Background(), other)).To(Succeed())
			want := reconcile.Request{NamespacedName: types.NamespacedName{Name: "web", Namespace: namespace}}

			store := esoObject(storeGVK, defaultSecretStoreName, "")
			Expect(r.mapSecretStoreDependencyToAppDefinitions(context.Background(), store)).To(ConsistOf(want))

			sa := &corev1.ServiceAccount{ObjectMeta: metav1.ObjectMeta{Name: namespace, Namespace: namespace}}
			Expect(r.mapSecretStoreDependencyToAppDefinitions(context.Background(), sa)).To(ConsistOf(want))

			unrelated := &corev1.ServiceAccount{ObjectMeta: metav1.ObjectMeta{Name: "default", Namespace: namespace}}
			Expect(r.mapSecretStoreDependencyToAppDefinitions(context.Background(), unrelated)).To(BeEmpty())
		})
	})
})
