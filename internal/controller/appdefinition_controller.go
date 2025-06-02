package controller

import (
	"context"
	"fmt"
	"time"

	"github.com/go-logr/logr"
	"k8s.io/apimachinery/pkg/api/resource"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/types"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/controller/controllerutil"
	"sigs.k8s.io/controller-runtime/pkg/log"

	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	networkingv1 "k8s.io/api/networking/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/util/intstr"

	v1 "github.com/abexamir/app-operator/api/v1"
)

// AppDefinitionReconciler reconciles a AppDefinition object
type AppDefinitionReconciler struct {
	client.Client
	Scheme *runtime.Scheme
	Log    logr.Logger
}

//+kubebuilder:rbac:groups=appdefinition.abexamir.me,resources=appdefinitions,verbs=get;list;watch;create;update;patch;delete
//+kubebuilder:rbac:groups=appdefinition.abexamir.me,resources=appdefinitions/status,verbs=get;update;patch
//+kubebuilder:rbac:groups=appdefinition.abexamir.me,resources=appdefinitions/finalizers,verbs=update
//+kubebuilder:rbac:groups=apps,resources=deployments,verbs=get;list;watch;create;update;patch;delete
//+kubebuilder:rbac:groups=core,resources=services,verbs=get;list;watch;create;update;patch;delete
//+kubebuilder:rbac:groups=core,resources=pods,verbs=get;list;watch
//+kubebuilder:rbac:groups=networking.k8s.io,resources=ingresses,verbs=get;list;watch;create;update;patch;delete
//+kubebuilder:rbac:groups=core,resources=persistentvolumeclaims,verbs=get;list;watch;create;update;patch;delete
//+kubebuilder:rbac:groups=monitoring.coreos.com,resources=servicemonitors,verbs=get;list;watch;create;update;patch;delete

// Reconcile is part of the main kubernetes reconciliation loop which aims to
// move the current state of the cluster closer to the desired state.
func (r *AppDefinitionReconciler) Reconcile(ctx context.Context, req ctrl.Request) (ctrl.Result, error) {
	log := log.FromContext(ctx)
	log.Info("Starting reconciliation", "namespace", req.Namespace, "name", req.Name)

	// Fetch the AppDefinition instance
	appDef := &v1.AppDefinition{}
	if err := r.Get(ctx, req.NamespacedName, appDef); err != nil {
		// Handle the case where the resource is not found
		return ctrl.Result{}, client.IgnoreNotFound(err)
	}

	// Set up finalizer
	if !controllerutil.ContainsFinalizer(appDef, "appdefinition.abeaxmir.me/finalizer") {
		controllerutil.AddFinalizer(appDef, "appdefinition.abeaxmir.me/finalizer")
		if err := r.Update(ctx, appDef); err != nil {
			return ctrl.Result{}, err
		}
	}

	// Handle deletion
	if !appDef.DeletionTimestamp.IsZero() {
		return r.handleDeletion(ctx, appDef)
	}

	// Reconcile Deployment
	if err := r.reconcileDeployment(ctx, appDef); err != nil {
		log.Error(err, "Failed to reconcile Deployment")
		return ctrl.Result{}, err
	}

	// Reconcile Service
	if err := r.reconcileService(ctx, appDef); err != nil {
		log.Error(err, "Failed to reconcile Service")
		return ctrl.Result{}, err
	}

	// Reconcile PVC if disk config is specified
	if appDef.Spec.Disk != nil {
		if err := r.reconcilePVC(ctx, appDef); err != nil {
			log.Error(err, "Failed to reconcile PVC")
			return ctrl.Result{}, err
		}
	}

	// Reconcile Ingress if domains are specified
	if len(appDef.Spec.Domains) > 0 {
		if err := r.reconcileIngress(ctx, appDef); err != nil {
			log.Error(err, "Failed to reconcile Ingress")
			return ctrl.Result{}, err
		}
	}

	return ctrl.Result{RequeueAfter: time.Minute}, nil
}

