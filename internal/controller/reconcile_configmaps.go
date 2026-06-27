package controller

import (
	"context"
	"fmt"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/controller/controllerutil"
	"sigs.k8s.io/controller-runtime/pkg/log"

	v1 "github.com/abexamir/app-operator/api/v1"
)

func (r *AppDefinitionReconciler) reconcileConfigMaps(ctx context.Context, appDef *v1.AppDefinition) error {
	logger := log.FromContext(ctx)
	for _, cm := range appDef.Spec.ConfigMaps {
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
	return nil
}
