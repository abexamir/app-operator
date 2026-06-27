package controller

import (
	"context"
	"fmt"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/util/intstr"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/controller/controllerutil"
	"sigs.k8s.io/controller-runtime/pkg/log"

	v1 "github.com/abexamir/app-operator/api/v1"
)

func (r *AppDefinitionReconciler) reconcileService(ctx context.Context, appDef *v1.AppDefinition) error {
	logger := log.FromContext(ctx)

	service := &corev1.Service{
		ObjectMeta: metav1.ObjectMeta{
			Name:      appDef.Name,
			Namespace: appDef.Namespace,
		},
	}

	op, err := ctrl.CreateOrUpdate(ctx, r.Client, service, func() error {
		service.Labels = standardLabels(appDef.Name)

		serviceType := corev1.ServiceTypeClusterIP
		if appDef.Spec.ServiceType != "" {
			serviceType = appDef.Spec.ServiceType
		}
		service.Spec.Type = serviceType
		service.Spec.Selector = selectorLabels(appDef.Name)

		service.Spec.Ports = nil
		for _, container := range appDef.Spec.Containers {
			for _, port := range container.Ports {
				if !port.Expose {
					continue
				}
				proto := corev1.Protocol(port.Protocol)
				if proto == "" {
					proto = corev1.ProtocolTCP
				}
				service.Spec.Ports = append(service.Spec.Ports, corev1.ServicePort{
					Name:       port.Name,
					Port:       port.ServicePort,
					TargetPort: intstr.FromInt32(port.ContainerPort),
					Protocol:   proto,
				})
			}
		}

		return ctrl.SetControllerReference(appDef, service, r.Scheme)
	})

	if err != nil {
		return fmt.Errorf("failed to reconcile Service: %w", err)
	}
	if op != controllerutil.OperationResultNone {
		logger.Info("Service reconciled", "operation", op)
	}
	return nil
}