func (r *AppDefinitionReconciler) handleDeletion(ctx context.Context, appDef *v1.AppDefinition) (ctrl.Result, error) {
	// Remove finalizer
	controllerutil.RemoveFinalizer(appDef, "appdefinition.abeaxmir.me/finalizer")
	if err := r.Update(ctx, appDef); err != nil {
		return ctrl.Result{}, err
	}
	return ctrl.Result{}, nil
}

func (r *AppDefinitionReconciler) reconcileDeployment(ctx context.Context, appDef *v1.AppDefinition) error {
	log := r.Log.WithValues("appdefinition", appDef.Name, "namespace", appDef.Namespace)

	// Create the deployment object
	deployment := &appsv1.Deployment{
		ObjectMeta: metav1.ObjectMeta{
			Name:      appDef.Name,
			Namespace: appDef.Namespace,
		},
	}

	// Create or update the deployment
	op, err := ctrl.CreateOrUpdate(ctx, r.Client, deployment, func() error {
		// Set replicas
		replicas := int32(1)
		if appDef.Spec.Replicas != nil {
			replicas = *appDef.Spec.Replicas
		}

		// Set labels and annotations
		deployment.Labels = map[string]string{
			"app.kubernetes.io/name":       appDef.Name,
			"app.kubernetes.io/instance":   appDef.Name,
			"app.kubernetes.io/managed-by": "app-operator",
		}

		// Set deployment spec
		deployment.Spec = appsv1.DeploymentSpec{
			Replicas: &replicas,
			Selector: &metav1.LabelSelector{
				MatchLabels: map[string]string{
					"app.kubernetes.io/name": appDef.Name,
				},
			},
			Template: corev1.PodTemplateSpec{
				ObjectMeta: metav1.ObjectMeta{
					Labels: map[string]string{
						"app.kubernetes.io/name": appDef.Name,
					},
				},
				Spec: corev1.PodSpec{
					Containers: make([]corev1.Container, 0),
				},
			},
		}

		// Set pod security context if specified
		if appDef.Spec.SecurityContext != nil {
			deployment.Spec.Template.Spec.SecurityContext = appDef.Spec.SecurityContext
		}

		// Set node selector if specified
		if appDef.Spec.NodeSelector != nil {
			deployment.Spec.Template.Spec.NodeSelector = appDef.Spec.NodeSelector
		}

		// Set tolerations if specified
		if appDef.Spec.Tolerations != nil {
			deployment.Spec.Template.Spec.Tolerations = appDef.Spec.Tolerations
		}

		// Set affinity if specified
		if appDef.Spec.Affinity != nil {
			deployment.Spec.Template.Spec.Affinity = appDef.Spec.Affinity
		}

		// Add containers based on source type
		switch appDef.Spec.Source.Type {
		case "dockerImage":
			if appDef.Spec.Source.DockerImage == nil {
				return fmt.Errorf("dockerImage source type specified but no DockerImage config provided")
			}
			for _, container := range appDef.Spec.Source.DockerImage.Containers {
				// Create container spec
				containerSpec := corev1.Container{
					Name:  container.Name,
					Image: container.Image,
				}

				// Set command and args if specified
				if len(container.Command) > 0 {
					containerSpec.Command = container.Command
				}
				if len(container.Args) > 0 {
					containerSpec.Args = container.Args
				}

				// Set environment variables
				if len(container.Env) > 0 {
					containerSpec.Env = container.Env
				}

				// Set ports
				if len(container.Ports) > 0 {
					containerSpec.Ports = make([]corev1.ContainerPort, 0, len(container.Ports))
					for _, port := range container.Ports {
						containerSpec.Ports = append(containerSpec.Ports, corev1.ContainerPort{
							Name:          port.Name,
							ContainerPort: port.ContainerPort,
							Protocol:      corev1.Protocol(port.Protocol),
						})
					}
				}

				// Set probes
				if container.ReadinessProbe != nil {
					containerSpec.ReadinessProbe = &corev1.Probe{
						InitialDelaySeconds: container.ReadinessProbe.InitialDelaySeconds,
						PeriodSeconds:       container.ReadinessProbe.PeriodSeconds,
						TimeoutSeconds:      container.ReadinessProbe.TimeoutSeconds,
						FailureThreshold:    container.ReadinessProbe.FailureThreshold,
						SuccessThreshold:    container.ReadinessProbe.SuccessThreshold,
					}
					if container.ReadinessProbe.HTTPGet != nil {
						containerSpec.ReadinessProbe.HTTPGet = container.ReadinessProbe.HTTPGet
					}
					if container.ReadinessProbe.TCPSocket != nil {
						containerSpec.ReadinessProbe.TCPSocket = container.ReadinessProbe.TCPSocket
					}
					if container.ReadinessProbe.Exec != nil {
						containerSpec.ReadinessProbe.Exec = container.ReadinessProbe.Exec
					}
				}

				if container.LivenessProbe != nil {
					containerSpec.LivenessProbe = &corev1.Probe{
						InitialDelaySeconds: container.LivenessProbe.InitialDelaySeconds,
						PeriodSeconds:       container.LivenessProbe.PeriodSeconds,
						TimeoutSeconds:      container.LivenessProbe.TimeoutSeconds,
						FailureThreshold:    container.LivenessProbe.FailureThreshold,
						SuccessThreshold:    container.LivenessProbe.SuccessThreshold,
					}
					if container.LivenessProbe.HTTPGet != nil {
						containerSpec.LivenessProbe.HTTPGet = container.LivenessProbe.HTTPGet
					}
					if container.LivenessProbe.TCPSocket != nil {
						containerSpec.LivenessProbe.TCPSocket = container.LivenessProbe.TCPSocket
					}
					if container.LivenessProbe.Exec != nil {
						containerSpec.LivenessProbe.Exec = container.LivenessProbe.Exec
					}
				}

				// Set resources if specified
				if len(container.Resources.Requests) > 0 || len(container.Resources.Limits) > 0 {
					containerSpec.Resources = container.Resources
				}

				// Add container to pod spec
				deployment.Spec.Template.Spec.Containers = append(deployment.Spec.Template.Spec.Containers, containerSpec)
			}

		case "gitRepo":
			// TODO: Implement Git repo source type
			return fmt.Errorf("gitRepo source type not yet implemented")

		case "helmChart":
			// TODO: Implement Helm chart source type
			return fmt.Errorf("helmChart source type not yet implemented")

		default:
			return fmt.Errorf("unsupported source type: %s", appDef.Spec.Source.Type)
		}

		// Set owner reference
		if err := ctrl.SetControllerReference(appDef, deployment, r.Scheme); err != nil {
			return err
		}

		return nil
	})

	if err != nil {
		return fmt.Errorf("failed to create/update deployment: %w", err)
	}

	if op != controllerutil.OperationResultNone {
		log.Info("Deployment reconciled", "operation", op)
	}

	return nil
}

