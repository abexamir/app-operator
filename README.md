# app-operator

Deploy containerized applications on Kubernetes with a single resource instead of managing Deployments, Services, Ingresses, PersistentVolumeClaims, HorizontalPodAutoscalers, and ServiceMonitors separately.

```yaml
apiVersion: appdefinition.abexamir.me/v1
kind: AppDefinition
metadata:
  name: my-app
spec:
  initContainers:
    - name: migrate
      image: my-app:latest
      command: ["./migrate", "--apply"]
  containers:
    - name: web
      image: nginx:1.25-alpine
      ports:
        - name: http
          containerPort: 80
          servicePort: 80
          expose: true
  replicas: 2
  domains:
    - name: my-app.example.com
      path: /
      tls: true
      certIssuer: letsencrypt-prod
  autoscaling:
    enabled: true
    minReplicas: 2
    maxReplicas: 10
    targetCPUUtilizationPercentage: 70
```

The operator reconciles this into a `Deployment`, `Service`, `Ingress`, and `HorizontalPodAutoscaler` — and keeps them in sync as you update the `AppDefinition`.

---

## Install

### One command

```sh
kubectl apply -f https://raw.githubusercontent.com/abexamir/app-operator/main/dist/install.yaml
```

This installs the CRD, RBAC, and the controller into the `appoperator-system` namespace. The controller image is pulled from `ghcr.io/abexamir/app-operator:latest`.

### Specific version

```sh
kubectl apply -f https://github.com/abexamir/app-operator/releases/download/v0.1.0/install.yaml
```

### Uninstall

```sh
kubectl delete -f https://raw.githubusercontent.com/abexamir/app-operator/main/dist/install.yaml
```

> **Note**: deleting the install manifest removes the controller and CRD. All `AppDefinition` resources and their child resources (Deployments, Services, PVCs, etc.) are deleted as part of CRD removal.

---

## What the operator manages

| AppDefinition field | Kubernetes resource |
|---|---|
| `containers` | `Deployment` |
| `initContainers` | Init containers on the `Deployment` (no separate resource) |
| `ports[].expose: true` | `Service` |
| `domains[]` | `Ingress` |
| `disk` | `PersistentVolumeClaim` (with `disk.annotations` merged into PVC metadata) |
| `autoscaling.enabled: true` | `HorizontalPodAutoscaler` |
| `configMaps[]` | `ConfigMap` per entry (operator-owned) |
| `secrets[]` with `data` | `Secret` per entry (operator-owned) |
| `secrets[]` with `secretRef` | No resource created — existing Secret is referenced |
| `monitoringConfig.enabled: true` | `ServiceMonitor` (requires prometheus-operator) |

All operator-owned child resources are garbage-collected when the `AppDefinition` is deleted.

---

## Status

```sh
kubectl get appdefinitions              # Phase, Ready, Replicas, Age
kubectl describe appdefinition my-app   # full Conditions and LastError
```

| Field | Values / Notes |
|---|---|
| `phase` | `Available` / `Progressing` / `Failed` / `Paused` |
| `readyReplicas` | Pods with a Ready condition |
| `replicas` | Desired replica count |
| `lastError` | Most recent reconciliation error |

### Conditions

| Type | Meaning |
|---|---|
| `Ready` | `True` when `readyReplicas >= replicas`. Message: `N/N replicas ready`. |
| `DiskReady` | Present when `disk` is set. `True` when PVC is Bound. Message: `bound (Xgi)` or `bound (Xgi, expanding to Ygi)` while a resize is in progress. |
| `IngressReady` | Present when `domains` is set. `True` when the ingress controller assigns an IP or hostname. |
| `HPAActive` | Present when `autoscaling.enabled: true`. `True` when the HPA exists. Message: `scaling N/M replicas (min X, max Y)`. |

---

## Spec Reference

### `containers` (required)

List of containers in the pod. The first container is the primary; subsequent ones are sidecars.

```yaml
containers:
  - name: web
    image: nginx:1.25-alpine
    command: ["nginx"]
    args: ["-g", "daemon off;"]
    env:
      - name: ENV
        value: production
    resources:
      requests:
        cpu: "100m"
        memory: "128Mi"
      limits:
        cpu: "500m"
        memory: "512Mi"
    ports:
      - name: http
        containerPort: 80
        servicePort: 80
        protocol: TCP
        expose: true          # adds this port to the Service
        metricsPath: /metrics # enables Prometheus scraping on this port
    readinessProbe:
      type: httpGet           # httpGet | tcpSocket | exec
      httpGet:
        path: /healthz
        port: 80
      initialDelaySeconds: 5
      periodSeconds: 10
      timeoutSeconds: 2
      failureThreshold: 3
      successThreshold: 1
    livenessProbe:
      type: exec
      exec:
        command: ["/bin/sh", "-c", "pgrep nginx"]
      initialDelaySeconds: 15
      periodSeconds: 20
```

### `initContainers`

