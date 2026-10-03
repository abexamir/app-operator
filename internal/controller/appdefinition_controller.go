package controller

import (
	"context"
	"fmt"
	"strings"
	"time"

	appsv1 "k8s.io/api/apps/v1"
	autoscalingv2 "k8s.io/api/autoscaling/v2"
	corev1 "k8s.io/api/core/v1"
	networkingv1 "k8s.io/api/networking/v1"
	apimeta "k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/client-go/tools/record"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/builder"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/controller/controllerutil"
	"sigs.k8s.io/controller-runtime/pkg/event"
	"sigs.k8s.io/controller-runtime/pkg/handler"
	"sigs.k8s.io/controller-runtime/pkg/log"
	"sigs.k8s.io/controller-runtime/pkg/predicate"
	"sigs.k8s.io/controller-runtime/pkg/reconcile"

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
// +kubebuilder:rbac:groups=core,resources=serviceaccounts,verbs=get;list;watch;create
// +kubebuilder:rbac:groups=monitoring.coreos.com,resources=servicemonitors,verbs=get;list;watch;create;update;patch;delete
// +kubebuilder:rbac:groups=external-secrets.io,resources=externalsecrets,verbs=get;list;watch;create;update;patch;delete
// +kubebuilder:rbac:groups=external-secrets.io,resources=secretstores,verbs=get;list;watch;create;patch
// +kubebuilder:rbac:groups=traefik.io,resources=middlewares,verbs=get;list;watch;create;update;patch;delete
// +kubebuilder:rbac:groups=traefik.containo.us,resources=middlewares,verbs=get;list;watch;create;update;patch;delete

func (r *AppDefinitionReconciler) Reconcile(ctx context.Context, req ctrl.Request) (ctrl.Result, error) {
	logger := log.FromContext(ctx)

	appDef := &v1.AppDefinition{}
	if err := r.Get(ctx, req.NamespacedName, appDef); err != nil {
		if client.IgnoreNotFound(err) == nil {
			forgetAppMetrics(req.Namespace, req.Name)
		}
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
	externalSecretsPending, reconcileErr := r.reconcileAll(ctx, appDef)

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
	if externalSecretsPending {
		return ctrl.Result{RequeueAfter: externalSecretsRetryInterval}, nil
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

// reconcileAll runs every reconcile step in order. The returned bool reports that a SecretStore
// or ExternalSecret is not Ready yet and the caller should requeue to retry it.
func (r *AppDefinitionReconciler) reconcileAll(ctx context.Context, appDef *v1.AppDefinition) (bool, error) {
	if appDef.Spec.Paused {
		logger := log.FromContext(ctx)
		logger.Info("AppDefinition is paused, skipping reconciliation")
		return false, nil
	}

	externalSecretsPending := false
	if err := observeReconcileStep("configmaps", func() error { return r.reconcileConfigMaps(ctx, appDef) }); err != nil {
		return false, err
	}
	if err := observeReconcileStep("secrets", func() error { return r.reconcileSecrets(ctx, appDef) }); err != nil {
		return false, err
	}
	if err := observeReconcileStep("default_secret_store", func() error { return r.reconcileDefaultSecretStore(ctx, appDef) }); err != nil {
		return false, err
	}
	if err := observeReconcileStep("per_app_secret_store", func() error { return r.reconcilePerAppSecretStore(ctx, appDef) }); err != nil {
		return false, err
	}
	if err := observeReconcileStep("external_secrets", func() error { return r.reconcileExternalSecrets(ctx, appDef) }); err != nil {
		return false, err
	}
	if err := observeReconcileStep("external_secrets_retry", func() error {
		var retryErr error
		externalSecretsPending, retryErr = r.retryFailedExternalSecrets(ctx, appDef)
		return retryErr
	}); err != nil {
		return false, err
	}
	if err := observeReconcileStep("deployment", func() error { return r.reconcileDeployment(ctx, appDef) }); err != nil {
		return false, err
	}
	if err := observeReconcileStep("service", func() error { return r.reconcileService(ctx, appDef) }); err != nil {
		return false, err
	}
	if appDef.Spec.Disk != nil {
		if err := observeReconcileStep("pvc", func() error { return r.reconcilePVC(ctx, appDef) }); err != nil {
			return false, err
		}
	}
	if err := observeReconcileStep("middlewares", func() error { return r.reconcileMiddlewares(ctx, appDef) }); err != nil {
		return false, err
	}
	if err := observeReconcileStep("ingress", func() error { return r.reconcileIngress(ctx, appDef) }); err != nil {
		return false, err
	}
	if err := observeReconcileStep("hpa", func() error { return r.reconcileHPA(ctx, appDef) }); err != nil {
		return false, err
	}
	if err := observeReconcileStep("service_monitor", func() error { return r.reconcileServiceMonitor(ctx, appDef) }); err != nil {
		return false, err
	}
	return externalSecretsPending, nil
}

func (r *AppDefinitionReconciler) SetupWithManager(mgr ctrl.Manager) error {
	b := ctrl.NewControllerManagedBy(mgr).
		For(&v1.AppDefinition{}).
		Owns(&appsv1.Deployment{}).
		Owns(&corev1.Service{}).
		Owns(&corev1.ConfigMap{}).
		Owns(&networkingv1.Ingress{}).
		Owns(&corev1.PersistentVolumeClaim{}).
		Owns(&autoscalingv2.HorizontalPodAutoscaler{}).
		// Label-based rather than Owns(&corev1.Secret{}): inline-managed Secrets carry our
		// standard labels and are owned directly, but ExternalSecret-produced Secrets are
		// owned by the ExternalSecret (ESO's creationPolicy: Owner sets that reference, not
		// this controller), so an owner-based watch would silently miss them. This is what
		// lets ESO syncing a new secret version trigger a Deployment rollout — see
		// externalSecretHash in reconcile_deployment.go — instead of requiring a manual
		// restart or waiting on an unrelated trigger.
		Watches(&corev1.Secret{}, handler.EnqueueRequestsFromMapFunc(mapManagedSecretToAppDefinition)).
		// The default-store ServiceAccount isn't owned by any AppDefinition, so this maps
		// both it and per-app ServiceAccounts back to their AppDefinitions for recreation.
		Watches(&corev1.ServiceAccount{},
			handler.EnqueueRequestsFromMapFunc(r.mapSecretStoreDependencyToAppDefinitions),
			builder.WithPredicates(esoDependencyPredicate))

	// SecretStore and ExternalSecret are optional CRDs, so they're only watched when installed
	// at startup. Installing ESO later requires a controller restart to pick these watches up.
	for _, resolve := range []func(apimeta.RESTMapper) (schema.GroupVersionKind, error){
		resolveSecretStoreGVK, resolveExternalSecretGVK,
	} {
		gvk, err := resolve(mgr.GetRESTMapper())
		if apimeta.IsNoMatchError(err) {
			continue
		}
		if err != nil {
			return fmt.Errorf("resolving external-secrets.io API version: %w", err)
		}
		obj := &unstructured.Unstructured{}
		obj.SetGroupVersionKind(gvk)
		b = b.Watches(obj,
			handler.EnqueueRequestsFromMapFunc(r.mapSecretStoreDependencyToAppDefinitions),
			builder.WithPredicates(esoDependencyPredicate))
	}
	return b.Complete(r)
}

// esoDependencyPredicate passes deletions (so the reconciler recreates the object), spec edits
// (to revert drift), and changes to ESO's Ready condition (so a recovered store immediately
// unblocks its ExternalSecrets). It drops creates and status-only churn like ESO's per-sync
// refreshTime updates.
var esoDependencyPredicate = predicate.Funcs{
	CreateFunc:  func(event.CreateEvent) bool { return false },
	DeleteFunc:  func(event.DeleteEvent) bool { return true },
	GenericFunc: func(event.GenericEvent) bool { return false },
	UpdateFunc: func(e event.UpdateEvent) bool {
		if e.ObjectOld.GetGeneration() != e.ObjectNew.GetGeneration() {
			return true
		}
		oldU, oldOK := e.ObjectOld.(*unstructured.Unstructured)
		newU, newOK := e.ObjectNew.(*unstructured.Unstructured)
		if !oldOK || !newOK {
			return false
		}
		oldKnown, oldReady := esoReadyStatus(oldU)
		newKnown, newReady := esoReadyStatus(newU)
		return oldKnown != newKnown || oldReady != newReady
	},
}

// mapSecretStoreDependencyToAppDefinitions maps a SecretStore, ExternalSecret or ServiceAccount
// to the AppDefinitions that need it. Per-app stores, their ServiceAccounts and ExternalSecrets
// are controlled by one AppDefinition. The namespace's default store and its ServiceAccount
// (named after the namespace) are shared, so every AppDefinition in the namespace that uses the
// default store is enqueued.
func (r *AppDefinitionReconciler) mapSecretStoreDependencyToAppDefinitions(ctx context.Context, obj client.Object) []reconcile.Request {
	if owner := metav1.GetControllerOf(obj); owner != nil {
		if owner.Kind == "AppDefinition" && strings.HasPrefix(owner.APIVersion, v1.GroupVersion.Group+"/") {
			return []reconcile.Request{{NamespacedName: types.NamespacedName{Name: owner.Name, Namespace: obj.GetNamespace()}}}
		}
		return nil
	}

	isDefaultStore := obj.GetObjectKind().GroupVersionKind().Kind == secretStoreGroupKind.Kind &&
		obj.GetName() == defaultSecretStoreName
	_, isServiceAccount := obj.(*corev1.ServiceAccount)
	isDefaultServiceAccount := isServiceAccount && obj.GetName() == obj.GetNamespace()
	if !isDefaultStore && !isDefaultServiceAccount {
		return nil
	}

	apps := &v1.AppDefinitionList{}
	if err := r.List(ctx, apps, client.InNamespace(obj.GetNamespace())); err != nil {
		log.FromContext(ctx).Error(err, "listing AppDefinitions for default SecretStore dependency", "namespace", obj.GetNamespace())
		return nil
	}
	var requests []reconcile.Request
	for i := range apps.Items {
		if usesDefaultSecretStore(&apps.Items[i]) {
			requests = append(requests, reconcile.Request{NamespacedName: client.ObjectKeyFromObject(&apps.Items[i])})
		}
	}
	return requests
}

// mapManagedSecretToAppDefinition enqueues a reconcile of the AppDefinition named by a
// Secret's app.kubernetes.io/instance label, for any Secret carrying this operator's
// standard "managed-by" label (set on both inline-managed Secrets and ExternalSecret
// target Secrets — see standardLabels and reconcile_externalsecrets.go).
func mapManagedSecretToAppDefinition(_ context.Context, obj client.Object) []reconcile.Request {
	labels := obj.GetLabels()
	if labels["app.kubernetes.io/managed-by"] != "app-operator" {
		return nil
	}
	name := labels["app.kubernetes.io/instance"]
	if name == "" {
		return nil
	}
	return []reconcile.Request{{NamespacedName: types.NamespacedName{Name: name, Namespace: obj.GetNamespace()}}}
}
