package controller

import (
	"context"
	"fmt"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/controller/controllerutil"
	"sigs.k8s.io/controller-runtime/pkg/log"

	v1 "github.com/abexamir/app-operator/api/v1"
)

func (r *AppDefinitionReconciler) reconcileConfigMaps(ctx context.Context, appDef *v1.AppDefinition) error {
	logger := log.FromContext(ctx)
	desired := make(map[string]struct{}, len(appDef.Spec.ConfigMaps))
	for _, cm := range appDef.Spec.ConfigMaps {
		desired[appDef.Name+"-"+cm.Name] = struct{}{}
		obj := &corev1.ConfigMap{
			ObjectMeta: metav1.ObjectMeta{
				Name:      appDef.Name + "-" + cm.Name,
				Namespace: appDef.Namespace,
			},
		}
		op, err := ctrl.CreateOrUpdate(ctx, r.Client, obj, func() error {
			obj.Labels = standardLabels(appDef.Name)
			obj.Data = cm.Data
			return ctrl.SetControllerReference(appDef, obj, r.Scheme)
		})
		if err != nil {
			return fmt.Errorf("failed to reconcile ConfigMap %s: %w", cm.Name, err)
		}
		if op != controllerutil.OperationResultNone {
			logger.Info("ConfigMap reconciled", "name", obj.Name, "operation", op)
		}
	}

	list := &corev1.ConfigMapList{}
	if err := r.List(ctx, list, client.InNamespace(appDef.Namespace), client.MatchingLabels(standardLabels(appDef.Name))); err != nil {
		return fmt.Errorf("listing managed ConfigMaps for pruning: %w", err)
	}
	for i := range list.Items {
		obj := &list.Items[i]
		if !metav1.IsControlledBy(obj, appDef) {
			continue
		}
		if _, keep := desired[obj.Name]; keep {
			continue
		}
		logger.Info("deleting stale ConfigMap", "name", obj.Name)
		if err := r.Delete(ctx, obj); client.IgnoreNotFound(err) != nil {
			return fmt.Errorf("deleting stale ConfigMap %s: %w", obj.Name, err)
		}
		recordManagedResourcePrune("ConfigMap")
	}
	return nil
}
