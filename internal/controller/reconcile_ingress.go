package controller

import (
	"context"
	"fmt"

	networkingv1 "k8s.io/api/networking/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/controller/controllerutil"
	"sigs.k8s.io/controller-runtime/pkg/log"

	v1 "github.com/abexamir/app-operator/api/v1"
)

func (r *AppDefinitionReconciler) reconcileIngress(ctx context.Context, appDef *v1.AppDefinition) error {
	logger := log.FromContext(ctx)

	ingress := &networkingv1.Ingress{
		ObjectMeta: metav1.ObjectMeta{
			Name:      appDef.Name,
			Namespace: appDef.Namespace,
		},
	}

	op, err := ctrl.CreateOrUpdate(ctx, r.Client, ingress, func() error {
		ingress.Labels = standardLabels(appDef.Name)

		// Merge global ingress annotations.
		ingress.Annotations = make(map[string]string)
		for k, v := range appDef.Spec.IngressAnnotations {
			ingress.Annotations[k] = v
		}

		// Always assign IngressClassName so that clearing the field removes it from the Ingress.
		if appDef.Spec.IngressClass != "" {
			ingress.Spec.IngressClassName = &appDef.Spec.IngressClass
		} else {
			ingress.Spec.IngressClassName = nil
		}

		// Merge per-domain annotations over all domains (TLS and non-TLS).
		// Done before the TLS/rules loops so domain annotations can be overridden
		// by cert-manager issuer annotations set in the TLS pass below.
		for _, domain := range appDef.Spec.Domains {
			if domain.TLS && domain.RedirectTLS {
				ingress.Annotations["nginx.ingress.kubernetes.io/force-ssl-redirect"] = "true"
			}
			for k, v := range domain.Annotations {
				ingress.Annotations[k] = v
			}
		}

		// Build TLS blocks — one entry per TLS-enabled domain with its own secret.
		ingress.Spec.TLS = nil
		for _, domain := range appDef.Spec.Domains {
			if !domain.TLS {
				continue
			}
			secretName := domain.SecretName
			if secretName == "" {
				secretName = tlsSecretName(appDef.Name, domain.Name)
			}
			ingress.Spec.TLS = append(ingress.Spec.TLS, networkingv1.IngressTLS{
				Hosts:      []string{domain.Name},
				SecretName: secretName,
			})
			// Per-domain cert-manager issuer annotation.
			if domain.CertIssuer != "" {
				ingress.Annotations["cert-manager.io/cluster-issuer"] = domain.CertIssuer
			}
		}

		// Build rules.
		pathType := networkingv1.PathTypePrefix
		ingress.Spec.Rules = nil
		for _, domain := range appDef.Spec.Domains {
			portName := domain.PortName
			if portName == "" {
				portName = "http"
			}
			path := domain.Path
			if path == "" {
				path = "/"
			}
			ingress.Spec.Rules = append(ingress.Spec.Rules, networkingv1.IngressRule{
				Host: domain.Name,
				IngressRuleValue: networkingv1.IngressRuleValue{
					HTTP: &networkingv1.HTTPIngressRuleValue{
						Paths: []networkingv1.HTTPIngressPath{
							{
								Path:     path,
								PathType: &pathType,
								Backend: networkingv1.IngressBackend{
									Service: &networkingv1.IngressServiceBackend{
										Name: appDef.Name,
										Port: networkingv1.ServiceBackendPort{Name: portName},
									},
								},
							},
						},
					},
				},
			})
		}

		return ctrl.SetControllerReference(appDef, ingress, r.Scheme)
	})

	if err != nil {
		return fmt.Errorf("failed to reconcile Ingress: %w", err)
	}
	if op != controllerutil.OperationResultNone {
		logger.Info("Ingress reconciled", "operation", op)
	}
	return nil
}
