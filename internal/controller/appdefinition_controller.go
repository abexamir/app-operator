package controller

import (
	"context"
	"crypto/sha256"
	"fmt"
	"sort"
	"strings"
	"time"

	apimeta "k8s.io/apimachinery/pkg/api/meta"
	"k8s.io/apimachinery/pkg/api/resource"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/apimachinery/pkg/types"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/controller/controllerutil"
	"sigs.k8s.io/controller-runtime/pkg/log"

	appsv1 "k8s.io/api/apps/v1"
	autoscalingv2 "k8s.io/api/autoscaling/v2"
	corev1 "k8s.io/api/core/v1"
	networkingv1 "k8s.io/api/networking/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/util/intstr"

	v1 "github.com/abexamir/app-operator/api/v1"
)

const (
	finalizer            = "appdefinition.abexamir.me/finalizer"
	configHashAnnotation = "appdefinition.abexamir.me/config-hash"
)

var serviceMonitorGVK = schema.GroupVersionKind{
	Group:   "monitoring.coreos.com",
	Version: "v1",
	Kind:    "ServiceMonitor",
}

// AppDefinitionReconciler reconciles a AppDefinition object.
type AppDefinitionReconciler struct {
	client.Client
	// APIReader bypasses the informer cache and reads directly from the API server.
	// Used for resources whose status is updated by external controllers (PVC resize),
	// where the cache can temporarily hold a stale intermediate value.
	APIReader client.Reader
	Scheme    *runtime.Scheme
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

// ----------------------------------------------------------------
// Deployment
// ----------------------------------------------------------------

func (r *AppDefinitionReconciler) reconcileDeployment(ctx context.Context, appDef *v1.AppDefinition) error {
	logger := log.FromContext(ctx)

	deployment := &appsv1.Deployment{
		ObjectMeta: metav1.ObjectMeta{
			Name:      appDef.Name,
			Namespace: appDef.Namespace,
		},
	}

	op, err := ctrl.CreateOrUpdate(ctx, r.Client, deployment, func() error {
		replicas := int32(1)
		if appDef.Spec.Replicas != nil {
			replicas = *appDef.Spec.Replicas
		}

		deployment.Labels = standardLabels(appDef.Name)
		deployment.Spec = appsv1.DeploymentSpec{
			Replicas: &replicas,
			Selector: &metav1.LabelSelector{
				MatchLabels: selectorLabels(appDef.Name),
			},
			Template: corev1.PodTemplateSpec{
				ObjectMeta: metav1.ObjectMeta{
					Labels:      selectorLabels(appDef.Name),
					Annotations: podTemplateAnnotations(appDef),
				},
				Spec: corev1.PodSpec{},
			},
		}

		podSpec := &deployment.Spec.Template.Spec

		if appDef.Spec.SecurityContext != nil {
			podSpec.SecurityContext = appDef.Spec.SecurityContext
		}
		if len(appDef.Spec.NodeSelector) > 0 {
			podSpec.NodeSelector = appDef.Spec.NodeSelector
		}
		if len(appDef.Spec.Tolerations) > 0 {
			podSpec.Tolerations = appDef.Spec.Tolerations
		}
		if appDef.Spec.Affinity != nil {
			podSpec.Affinity = appDef.Spec.Affinity
		}
		if len(appDef.Spec.ImagePullSecrets) > 0 {
			podSpec.ImagePullSecrets = appDef.Spec.ImagePullSecrets
		}

		podSpec.Volumes = buildVolumes(appDef)
		podSpec.InitContainers = buildInitContainers(appDef)
		podSpec.Containers = buildContainers(appDef)

		return ctrl.SetControllerReference(appDef, deployment, r.Scheme)
	})

	if err != nil {
		return fmt.Errorf("failed to reconcile Deployment: %w", err)
	}
	if op != controllerutil.OperationResultNone {
		logger.Info("Deployment reconciled", "operation", op)
	}
	return nil
}

// podTemplateAnnotations returns annotations for the pod template.
// The config hash annotation triggers a rolling restart when inline ConfigMap or Secret data changes.
func podTemplateAnnotations(appDef *v1.AppDefinition) map[string]string {
	hash := computeConfigHash(appDef)
	if hash == "" {
		return nil
	}
	return map[string]string{configHashAnnotation: hash}
}

// computeConfigHash returns a 16-char hex hash of all inline ConfigMap and Secret data.
// Returns empty string when there is no inline data to hash.
func computeConfigHash(appDef *v1.AppDefinition) string {
	hasInlineData := false
	for _, sec := range appDef.Spec.Secrets {
		if len(sec.Data) > 0 {
			hasInlineData = true
			break
		}
	}
	if len(appDef.Spec.ConfigMaps) == 0 && !hasInlineData {
		return ""
	}

	h := sha256.New()

	cms := make([]v1.ConfigMapMount, len(appDef.Spec.ConfigMaps))
	copy(cms, appDef.Spec.ConfigMaps)
	sort.Slice(cms, func(i, j int) bool { return cms[i].Name < cms[j].Name })
	for _, cm := range cms {
		keys := make([]string, 0, len(cm.Data))
		for k := range cm.Data {
			keys = append(keys, k)
		}
		sort.Strings(keys)
		fmt.Fprintf(h, "cm:%s\n", cm.Name)
		for _, k := range keys {
			fmt.Fprintf(h, "%s=%s\n", k, cm.Data[k])
		}
	}

	secs := make([]v1.SecretMount, len(appDef.Spec.Secrets))
	copy(secs, appDef.Spec.Secrets)
	sort.Slice(secs, func(i, j int) bool { return secs[i].Name < secs[j].Name })
	for _, sec := range secs {
		if len(sec.Data) == 0 {
			continue
		}
		keys := make([]string, 0, len(sec.Data))
		for k := range sec.Data {
			keys = append(keys, k)
		}
		sort.Strings(keys)
		fmt.Fprintf(h, "secret:%s\n", sec.Name)
		for _, k := range keys {
			fmt.Fprintf(h, "%s=%s\n", k, sec.Data[k])
		}
	}

	return fmt.Sprintf("%x", h.Sum(nil))[:16]
}

func buildVolumes(appDef *v1.AppDefinition) []corev1.Volume {
	count := len(appDef.Spec.ConfigMaps)
	if appDef.Spec.Disk != nil {
		count++
	}
	for _, sec := range appDef.Spec.Secrets {
		if sec.MountPath != "" {
			count++
		}
	}
	volumes := make([]corev1.Volume, 0, count)

	if appDef.Spec.Disk != nil {
		volumes = append(volumes, corev1.Volume{
			Name: "app-disk",
			VolumeSource: corev1.VolumeSource{
				PersistentVolumeClaim: &corev1.PersistentVolumeClaimVolumeSource{
					ClaimName: pvcName(appDef.Name),
				},
			},
		})
	}

	for _, cm := range appDef.Spec.ConfigMaps {
		volumes = append(volumes, corev1.Volume{
			Name: "cm-" + cm.Name,
			VolumeSource: corev1.VolumeSource{
				ConfigMap: &corev1.ConfigMapVolumeSource{
					LocalObjectReference: corev1.LocalObjectReference{
						Name: appDef.Name + "-" + cm.Name,
					},
				},
			},
		})
	}

	for _, sec := range appDef.Spec.Secrets {
		if sec.MountPath == "" {
			continue
		}
		volumes = append(volumes, corev1.Volume{
			Name: "secret-" + sec.Name,
			VolumeSource: corev1.VolumeSource{
				Secret: &corev1.SecretVolumeSource{
					SecretName: resolvedSecretName(appDef.Name, sec),
				},
			},
		})
	}

	return volumes
}

// buildVolumeMounts returns the volume mounts shared by all containers (main and init).
func buildVolumeMounts(appDef *v1.AppDefinition) []corev1.VolumeMount {
	mountCount := len(appDef.Spec.ConfigMaps)
	if appDef.Spec.Disk != nil {
		mountCount += len(appDef.Spec.Disk.Partitions)
	}
	for _, sec := range appDef.Spec.Secrets {
		if sec.MountPath != "" {
			mountCount++
		}
	}
	mounts := make([]corev1.VolumeMount, 0, mountCount)

	if appDef.Spec.Disk != nil {
		for _, p := range appDef.Spec.Disk.Partitions {
			mounts = append(mounts, corev1.VolumeMount{
				Name:      "app-disk",
				MountPath: p.MountPath,
				SubPath:   p.SubPath,
			})
		}
	}
	for _, cm := range appDef.Spec.ConfigMaps {
		mounts = append(mounts, corev1.VolumeMount{
			Name:      "cm-" + cm.Name,
			MountPath: cm.MountPath,
		})
	}
	for _, sec := range appDef.Spec.Secrets {
		if sec.MountPath == "" {
			continue
		}
		mounts = append(mounts, corev1.VolumeMount{
			Name:      "secret-" + sec.Name,
			MountPath: sec.MountPath,
		})
	}
	return mounts
}

func buildContainers(appDef *v1.AppDefinition) []corev1.Container {
	containers := make([]corev1.Container, 0, len(appDef.Spec.Containers))
	for i, c := range appDef.Spec.Containers {
		containers = append(containers, buildContainer(appDef, c, i == 0))
	}
	return containers
}

// buildContainer builds a corev1.Container from a ContainerSpec.
// isPrimary marks the first container, which receives pod-level lifecycle hooks.
func buildContainer(appDef *v1.AppDefinition, c v1.ContainerSpec, isPrimary bool) corev1.Container {
	container := corev1.Container{
		Name:    c.Name,
		Image:   c.Image,
		Command: c.Command,
		Args:    c.Args,
		Env:     c.Env,
	}

	for _, p := range c.Ports {
		proto := corev1.Protocol(p.Protocol)
		if proto == "" {
			proto = corev1.ProtocolTCP
		}
		container.Ports = append(container.Ports, corev1.ContainerPort{
			Name:          p.Name,
			ContainerPort: p.ContainerPort,
			Protocol:      proto,
		})
	}

	if c.ReadinessProbe != nil {
		container.ReadinessProbe = buildProbe(c.ReadinessProbe)
	}
	if c.LivenessProbe != nil {
		container.LivenessProbe = buildProbe(c.LivenessProbe)
	}
	if len(c.Resources.Requests) > 0 || len(c.Resources.Limits) > 0 {
		container.Resources = c.Resources
	}

	// Apply pod-level lifecycle hooks to the primary container only.
	// Sidecars often lack a shell or the expected binaries, so applying hooks
	// to all containers causes FailedPostStartHook / CrashLoopBackOff.
	if isPrimary && appDef.Spec.Lifecycle != nil {
		lc := &corev1.Lifecycle{}
		if appDef.Spec.Lifecycle.PostStart != nil && appDef.Spec.Lifecycle.PostStart.Exec != nil {
			lc.PostStart = &corev1.LifecycleHandler{Exec: appDef.Spec.Lifecycle.PostStart.Exec}
		}
		if appDef.Spec.Lifecycle.PreStop != nil && appDef.Spec.Lifecycle.PreStop.Exec != nil {
			lc.PreStop = &corev1.LifecycleHandler{Exec: appDef.Spec.Lifecycle.PreStop.Exec}
		}
		if lc.PostStart != nil || lc.PreStop != nil {
			container.Lifecycle = lc
		}
	}

	// envFrom: inject inline secrets marked as env vars.
	for _, sec := range appDef.Spec.Secrets {
		if !sec.AsEnvVars {
			continue
		}
		container.EnvFrom = append(container.EnvFrom, corev1.EnvFromSource{
			SecretRef: &corev1.SecretEnvSource{
				LocalObjectReference: corev1.LocalObjectReference{
					Name: resolvedSecretName(appDef.Name, sec),
				},
			},
		})
	}

	container.VolumeMounts = buildVolumeMounts(appDef)
	return container
}

// buildInitContainers builds corev1.Containers for all initContainers in the spec.
// Init containers share the same volumes, secret env injection, and mounts as main containers
// but do not have ports or lifecycle hooks.
func buildInitContainers(appDef *v1.AppDefinition) []corev1.Container {
	if len(appDef.Spec.InitContainers) == 0 {
		return nil
	}
	containers := make([]corev1.Container, 0, len(appDef.Spec.InitContainers))
	for _, c := range appDef.Spec.InitContainers {
		container := corev1.Container{
			Name:    c.Name,
			Image:   c.Image,
			Command: c.Command,
			Args:    c.Args,
			Env:     c.Env,
		}
		if len(c.Resources.Requests) > 0 || len(c.Resources.Limits) > 0 {
			container.Resources = c.Resources
		}
		for _, sec := range appDef.Spec.Secrets {
			if !sec.AsEnvVars {
				continue
			}
			container.EnvFrom = append(container.EnvFrom, corev1.EnvFromSource{
				SecretRef: &corev1.SecretEnvSource{
					LocalObjectReference: corev1.LocalObjectReference{
						Name: resolvedSecretName(appDef.Name, sec),
					},
				},
			})
		}
		container.VolumeMounts = buildVolumeMounts(appDef)
		containers = append(containers, container)
	}
	return containers
}

func buildProbe(p *v1.Probe) *corev1.Probe {
	return &corev1.Probe{
		ProbeHandler: corev1.ProbeHandler{
			HTTPGet:   p.HTTPGet,
			TCPSocket: p.TCPSocket,
			Exec:      p.Exec,
		},
		InitialDelaySeconds: p.InitialDelaySeconds,
		PeriodSeconds:       p.PeriodSeconds,
		TimeoutSeconds:      p.TimeoutSeconds,
		FailureThreshold:    p.FailureThreshold,
		SuccessThreshold:    p.SuccessThreshold,
	}
}

// ----------------------------------------------------------------
// Service
// ----------------------------------------------------------------

func (r *AppDefinitionReconciler) reconcileService(ctx context.Context, appDef *v1.AppDefinition) error {
	logger := log.FromContext(ctx)

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
		for _, container := range appDef.Spec.Containers {
			for _, port := range container.Ports {
				if !port.Expose {
					continue
				}
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

// ----------------------------------------------------------------
// Inline ConfigMaps
// ----------------------------------------------------------------

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

// ----------------------------------------------------------------
// Inline and External Secrets
// ----------------------------------------------------------------

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
			obj.StringData = sec.Data
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

// ----------------------------------------------------------------
// PersistentVolumeClaim
// ----------------------------------------------------------------

func (r *AppDefinitionReconciler) reconcilePVC(ctx context.Context, appDef *v1.AppDefinition) error {
	logger := log.FromContext(ctx)

	pvc := &corev1.PersistentVolumeClaim{
		ObjectMeta: metav1.ObjectMeta{
			Name:      pvcName(appDef.Name),
			Namespace: appDef.Namespace,
		},
	}

	op, err := ctrl.CreateOrUpdate(ctx, r.Client, pvc, func() error {
		pvc.Labels = standardLabels(appDef.Name)

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

// ----------------------------------------------------------------
// Ingress
// ----------------------------------------------------------------

func (r *AppDefinitionReconciler) reconcileIngress(ctx context.Context, appDef *v1.AppDefinition) error {
	logger := log.FromContext(ctx)

	ingress := &networkingv1.Ingress{
		ObjectMeta: metav1.ObjectMeta{
			Name:      appDef.Name,
			Namespace: appDef.Namespace,
		},
	}

	op, err := ctrl.CreateOrUpdate(ctx, r.Client, ingress, func() error {
		ingress.Labels = standardLabels(appDef.Name)

		// Merge global ingress annotations.
		ingress.Annotations = make(map[string]string)
		for k, v := range appDef.Spec.IngressAnnotations {
			ingress.Annotations[k] = v
		}
		if appDef.Spec.IngressClass != "" {
			ingress.Spec.IngressClassName = &appDef.Spec.IngressClass
		}

		// Build TLS blocks — one entry per TLS-enabled domain with its own secret.
		ingress.Spec.TLS = nil
		for _, domain := range appDef.Spec.Domains {
			if !domain.TLS {
				continue
			}
			secretName := domain.SecretName
			if secretName == "" {
				secretName = tlsSecretName(appDef.Name, domain.Name)
			}
			ingress.Spec.TLS = append(ingress.Spec.TLS, networkingv1.IngressTLS{
				Hosts:      []string{domain.Name},
				SecretName: secretName,
			})
			// Per-domain cert-manager issuer annotation.
			if domain.CertIssuer != "" {
				ingress.Annotations["cert-manager.io/cluster-issuer"] = domain.CertIssuer
			}
		}

		// Build rules.
		pathType := networkingv1.PathTypePrefix
		ingress.Spec.Rules = nil
		for _, domain := range appDef.Spec.Domains {
			portName := domain.PortName
			if portName == "" {
				portName = "http"
			}
			path := domain.Path
			if path == "" {
				path = "/"
			}
			ingress.Spec.Rules = append(ingress.Spec.Rules, networkingv1.IngressRule{
				Host: domain.Name,
				IngressRuleValue: networkingv1.IngressRuleValue{
					HTTP: &networkingv1.HTTPIngressRuleValue{
						Paths: []networkingv1.HTTPIngressPath{
							{
								Path:     path,
								PathType: &pathType,
								Backend: networkingv1.IngressBackend{
									Service: &networkingv1.IngressServiceBackend{
										Name: appDef.Name,
										Port: networkingv1.ServiceBackendPort{Name: portName},
									},
								},
							},
						},
					},
				},
			})
		}

		return ctrl.SetControllerReference(appDef, ingress, r.Scheme)
	})

	if err != nil {
		return fmt.Errorf("failed to reconcile Ingress: %w", err)
	}
	if op != controllerutil.OperationResultNone {
		logger.Info("Ingress reconciled", "operation", op)
	}
	return nil
}

// ----------------------------------------------------------------
// HorizontalPodAutoscaler
// ----------------------------------------------------------------

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

// ----------------------------------------------------------------
// Status
// ----------------------------------------------------------------

func (r *AppDefinitionReconciler) updateStatus(ctx context.Context, appDef *v1.AppDefinition, reconcileErr error) error {
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
		if err := r.APIReader.Get(ctx, types.NamespacedName{Name: pvcName(appDef.Name), Namespace: appDef.Namespace}, pvc); err == nil {
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

// ----------------------------------------------------------------
// ServiceMonitor (monitoring.coreos.com/v1)
// ----------------------------------------------------------------

// reconcileServiceMonitor creates or updates a ServiceMonitor when monitoringConfig.enabled
// is true and at least one port has a metricsPath set.  When the prometheus-operator CRDs
// are not installed the call is silently skipped — the app still works, just without
// Prometheus scraping.
func (r *AppDefinitionReconciler) reconcileServiceMonitor(ctx context.Context, appDef *v1.AppDefinition) error {
	logger := log.FromContext(ctx)
	smKey := types.NamespacedName{Name: appDef.Name, Namespace: appDef.Namespace}

	enabled := appDef.Spec.MonitoringConfig != nil && appDef.Spec.MonitoringConfig.Enabled
	endpoints := buildSMEndpoints(appDef)

	existing := &unstructured.Unstructured{}
	existing.SetGroupVersionKind(serviceMonitorGVK)

	if !enabled || len(endpoints) == 0 {
		// Remove any previously created ServiceMonitor.
		err := r.Get(ctx, smKey, existing)
		if err == nil {
			return r.Delete(ctx, existing)
		}
		if apimeta.IsNoMatchError(err) || apierrors.IsNotFound(err) {
			return nil
		}
		return err
	}

	// Build labels merging standard labels with user-supplied monitoring labels.
	labels := make(map[string]interface{})
	for k, v := range standardLabels(appDef.Name) {
		labels[k] = v
	}
	for k, v := range appDef.Spec.MonitoringConfig.Labels {
		labels[k] = v
	}

	desired := &unstructured.Unstructured{
		Object: map[string]interface{}{
			"apiVersion": "monitoring.coreos.com/v1",
			"kind":       "ServiceMonitor",
			"metadata": map[string]interface{}{
				"name":      appDef.Name,
				"namespace": appDef.Namespace,
				"labels":    labels,
			},
			"spec": map[string]interface{}{
				"selector": map[string]interface{}{
					"matchLabels": map[string]interface{}{
						"app.kubernetes.io/name": appDef.Name,
					},
				},
				"endpoints": endpoints,
				"namespaceSelector": map[string]interface{}{
					"matchNames": []interface{}{appDef.Namespace},
				},
			},
		},
	}

	if err := ctrl.SetControllerReference(appDef, desired, r.Scheme); err != nil {
		return fmt.Errorf("setting owner reference on ServiceMonitor: %w", err)
	}

	err := r.Get(ctx, smKey, existing)
	if err != nil {
		if apimeta.IsNoMatchError(err) {
			logger.V(1).Info("ServiceMonitor CRD not installed, skipping")
			return nil
		}
		if apierrors.IsNotFound(err) {
			logger.Info("creating ServiceMonitor")
			if createErr := r.Create(ctx, desired); createErr != nil {
				if apimeta.IsNoMatchError(createErr) {
					logger.V(1).Info("ServiceMonitor CRD not installed, skipping")
					return nil
				}
				return createErr
			}
			return nil
		}
		return err
	}

	desired.SetResourceVersion(existing.GetResourceVersion())
	logger.Info("updating ServiceMonitor")
	return r.Update(ctx, desired)
}

// buildSMEndpoints returns the list of scrape endpoints from ports that have metricsPath set.
func buildSMEndpoints(appDef *v1.AppDefinition) []interface{} {
	var endpoints []interface{}
	seen := map[string]bool{}
	for _, c := range appDef.Spec.Containers {
		for _, p := range c.Ports {
			if p.MetricsPath == "" || seen[p.Name] {
				continue
			}
			seen[p.Name] = true
			ep := map[string]interface{}{
				"port": p.Name,
				"path": p.MetricsPath,
			}
			if appDef.Spec.MonitoringConfig.ScrapeInterval != "" {
				ep["interval"] = appDef.Spec.MonitoringConfig.ScrapeInterval
			}
			endpoints = append(endpoints, ep)
		}
	}
	return endpoints
}

// ----------------------------------------------------------------
// Manager setup
// ----------------------------------------------------------------

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

// ----------------------------------------------------------------
// Helpers
// ----------------------------------------------------------------

func standardLabels(name string) map[string]string {
	return map[string]string{
		"app.kubernetes.io/name":       name,
		"app.kubernetes.io/instance":   name,
		"app.kubernetes.io/managed-by": "app-operator",
	}
}

func selectorLabels(name string) map[string]string {
	return map[string]string{
		"app.kubernetes.io/name": name,
	}
}

func pvcName(appName string) string {
	return appName + "-disk"
}

// resolvedSecretName returns the Kubernetes Secret name to use for a SecretMount.
// If SecretRef is set, the referenced secret is used directly (operator does not manage it).
// Otherwise the operator-managed secret named "<app>-<name>" is used.
func resolvedSecretName(appName string, sec v1.SecretMount) string {
	if sec.SecretRef != "" {
		return sec.SecretRef
	}
	return appName + "-" + sec.Name
}

// tlsSecretName generates a DNS-safe TLS secret name from the app name and domain.
func tlsSecretName(appName, domain string) string {
	safe := sanitizeDNS(domain)
	return fmt.Sprintf("%s-%s-tls", appName, safe)
}

func sanitizeDNS(s string) string {
	var b strings.Builder
	for _, r := range strings.ToLower(s) {
		if (r >= 'a' && r <= 'z') || (r >= '0' && r <= '9') || r == '-' {
			b.WriteRune(r)
		} else {
			b.WriteRune('-')
		}
	}
	return strings.Trim(b.String(), "-")
}
