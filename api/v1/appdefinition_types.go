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
// Source (multi-container docker image)
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

type DockerImageSource struct {
	Containers []ContainerSpec `json:"containers"`
}

type SourceSpec struct {
	// +kubebuilder:validation:Enum=dockerImage
	Type        string             `json:"type"` // Only dockerImage for now
	DockerImage *DockerImageSource `json:"dockerImage,omitempty"`
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
// Volume mounts
// ---------------------------------------

// ConfigMapMount mounts a ConfigMap as a directory into all containers.
type ConfigMapMount struct {
	Name      string `json:"name"`
	MountPath string `json:"mountPath"`
	Optional  bool   `json:"optional,omitempty"`
}

// SecretMount mounts a Secret as a directory into all containers.
type SecretMount struct {
	Name      string `json:"name"`
	MountPath string `json:"mountPath"`
	Optional  bool   `json:"optional,omitempty"`
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

type AppDefinitionSpec struct {
	Source             SourceSpec                 `json:"source"`
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
	// ConfigMaps to mount as directories into all containers.
	ConfigMaps []ConfigMapMount `json:"configMaps,omitempty"`
	// Secrets to mount as directories into all containers.
	Secrets []SecretMount `json:"secrets,omitempty"`
}

// ---------------------------------------
// Status
// ---------------------------------------

const (
	ConditionTypeReady       = "Ready"
	ConditionTypeProgressing = "Progressing"
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