func (r *AppDefinitionReconciler) reconcileService(ctx context.Context, appDef *v1.AppDefinition) error {
	log := r.Log.WithValues("appdefinition", appDef.Name, "namespace", appDef.Namespace)

	// Create the service object
	service := &corev1.Service{
		ObjectMeta: metav1.ObjectMeta{
			Name:      appDef.Name,
			Namespace: appDef.Namespace,
		},
	}

	// Create or update the service
	op, err := ctrl.CreateOrUpdate(ctx, r.Client, service, func() error {
		// Set labels and annotations
		service.Labels = map[string]string{
			"app.kubernetes.io/name":       appDef.Name,
			"app.kubernetes.io/instance":   appDef.Name,
			"app.kubernetes.io/managed-by": "app-operator",
		}

		// Set service type
		serviceType := corev1.ServiceTypeClusterIP
		if appDef.Spec.ServiceType != "" {
			serviceType = appDef.Spec.ServiceType
		}
		service.Spec.Type = serviceType

		// Set selector
		service.Spec.Selector = map[string]string{
			"app.kubernetes.io/name": appDef.Name,
		}

		// Add ports based on container specs
		service.Spec.Ports = make([]corev1.ServicePort, 0)
		if appDef.Spec.Source.Type == "dockerImage" && appDef.Spec.Source.DockerImage != nil {
			for _, container := range appDef.Spec.Source.DockerImage.Containers {
				for _, port := range container.Ports {
					if port.Expose {
						servicePort := corev1.ServicePort{
							Name:       port.Name,
							Port:       port.ServicePort,
							TargetPort: intstr.FromInt32(port.ContainerPort),
							Protocol:   corev1.Protocol(port.Protocol),
						}
						service.Spec.Ports = append(service.Spec.Ports, servicePort)
					}
				}
			}
		}

		// Set owner reference
		if err := ctrl.SetControllerReference(appDef, service, r.Scheme); err != nil {
			return err
		}

		return nil
	})

	if err != nil {
		return fmt.Errorf("failed to create/update service: %w", err)
	}

	if op != controllerutil.OperationResultNone {
		log.Info("Service reconciled", "operation", op)
	}

	return nil
}

