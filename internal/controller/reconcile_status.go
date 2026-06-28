package controller

import (
	"context"
	"fmt"
	"strings"

	appsv1 "k8s.io/api/apps/v1"
	autoscalingv2 "k8s.io/api/autoscaling/v2"
	corev1 "k8s.io/api/core/v1"
	networkingv1 "k8s.io/api/networking/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	apimeta "k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/client-go/util/retry"
	"sigs.k8s.io/controller-runtime/pkg/client"

	v1 "github.com/abexamir/app-operator/api/v1"
)

func (r *AppDefinitionReconciler) updateStatus(ctx context.Context, appDef *v1.AppDefinition, reconcileErr error) error {
	return retry.RetryOnConflict(retry.DefaultBackoff, func() error {
		return r.updateStatusOnce(ctx, appDef, reconcileErr)
	})
}

func (r *AppDefinitionReconciler) updateStatusOnce(ctx context.Context, appDef *v1.AppDefinition, reconcileErr error) error {
	desiredReplicas := int32(1)
	if appDef.Spec.Replicas != nil {
		desiredReplicas = *appDef.Spec.Replicas
	}

	// Fetch deployment to get ready replica count.
	deployment := &appsv1.Deployment{}
	deploymentFound := true
	if err := r.Get(ctx, types.NamespacedName{Name: appDef.Name, Namespace: appDef.Namespace}, deployment); err != nil {
		if client.IgnoreNotFound(err) != nil {
			return fmt.Errorf("failed to get Deployment for status: %w", err)
		}
		deploymentFound = false
	}

	var readyReplicas int32
	if deploymentFound {
		readyReplicas = deployment.Status.ReadyReplicas
	}

	now := metav1.Now()
	ready := metav1.ConditionFalse
	readyReason := "Progressing"
	readyMsg := fmt.Sprintf("%d/%d replicas ready", readyReplicas, desiredReplicas)
	phase := "Progressing"
	lastError := ""

	switch {
	case appDef.Spec.Paused:
		phase = "Paused"
		readyReason = "Paused"
		readyMsg = "Reconciliation is paused"
	case reconcileErr != nil:
		phase = "Failed"
		lastError = reconcileErr.Error()
		readyReason = "ReconcileError"
		readyMsg = reconcileErr.Error()
	case deploymentFound && readyReplicas >= desiredReplicas:
		phase = "Available"
		ready = metav1.ConditionTrue
		readyReason = "DeploymentAvailable"
	}

	// Re-fetch to get the latest resourceVersion and avoid update conflicts.
	fresh := &v1.AppDefinition{}
	if err := r.Get(ctx, types.NamespacedName{Name: appDef.Name, Namespace: appDef.Namespace}, fresh); err != nil {
		return fmt.Errorf("failed to re-fetch AppDefinition for status update: %w", err)
	}
	fresh.Status.Phase = phase
	fresh.Status.Replicas = desiredReplicas
	fresh.Status.ReadyReplicas = readyReplicas
	fresh.Status.ObservedGeneration = fresh.Generation
	fresh.Status.LastError = lastError

	apimeta.SetStatusCondition(&fresh.Status.Conditions, metav1.Condition{
		Type:               v1.ConditionTypeReady,
		Status:             ready,
		Reason:             readyReason,
		Message:            readyMsg,
		LastTransitionTime: now,
		ObservedGeneration: fresh.Generation,
	})

	// DiskReady: set when a PVC is declared; reflects PVC phase.
	if appDef.Spec.Disk != nil {
		pvc := &corev1.PersistentVolumeClaim{}
		pvcStatus := metav1.ConditionFalse
		pvcReason := "Provisioning"
		pvcMsg := "PVC is being provisioned"
		if err := r.APIReader.Get(ctx, types.NamespacedName{Name: pvcName(appDef.Name), Namespace: appDef.Namespace}, pvc); err != nil {
			if apierrors.IsNotFound(err) && appDef.Spec.Disk.Protect {
				pvcReason = "Protected"
				pvcMsg = fmt.Sprintf("PVC %s not found; disk.protect is true, operator will not recreate it", pvcName(appDef.Name))
			}
		} else {
			if pvc.Status.Phase == corev1.ClaimBound {
				pvcStatus = metav1.ConditionTrue
				pvcReason = "Bound"
				actual := pvc.Status.Capacity[corev1.ResourceStorage]
				requested := pvc.Spec.Resources.Requests[corev1.ResourceStorage]
				if actual.Cmp(requested) < 0 {
					// Expansion requested but not yet reflected by the provisioner.
					pvcMsg = fmt.Sprintf("PVC %s is bound (%s, expanding to %s)", pvcName(appDef.Name), actual.String(), requested.String())
				} else {
					pvcMsg = fmt.Sprintf("PVC %s is bound (%s)", pvcName(appDef.Name), actual.String())
				}
			} else {
				pvcMsg = fmt.Sprintf("PVC phase: %s", pvc.Status.Phase)
			}
		}
		apimeta.SetStatusCondition(&fresh.Status.Conditions, metav1.Condition{
			Type:               v1.ConditionTypeDiskReady,
			Status:             pvcStatus,
			Reason:             pvcReason,
			Message:            pvcMsg,
			LastTransitionTime: now,
			ObservedGeneration: fresh.Generation,
		})
	}

	// IngressReady: set when domains are declared; reflects whether the ingress controller
	// has assigned an IP or hostname.
	if len(appDef.Spec.Domains) > 0 {
		ingress := &networkingv1.Ingress{}
		ingressStatus := metav1.ConditionFalse
		ingressReason := "Pending"
		ingressMsg := "Ingress controller has not yet assigned an address"
		if err := r.Get(ctx, types.NamespacedName{Name: appDef.Name, Namespace: appDef.Namespace}, ingress); err == nil {
			if len(ingress.Status.LoadBalancer.Ingress) > 0 {
				addrs := make([]string, 0, len(ingress.Status.LoadBalancer.Ingress))
				for _, lb := range ingress.Status.LoadBalancer.Ingress {
					if lb.IP != "" {
						addrs = append(addrs, lb.IP)
					} else if lb.Hostname != "" {
						addrs = append(addrs, lb.Hostname)
					}
				}
				ingressStatus = metav1.ConditionTrue
				ingressReason = "Assigned"
				ingressMsg = strings.Join(addrs, ", ")
			}
		}
		apimeta.SetStatusCondition(&fresh.Status.Conditions, metav1.Condition{
			Type:               v1.ConditionTypeIngressReady,
			Status:             ingressStatus,
			Reason:             ingressReason,
			Message:            ingressMsg,
			LastTransitionTime: now,
			ObservedGeneration: fresh.Generation,
		})
	}

	// HPAActive: set when autoscaling is enabled; reflects whether the HPA is operating.
	if appDef.Spec.Autoscaling != nil && appDef.Spec.Autoscaling.Enabled {
		hpa := &autoscalingv2.HorizontalPodAutoscaler{}
		hpaStatus := metav1.ConditionFalse
		hpaReason := "Creating"
		hpaMsg := "HPA is being created"
		if err := r.Get(ctx, types.NamespacedName{Name: appDef.Name, Namespace: appDef.Namespace}, hpa); err == nil {
			hpaStatus = metav1.ConditionTrue
			hpaReason = "Active"
			hpaMsg = fmt.Sprintf("scaling %d/%d replicas (min %d, max %d)",
				hpa.Status.CurrentReplicas, hpa.Status.DesiredReplicas,
				*hpa.Spec.MinReplicas, hpa.Spec.MaxReplicas)
		}
		apimeta.SetStatusCondition(&fresh.Status.Conditions, metav1.Condition{
			Type:               v1.ConditionTypeHPAActive,
			Status:             hpaStatus,
			Reason:             hpaReason,
			Message:            hpaMsg,
			LastTransitionTime: now,
			ObservedGeneration: fresh.Generation,
		})
	}

	if err := r.Status().Update(ctx, fresh); err != nil {
		return fmt.Errorf("failed to update AppDefinition status: %w", err)
	}
	return nil
}
