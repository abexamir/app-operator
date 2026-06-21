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
	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	"sigs.k8s.io/controller-runtime/pkg/reconcile"

	appdefinitionv1 "github.com/abexamir/app-operator/api/v1"
)

var _ = Describe("AppDefinition Controller", func() {
	Context("When reconciling a resource", func() {
		const resourceName = "test-app"
		const namespace = "default"

		ctx := context.Background()
		namespacedName := types.NamespacedName{Name: resourceName, Namespace: namespace}

		minimalSpec := appdefinitionv1.AppDefinitionSpec{
			Source: appdefinitionv1.SourceSpec{
				Type: "dockerImage",
				DockerImage: &appdefinitionv1.DockerImageSource{
					Containers: []appdefinitionv1.ContainerSpec{
						{
							Name:  "web",
							Image: "nginx:latest",
							Ports: []appdefinitionv1.PortSpec{
								{
									Name:          "http",
									ContainerPort: 80,
									ServicePort:   80,
									Protocol:      "TCP",
									Expose:        true,
								},
							},
						},
					},
				},
			},
		}

		BeforeEach(func() {
			appDef := &appdefinitionv1.AppDefinition{}
			err := k8sClient.Get(ctx, namespacedName, appDef)
			if err != nil && errors.IsNotFound(err) {
				Expect(k8sClient.Create(ctx, &appdefinitionv1.AppDefinition{
					ObjectMeta: metav1.ObjectMeta{
						Name:      resourceName,
						Namespace: namespace,
					},
					Spec: minimalSpec,
				})).To(Succeed())
			}
		})

		AfterEach(func() {
			appDef := &appdefinitionv1.AppDefinition{}
			if err := k8sClient.Get(ctx, namespacedName, appDef); err == nil {
				Expect(k8sClient.Delete(ctx, appDef)).To(Succeed())
			}
		})

		// reconcileTwice runs two reconcile passes: the first adds the finalizer,
		// the second performs the actual resource reconciliation.
		reconcileTwice := func(r *AppDefinitionReconciler, nn types.NamespacedName) {
			_, err := r.Reconcile(ctx, reconcile.Request{NamespacedName: nn})
			Expect(err).NotTo(HaveOccurred())
			_, err = r.Reconcile(ctx, reconcile.Request{NamespacedName: nn})
			Expect(err).NotTo(HaveOccurred())
		}

		It("should reconcile without error", func() {
			r := &AppDefinitionReconciler{Client: k8sClient, Scheme: k8sClient.Scheme()}
			reconcileTwice(r, namespacedName)
		})

		It("should create a Deployment", func() {
			r := &AppDefinitionReconciler{Client: k8sClient, Scheme: k8sClient.Scheme()}
			reconcileTwice(r, namespacedName)

			deployment := &appsv1.Deployment{}
			Expect(k8sClient.Get(ctx, namespacedName, deployment)).To(Succeed())
			Expect(deployment.Spec.Template.Spec.Containers).To(HaveLen(1))
			Expect(deployment.Spec.Template.Spec.Containers[0].Image).To(Equal("nginx:latest"))
		})

		It("should create a Service with exposed ports", func() {
			r := &AppDefinitionReconciler{Client: k8sClient, Scheme: k8sClient.Scheme()}
			reconcileTwice(r, namespacedName)

			service := &corev1.Service{}
			Expect(k8sClient.Get(ctx, namespacedName, service)).To(Succeed())
			Expect(service.Spec.Ports).To(HaveLen(1))
			Expect(service.Spec.Ports[0].Name).To(Equal("http"))
			Expect(service.Spec.Ports[0].Port).To(Equal(int32(80)))
		})

		It("should set owner references on child resources", func() {
			r := &AppDefinitionReconciler{Client: k8sClient, Scheme: k8sClient.Scheme()}
			reconcileTwice(r, namespacedName)

			deployment := &appsv1.Deployment{}
			Expect(k8sClient.Get(ctx, namespacedName, deployment)).To(Succeed())
			Expect(deployment.OwnerReferences).To(HaveLen(1))
			Expect(deployment.OwnerReferences[0].Name).To(Equal(resourceName))
		})

		It("should add a finalizer on the first reconcile", func() {
			r := &AppDefinitionReconciler{Client: k8sClient, Scheme: k8sClient.Scheme()}
			_, err := r.Reconcile(ctx, reconcile.Request{NamespacedName: namespacedName})
			Expect(err).NotTo(HaveOccurred())

			appDef := &appdefinitionv1.AppDefinition{}
			Expect(k8sClient.Get(ctx, namespacedName, appDef)).To(Succeed())
			Expect(appDef.Finalizers).To(ContainElement(finalizer))
		})

		It("should skip resource creation when paused", func() {
			pausedName := types.NamespacedName{Name: "paused-app", Namespace: namespace}
			pausedSpec := minimalSpec
			pausedSpec.Paused = true
			paused := &appdefinitionv1.AppDefinition{
				ObjectMeta: metav1.ObjectMeta{Name: "paused-app", Namespace: namespace},
				Spec:       pausedSpec,
			}
			Expect(k8sClient.Create(ctx, paused)).To(Succeed())
			DeferCleanup(func() { _ = k8sClient.Delete(ctx, paused) })

			r := &AppDefinitionReconciler{Client: k8sClient, Scheme: k8sClient.Scheme()}
			// Two reconcile calls: first adds finalizer, second hits Paused guard.
			reconcileTwice(r, pausedName)

			deployment := &appsv1.Deployment{}
			err := k8sClient.Get(ctx, pausedName, deployment)
			Expect(errors.IsNotFound(err)).To(BeTrue())
		})
	})
})
