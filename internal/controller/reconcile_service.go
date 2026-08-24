package controller

import (
	"context"
	"fmt"

	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/apimachinery/pkg/util/intstr"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/controller/controllerutil"
	"sigs.k8s.io/controller-runtime/pkg/log"

	v1 "github.com/abexamir/app-operator/api/v1"
)

// exposedPorts collects every container port with expose: true across spec.containers.
func exposedPorts(appDef *v1.AppDefinition) []v1.PortSpec {
	var ports []v1.PortSpec
	for _, container := range appDef.Spec.Containers {
		for _, port := range container.Ports {
			if port.Expose {
				ports = append(ports, port)
			}
		}
	}
	return ports
}

// reconcileService creates/updates the Service for apps with at least one exposed port. Worker-
// style AppDefinitions with none (e.g. a Celery/queue consumer with no HTTP server) previously
// still got a Service reconcile attempt with an empty ports list, which Kubernetes rejects
// ("spec.ports: Required value") — that failure surfaced as the whole AppDefinition sitting in
// phase Failed forever, even though the Deployment/pods themselves were healthy. Skipping
// (and cleaning up any stale Service left over from a spec change that removed all exposed
// ports) avoids that invalid-but-inevitable API call entirely.
func (r *AppDefinitionReconciler) reconcileService(ctx context.Context, appDef *v1.AppDefinition) error {
	logger := log.FromContext(ctx)
	ports := exposedPorts(appDef)

	if len(ports) == 0 {
		existing := &corev1.Service{}
		key := types.NamespacedName{Name: appDef.Name, Namespace: appDef.Namespace}
		if err := r.Get(ctx, key, existing); err != nil {
			if apierrors.IsNotFound(err) {
				return nil
			}
			return fmt.Errorf("checking for stale Service: %w", err)
		}
		logger.Info("deleting Service, no ports exposed", "name", appDef.Name)
		if err := r.Delete(ctx, existing); err != nil && !apierrors.IsNotFound(err) {
			return fmt.Errorf("deleting stale Service: %w", err)
		}
		recordManagedResourcePrune("Service")
		return nil
	}

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
		for _, port := range ports {
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
