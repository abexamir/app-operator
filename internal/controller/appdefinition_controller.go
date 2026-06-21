package controller

import (
	"context"
	"fmt"
	"strings"
	"time"

	apimeta "k8s.io/apimachinery/pkg/api/meta"
	"k8s.io/apimachinery/pkg/api/resource"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/types"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/controller/controllerutil"
	"sigs.k8s.io/controller-runtime/pkg/log"

	appsv1 "k8s.io/api/apps/v1"
	autoscalingv2 "k8s.io/api/autoscaling/v2"
	corev1 "k8s.io/api/core/v1"
	networkingv1 "k8s.io/api/networking/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/util/intstr"

	v1 "github.com/abexamir/app-operator/api/v1"
)

const finalizer = "appdefinition.abexamir.me/finalizer"

// AppDefinitionReconciler reconciles a AppDefinition object.
type AppDefinitionReconciler struct {
	client.Client
	Scheme *runtime.Scheme
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
	return r.reconcileHPA(ctx, appDef)
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
					Labels: selectorLabels(appDef.Name),
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

		if appDef.Spec.Source.DockerImage == nil {
			return fmt.Errorf("dockerImage source type specified but no dockerImage config provided")
		}
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

func buildVolumes(appDef *v1.AppDefinition) []corev1.Volume {
	count := len(appDef.Spec.ConfigMaps) + len(appDef.Spec.Secrets)
	if appDef.Spec.Disk != nil {
		count++
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
		optional := cm.Optional
		volumes = append(volumes, corev1.Volume{
			Name: "cm-" + cm.Name,
			VolumeSource: corev1.VolumeSource{
				ConfigMap: &corev1.ConfigMapVolumeSource{
					LocalObjectReference: corev1.LocalObjectReference{Name: cm.Name},
					Optional:             &optional,
				},
			},
		})
	}

	for _, sec := range appDef.Spec.Secrets {
		optional := sec.Optional
		volumes = append(volumes, corev1.Volume{
			Name: "secret-" + sec.Name,
			VolumeSource: corev1.VolumeSource{
				Secret: &corev1.SecretVolumeSource{
					SecretName: sec.Name,
					Optional:   &optional,
				},
			},
		})
	}

	return volumes
}

func buildContainers(appDef *v1.AppDefinition) []corev1.Container {
	containers := make([]corev1.Container, 0, len(appDef.Spec.Source.DockerImage.Containers))
	for _, c := range appDef.Spec.Source.DockerImage.Containers {
		containers = append(containers, buildContainer(appDef, c))
	}
	return containers
}

func buildContainer(appDef *v1.AppDefinition, c v1.ContainerSpec) corev1.Container {
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

	// Apply pod-level lifecycle hooks to every container.
	if appDef.Spec.Lifecycle != nil {
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

	// Volume mounts: disk partitions, configmaps, secrets.
	mountCount := len(appDef.Spec.ConfigMaps) + len(appDef.Spec.Secrets)
	if appDef.Spec.Disk != nil {
		mountCount += len(appDef.Spec.Disk.Partitions)
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
		mounts = append(mounts, corev1.VolumeMount{
			Name:      "secret-" + sec.Name,
			MountPath: sec.MountPath,
		})
	}
	container.VolumeMounts = mounts

	return container
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
		if appDef.Spec.Source.DockerImage != nil {
			for _, container := range appDef.Spec.Source.DockerImage.Containers {
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

		// PVC storage is immutable after creation; only set spec when creating.
		if pvc.ResourceVersion == "" {
			storageClass := appDef.Spec.Disk.StorageClassName
			pvc.Spec = corev1.PersistentVolumeClaimSpec{
				AccessModes: []corev1.PersistentVolumeAccessMode{corev1.ReadWriteOnce},
				Resources: corev1.VolumeResourceRequirements{
					Requests: corev1.ResourceList{
						corev1.ResourceStorage: resource.MustParse(
							fmt.Sprintf("%dGi", appDef.Spec.Disk.SizeInGi),
						),
					},
				},
				StorageClassName: &storageClass,
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
			ingress.Annotations["kubernetes.io/ingress.class"] = appDef.Spec.IngressClass
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
	appDef.Status.ObservedGeneration = appDef.Generation

	desiredReplicas := int32(1)
	if appDef.Spec.Replicas != nil {
		desiredReplicas = *appDef.Spec.Replicas
	}
	appDef.Status.Replicas = desiredReplicas

	// Fetch deployment to get ready replica count.
	deployment := &appsv1.Deployment{}
	deploymentFound := true
	if err := r.Get(ctx, types.NamespacedName{Name: appDef.Name, Namespace: appDef.Namespace}, deployment); err != nil {
		if client.IgnoreNotFound(err) != nil {
			return fmt.Errorf("failed to get Deployment for status: %w", err)
		}
		deploymentFound = false
	}
	if deploymentFound {
		appDef.Status.ReadyReplicas = deployment.Status.ReadyReplicas
	}

	now := metav1.Now()
	ready := metav1.ConditionFalse
	readyReason := "Progressing"
	readyMsg := fmt.Sprintf("%d/%d replicas ready", appDef.Status.ReadyReplicas, desiredReplicas)

	switch {
	case appDef.Spec.Paused:
		appDef.Status.Phase = "Paused"
		readyReason = "Paused"
		readyMsg = "Reconciliation is paused"
	case reconcileErr != nil:
		appDef.Status.Phase = "Failed"
		appDef.Status.LastError = reconcileErr.Error()
		readyReason = "ReconcileError"
		readyMsg = reconcileErr.Error()
	case deploymentFound && deployment.Status.ReadyReplicas >= desiredReplicas:
		appDef.Status.Phase = "Available"
		appDef.Status.LastError = ""
		ready = metav1.ConditionTrue
		readyReason = "DeploymentAvailable"
	default:
		appDef.Status.Phase = "Progressing"
	}

	apimeta.SetStatusCondition(&appDef.Status.Conditions, metav1.Condition{
		Type:               v1.ConditionTypeReady,
		Status:             ready,
		Reason:             readyReason,
		Message:            readyMsg,
		LastTransitionTime: now,
		ObservedGeneration: appDef.Generation,
	})

	if err := r.Status().Update(ctx, appDef); err != nil {
		return fmt.Errorf("failed to update AppDefinition status: %w", err)
	}
	return nil
}

// ----------------------------------------------------------------
// Manager setup
// ----------------------------------------------------------------

func (r *AppDefinitionReconciler) SetupWithManager(mgr ctrl.Manager) error {
	return ctrl.NewControllerManagedBy(mgr).
		For(&v1.AppDefinition{}).
		Owns(&appsv1.Deployment{}).
		Owns(&corev1.Service{}).
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
