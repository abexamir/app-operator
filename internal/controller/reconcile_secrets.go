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

func (r *AppDefinitionReconciler) reconcileSecrets(ctx context.Context, appDef *v1.AppDefinition) error {
	logger := log.FromContext(ctx)
	for _, sec := range appDef.Spec.Secrets {
		// External secrets are referenced, not managed — skip creation.
		if sec.SecretRef != "" {
			continue
		}
		obj := &corev1.Secret{
			ObjectMeta: metav1.ObjectMeta{
				Name:      appDef.Name + "-" + sec.Name,
				Namespace: appDef.Namespace,
			},
		}
		op, err := ctrl.CreateOrUpdate(ctx, r.Client, obj, func() error {
			obj.Labels = standardLabels(appDef.Name)
			obj.Type = corev1.SecretTypeOpaque
			// StringData is write-only: Kubernetes converts it to Data on storage
			// and clears it, so reading back always produces nil StringData.
			// Write to Data directly to get stable DeepEqual comparisons.
			obj.Data = make(map[string][]byte, len(sec.Data))
			for k, v := range sec.Data {
				obj.Data[k] = []byte(v)
			}
			return ctrl.SetControllerReference(appDef, obj, r.Scheme)
		})
		if err != nil {
			return fmt.Errorf("failed to reconcile Secret %s: %w", sec.Name, err)
		}
		if op != controllerutil.OperationResultNone {
			logger.Info("Secret reconciled", "name", obj.Name, "operation", op)
		}
	}
	return nil
}
