package controller

import (
	"context"
	"crypto/sha256"
	"fmt"
	"io"
	"sort"

	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/controller/controllerutil"
	"sigs.k8s.io/controller-runtime/pkg/log"

	v1 "github.com/abexamir/app-operator/api/v1"
)

const configHashAnnotation = "appdefinition.abexamir.me/config-hash"

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
		deployment.Spec.Replicas = &replicas
		// Selector is immutable after creation; only set it on new deployments.
		if deployment.Spec.Selector == nil {
			deployment.Spec.Selector = &metav1.LabelSelector{
				MatchLabels: selectorLabels(appDef.Name),
			}
		}
		deployment.Spec.Template.Labels = selectorLabels(appDef.Name)
		deployment.Spec.Template.Annotations = podTemplateAnnotations(appDef)

		// Surgical pod spec update: only touch fields the operator owns.
		// Preserving the existing PodSpec keeps Kubernetes-injected defaults
		// (DNSPolicy, RestartPolicy, etc.) in place across reconciles so that
		// equality.Semantic.DeepEqual does not see a spurious diff.
		podSpec := &deployment.Spec.Template.Spec
		podSpec.SecurityContext = appDef.Spec.SecurityContext
		podSpec.NodeSelector = appDef.Spec.NodeSelector
		podSpec.Tolerations = appDef.Spec.Tolerations
		podSpec.Affinity = appDef.Spec.Affinity
		podSpec.ImagePullSecrets = appDef.Spec.ImagePullSecrets
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
	// hash.Hash.Write (via io.Writer) never returns an error; writes are safe to ignore.
	write := func(s string) { _, _ = io.WriteString(h, s) }

	cms := make([]v1.ConfigMapMount, len(appDef.Spec.ConfigMaps))
	copy(cms, appDef.Spec.ConfigMaps)
	sort.Slice(cms, func(i, j int) bool { return cms[i].Name < cms[j].Name })
	for _, cm := range cms {
		keys := make([]string, 0, len(cm.Data))
		for k := range cm.Data {
			keys = append(keys, k)
		}
		sort.Strings(keys)
		write("cm:" + cm.Name + "\n")
		for _, k := range keys {
			write(k + "=" + cm.Data[k] + "\n")
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
		write("secret:" + sec.Name + "\n")
		for _, k := range keys {
			write(k + "=" + sec.Data[k] + "\n")
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
		mode := int32(0644)
		volumes = append(volumes, corev1.Volume{
			Name: "cm-" + cm.Name,
			VolumeSource: corev1.VolumeSource{
				ConfigMap: &corev1.ConfigMapVolumeSource{
					LocalObjectReference: corev1.LocalObjectReference{
						Name: appDef.Name + "-" + cm.Name,
					},
					DefaultMode: &mode,
				},
			},
		})
	}

	for _, sec := range appDef.Spec.Secrets {
		if sec.MountPath == "" {
			continue
		}
		mode := int32(0644)
		volumes = append(volumes, corev1.Volume{
			Name: "secret-" + sec.Name,
			VolumeSource: corev1.VolumeSource{
				Secret: &corev1.SecretVolumeSource{
					SecretName:  resolvedSecretName(appDef.Name, sec),
					DefaultMode: &mode,
				},
			},
		})
	}

	for _, es := range appDef.Spec.ExternalSecrets {
		if es.MountPath == "" {
			continue
		}
		mode := int32(0644)
		volumes = append(volumes, corev1.Volume{
			Name: "es-" + es.Name,
			VolumeSource: corev1.VolumeSource{
				Secret: &corev1.SecretVolumeSource{
					SecretName:  appDef.Name + "-" + es.Name,
					DefaultMode: &mode,
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
	for _, es := range appDef.Spec.ExternalSecrets {
		if es.MountPath == "" {
			continue
		}
		mounts = append(mounts, corev1.VolumeMount{
			Name:      "es-" + es.Name,
			MountPath: es.MountPath,
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
		// Explicitly match Kubernetes admission defaults so DeepEqual is stable
		// across reconciles (avoids a storm where K8s re-defaults on every update).
		ImagePullPolicy:          corev1.PullIfNotPresent,
		TerminationMessagePath:   "/dev/termination-log",
		TerminationMessagePolicy: corev1.TerminationMessageReadFile,
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
	for _, es := range appDef.Spec.ExternalSecrets {
		if !es.AsEnvVars {
			continue
		}
		container.EnvFrom = append(container.EnvFrom, corev1.EnvFromSource{
			SecretRef: &corev1.SecretEnvSource{
				LocalObjectReference: corev1.LocalObjectReference{
					Name: appDef.Name + "-" + es.Name,
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
			Name:                     c.Name,
			Image:                    c.Image,
			Command:                  c.Command,
			Args:                     c.Args,
			Env:                      c.Env,
			ImagePullPolicy:          corev1.PullIfNotPresent,
			TerminationMessagePath:   "/dev/termination-log",
			TerminationMessagePolicy: corev1.TerminationMessageReadFile,
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
		for _, es := range appDef.Spec.ExternalSecrets {
			if !es.AsEnvVars {
				continue
			}
			container.EnvFrom = append(container.EnvFrom, corev1.EnvFromSource{
				SecretRef: &corev1.SecretEnvSource{
					LocalObjectReference: corev1.LocalObjectReference{
						Name: appDef.Name + "-" + es.Name,
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
	probe := &corev1.Probe{
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
	// Apply the same defaults Kubernetes admission would set so that
	// DeepEqual is stable when the user omits these fields.
	if probe.PeriodSeconds == 0 {
		probe.PeriodSeconds = 10
	}
	if probe.TimeoutSeconds == 0 {
		probe.TimeoutSeconds = 1
	}
	if probe.SuccessThreshold == 0 {
		probe.SuccessThreshold = 1
	}
	if probe.FailureThreshold == 0 {
		probe.FailureThreshold = 3
	}
	if probe.ProbeHandler.HTTPGet != nil && probe.ProbeHandler.HTTPGet.Scheme == "" {
		probe.ProbeHandler.HTTPGet.Scheme = corev1.URISchemeHTTP
	}
	return probe
}
