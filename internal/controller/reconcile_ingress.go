package controller

import (
	"context"
	"fmt"
	"strings"

	networkingv1 "k8s.io/api/networking/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/controller/controllerutil"
	"sigs.k8s.io/controller-runtime/pkg/log"

	v1 "github.com/abexamir/app-operator/api/v1"
)

// reconcileIngress creates or updates one Ingress object per domain, rather than one shared
// Ingress with a rule per domain — see domainIngressName's doc comment for why: Traefik's
// router.middlewares annotation applies to every rule in an Ingress object, so a domain's
// middlewares (see reconcileMiddlewares) can only be scoped correctly when each domain has its
// own Ingress.
func (r *AppDefinitionReconciler) reconcileIngress(ctx context.Context, appDef *v1.AppDefinition) error {
	logger := log.FromContext(ctx)
	pathType := networkingv1.PathTypePrefix

	desiredNames := make(map[string]struct{}, len(appDef.Spec.Domains))

	for _, domain := range appDef.Spec.Domains {
		name := domainIngressName(appDef.Name, domain.Name)
		desiredNames[name] = struct{}{}

		ingress := &networkingv1.Ingress{
			ObjectMeta: metav1.ObjectMeta{
				Name:      name,
				Namespace: appDef.Namespace,
			},
		}

		op, err := ctrl.CreateOrUpdate(ctx, r.Client, ingress, func() error {
			ingress.Labels = standardLabels(appDef.Name)

			// Merge global annotations, then this domain's own — domain annotations can
			// override global ones for this domain's Ingress only, now that each domain has
			// its own Ingress object.
			annotations := make(map[string]string, len(appDef.Spec.IngressAnnotations)+len(domain.Annotations))
			for k, v := range appDef.Spec.IngressAnnotations {
				annotations[k] = v
			}
			for k, v := range domain.Annotations {
				annotations[k] = v
			}
			if domain.TLS && domain.CertIssuer != "" {
				annotations["cert-manager.io/cluster-issuer"] = domain.CertIssuer
			}
			if kinds := enabledMiddlewareKinds(domain); len(kinds) > 0 {
				refs := make([]string, len(kinds))
				for i, kind := range kinds {
					refs[i] = fmt.Sprintf("%s-%s@kubernetescrd", appDef.Namespace, middlewareName(appDef.Name, domain.Name, kind))
				}
				annotations["traefik.ingress.kubernetes.io/router.middlewares"] = strings.Join(refs, ",")
			}
			ingress.Annotations = annotations

			// Always assign IngressClassName so that clearing the field removes it from the Ingress.
			if appDef.Spec.IngressClass != "" {
				ingress.Spec.IngressClassName = &appDef.Spec.IngressClass
			} else {
				ingress.Spec.IngressClassName = nil
			}

			ingress.Spec.TLS = nil
			if domain.TLS {
				secretName := domain.SecretName
				if secretName == "" {
					secretName = tlsSecretName(appDef.Name, domain.Name)
				}
				ingress.Spec.TLS = []networkingv1.IngressTLS{{
					Hosts:      []string{domain.Name},
					SecretName: secretName,
				}}
			}

			portName := domain.PortName
			if portName == "" {
				portName = "http"
			}
			path := domain.Path
			if path == "" {
				path = "/"
			}
			ingress.Spec.Rules = []networkingv1.IngressRule{{
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
			}}

			return ctrl.SetControllerReference(appDef, ingress, r.Scheme)
		})

		if err != nil {
			return fmt.Errorf("failed to reconcile Ingress %s: %w", name, err)
		}
		if op != controllerutil.OperationResultNone {
			logger.Info("Ingress reconciled", "name", name, "operation", op)
		}
	}

	return r.pruneIngresses(ctx, appDef, desiredNames)
}

func (r *AppDefinitionReconciler) pruneIngresses(ctx context.Context, appDef *v1.AppDefinition, desiredNames map[string]struct{}) error {
	logger := log.FromContext(ctx)
	list := &networkingv1.IngressList{}
	if err := r.List(ctx, list,
		client.InNamespace(appDef.Namespace),
		client.MatchingLabels(standardLabels(appDef.Name)),
	); err != nil {
		return fmt.Errorf("listing managed Ingresses for pruning: %w", err)
	}

	for i := range list.Items {
		ing := &list.Items[i]
		if !metav1.IsControlledBy(ing, appDef) {
			continue
		}
		if _, keep := desiredNames[ing.Name]; keep {
			continue
		}
		logger.Info("deleting stale Ingress", "name", ing.Name)
		if err := r.Delete(ctx, ing); err != nil && !apierrors.IsNotFound(err) {
			return fmt.Errorf("deleting stale Ingress %s: %w", ing.Name, err)
		}
		recordManagedResourcePrune("Ingress")
	}
	return nil
}