func (r *AppDefinitionReconciler) reconcilePVC(ctx context.Context, appDef *v1.AppDefinition) error {
	log := r.Log.WithValues("appdefinition", appDef.Name, "namespace", appDef.Namespace)

	if appDef.Spec.Disk == nil {
		return nil
	}

	// Create the PVC object
	pvc := &corev1.PersistentVolumeClaim{
		ObjectMeta: metav1.ObjectMeta{
			Name:      fmt.Sprintf("%s-disk", appDef.Name),
			Namespace: appDef.Namespace,
		},
	}

	// Create or update the PVC
	op, err := ctrl.CreateOrUpdate(ctx, r.Client, pvc, func() error {
		// Set labels and annotations
		pvc.Labels = map[string]string{
			"app.kubernetes.io/name":       appDef.Name,
			"app.kubernetes.io/instance":   appDef.Name,
			"app.kubernetes.io/managed-by": "app-operator",
		}

		// Set PVC spec
		pvc.Spec = corev1.PersistentVolumeClaimSpec{
			AccessModes: []corev1.PersistentVolumeAccessMode{
				corev1.ReadWriteOnce,
			},
			Resources: corev1.VolumeResourceRequirements{
				Requests: corev1.ResourceList{
					corev1.ResourceStorage: resource.MustParse(fmt.Sprintf("%dGi", appDef.Spec.Disk.SizeInGi)),
				},
			},
		}

		// Set storage class if specified
		if appDef.Spec.Disk.StorageClassName != "" {
			pvc.Spec.StorageClassName = &appDef.Spec.Disk.StorageClassName
		}

		// Set owner reference
		if err := ctrl.SetControllerReference(appDef, pvc, r.Scheme); err != nil {
			return err
		}

		return nil
	})

	if err != nil {
		return fmt.Errorf("failed to create/update PVC: %w", err)
	}

	if op != controllerutil.OperationResultNone {
		log.Info("PVC reconciled", "operation", op)
	}

	// Update the deployment to include the volume
	deployment := &appsv1.Deployment{}
	if err := r.Get(ctx, types.NamespacedName{Name: appDef.Name, Namespace: appDef.Namespace}, deployment); err != nil {
		return fmt.Errorf("failed to get deployment: %w", err)
	}

	// Add volume to pod spec
	volume := corev1.Volume{
		Name: "app-disk",
		VolumeSource: corev1.VolumeSource{
			PersistentVolumeClaim: &corev1.PersistentVolumeClaimVolumeSource{
				ClaimName: pvc.Name,
			},
		},
	}
	deployment.Spec.Template.Spec.Volumes = append(deployment.Spec.Template.Spec.Volumes, volume)

	// Add volume mounts to containers
	for i := range deployment.Spec.Template.Spec.Containers {
		container := &deployment.Spec.Template.Spec.Containers[i]
		for _, partition := range appDef.Spec.Disk.Partitions {
			volumeMount := corev1.VolumeMount{
				Name:      "app-disk",
				MountPath: partition.MountPath,
				SubPath:   partition.SubPath,
			}
			container.VolumeMounts = append(container.VolumeMounts, volumeMount)
		}
	}

	// Update the deployment
	if err := r.Update(ctx, deployment); err != nil {
		return fmt.Errorf("failed to update deployment with volume mounts: %w", err)
	}

	return nil
}

