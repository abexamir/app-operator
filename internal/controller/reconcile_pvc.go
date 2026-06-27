package controller

import (
	"context"
	"fmt"

	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/api/resource"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/controller/controllerutil"
	"sigs.k8s.io/controller-runtime/pkg/log"

	v1 "github.com/abexamir/app-operator/api/v1"
)

func (r *AppDefinitionReconciler) reconcilePVC(ctx context.Context, appDef *v1.AppDefinition) error {
	logger := log.FromContext(ctx)

	// When protect is set, skip creation but still allow annotation updates and size expansion
	// on an existing PVC (e.g. triggered alongside a migration).
	if appDef.Spec.Disk.Protect {
		existing := &corev1.PersistentVolumeClaim{}
		if err := r.Get(ctx, types.NamespacedName{Name: pvcName(appDef.Name), Namespace: appDef.Namespace}, existing); err != nil {
			if apierrors.IsNotFound(err) {
				logger.V(1).Info("disk.protect is true and PVC is absent, skipping creation")
				r.Recorder.Event(appDef, corev1.EventTypeWarning, "DiskProtected",
					fmt.Sprintf("PVC %s not found; disk.protect is true, operator will not recreate it", pvcName(appDef.Name)))
				return nil
			}
			return fmt.Errorf("getting PVC: %w", err)
		}
		// PVC exists — fall through to normal reconcile so annotations and size are kept in sync.
	}

	pvc := &corev1.PersistentVolumeClaim{
		ObjectMeta: metav1.ObjectMeta{
			Name:      pvcName(appDef.Name),
			Namespace: appDef.Namespace,
		},
	}

	op, err := ctrl.CreateOrUpdate(ctx, r.Client, pvc, func() error {
		pvc.Labels = standardLabels(appDef.Name)

		// Merge user-supplied annotations without clobbering annotations set by
		// the PVC controller (e.g. pv.kubernetes.io/bind-completed).
		if len(appDef.Spec.Disk.Annotations) > 0 {
			if pvc.Annotations == nil {
				pvc.Annotations = make(map[string]string)
			}
			for k, v := range appDef.Spec.Disk.Annotations {
				pvc.Annotations[k] = v
			}
		}

		requested := resource.MustParse(fmt.Sprintf("%dGi", appDef.Spec.Disk.SizeInGi))

		if pvc.ResourceVersion == "" {
			// New PVC: set full spec.
			storageClass := appDef.Spec.Disk.StorageClassName
			pvc.Spec = corev1.PersistentVolumeClaimSpec{
				AccessModes: []corev1.PersistentVolumeAccessMode{corev1.ReadWriteOnce},
				Resources: corev1.VolumeResourceRequirements{
					Requests: corev1.ResourceList{
						corev1.ResourceStorage: requested,
					},
				},
				StorageClassName: &storageClass,
			}
		} else {
			// Existing PVC: attempt expansion if size increased.
			// Shrinks are rejected by the Kubernetes API; silently ignored here.
			current := pvc.Spec.Resources.Requests[corev1.ResourceStorage]
			if requested.Cmp(current) > 0 {
				pvc.Spec.Resources.Requests[corev1.ResourceStorage] = requested
			}
		}

		return ctrl.SetControllerReference(appDef, pvc, r.Scheme)
	})

	if err != nil {
		return fmt.Errorf("failed to reconcile PVC: %w", err)
	}
	if op != controllerutil.OperationResultNone {
		logger.Info("PVC reconciled", "operation", op)
	}
	return nil
}
