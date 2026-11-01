package controller

import (
	"context"
	"path/filepath"
	"strings"
	"testing"

	appdefinitionv1 "github.com/abexamir/app-operator/api/v1"
	. "github.com/onsi/gomega"
	networkingv1 "k8s.io/api/networking/v1"
	apiextensionsv1 "k8s.io/apiextensions-apiserver/pkg/apis/apiextensions/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/apimachinery/pkg/types"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/envtest"
)

// Use a separate real API server so installing Traefik's resource type does not change
// the main suite's missing-CRD tests. The fixture preserves the rendered Traefik spec.
func TestCompressionMiddleware(t *testing.T) {
	g := NewWithT(t)
	preserve := true
	environment := &envtest.Environment{
		CRDDirectoryPaths:     []string{filepath.Join("..", "..", "config", "crd", "bases")},
		ErrorIfCRDPathMissing: true,
		BinaryAssetsDirectory: getFirstFoundEnvTestBinaryDir(),
		CRDs: []*apiextensionsv1.CustomResourceDefinition{{
			ObjectMeta: metav1.ObjectMeta{Name: "middlewares.traefik.io"},
			Spec: apiextensionsv1.CustomResourceDefinitionSpec{
				Group: "traefik.io", Scope: apiextensionsv1.NamespaceScoped,
				Names: apiextensionsv1.CustomResourceDefinitionNames{Plural: "middlewares", Singular: "middleware", Kind: "Middleware", ListKind: "MiddlewareList"},
				Versions: []apiextensionsv1.CustomResourceDefinitionVersion{{
					Name: "v1alpha1", Served: true, Storage: true,
					Schema: &apiextensionsv1.CustomResourceValidation{OpenAPIV3Schema: &apiextensionsv1.JSONSchemaProps{
						Type: "object", Properties: map[string]apiextensionsv1.JSONSchemaProps{
							"spec": {Type: "object", XPreserveUnknownFields: &preserve},
						},
					}},
				}},
			},
		}},
	}
	config, err := environment.Start()
	g.Expect(err).NotTo(HaveOccurred())
	t.Cleanup(func() { g.Expect(environment.Stop()).To(Succeed()) })
	scheme := runtime.NewScheme()
	g.Expect(appdefinitionv1.AddToScheme(scheme)).To(Succeed())
	g.Expect(networkingv1.AddToScheme(scheme)).To(Succeed())
	c, err := client.New(config, client.Options{Scheme: scheme})
	g.Expect(err).NotTo(HaveOccurred())
	r := &AppDefinitionReconciler{Client: c, APIReader: c, Scheme: scheme}
	ctx := context.Background()
	app := &appdefinitionv1.AppDefinition{
		ObjectMeta: metav1.ObjectMeta{Name: "compress-test", Namespace: "default"},
		Spec: appdefinitionv1.AppDefinitionSpec{
			Containers: []appdefinitionv1.ContainerSpec{{Name: "web", Image: "nginx:latest"}},
			Domains: []appdefinitionv1.DomainSpec{{
				Name: "compress.example.com", TLS: true, RedirectTLS: true,
				Middlewares: &appdefinitionv1.MiddlewaresSpec{Compress: &appdefinitionv1.CompressSpec{}},
			}},
		},
	}
	g.Expect(c.Create(ctx, app)).To(Succeed())
	key := types.NamespacedName{Name: app.Name, Namespace: app.Namespace}
	g.Expect(c.Get(ctx, key, app)).To(Succeed())
	g.Expect(app.Spec.Domains[0].Middlewares.Compress).NotTo(BeNil())
	middleware := &unstructured.Unstructured{}
	middleware.SetGroupVersionKind(schema.GroupVersionKind{Group: "traefik.io", Version: "v1alpha1", Kind: "Middleware"})
	middlewareKey := types.NamespacedName{Name: middlewareName(app.Name, app.Spec.Domains[0].Name, "", "compress"), Namespace: app.Namespace}
	reconcile := func() {
		g.Expect(r.reconcileMiddlewares(ctx, app)).To(Succeed())
		g.Expect(r.reconcileIngress(ctx, app)).To(Succeed())
	}
	reconcile()
	g.Expect(c.Get(ctx, middlewareKey, middleware)).To(Succeed())
	g.Expect(middleware.Object["spec"]).To(Equal(map[string]interface{}{"compress": map[string]interface{}{}}))
	g.Expect(metav1.IsControlledBy(middleware, app)).To(BeTrue())

	// Update defaults to explicit settings, including zero, and reconcile twice for idempotency.
	zero := int64(0)
	app.Spec.Domains[0].Middlewares = &appdefinitionv1.MiddlewaresSpec{
		Compress: &appdefinitionv1.CompressSpec{MinResponseBodyBytes: &zero, ExcludedContentTypes: []string{"text/event-stream"}},
		Headers:  &appdefinitionv1.HeadersSpec{FrameDeny: true},
		Rewrite:  &appdefinitionv1.RewriteSpec{AddPrefix: "/api"},
	}
	g.Expect(c.Update(ctx, app)).To(Succeed())
	reconcile()
	reconcile()
	g.Expect(c.Get(ctx, middlewareKey, middleware)).To(Succeed())
	g.Expect(middleware.Object["spec"]).To(Equal(map[string]interface{}{"compress": map[string]interface{}{
		"minResponseBodyBytes": int64(0), "excludedContentTypes": []interface{}{"text/event-stream"},
	}}))
	ingress := &networkingv1.Ingress{}
	ingressKey := types.NamespacedName{Name: domainIngressName(app.Name, app.Spec.Domains[0].Name, ""), Namespace: app.Namespace}
	g.Expect(c.Get(ctx, ingressKey, ingress)).To(Succeed())
	refs := []string{}
	for _, kind := range []string{"redirectscheme", "headers", "compress", "rewrite"} {
		refs = append(refs, app.Namespace+"-"+middlewareName(app.Name, app.Spec.Domains[0].Name, "", kind)+"@kubernetescrd")
	}
	g.Expect(ingress.Annotations["traefik.ingress.kubernetes.io/router.middlewares"]).To(Equal(strings.Join(refs, ",")))

	app.Spec.Domains[0].Middlewares.Compress.ExcludedContentTypes = nil
	app.Spec.Domains[0].Middlewares.Compress.IncludedContentTypes = []string{"application/json"}
	app.Spec.Domains[0].Middlewares.Compress.MinResponseBodyBytes = nil
	g.Expect(c.Update(ctx, app)).To(Succeed())
	reconcile()
	g.Expect(c.Get(ctx, middlewareKey, middleware)).To(Succeed())
	g.Expect(middleware.Object["spec"]).To(Equal(map[string]interface{}{"compress": map[string]interface{}{
		"includedContentTypes": []interface{}{"application/json"},
	}}))

	// The generated AppDefinition CRD must reject invalid compression settings.
	invalid := app.DeepCopy()
	invalid.Spec.Domains[0].Middlewares.Compress.ExcludedContentTypes = []string{"text/plain"}
	g.Expect(apierrors.IsInvalid(c.Update(ctx, invalid))).To(BeTrue())
	negative := int64(-1)
	invalid = app.DeepCopy()
	invalid.Spec.Domains[0].Middlewares.Compress.MinResponseBodyBytes = &negative
	g.Expect(apierrors.IsInvalid(c.Update(ctx, invalid))).To(BeTrue())

	// Removing compression removes both the resource and the generated ingress reference.
	app.Spec.Domains[0].Middlewares.Compress = nil
	g.Expect(c.Update(ctx, app)).To(Succeed())
	reconcile()
	g.Expect(apierrors.IsNotFound(c.Get(ctx, middlewareKey, middleware))).To(BeTrue())
	g.Expect(c.Get(ctx, ingressKey, ingress)).To(Succeed())
	g.Expect(ingress.Annotations["traefik.ingress.kubernetes.io/router.middlewares"]).NotTo(ContainSubstring("-compress@kubernetescrd"))

	// Explicit scheme redirection works without TLS and is ordered before compression.
	app.Spec.Domains[0].TLS = false
	app.Spec.Domains[0].RedirectTLS = false
	app.Spec.Domains[0].Middlewares = &appdefinitionv1.MiddlewaresSpec{
		RedirectScheme: &appdefinitionv1.RedirectSchemeSpec{Scheme: "https", Permanent: true},
		Compress:       &appdefinitionv1.CompressSpec{},
	}
	g.Expect(c.Update(ctx, app)).To(Succeed())
	reconcile()
	redirect := &unstructured.Unstructured{}
	redirect.SetGroupVersionKind(middleware.GroupVersionKind())
	redirectKey := types.NamespacedName{Name: middlewareName(app.Name, app.Spec.Domains[0].Name, "", "redirectscheme"), Namespace: app.Namespace}
	g.Expect(c.Get(ctx, redirectKey, redirect)).To(Succeed())
	g.Expect(redirect.Object["spec"]).To(Equal(map[string]interface{}{"redirectScheme": map[string]interface{}{"scheme": "https", "permanent": true}}))
	g.Expect(metav1.IsControlledBy(redirect, app)).To(BeTrue())
	g.Expect(c.Get(ctx, ingressKey, ingress)).To(Succeed())
	g.Expect(ingress.Spec.TLS).To(BeEmpty())
	g.Expect(ingress.Annotations["traefik.ingress.kubernetes.io/router.middlewares"]).To(Equal(
		app.Namespace + "-" + redirectKey.Name + "@kubernetescrd," + app.Namespace + "-" + middlewareKey.Name + "@kubernetescrd"))

	// Explicit options override automatic TLS redirection without a second middleware.
	app.Spec.Domains[0].TLS = true
	app.Spec.Domains[0].RedirectTLS = true
	app.Spec.Domains[0].Middlewares.RedirectScheme = &appdefinitionv1.RedirectSchemeSpec{Scheme: "http", Port: "8080"}
	g.Expect(c.Update(ctx, app)).To(Succeed())
	reconcile()
	reconcile()
	g.Expect(c.Get(ctx, redirectKey, redirect)).To(Succeed())
	g.Expect(redirect.Object["spec"]).To(Equal(map[string]interface{}{"redirectScheme": map[string]interface{}{"scheme": "http", "permanent": false, "port": "8080"}}))
	g.Expect(c.Get(ctx, ingressKey, ingress)).To(Succeed())
	g.Expect(strings.Count(ingress.Annotations["traefik.ingress.kubernetes.io/router.middlewares"], "-redirectscheme@kubernetescrd")).To(Equal(1))
	invalid = app.DeepCopy()
	invalid.Spec.Domains[0].Middlewares.RedirectScheme.Scheme = ""
	g.Expect(apierrors.IsInvalid(c.Update(ctx, invalid))).To(BeTrue())

	// Removing the explicit block prunes it; redirect_tls alone still requires TLS.
	app.Spec.Domains[0].TLS = false
	app.Spec.Domains[0].Middlewares.RedirectScheme = nil
	g.Expect(c.Update(ctx, app)).To(Succeed())
	reconcile()
	g.Expect(apierrors.IsNotFound(c.Get(ctx, redirectKey, redirect))).To(BeTrue())
	g.Expect(c.Get(ctx, ingressKey, ingress)).To(Succeed())
	g.Expect(ingress.Annotations["traefik.ingress.kubernetes.io/router.middlewares"]).To(Equal(app.Namespace + "-" + middlewareKey.Name + "@kubernetescrd"))
}
