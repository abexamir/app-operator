package v1

import (
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

// PortSpec declares a container port and how it is exposed to the cluster.
type PortSpec struct {
	// Name is the port identifier used in the Service, Ingress, and ServiceMonitor.
	// Must be unique across all containers in the AppDefinition.
	Name string `json:"name"`
	// ContainerPort is the port the process listens on inside the container.
	ContainerPort int32 `json:"containerPort"`
	// ServicePort is the port number published on the Kubernetes Service.
	ServicePort int32 `json:"servicePort"`
	// Protocol is the network protocol. Defaults to TCP.
	// +kubebuilder:default=TCP
	// +kubebuilder:validation:Enum=TCP;UDP
	Protocol string `json:"protocol,omitempty"`
	// Expose adds this port to the Service when true. Ports with metrics only
	// (expose: false) can still be scraped via the Service's named port.
	Expose bool `json:"expose,omitempty"`
	// Metrics configures Prometheus scraping for this port via a ServiceMonitor.
	Metrics *MetricsSpec `json:"metrics,omitempty"`
}

// Probe configures a container health check. Set exactly one of httpGet, tcpSocket, or exec
// to match the type field.
type Probe struct {
	// Type selects the probe mechanism: httpGet, tcpSocket, or exec.
	// +kubebuilder:validation:Enum=httpGet;tcpSocket;exec
	Type string `json:"type"`
	// HTTPGet probes an HTTP endpoint. Use when type is httpGet.
	HTTPGet *corev1.HTTPGetAction `json:"httpGet,omitempty"`
	// TCPSocket probes a TCP port. Use when type is tcpSocket.
	TCPSocket *corev1.TCPSocketAction `json:"tcpSocket,omitempty"`
	// Exec runs a command inside the container. Use when type is exec.
	Exec *corev1.ExecAction `json:"exec,omitempty"`
	// InitialDelaySeconds waits before the first probe after container start.
	InitialDelaySeconds int32 `json:"initialDelaySeconds,omitempty"`
	// PeriodSeconds is the interval between probe attempts.
	PeriodSeconds int32 `json:"periodSeconds,omitempty"`
	// TimeoutSeconds is the per-probe timeout.
	TimeoutSeconds int32 `json:"timeoutSeconds,omitempty"`
	// FailureThreshold is the number of consecutive failures before marking unhealthy.
	FailureThreshold int32 `json:"failureThreshold,omitempty"`
	// SuccessThreshold is the number of consecutive successes before marking healthy.
	SuccessThreshold int32 `json:"successThreshold,omitempty"`
}

// DiskPartition mounts a subdirectory of the app PVC into a container path.
type DiskPartition struct {
	// SubPath is the directory within the PVC volume to mount.
	SubPath string `json:"subPath"`
	// MountPath is the absolute path inside every container where SubPath is mounted.
	MountPath string `json:"mountPath"`
}

// DiskConfig provisions a ReadWriteOnce PVC named "<app>-disk" and mounts it into
// every container. Apps with disk are stateful: limited to one replica, Recreate
// deployment strategy, and no autoscaling.
type DiskConfig struct {
	// SizeInGi is the requested PVC capacity in gibibytes.
	// +kubebuilder:validation:Minimum=1
	SizeInGi int `json:"sizeInGi"`
	// StorageClassName is the StorageClass used to provision the PVC.
	StorageClassName string `json:"storageClassName"`
	// SetFsGroup injects fsGroup into the pod securityContext for group write access
	// to the volume. Prefer spec.securityContext.fsGroup for an explicit GID.
	SetFsGroup bool `json:"setFsGroup,omitempty"`
	// Partitions mount subdirectories of the PVC at different paths. When empty,
	// the entire volume is mounted at /data in every container.
	Partitions []DiskPartition `json:"partitions,omitempty"`
	// Annotations are merged into PVC metadata on every reconcile. Provisioner-owned
	// annotations (e.g. pv.kubernetes.io/bind-completed) are never removed.
	Annotations map[string]string `json:"annotations,omitempty"`
	// Protect blocks PVC creation when the PVC is absent, preventing the operator
	// from racing during manual migrations or PV rebinds. Annotation updates and
	// size expansion still apply to an existing PVC.
	Protect bool `json:"protect,omitempty"`
}

// LoggingFile describes a log file path and parsing hints for future log shipping.
type LoggingFile struct {
	// Path is the absolute path to the log file inside the container.
	Path string `json:"path"`
	// Format is the log encoding: json or text.
	// +kubebuilder:validation:Enum=json;text
	Format string `json:"format"`
	// MultilinePattern is a regex matching the first line of each multiline log entry.
	MultilinePattern string `json:"multilinePattern,omitempty"`
}

// LoggingStream describes stdout or stderr collection intent.
type LoggingStream struct {
	// Enabled marks this stream for future log shipping integration.
	Enabled bool `json:"enabled"`
	// Format is the log encoding: json or text.
	// +kubebuilder:validation:Enum=json;text
	Format string `json:"format"`
}

// LoggingConfig declares log collection intent. Stored in the spec but not yet
// acted on by the operator — log shipping is handled at the cluster layer.
type LoggingConfig struct {
	// Stdout configures collection of the container's standard output stream.
	Stdout *LoggingStream `json:"stdout,omitempty"`
	// Stderr configures collection of the container's standard error stream.
	Stderr *LoggingStream `json:"stderr,omitempty"`
	// Files lists container log file paths and parsing hints.
	Files []LoggingFile `json:"files,omitempty"`
}

// MetricsSpec configures Prometheus scraping for a port. When enabled, the operator
// adds an endpoint to the app's ServiceMonitor. Silently skipped if
// prometheus-operator CRDs are not installed.
type MetricsSpec struct {
	// Enabled adds this port as a ServiceMonitor scrape endpoint.
	// +kubebuilder:default=false
	Enabled bool `json:"enabled"`
	// Path is the HTTP path Prometheus scrapes. Defaults to /metrics.
	// +kubebuilder:default="/metrics"
	Path string `json:"path,omitempty"`
	// Interval is the per-endpoint scrape interval (e.g. "15s", "30s").
	Interval string `json:"interval,omitempty"`
	// Labels are merged onto ServiceMonitor metadata (e.g. release: prometheus-stack
	// to match a Prometheus serviceMonitorSelector).
	Labels map[string]string `json:"labels,omitempty"`
}

// ContainerSpec defines a main application container in the pod.
type ContainerSpec struct {
	// Name is the container name within the pod. Must be unique among all containers.
	Name string `json:"name"`
	// Image is the OCI image reference (registry/repo:tag or digest).
	Image string `json:"image"`
	// Command overrides the image ENTRYPOINT.
	Command []string `json:"command,omitempty"`
	// Args overrides the image CMD.
	Args []string `json:"args,omitempty"`
	// Env sets container environment variables.
	Env []corev1.EnvVar `json:"env,omitempty"`
	// Ports declares container ports, Service exposure, and metrics scraping.
	Ports []PortSpec `json:"ports,omitempty"`
	// ReadinessProbe gates Service endpoints and load balancer traffic.
	ReadinessProbe *Probe `json:"readinessProbe,omitempty"`
	// LivenessProbe restarts the container when it fails.
	LivenessProbe *Probe `json:"livenessProbe,omitempty"`
	// Resources sets CPU and memory requests and limits.
	Resources corev1.ResourceRequirements `json:"resources,omitempty"`
}

// InitContainerSpec defines a container that runs to completion before main containers start.
// Init containers share volumes, ConfigMaps, Secrets, and disk mounts with main containers
// but do not expose ports or receive lifecycle hooks.
type InitContainerSpec struct {
	// Name is the init container name. Must be unique among all containers.
	Name string `json:"name"`
	// Image is the OCI image reference.
	Image string `json:"image"`
	// Command overrides the image ENTRYPOINT.
	Command []string `json:"command,omitempty"`
	// Args overrides the image CMD.
	Args []string `json:"args,omitempty"`
	// Env sets init container environment variables.
	Env []corev1.EnvVar `json:"env,omitempty"`
	// Resources sets CPU and memory requests and limits.
	Resources corev1.ResourceRequirements `json:"resources,omitempty"`
}

// AutoscalingSpec configures a HorizontalPodAutoscaler targeting the app's Deployment.
// Cannot be combined with disk (stateful apps are fixed at one replica).
type AutoscalingSpec struct {
	// Enabled creates or updates an HPA when true. Deletes the HPA when false.
	Enabled bool `json:"enabled"`
	// MinReplicas is the HPA floor. Defaults to spec.replicas or 1 when unset.
	MinReplicas *int32 `json:"minReplicas,omitempty"`
	// MaxReplicas is the HPA ceiling.
	// +kubebuilder:validation:Minimum=1
	MaxReplicas int32 `json:"maxReplicas"`
	// TargetCPUUtilizationPercentage scales on average CPU utilization across pods.
	TargetCPUUtilizationPercentage *int32 `json:"targetCPUUtilizationPercentage,omitempty"`
	// TargetMemoryUtilizationPercentage scales on average memory utilization across pods.
	TargetMemoryUtilizationPercentage *int32 `json:"targetMemoryUtilizationPercentage,omitempty"`
}

// ExternalSecretRemoteRef points to a key in an external secret store.
type ExternalSecretRemoteRef struct {
	// Key is the secret path in the store (e.g. "secret/data/myapp/db").
	Key string `json:"key"`
	// Property is an optional sub-key within a KV secret (e.g. "password").
	Property string `json:"property,omitempty"`
	// Version pins a specific secret version. Omit for latest.
	Version string `json:"version,omitempty"`
}

// ExternalSecretData maps one remote key to one key in the resulting Kubernetes Secret.
type ExternalSecretData struct {
	// SecretKey is the key written in the Kubernetes Secret.
	SecretKey string `json:"secretKey"`
	// RemoteRef locates the value in the external store.
	RemoteRef ExternalSecretRemoteRef `json:"remoteRef"`
}

// ExternalSecretDataFrom bulk-imports all keys from one store path into the Secret.
type ExternalSecretDataFrom struct {
	// Key is the store path whose contents are extracted into the Secret.
	Key string `json:"key"`
	// Version pins a specific secret version. Omit for latest.
	Version string `json:"version,omitempty"`
}

// ExternalSecretMount creates an ExternalSecret (external-secrets.io/v1) that ESO
// syncs into a Kubernetes Secret named "<app>-<name>". Values never appear in the
// AppDefinition manifest. Silently skipped if ESO CRDs are not installed.
//
// +kubebuilder:validation:XValidation:rule="size(self.data) > 0 || size(self.dataFrom) > 0",message="at least one of data or dataFrom must be set"
type ExternalSecretMount struct {
	// Name identifies this entry. The ExternalSecret and Secret are named "<app>-<name>".
	Name string `json:"name"`
	// Store is the SecretStore or ClusterSecretStore name to read from.
	Store string `json:"store"`
	// StoreKind is SecretStore or ClusterSecretStore. Defaults to ClusterSecretStore.
	// +kubebuilder:default=ClusterSecretStore
	// +kubebuilder:validation:Enum=SecretStore;ClusterSecretStore
	StoreKind string `json:"storeKind,omitempty"`
	// RefreshInterval is how often ESO re-reads the store. Defaults to 1h.
	// +kubebuilder:default="1h"
	RefreshInterval string `json:"refreshInterval,omitempty"`
	// MountPath mounts the synced Secret as files in all containers.
	MountPath string `json:"mountPath,omitempty"`
	// AsEnvVars injects all synced Secret keys as environment variables in all containers.
	AsEnvVars bool `json:"asEnvVars,omitempty"`
	// Data maps individual remote keys to Secret keys.
	Data []ExternalSecretData `json:"data,omitempty"`
	// DataFrom bulk-imports all keys from remote paths.
	DataFrom []ExternalSecretDataFrom `json:"dataFrom,omitempty"`
}

// ConfigMapMount embeds a ConfigMap in the AppDefinition. The operator creates and
// owns "<app>-<name>" and mounts it at MountPath in every container.
type ConfigMapMount struct {
	// Name identifies this entry. The ConfigMap is named "<app>-<name>".
	Name string `json:"name"`
	// MountPath is the directory where ConfigMap keys appear as files in all containers.
	MountPath string `json:"mountPath"`
	// Data is the ConfigMap key-value content.
	Data map[string]string `json:"data"`
}

// SecretMount defines an inline or pre-existing Secret for the application.
// Data and SecretRef are mutually exclusive. MountPath and AsEnvVars may both be set.
//
// +kubebuilder:validation:XValidation:rule="!(has(self.secretRef) && size(self.secretRef) > 0 && has(self.data))",message="secretRef and data are mutually exclusive"
type SecretMount struct {
	// Name identifies this entry. Inline secrets are named "<app>-<name>".
	Name string `json:"name"`
	// MountPath mounts the Secret as files in all containers.
	MountPath string `json:"mountPath,omitempty"`
	// AsEnvVars injects all Secret keys as environment variables in all containers.
	AsEnvVars bool `json:"asEnvVars,omitempty"`
	// Data embeds secret values. The operator creates and owns the Secret.
	// Mutually exclusive with SecretRef.
	Data map[string]string `json:"data,omitempty"`
	// SecretRef references a pre-existing Secret in the same namespace.
	// The operator mounts or injects it but does not manage its lifecycle.
	// Mutually exclusive with Data.
	SecretRef string `json:"secretRef,omitempty"`
}

// DomainSpec declares an Ingress host rule and optional TLS.
type DomainSpec struct {
	// Name is the DNS hostname (e.g. app.example.com).
	Name string `json:"name"`
	// TLS enables HTTPS for this host using a TLS secret.
	TLS bool `json:"tls"`
	// RedirectTLS redirects HTTP to HTTPS when true and TLS is enabled.
	RedirectTLS bool `json:"redirect_tls,omitempty"`
	// CertIssuer is the cert-manager ClusterIssuer or Issuer name for automatic TLS.
	CertIssuer string `json:"certIssuer,omitempty"`
	// Path is the URL path prefix routed to the backend. Defaults to /.
	Path string `json:"path,omitempty"`
	// Annotations are merged into the Ingress rule metadata.
	Annotations map[string]string `json:"annotations,omitempty"`
	// PortName is the Service port name to route to. Defaults to http.
	PortName string `json:"portName,omitempty"`
	// SecretName is the TLS secret for this host. Defaults to <app>-<domain>-tls.
	SecretName string `json:"secretName,omitempty"`
}

// LifecycleHook runs an exec action inside the container.
type LifecycleHook struct {
	// Exec runs a command in the container filesystem.
	Exec *corev1.ExecAction `json:"exec,omitempty"`
}

// LifecycleSpec configures postStart and preStop hooks. Applied to the first
// container only — sidecars often lack a shell and exec hooks can crash them.
type LifecycleSpec struct {
	// PostStart runs immediately after the first container starts.
	PostStart *LifecycleHook `json:"postStart,omitempty"`
	// PreStop runs before SIGTERM, useful for connection draining.
	PreStop *LifecycleHook `json:"preStop,omitempty"`
}

// AppDefinitionSpec declares the desired state of an application.
//
// +kubebuilder:validation:XValidation:rule="!has(self.disk) || !has(self.replicas) || self.replicas <= 1",message="stateful apps with persistent disk cannot have more than 1 replica; multiple instances would corrupt the shared TSDB/volume"
// +kubebuilder:validation:XValidation:rule="!has(self.autoscaling) || !self.autoscaling.enabled || !has(self.disk)",message="autoscaling cannot be enabled on stateful apps with persistent disk"
type AppDefinitionSpec struct {
	// Containers are the main application containers. At least one is required.
	// +kubebuilder:validation:MinItems=1
	Containers []ContainerSpec `json:"containers"`
	// InitContainers run to completion before main containers start.
	InitContainers []InitContainerSpec `json:"initContainers,omitempty"`
	// Replicas is the desired pod count. Defaults to 1. Capped at 1 when disk is set.
	Replicas *int32 `json:"replicas,omitempty"`
	// Domains creates an Ingress with one rule per entry.
	Domains []DomainSpec `json:"domains,omitempty"`
	// Disk provisions a persistent volume. Makes the app stateful (Recreate strategy,
	// max one replica, no autoscaling).
	Disk *DiskConfig `json:"disk,omitempty"`
	// LoggingConfig declares log shipping intent (not yet enforced by the operator).
	LoggingConfig *LoggingConfig `json:"loggingConfig,omitempty"`
	// Lifecycle sets postStart/preStop hooks on the first container.
	Lifecycle *LifecycleSpec `json:"lifecycle,omitempty"`
	// SecurityContext is the pod-level security context applied to every container.
	SecurityContext *corev1.PodSecurityContext `json:"securityContext,omitempty"`
	// ServiceType is the Kubernetes Service type. Defaults to ClusterIP.
	ServiceType corev1.ServiceType `json:"serviceType,omitempty"`
	// IngressClass is the IngressClass name for all domain rules.
	IngressClass string `json:"ingressClass,omitempty"`
	// IngressAnnotations are merged into the Ingress metadata.
	IngressAnnotations map[string]string `json:"ingressAnnotations,omitempty"`
	// NodeSelector constrains pods to nodes with matching labels.
	NodeSelector map[string]string `json:"nodeSelector,omitempty"`
	// Tolerations allow scheduling onto tainted nodes.
	Tolerations []corev1.Toleration `json:"tolerations,omitempty"`
	// Affinity sets pod affinity, anti-affinity, and node affinity rules.
	Affinity *corev1.Affinity `json:"affinity,omitempty"`
	// Paused suspends reconciliation. Existing resources are left unchanged.
	Paused bool `json:"paused,omitempty"`
	// Autoscaling configures an HPA for the Deployment.
	Autoscaling *AutoscalingSpec `json:"autoscaling,omitempty"`
	// ImagePullSecrets references Secrets used to pull private images.
	ImagePullSecrets []corev1.LocalObjectReference `json:"imagePullSecrets,omitempty"`
	// ConfigMaps creates operator-owned ConfigMaps mounted in all containers.
	ConfigMaps []ConfigMapMount `json:"configMaps,omitempty"`
	// Secrets creates inline or references pre-existing Secrets.
	Secrets []SecretMount `json:"secrets,omitempty"`
	// ExternalSecrets creates ExternalSecret resources synced by ESO.
	ExternalSecrets []ExternalSecretMount `json:"externalSecrets,omitempty"`
}

const (
	ConditionTypeReady        = "Ready"
	ConditionTypeProgressing  = "Progressing"
	ConditionTypeDiskReady    = "DiskReady"
	ConditionTypeIngressReady = "IngressReady"
	ConditionTypeHPAActive    = "HPAActive"
)

// AppDefinitionStatus reports the observed application state. Set by the operator.
type AppDefinitionStatus struct {
	// Phase summarizes readiness: Available, Progressing, Failed, or Paused.
	Phase string `json:"phase,omitempty"`
	// ReadyReplicas is pods with a Ready condition.
	ReadyReplicas int32 `json:"readyReplicas,omitempty"`
	// Replicas is the desired replica count from spec.
	Replicas int32 `json:"replicas,omitempty"`
	// ObservedGeneration is the last spec generation reconciled.
	ObservedGeneration int64 `json:"observedGeneration,omitempty"`
	// Conditions report Ready, DiskReady, IngressReady, and HPAActive status.
	// +patchMergeKey=type
	// +patchStrategy=merge
	// +listType=map
	// +listMapKey=type
	Conditions []metav1.Condition `json:"conditions,omitempty"`
	// LastError is the most recent reconciliation error message.
	LastError string `json:"lastError,omitempty"`
}

// AppDefinition deploys a complete application from one resource: Deployment, Service,
// and optional Ingress, PVC, HPA, ConfigMaps, Secrets, ExternalSecrets, and ServiceMonitor.
// Child resources are owned and garbage-collected on deletion.
//
// +kubebuilder:object:root=true
// +kubebuilder:subresource:status
// +kubebuilder:printcolumn:name="Phase",type="string",JSONPath=".status.phase"
// +kubebuilder:printcolumn:name="Ready",type="integer",JSONPath=".status.readyReplicas"
// +kubebuilder:printcolumn:name="Replicas",type="integer",JSONPath=".status.replicas"
// +kubebuilder:printcolumn:name="Age",type="date",JSONPath=".metadata.creationTimestamp"
type AppDefinition struct {
	metav1.TypeMeta   `json:",inline"`
	metav1.ObjectMeta `json:"metadata,omitempty"`

	// Spec defines the desired application configuration.
	Spec AppDefinitionSpec `json:"spec,omitempty"`
	// Status reports the observed application state.
	Status AppDefinitionStatus `json:"status,omitempty"`
}

// AppDefinitionList is a list of AppDefinition resources.
//
// +kubebuilder:object:root=true
type AppDefinitionList struct {
	metav1.TypeMeta `json:",inline"`
	metav1.ListMeta `json:"metadata,omitempty"`
	Items           []AppDefinition `json:"items"`
}

func init() {
	SchemeBuilder.Register(&AppDefinition{}, &AppDefinitionList{})
}
