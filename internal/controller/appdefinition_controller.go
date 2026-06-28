package controller

import (
	"context"
	"fmt"
	"time"

	appsv1 "k8s.io/api/apps/v1"
	autoscalingv2 "k8s.io/api/autoscaling/v2"
	corev1 "k8s.io/api/core/v1"
	networkingv1 "k8s.io/api/networking/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/client-go/tools/record"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/controller/controllerutil"
	"sigs.k8s.io/controller-runtime/pkg/log"

	v1 "github.com/abexamir/app-operator/api/v1"
)

const finalizer = "appdefinition.abexamir.me/finalizer"

// AppDefinitionReconciler reconciles a AppDefinition object.
type AppDefinitionReconciler struct {
	client.Client
	// APIReader bypasses the informer cache and reads directly from the API server.
	// Used for resources whose status is updated by external controllers (PVC resize),
	// where the cache can temporarily hold a stale intermediate value.
	APIReader client.Reader
	Scheme    *runtime.Scheme
	Recorder  record.EventRecorder
}

// +kubebuilder:rbac:groups=appdefinition.abexamir.me,resources=appdefinitions,verbs=get;list;watch;create;update;patch;delete
// +kubebuilder:rbac:groups=appdefinition.abexamir.me,resources=appdefinitions/status,verbs=get;update;patch
// +kubebuilder:rbac:groups=appdefinition.abexamir.me,resources=appdefinitions/finalizers,verbs=update
// +kubebuilder:rbac:groups=apps,resources=deployments,verbs=get;list;watch;create;update;patch;delete
// +kubebuilder:rbac:groups=core,resources=services,verbs=get;list;watch;create;update;patch;delete
// +kubebuilder:rbac:groups=core,resources=pods,verbs=get;list;watch
// +kubebuilder:rbac:groups=networking.k8s.io,resources=ingresses,verbs=get;list;watch;create;update;patch;delete
// +kubebuilder:rbac:groups=core,resources=persistentvolumeclaims,verbs=get;list;watch;create;update;patch;delete
// +kubebuilder:rbac:groups=autoscaling,resources=horizontalpodautoscalers,verbs=get;list;watch;create;update;patch;delete
// +kubebuilder:rbac:groups=core,resources=configmaps,verbs=get;list;watch;create;update;patch;delete
// +kubebuilder:rbac:groups=core,resources=secrets,verbs=get;list;watch;create;update;patch;delete
// +kubebuilder:rbac:groups=monitoring.coreos.com,resources=servicemonitors,verbs=get;list;watch;create;update;patch;delete
// +kubebuilder:rbac:groups=external-secrets.io,resources=externalsecrets,verbs=get;list;watch;create;update;patch;delete

func (r *AppDefinitionReconciler) Reconcile(ctx context.Context, req ctrl.Request) (ctrl.Result, error) {
	logger := log.FromContext(ctx)

	appDef := &v1.AppDefinition{}
	if err := r.Get(ctx, req.NamespacedName, appDef); err != nil {
		return ctrl.Result{}, client.IgnoreNotFound(err)
	}

	// Register finalizer on first encounter.
	if !controllerutil.ContainsFinalizer(appDef, finalizer) {
		controllerutil.AddFinalizer(appDef, finalizer)
		if err := r.Update(ctx, appDef); err != nil {
			return ctrl.Result{}, fmt.Errorf("failed to add finalizer: %w", err)
		}
		return ctrl.Result{Requeue: true}, nil
	}

	// Handle deletion.
	if !appDef.DeletionTimestamp.IsZero() {
		return r.handleDeletion(ctx, appDef)
	}

	// Run reconciliation and always update status afterwards.
	reconcileErr := r.reconcileAll(ctx, appDef)

	if statusErr := r.updateStatus(ctx, appDef, reconcileErr); statusErr != nil {
		logger.Error(statusErr, "Failed to update status")
		if reconcileErr == nil {
			return ctrl.Result{}, statusErr
		}
	}

	if reconcileErr != nil {
		return ctrl.Result{}, reconcileErr
	}

	logger.Info("Reconciliation complete")
	if appDef.Spec.Paused {
		return ctrl.Result{}, nil
	}
	fresh := &v1.AppDefinition{}
	if err := r.Get(ctx, client.ObjectKeyFromObject(appDef), fresh); err == nil && fresh.Status.Phase == "Available" {
		return ctrl.Result{}, nil
	}
	return ctrl.Result{RequeueAfter: 30 * time.Second}, nil
}

func (r *AppDefinitionReconciler) handleDeletion(ctx context.Context, appDef *v1.AppDefinition) (ctrl.Result, error) {
	controllerutil.RemoveFinalizer(appDef, finalizer)
	if err := r.Update(ctx, appDef); err != nil {
		return ctrl.Result{}, fmt.Errorf("failed to remove finalizer: %w", err)
	}
	return ctrl.Result{}, nil
}

func (r *AppDefinitionReconciler) reconcileAll(ctx context.Context, appDef *v1.AppDefinition) error {
	if appDef.Spec.Paused {
		logger := log.FromContext(ctx)
		logger.Info("AppDefinition is paused, skipping reconciliation")
		return nil
	}

	if err := r.reconcileConfigMaps(ctx, appDef); err != nil {
		return err
	}
	if err := r.reconcileSecrets(ctx, appDef); err != nil {
		return err
	}
	if err := r.reconcileExternalSecrets(ctx, appDef); err != nil {
		return err
	}
	if err := r.reconcileDeployment(ctx, appDef); err != nil {
		return err
	}
	if err := r.reconcileService(ctx, appDef); err != nil {
		return err
	}
	if appDef.Spec.Disk != nil {
		if err := r.reconcilePVC(ctx, appDef); err != nil {
			return err
		}
	}
	if len(appDef.Spec.Domains) > 0 {
		if err := r.reconcileIngress(ctx, appDef); err != nil {
			return err
		}
	}
	if err := r.reconcileHPA(ctx, appDef); err != nil {
		return err
	}
	return r.reconcileServiceMonitor(ctx, appDef)
}

func (r *AppDefinitionReconciler) SetupWithManager(mgr ctrl.Manager) error {
	return ctrl.NewControllerManagedBy(mgr).
		For(&v1.AppDefinition{}).
		Owns(&appsv1.Deployment{}).
		Owns(&corev1.Service{}).
		Owns(&corev1.ConfigMap{}).
		Owns(&corev1.Secret{}).
		Owns(&networkingv1.Ingress{}).
		Owns(&corev1.PersistentVolumeClaim{}).
		Owns(&autoscalingv2.HorizontalPodAutoscaler{}).
		Complete(r)
}
