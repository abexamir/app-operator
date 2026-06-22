package v1

import (
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

// ---------------------------------------
// Port, Probes, Disk, Logging, Monitoring
// ---------------------------------------

type PortSpec struct {
	Name          string `json:"name"`
	ContainerPort int32  `json:"containerPort"`
	ServicePort   int32  `json:"servicePort"`
	Protocol      string `json:"protocol,omitempty"`
	Expose        bool   `json:"expose,omitempty"`
	MetricsPath   string `json:"metricsPath,omitempty"`
}

type Probe struct {
	Type                string                  `json:"type"` // httpGet, tcpSocket, exec
	HTTPGet             *corev1.HTTPGetAction   `json:"httpGet,omitempty"`
	TCPSocket           *corev1.TCPSocketAction `json:"tcpSocket,omitempty"`
	Exec                *corev1.ExecAction      `json:"exec,omitempty"`
	InitialDelaySeconds int32                   `json:"initialDelaySeconds,omitempty"`
	PeriodSeconds       int32                   `json:"periodSeconds,omitempty"`
	TimeoutSeconds      int32                   `json:"timeoutSeconds,omitempty"`
	FailureThreshold    int32                   `json:"failureThreshold,omitempty"`
	SuccessThreshold    int32                   `json:"successThreshold,omitempty"`
}

type DiskPartition struct {
	SubPath   string `json:"subPath"`
	MountPath string `json:"mountPath"`
}

type DiskConfig struct {
	SizeInGi         int             `json:"sizeInGi"`
	StorageClassName string          `json:"storageClassName"`
	SetFsGroup       bool            `json:"setFsGroup,omitempty"`
	Partitions       []DiskPartition `json:"partitions,omitempty"`
	// Annotations are added verbatim to the PVC metadata. Useful for storage-class-specific
	// hints, backup policies, or custom tooling that selects PVCs by annotation.
	Annotations map[string]string `json:"annotations,omitempty"`
	// Protect skips PVC creation and modification when true. Use during PVC migrations
	// or manual PV rebinding to prevent the operator from racing to recreate a deleted PVC.
	// The rest of the AppDefinition (Deployment, Ingress, etc.) continues reconciling normally.
	Protect bool `json:"protect,omitempty"`
}

type LoggingFile struct {
	Path             string `json:"path"`
	Format           string `json:"format"` // json or text
	MultilinePattern string `json:"multilinePattern,omitempty"`
}

type LoggingStream struct {
	Enabled bool   `json:"enabled"`
	Format  string `json:"format"` // json or text
}

// LoggingConfig declares intent for log collection. It is parsed and stored but
// the operator does not act on it yet — log shipping is handled at the cluster
// infrastructure layer, outside the operator's reconcile loop.
//
// # Implementation paths to consider
//
// Option A — Loki + Promtail (simplest, already Prometheus-compatible)
//
//	Promtail runs as a DaemonSet and tails /var/log/pods/ on each node.
//	It auto-discovers all pods via the Kubernetes API; no CRD is needed.
//	Opt-in scraping can be enforced by having the operator inject a pod
//	annotation (e.g. promtail.io/scrape: "true") only when loggingConfig
//	is present, so Promtail's relabel rules can filter on it.
//	Logs land in Loki and are queryable with LogQL alongside Prometheus metrics.
//
// Option B — OpenTelemetry Operator + PodLogs CRD (closest to ServiceMonitor)
//
//	The OTel Operator introduces a PodLogs CRD. The operator can create a
//	PodLogs object (via unstructured, same pattern as ServiceMonitor) that
//	targets pods by label selector. The OTel Collector pipeline ships logs
//	to any backend (Loki, Elasticsearch, S3, etc.).
//	Gives explicit, per-app opt-in control — identical model to ServiceMonitor.
//
// Option C — Fluent Bit / Vector DaemonSet with annotation-based filtering
//
//	Similar to Option A but using Fluent Bit (lightweight C) or Vector
//	(Rust, rich transform DSL). The operator injects annotations that the
//	DaemonSet's config uses to include/exclude pods and apply parsing rules.
//	Good if the team already operates one of these shippers.
//
// The loggingConfig fields (stdout/stderr format, file paths, multiline patterns)
// map naturally onto Promtail pipeline_stages or OTel transform processors and
// can be translated by the operator once an implementation path is chosen.
type LoggingConfig struct {
	Stdout *LoggingStream `json:"stdout,omitempty"`
	Stderr *LoggingStream `json:"stderr,omitempty"`
	Files  []LoggingFile  `json:"files,omitempty"`
}

type MonitoringConfig struct {
	Enabled        bool              `json:"enabled"`
	ScrapeInterval string            `json:"scrapeInterval,omitempty"`
	Labels         map[string]string `json:"labels,omitempty"`
}

// ---------------------------------------
// Containers
// ---------------------------------------

type ContainerSpec struct {
	Name           string                      `json:"name"`
	Image          string                      `json:"image"`
	Command        []string                    `json:"command,omitempty"`
	Args           []string                    `json:"args,omitempty"`
	Env            []corev1.EnvVar             `json:"env,omitempty"`
	Ports          []PortSpec                  `json:"ports,omitempty"`
	ReadinessProbe *Probe                      `json:"readinessProbe,omitempty"`
	LivenessProbe  *Probe                      `json:"livenessProbe,omitempty"`
	Resources      corev1.ResourceRequirements `json:"resources,omitempty"`
}

// InitContainerSpec defines a container that runs to completion before any main
// containers start. Init containers share the same volumes, ConfigMaps, Secrets,
// and disk mounts as the main containers but do not expose ports and do not
// receive lifecycle hooks.
type InitContainerSpec struct {
	Name      string                      `json:"name"`
	Image     string                      `json:"image"`
	Command   []string                    `json:"command,omitempty"`
	Args      []string                    `json:"args,omitempty"`
	Env       []corev1.EnvVar             `json:"env,omitempty"`
	Resources corev1.ResourceRequirements `json:"resources,omitempty"`
}

// ---------------------------------------
// Autoscaling
// ---------------------------------------

// AutoscalingSpec configures a HorizontalPodAutoscaler for the AppDefinition.
type AutoscalingSpec struct {
	Enabled                           bool   `json:"enabled"`
	MinReplicas                       *int32 `json:"minReplicas,omitempty"`
	MaxReplicas                       int32  `json:"maxReplicas"`
	TargetCPUUtilizationPercentage    *int32 `json:"targetCPUUtilizationPercentage,omitempty"`
	TargetMemoryUtilizationPercentage *int32 `json:"targetMemoryUtilizationPercentage,omitempty"`
}

// ---------------------------------------
// Inline ConfigMaps and Secrets
// ---------------------------------------

// ExternalSecretRemoteRef points to a specific key inside an external secret store.
type ExternalSecretRemoteRef struct {
	// Key is the path or name of the secret in the store (e.g. "secret/data/myapp/db").
	Key string `json:"key"`
	// Property is an optional sub-key within the secret (e.g. "password" for a KV map).
	Property string `json:"property,omitempty"`
	// Version pins to a specific version of the secret. Omit for the latest.
	Version string `json:"version,omitempty"`
}

// ExternalSecretData maps one remote key to one key in the resulting Kubernetes Secret.
type ExternalSecretData struct {
	// SecretKey is the key that will appear in the resulting Kubernetes Secret.
	SecretKey string `json:"secretKey"`
	// RemoteRef points to the value in the external store.
	RemoteRef ExternalSecretRemoteRef `json:"remoteRef"`
}

// ExternalSecretDataFrom bulk-imports all keys from a single path in the store.
type ExternalSecretDataFrom struct {
	// Key is the store path whose contents are extracted wholesale into the Secret.
	Key string `json:"key"`
	// Version pins to a specific version. Omit for the latest.
	Version string `json:"version,omitempty"`
}

// ExternalSecretMount creates an ExternalSecret (external-secrets.io/v1beta1) that ESO
// syncs into a Kubernetes Secret named "<app>-<name>". The resulting Secret is then
// mounted or injected exactly like an inline secret — but its values never appear in
// the AppDefinition manifest.
//
// At least one of Data or DataFrom must be set.
//
// +kubebuilder:validation:XValidation:rule="size(self.data) > 0 || size(self.dataFrom) > 0",message="at least one of data or dataFrom must be set"
type ExternalSecretMount struct {
	// Name identifies this entry. The ExternalSecret and resulting Secret are named "<app>-<name>".
	Name string `json:"name"`
	// Store is the name of the SecretStore or ClusterSecretStore to read from.
	Store string `json:"store"`
	// StoreKind is "SecretStore" or "ClusterSecretStore". Defaults to "ClusterSecretStore".
	// +kubebuilder:default=ClusterSecretStore
	// +kubebuilder:validation:Enum=SecretStore;ClusterSecretStore
	StoreKind string `json:"storeKind,omitempty"`
	// RefreshInterval controls how often ESO re-reads the external store. Defaults to "1h".
	// +kubebuilder:default="1h"
	RefreshInterval string `json:"refreshInterval,omitempty"`
	// MountPath mounts the resulting Secret as a directory of files into all containers.
	MountPath string `json:"mountPath,omitempty"`
	// AsEnvVars injects all keys of the resulting Secret as environment variables into all containers.
	AsEnvVars bool `json:"asEnvVars,omitempty"`
	// Data maps individual remote keys to keys in the resulting Secret.
	Data []ExternalSecretData `json:"data,omitempty"`
	// DataFrom bulk-imports all keys from a remote path into the resulting Secret.
	DataFrom []ExternalSecretDataFrom `json:"dataFrom,omitempty"`
}

// ConfigMapMount defines a ConfigMap whose content is embedded directly in the AppDefinition.
// The operator creates and owns a ConfigMap named "<appname>-<name>" and mounts it at MountPath
// in every container.
type ConfigMapMount struct {
	Name      string            `json:"name"`
	MountPath string            `json:"mountPath"`
	Data      map[string]string `json:"data"`
}

// SecretMount defines a Secret used by the application.
// Use Data to embed secret values directly (the operator creates and owns the Secret).
// Use SecretRef to reference a pre-existing Secret in the same namespace (operator does not manage it).
// Data and SecretRef are mutually exclusive.
// Set MountPath to mount the secret as a directory of files into all containers.
// Set AsEnvVars to inject every key as an environment variable into all containers.
// Both MountPath and AsEnvVars may be used at the same time.
//
// +kubebuilder:validation:XValidation:rule="!(has(self.secretRef) && size(self.secretRef) > 0 && has(self.data))",message="secretRef and data are mutually exclusive"
type SecretMount struct {
	Name string `json:"name"`
	// MountPath mounts the secret as a directory of files into all containers. Optional.
	MountPath string `json:"mountPath,omitempty"`
	// AsEnvVars injects all secret keys as environment variables into all containers.
	AsEnvVars bool `json:"asEnvVars,omitempty"`
	// Data defines inline secret values. The operator creates a Secret named "<app>-<name>".
	// Mutually exclusive with SecretRef.
	Data map[string]string `json:"data,omitempty"`
	// SecretRef names a pre-existing Secret in the same namespace. The operator mounts or
	// injects it but does not create, update, or delete it. Mutually exclusive with Data.
	SecretRef string `json:"secretRef,omitempty"`
}

// ---------------------------------------
// AppDefinition Spec
// ---------------------------------------

type DomainSpec struct {
	Name        string            `json:"name"`
	TLS         bool              `json:"tls"`
	RedirectTLS bool              `json:"redirect_tls,omitempty"`
	CertIssuer  string            `json:"certIssuer,omitempty"`
	Path        string            `json:"path,omitempty"`
	Annotations map[string]string `json:"annotations,omitempty"`
	// PortName is the service port name to route traffic to. Defaults to "http".
	PortName string `json:"portName,omitempty"`
	// SecretName is the TLS secret name. Auto-generated as <app>-<domain>-tls if not set.
	SecretName string `json:"secretName,omitempty"`
}

type LifecycleHook struct {
	Exec *corev1.ExecAction `json:"exec,omitempty"`
}

type LifecycleSpec struct {
	PostStart *LifecycleHook `json:"postStart,omitempty"`
	PreStop   *LifecycleHook `json:"preStop,omitempty"`
}

// +kubebuilder:validation:XValidation:rule="!has(self.disk) || !has(self.replicas) || self.replicas <= 1",message="stateful apps with persistent disk cannot have more than 1 replica; multiple instances would corrupt the shared TSDB/volume"
// +kubebuilder:validation:XValidation:rule="!has(self.autoscaling) || !self.autoscaling.enabled || !has(self.disk)",message="autoscaling cannot be enabled on stateful apps with persistent disk"
type AppDefinitionSpec struct {
	// Containers defines the application containers to run.
	Containers []ContainerSpec `json:"containers"`
	// InitContainers run to completion before the main containers start.
	// Useful for migrations, permission setup, or config rendering.
	InitContainers     []InitContainerSpec        `json:"initContainers,omitempty"`
	Replicas           *int32                     `json:"replicas,omitempty"`
	Domains            []DomainSpec               `json:"domains,omitempty"`
	Disk               *DiskConfig                `json:"disk,omitempty"`
	LoggingConfig      *LoggingConfig             `json:"loggingConfig,omitempty"`
	MonitoringConfig   *MonitoringConfig          `json:"monitoringConfig,omitempty"`
	Lifecycle          *LifecycleSpec             `json:"lifecycle,omitempty"`
	SecurityContext    *corev1.PodSecurityContext `json:"securityContext,omitempty"`
	ServiceType        corev1.ServiceType         `json:"serviceType,omitempty"`
	IngressClass       string                     `json:"ingressClass,omitempty"`
	IngressAnnotations map[string]string          `json:"ingressAnnotations,omitempty"`
	NodeSelector       map[string]string          `json:"nodeSelector,omitempty"`
	Tolerations        []corev1.Toleration        `json:"tolerations,omitempty"`
	Affinity           *corev1.Affinity           `json:"affinity,omitempty"`
	// Paused suspends reconciliation when true. Existing resources are left unchanged.
	Paused bool `json:"paused,omitempty"`
	// Autoscaling configures an HPA. When enabled, the replicas field sets minReplicas.
	Autoscaling *AutoscalingSpec `json:"autoscaling,omitempty"`
	// ImagePullSecrets are references to secrets in the same namespace for pulling container images.
	ImagePullSecrets []corev1.LocalObjectReference `json:"imagePullSecrets,omitempty"`
	// ConfigMaps defines inline ConfigMaps created and managed by the operator.
	// Each entry's data is mounted at MountPath in all containers.
	ConfigMaps []ConfigMapMount `json:"configMaps,omitempty"`
	// Secrets defines inline or externally-referenced Secrets.
	// Use MountPath to mount as files and/or AsEnvVars to inject as environment variables.
	Secrets []SecretMount `json:"secrets,omitempty"`
	// ExternalSecrets creates ExternalSecret resources (external-secrets.io/v1beta1) that ESO
	// syncs from a SecretStore or ClusterSecretStore into Kubernetes Secrets. The operator then
	// mounts or injects those Secrets identically to inline secrets — without storing any values
	// in the AppDefinition manifest. Silently skipped if the ESO CRDs are not installed.
	ExternalSecrets []ExternalSecretMount `json:"externalSecrets,omitempty"`
}

// ---------------------------------------
// Status
// ---------------------------------------

const (
	ConditionTypeReady        = "Ready"
	ConditionTypeProgressing  = "Progressing"
	ConditionTypeDiskReady    = "DiskReady"
	ConditionTypeIngressReady = "IngressReady"
	ConditionTypeHPAActive    = "HPAActive"
)

type AppDefinitionStatus struct {
	// Phase is a high-level summary of the AppDefinition state: Available, Progressing, Failed, or Paused.
	Phase string `json:"phase,omitempty"`
	// ReadyReplicas is the number of pods targeted by this AppDefinition with a Ready condition.
	ReadyReplicas int32 `json:"readyReplicas,omitempty"`
	// Replicas is the desired number of replicas.
	Replicas int32 `json:"replicas,omitempty"`
	// ObservedGeneration is the most recent generation observed.
	ObservedGeneration int64 `json:"observedGeneration,omitempty"`
	// Conditions represent the latest available observations of the AppDefinition's state.
	// +patchMergeKey=type
	// +patchStrategy=merge
	// +listType=map
	// +listMapKey=type
	Conditions []metav1.Condition `json:"conditions,omitempty"`
	// LastError holds the last reconciliation error message.
	LastError string `json:"lastError,omitempty"`
}

// +kubebuilder:object:root=true
// +kubebuilder:subresource:status
// +kubebuilder:printcolumn:name="Phase",type="string",JSONPath=".status.phase"
// +kubebuilder:printcolumn:name="Ready",type="integer",JSONPath=".status.readyReplicas"
// +kubebuilder:printcolumn:name="Replicas",type="integer",JSONPath=".status.replicas"
// +kubebuilder:printcolumn:name="Age",type="date",JSONPath=".metadata.creationTimestamp"
type AppDefinition struct {
	metav1.TypeMeta   `json:",inline"`
	metav1.ObjectMeta `json:"metadata,omitempty"`

	Spec   AppDefinitionSpec   `json:"spec,omitempty"`
	Status AppDefinitionStatus `json:"status,omitempty"`
}

// +kubebuilder:object:root=true
type AppDefinitionList struct {
	metav1.TypeMeta `json:",inline"`
	metav1.ListMeta `json:"metadata,omitempty"`
	Items           []AppDefinition `json:"items"`
}

func init() {
	SchemeBuilder.Register(&AppDefinition{}, &AppDefinitionList{})
}
