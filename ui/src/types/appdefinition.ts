export interface MetricsSpec {
  enabled: boolean
  path?: string
  interval?: string
  labels?: Record<string, string>
}

export interface PortSpec {
  name: string
  containerPort: number
  servicePort: number
  protocol?: string
  expose?: boolean
  metrics?: MetricsSpec
}

export interface Probe {
  type?: string
  httpGet?: { path: string; port: number | string; scheme?: string }
  tcpSocket?: { port: number | string }
  exec?: { command: string[] }
  initialDelaySeconds?: number
  periodSeconds?: number
  timeoutSeconds?: number
  failureThreshold?: number
  successThreshold?: number
}

export interface ContainerSpec {
  name: string
  image: string
  command?: string[]
  args?: string[]
  env?: { name: string; value?: string; valueFrom?: unknown }[]
  ports?: PortSpec[]
  readinessProbe?: Probe
  livenessProbe?: Probe
  resources?: {
    requests?: Record<string, string>
    limits?: Record<string, string>
  }
}

export interface DomainSpec {
  name: string
  tls: boolean
  redirect_tls?: boolean
  certIssuer?: string
  path?: string
  portName?: string
  secretName?: string
  annotations?: Record<string, string>
}

export interface DiskPartition {
  subPath: string
  mountPath: string
}

export interface DiskConfig {
  sizeInGi: number
  storageClassName?: string
  setFsGroup?: boolean
  protect?: boolean
  partitions?: DiskPartition[]
  annotations?: Record<string, string>
}

export interface AutoscalingSpec {
  enabled: boolean
  minReplicas?: number
  maxReplicas: number
  targetCPUUtilizationPercentage?: number
  targetMemoryUtilizationPercentage?: number
}

export interface ConfigMapMount {
  name: string
  mountPath: string
  data: Record<string, string>
}

export interface SecretMount {
  name: string
  mountPath?: string
  asEnvVars?: boolean
  data?: Record<string, string>
  secretRef?: string
}

export interface ExternalSecretRemoteRef {
  key: string
  property?: string
  version?: string
}

export interface ExternalSecretData {
  secretKey: string
  remoteRef: ExternalSecretRemoteRef
}

export interface ExternalSecretDataFrom {
  key: string
  version?: string
}

export interface ExternalSecretMount {
  name: string
  store: string
  storeKind?: string
  refreshInterval?: string
  mountPath?: string
  asEnvVars?: boolean
  data?: ExternalSecretData[]
  dataFrom?: ExternalSecretDataFrom[]
}

export interface LoggingStream {
  enabled: boolean
  format: string
}

export interface LoggingFile {
  path: string
  format: string
  multilinePattern?: string
}

export interface LoggingConfig {
  stdout?: LoggingStream
  stderr?: LoggingStream
  files?: LoggingFile[]
}

export interface LifecycleHook {
  exec?: { command: string[] }
}

export interface LifecycleSpec {
  postStart?: LifecycleHook
  preStop?: LifecycleHook
}

export interface AppDefinitionSpec {
  containers: ContainerSpec[]
  initContainers?: ContainerSpec[]
  replicas?: number
  domains?: DomainSpec[]
  disk?: DiskConfig
  loggingConfig?: LoggingConfig
  lifecycle?: LifecycleSpec
  securityContext?: Record<string, unknown>
  serviceType?: string
  ingressClass?: string
  ingressAnnotations?: Record<string, string>
  nodeSelector?: Record<string, string>
  tolerations?: unknown[]
  affinity?: unknown
  paused?: boolean
  autoscaling?: AutoscalingSpec
  imagePullSecrets?: { name: string }[]
  configMaps?: ConfigMapMount[]
  secrets?: SecretMount[]
  externalSecrets?: ExternalSecretMount[]
}

export interface Condition {
  type: string
  status: string
  reason?: string
  message?: string
  lastTransitionTime?: string
}

export interface AppDefinitionStatus {
  phase?: string
  readyReplicas?: number
  replicas?: number
  observedGeneration?: number
  conditions?: Condition[]
  lastError?: string
}

export interface AppDefinition {
  apiVersion?: string
  kind?: string
  metadata: {
    name: string
    namespace: string
    uid?: string
    resourceVersion?: string
    creationTimestamp?: string
    labels?: Record<string, string>
    annotations?: Record<string, string>
    finalizers?: string[]
  }
  spec: AppDefinitionSpec
  status?: AppDefinitionStatus
}

export interface AppDefinitionList {
  items: AppDefinition[]
  metadata?: { continue?: string }
}
