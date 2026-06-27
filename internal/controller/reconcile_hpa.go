package controller

import (
	"context"
	"fmt"

	autoscalingv2 "k8s.io/api/autoscaling/v2"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/controller/controllerutil"
	"sigs.k8s.io/controller-runtime/pkg/log"

	v1 "github.com/abexamir/app-operator/api/v1"
)

func (r *AppDefinitionReconciler) reconcileHPA(ctx context.Context, appDef *v1.AppDefinition) error {
	logger := log.FromContext(ctx)

	hpa := &autoscalingv2.HorizontalPodAutoscaler{
		ObjectMeta: metav1.ObjectMeta{
			Name:      appDef.Name,
			Namespace: appDef.Namespace,
		},
	}

	if appDef.Spec.Autoscaling == nil || !appDef.Spec.Autoscaling.Enabled {
		if err := r.Delete(ctx, hpa); client.IgnoreNotFound(err) != nil {
			return fmt.Errorf("failed to delete HPA: %w", err)
		}
		return nil
	}

	op, err := ctrl.CreateOrUpdate(ctx, r.Client, hpa, func() error {
		hpa.Labels = standardLabels(appDef.Name)

		as := appDef.Spec.Autoscaling
		hpa.Spec = autoscalingv2.HorizontalPodAutoscalerSpec{
			ScaleTargetRef: autoscalingv2.CrossVersionObjectReference{
				APIVersion: "apps/v1",
				Kind:       "Deployment",
				Name:       appDef.Name,
			},
			MinReplicas: as.MinReplicas,
			MaxReplicas: as.MaxReplicas,
		}

		hpa.Spec.Metrics = nil
		if as.TargetCPUUtilizationPercentage != nil {
			hpa.Spec.Metrics = append(hpa.Spec.Metrics, autoscalingv2.MetricSpec{
				Type: autoscalingv2.ResourceMetricSourceType,
				Resource: &autoscalingv2.ResourceMetricSource{
					Name: corev1.ResourceCPU,
					Target: autoscalingv2.MetricTarget{
						Type:               autoscalingv2.UtilizationMetricType,
						AverageUtilization: as.TargetCPUUtilizationPercentage,
					},
				},
			})
		}
		if as.TargetMemoryUtilizationPercentage != nil {
			hpa.Spec.Metrics = append(hpa.Spec.Metrics, autoscalingv2.MetricSpec{
				Type: autoscalingv2.ResourceMetricSourceType,
				Resource: &autoscalingv2.ResourceMetricSource{
					Name: corev1.ResourceMemory,
					Target: autoscalingv2.MetricTarget{
						Type:               autoscalingv2.UtilizationMetricType,
						AverageUtilization: as.TargetMemoryUtilizationPercentage,
					},
				},
			})
		}

		return ctrl.SetControllerReference(appDef, hpa, r.Scheme)
	})

	if err != nil {
		return fmt.Errorf("failed to reconcile HPA: %w", err)
	}
	if op != controllerutil.OperationResultNone {
		logger.Info("HPA reconciled", "operation", op)
	}
	return nil
}