Containers that run to completion before any main containers start. Useful for DB migrations, permission setup, and config rendering. Init containers share the same volumes, ConfigMaps, Secrets, and disk mounts as main containers but do not expose ports and do not receive lifecycle hooks.

```yaml
initContainers:
  - name: migrate
    image: my-app:latest
    command: ["./migrate", "--apply"]
    env:
      - name: DB_HOST
        value: postgres.default.svc
    resources:
      requests:
        cpu: "100m"
        memory: "128Mi"
```

### `replicas`

```yaml
replicas: 3   # defaults to 1
```

Ignored when `autoscaling.enabled: true` — the HPA controls the replica count.

Apps with `disk` are limited to `replicas: 1`. The PVC uses `ReadWriteOnce`; multiple pods sharing it simultaneously corrupt the data. This is enforced at the API level.

### `autoscaling`

Creates an `autoscaling/v2` HPA. When enabled, `replicas` becomes `minReplicas`.

```yaml
autoscaling:
  enabled: true
  minReplicas: 2
  maxReplicas: 10
  targetCPUUtilizationPercentage: 70
  targetMemoryUtilizationPercentage: 80
```

Disabling autoscaling removes the HPA if one exists. Autoscaling cannot be enabled on apps with `disk`.

### `domains`

Creates an `Ingress`. Each domain gets its own rule; TLS domains each get a TLS block.

```yaml
domains:
  - name: app.example.com
    path: /
    tls: true
    redirect_tls: true
    certIssuer: letsencrypt-prod      # sets cert-manager.io/cluster-issuer annotation
    portName: http                     # service port to route to (default: "http")
    secretName: my-tls-secret          # TLS secret name; auto-generated as <app>-<domain>-tls if omitted
    annotations:
      nginx.ingress.kubernetes.io/ssl-redirect: "true"
  - name: api.example.com
    path: /api
    portName: api
```

### `disk`

Creates a `ReadWriteOnce` PVC named `<app>-disk`. Mounted into every container.

```yaml
disk:
  sizeInGi: 20
  storageClassName: standard
  setFsGroup: true       # sets fsGroup in securityContext for group write access
  partitions:
    - mountPath: /data
      subPath: data
    - mountPath: /logs
      subPath: logs
  annotations:
    backup.velero.io/backup-volumes: "app-disk"
    snapshot.storage.kubernetes.io/allow-volume-snapshot: "true"
```

`disk.annotations` are merged into the PVC's metadata annotations on every reconcile. Annotations set by the storage provisioner (e.g. `pv.kubernetes.io/bind-completed`) are preserved — user-supplied keys are added or updated, never removed.

Storage size can be **expanded** by increasing `sizeInGi`. The storage class must have `allowVolumeExpansion: true`. While expansion is in progress, `DiskReady` shows `bound (Xgi, expanding to Ygi)`. Shrinking is not supported — to downsize, delete the AppDefinition and recreate it.

### `configMaps`

Inline ConfigMaps created and owned by the operator. Named `<app>-<name>`, mounted read-only in every container. Changing `data` triggers an automatic rolling restart via a pod template annotation that tracks a SHA-256 hash of all inline ConfigMap and Secret data.

```yaml
configMaps:
  - name: app-config
    mountPath: /etc/config
    data:
      config.yaml: |
        port: 8080
        debug: false
  - name: nginx-config
    mountPath: /etc/nginx/conf.d
    data:
      default.conf: |
        server { listen 80; }
```

### `secrets`

Secrets used by the application. Two modes:

**Inline** — the operator creates and owns the Secret, named `<app>-<name>`. Changing `data` triggers an automatic rolling restart (same hash mechanism as ConfigMaps).

**External reference** — the operator mounts or injects an existing Secret without creating, updating, or deleting it. Use this for Secrets managed by External Secrets Operator, Vault agent, Sealed Secrets, or any other external source.

`data` and `secretRef` are mutually exclusive and enforced at the API level.

```yaml
secrets:
  # Inline: mount as a directory of files
  - name: tls-certs
    mountPath: /etc/tls
    data:
      tls.crt: "..."
      tls.key: "..."

  # Inline: inject all keys as environment variables
  - name: db-credentials
    asEnvVars: true
    data:
      DB_PASSWORD: "super-secret"
      DB_HOST: "postgres.example.com"

  # Inline: mount and inject at the same time
  - name: api-keys
    mountPath: /etc/secrets
    asEnvVars: true
    data:
      API_KEY: "abc123"

  # External reference: mount an existing Secret as env vars
  - name: external-db
    asEnvVars: true
    secretRef: my-existing-secret    # must exist in the same namespace

  # External reference: mount an existing Secret as files
  - name: vault-tls
    mountPath: /etc/vault/tls
    secretRef: vault-agent-tls
```