func (r *AppDefinitionReconciler) reconcileIngress(ctx context.Context, appDef *v1.AppDefinition) error {
	log := r.Log.WithValues("appdefinition", appDef.Name, "namespace", appDef.Namespace)

	if len(appDef.Spec.Domains) == 0 {
		return nil
	}

	// Create the ingress object
	ingress := &networkingv1.Ingress{
		ObjectMeta: metav1.ObjectMeta{
			Name:      appDef.Name,
			Namespace: appDef.Namespace,
		},
	}

	// Create or update the ingress
	op, err := ctrl.CreateOrUpdate(ctx, r.Client, ingress, func() error {
		// Set labels and annotations
		ingress.Labels = map[string]string{
			"app.kubernetes.io/name":       appDef.Name,
			"app.kubernetes.io/instance":   appDef.Name,
			"app.kubernetes.io/managed-by": "app-operator",
		}

		// Set ingress annotations
		ingress.Annotations = make(map[string]string)
		if appDef.Spec.IngressAnnotations != nil {
			for k, v := range appDef.Spec.IngressAnnotations {
				ingress.Annotations[k] = v
			}
		}

		// Set ingress class if specified
		if appDef.Spec.IngressClass != "" {
			ingress.Annotations["kubernetes.io/ingress.class"] = appDef.Spec.IngressClass
		}

		// Configure TLS if enabled
		var tlsHosts []string
		for _, domain := range appDef.Spec.Domains {
			if domain.TLS {
				tlsHosts = append(tlsHosts, domain.Name)
			}
		}
		if len(tlsHosts) > 0 {
			ingress.Spec.TLS = []networkingv1.IngressTLS{
				{
					Hosts: tlsHosts,
				},
			}
		}

		// Configure rules
		ingress.Spec.Rules = make([]networkingv1.IngressRule, 0, len(appDef.Spec.Domains))
		for _, domain := range appDef.Spec.Domains {
			rule := networkingv1.IngressRule{
				Host: domain.Name,
				IngressRuleValue: networkingv1.IngressRuleValue{
					HTTP: &networkingv1.HTTPIngressRuleValue{
						Paths: []networkingv1.HTTPIngressPath{
							{
								Path:     domain.Path,
								PathType: &[]networkingv1.PathType{networkingv1.PathTypePrefix}[0],
								Backend: networkingv1.IngressBackend{
									Service: &networkingv1.IngressServiceBackend{
										Name: appDef.Name,
										Port: networkingv1.ServiceBackendPort{
											Name: "http",
										},
									},
								},
							},
						},
					},
				},
			}
			ingress.Spec.Rules = append(ingress.Spec.Rules, rule)
		}

		// Set owner reference
		if err := ctrl.SetControllerReference(appDef, ingress, r.Scheme); err != nil {
			return err
		}

		return nil
	})

	if err != nil {
		return fmt.Errorf("failed to create/update ingress: %w", err)
	}

	if op != controllerutil.OperationResultNone {
		log.Info("Ingress reconciled", "operation", op)
	}

	return nil
}

// SetupWithManager sets up the controller with the Manager.
func (r *AppDefinitionReconciler) SetupWithManager(mgr ctrl.Manager) error {
	return ctrl.NewControllerManagedBy(mgr).
		For(&v1.AppDefinition{}).
		Owns(&appsv1.Deployment{}).
		Owns(&corev1.Service{}).
		Owns(&networkingv1.Ingress{}).
		Owns(&corev1.PersistentVolumeClaim{}).
		Complete(r)
}
