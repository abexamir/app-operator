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

func (r *AppDefinitionReconciler) reconcileSecrets(ctx context.Context, appDef *v1.AppDefinition) error {
	logger := log.FromContext(ctx)
	desired := make(map[string]struct{}, len(appDef.Spec.Secrets))
	for _, sec := range appDef.Spec.Secrets {
		// External secrets are referenced, not managed — skip creation.
		if sec.SecretRef != "" {
			continue
		}
		desired[appDef.Name+"-"+sec.Name] = struct{}{}
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

	list := &corev1.SecretList{}
	if err := r.List(ctx, list, client.InNamespace(appDef.Namespace), client.MatchingLabels(standardLabels(appDef.Name))); err != nil {
		return fmt.Errorf("listing managed Secrets for pruning: %w", err)
	}
	for i := range list.Items {
		obj := &list.Items[i]
		// ExternalSecret-produced Secrets have the same labels but are controlled by
		// the ExternalSecret. Only prune inline Secrets directly owned by this app.
		if !metav1.IsControlledBy(obj, appDef) {
			continue
		}
		if _, keep := desired[obj.Name]; keep {
			continue
		}
		logger.Info("deleting stale Secret", "name", obj.Name)
		if err := r.Delete(ctx, obj); client.IgnoreNotFound(err) != nil {
			return fmt.Errorf("deleting stale Secret %s: %w", obj.Name, err)
		}
	}
	return nil
}