> **Warning**: Inline `data` is stored in plain text in the AppDefinition spec and in etcd. For production, prefer `secretRef` pointing to a Secret managed by [External Secrets Operator](https://external-secrets.io) or [Sealed Secrets](https://github.com/bitnami-labs/sealed-secrets).

### `imagePullSecrets`

References to pre-existing Secrets for pulling private images.

```yaml
imagePullSecrets:
  - name: my-registry-secret
```

### `monitoringConfig`

When `enabled: true` and at least one port has `metricsPath` set, a `ServiceMonitor` (monitoring.coreos.com/v1) is created. If the prometheus-operator CRDs are not installed, this step is silently skipped.

```yaml
monitoringConfig:
  enabled: true
  scrapeInterval: "30s"
  labels:
    release: prometheus-stack   # must match your Prometheus serviceMonitorSelector
```

### `loggingConfig`

Describes logging intent. Stored in the spec but not yet acted on by the operator — log shipping is handled at the cluster infrastructure layer. See [docs/development.md](docs/development.md) for implementation options.

```yaml
loggingConfig:
  stdout:
    enabled: true
    format: json       # json | text
  stderr:
    enabled: true
    format: json
  files:
    - path: /logs/app.log
      format: json
      multilinePattern: "^\\d{4}-\\d{2}-\\d{2}"
```

### `lifecycle`

Exec-based `postStart` and `preStop` hooks. Applied to the **first container only** — sidecars often lack a shell and applying exec hooks to them causes crash loops.

```yaml
lifecycle:
  postStart:
    exec:
      command: ["/bin/sh", "-c", "echo started"]
  preStop:
    exec:
      command: ["/bin/sh", "-c", "sleep 5"]
```

### `securityContext`

Pod-level security context.

```yaml
securityContext:
  runAsUser: 1000
  runAsGroup: 1000
  fsGroup: 1000
  runAsNonRoot: true
```

### `serviceType`

```yaml
serviceType: ClusterIP   # ClusterIP | NodePort | LoadBalancer
```

`serviceType: LoadBalancer` provisions a cloud load balancer and assigns an external IP to the Service. `ports[].expose: true` controls which ports are included in the Service regardless of type — the two are orthogonal.

### `ingressClass` and `ingressAnnotations`

`ingressAnnotations` are set verbatim on the Ingress resource's metadata. Use them for any ingress-controller-specific configuration.

```yaml
ingressClass: nginx
ingressAnnotations:
  nginx.ingress.kubernetes.io/proxy-body-size: "50m"
  nginx.ingress.kubernetes.io/proxy-read-timeout: "300"
  nginx.ingress.kubernetes.io/ssl-redirect: "true"
  cert-manager.io/cluster-issuer: letsencrypt-prod
```

### `nodeSelector`, `tolerations`, `affinity`

Standard Kubernetes scheduling controls passed directly to the pod spec.

```yaml
nodeSelector:
  kubernetes.io/arch: amd64

tolerations:
  - key: "dedicated"
    operator: "Equal"
    value: "gpu"
    effect: "NoSchedule"

affinity:
  podAntiAffinity:
    preferredDuringSchedulingIgnoredDuringExecution:
      - weight: 100
        podAffinityTerm:
          topologyKey: kubernetes.io/hostname
          labelSelector:
            matchLabels:
              app.kubernetes.io/name: my-app
```

### `paused`

Suspends reconciliation without deleting existing resources. Deletion still works.

```yaml
paused: true
```

---

## Samples

All samples live in `config/samples/`. Apply one:

```sh
kubectl apply -f config/samples/appdefinition_v1_sidecar_demo.yaml
```

| Sample | What it demonstrates |
|---|---|
| `nginx_demo` | Minimal app — image, ports, replicas |
| `whoami_demo` | LoadBalancer service type |
| `ingress_demo` | Ingress with path routing |
| `ingress_tls_demo` | Multi-container pod, TLS ingress, per-domain port routing |
| `probes_demo` | All three probe types: `httpGet`, `tcpSocket`, `exec` |
| `lifecycle_demo` | `postStart` and `preStop` hooks |
| `autoscaling_demo` | CPU-based HPA |
| `autoscaling_memory_demo` | Memory-based HPA |
| `disk_demo` | PVC with sub-path partitions |
| `config_demo` | Inline ConfigMap mounted as a directory |
| `multi_config_demo` | Multiple ConfigMaps + all three Secret modes |
| `security_context_demo` | Pod security context, non-root user |
| `scheduling_demo` | nodeSelector, tolerations, pod anti-affinity |
| `tcp_service_demo` | TCP-only service (no HTTP) |
| `sidecar_demo` | nginx + metrics-exporter + log-shipper sidecars with ServiceMonitor |
| `private_registry_demo` | imagePullSecrets for a private registry |
| `paused_demo` | Suspended reconciliation |
| `complex_app` | Multi-container with PVC, ConfigMaps, Secrets, monitoring, ingress |

---

## Development

See [docs/development.md](docs/development.md) for architecture, local setup, testing, known limitations, and planned improvements.

---

## License

Copyright 2025. Licensed under the [Apache License, Version 2.0](LICENSE).
