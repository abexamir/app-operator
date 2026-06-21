# app-operator

A Kubernetes operator that lets you deploy containerized applications using a single high-level `AppDefinition` custom resource, instead of manually managing Deployments, Services, Ingresses, PersistentVolumeClaims, and HorizontalPodAutoscalers.

Built with [Kubebuilder](https://book.kubebuilder.io/) v4 on top of [controller-runtime](https://github.com/kubernetes-sigs/controller-runtime).

---

## Overview

`AppDefinition` is a namespaced CRD (`appdefinition.abexamir.me/v1`) that describes everything about how an application should run. The operator watches for these resources and reconciles the underlying Kubernetes primitives:

| AppDefinition feature | Kubernetes resource created |
|---|---|
| `source.dockerImage` containers | `Deployment` |
| `ports[].expose: true` | `Service` |
| `domains[]` | `Ingress` |
| `disk` | `PersistentVolumeClaim` |
| `autoscaling.enabled: true` | `HorizontalPodAutoscaler` |

All child resources are owned by the `AppDefinition` and are garbage-collected when it is deleted. A finalizer (`appdefinition.abexamir.me/finalizer`) is added on creation to manage orderly cleanup.

---

## Status

The operator reports its state on the `AppDefinition` status subresource. You can inspect it with:

```sh
kubectl get appdefinition <name>   # shows Phase, Ready replicas, Replicas
kubectl describe appdefinition <name>   # shows full Conditions
```

| Status field | Description |
|---|---|
| `phase` | High-level state: `Available`, `Progressing`, `Failed`, or `Paused` |
| `readyReplicas` | Number of pods with a Ready condition |
| `replicas` | Desired replica count |
| `observedGeneration` | Last generation the controller processed |
| `conditions` | Standard Kubernetes conditions: `Ready` and `Progressing` |
| `lastError` | Last reconciliation error message |

---

## AppDefinition Spec Reference

### `source` (required)

Defines where to pull container images from. Only `dockerImage` is supported.

```yaml
source:
  type: dockerImage
  dockerImage:
    containers:
      - name: web
        image: nginx:1.25-alpine
        command: ["nginx"]
        args: ["-g", "daemon off;"]
        env:
          - name: PORT
            value: "8080"
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
            expose: true         # creates a Service port for this
            metricsPath: /metrics
        readinessProbe:
          type: httpGet          # httpGet | tcpSocket | exec
          httpGet:
            path: /health
            port: 80
          initialDelaySeconds: 5
          periodSeconds: 10
          timeoutSeconds: 2
          failureThreshold: 3
          successThreshold: 1
        livenessProbe:
          type: httpGet
          httpGet:
            path: /health
            port: 80
          initialDelaySeconds: 15
          periodSeconds: 20
```

Multiple containers are supported (main app + sidecars). Only ports with `expose: true` are added to the Service.

### `replicas`

```yaml
replicas: 3   # defaults to 1
```

Ignored when `autoscaling.enabled: true` (the HPA controls the replica count instead).

### `paused`

Suspends reconciliation without deleting any existing resources.

```yaml
paused: true
```

When set, the operator stops reconciling child resources and sets `status.phase` to `Paused`. Deletion still works normally.

### `autoscaling`

Creates a `HorizontalPodAutoscaler` (autoscaling/v2). When enabled, the `replicas` field is used as `minReplicas`.

```yaml
autoscaling:
  enabled: true
  minReplicas: 2
  maxReplicas: 10
  targetCPUUtilizationPercentage: 70
  targetMemoryUtilizationPercentage: 80
```

Disabling autoscaling (`enabled: false`) deletes the HPA if one exists.

### `imagePullSecrets`

References to Secrets in the same namespace used for pulling private container images.

```yaml
imagePullSecrets:
  - name: my-registry-secret
```

### `configMaps`

Mounts ConfigMaps as read-only directories into all containers.

```yaml
configMaps:
  - name: app-config
    mountPath: /etc/config
  - name: feature-flags
    mountPath: /etc/flags
    optional: true
```

### `secrets`

Mounts Secrets as read-only directories into all containers.

```yaml
secrets:
  - name: app-tls
    mountPath: /etc/tls
  - name: db-credentials
    mountPath: /etc/db
    optional: true
```

### `domains`

Creates an Ingress. Supports TLS, cert-manager integration, per-domain annotations, and routing to specific service ports.

```yaml
domains:
  - name: app.example.com
    path: /
    tls: true
    redirect_tls: true
    certIssuer: letsencrypt-prod
    portName: http              # service port name to route to (default: "http")
    secretName: my-tls-secret   # TLS secret override (auto-generated as <app>-<domain>-tls if omitted)
    annotations:
      cert-manager.io/cluster-issuer: letsencrypt-prod
      nginx.ingress.kubernetes.io/ssl-redirect: "true"
  - name: api.example.com
    path: /api
    tls: false
    portName: api
```

### `disk`

Creates a `PersistentVolumeClaim` and mounts it into every container.

```yaml
disk:
  sizeInGi: 10
  storageClassName: standard
  setFsGroup: true
  partitions:
    - mountPath: /data
      subPath: data
    - mountPath: /logs
      subPath: logs
```

A single PVC named `<app-name>-disk` is created with `ReadWriteOnce`. Storage size cannot be changed after creation.

### `lifecycle`

Exec-based `postStart` and `preStop` lifecycle hooks applied to all containers.

```yaml
lifecycle:
  postStart:
    exec:
      command: ["/bin/sh", "-c", "echo started"]
  preStop:
    exec:
      command: ["/bin/sh", "-c", "sleep 5"]
```

### `loggingConfig`

Metadata that describes the logging configuration (used by log collectors/agents).

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

### `monitoringConfig`

Metadata for Prometheus ServiceMonitor integration.

```yaml
monitoringConfig:
  enabled: true
  scrapeInterval: "30s"
  labels:
    app.kubernetes.io/name: my-app
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

Kubernetes Service type. Defaults to `ClusterIP`.

```yaml
serviceType: ClusterIP   # ClusterIP | NodePort | LoadBalancer
```

### `ingressClass` and `ingressAnnotations`

```yaml
ingressClass: nginx
ingressAnnotations:
  nginx.ingress.kubernetes.io/proxy-body-size: "50m"
  nginx.ingress.kubernetes.io/proxy-read-timeout: "300"
```

### `nodeSelector`, `tolerations`, `affinity`

Standard Kubernetes scheduling controls passed directly to the pod spec.

```yaml
nodeSelector:
  kubernetes.io/arch: amd64

tolerations:
  - key: "dedicated"
    operator: "Equal"
    value: "app"
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

---

## Minimal Example

```yaml
apiVersion: appdefinition.abexamir.me/v1
kind: AppDefinition
metadata:
  name: sample-app
spec:
  source:
    type: dockerImage
    dockerImage:
      containers:
        - name: web
          image: nginx:latest
          ports:
            - name: http
              containerPort: 80
              servicePort: 80
              protocol: TCP
              expose: true
  domains:
    - name: example.com
      path: /
      tls: true
```

## Full-featured Example

```yaml
apiVersion: appdefinition.abexamir.me/v1
kind: AppDefinition
metadata:
  name: my-app
spec:
  source:
    type: dockerImage
    dockerImage:
      containers:
        - name: web
          image: my-registry.io/my-app:v1.2.3
          ports:
            - name: http
              containerPort: 8080
              servicePort: 80
              protocol: TCP
              expose: true
          resources:
            requests:
              cpu: "100m"
              memory: "128Mi"
            limits:
              cpu: "500m"
              memory: "512Mi"
          readinessProbe:
            type: httpGet
            httpGet:
              path: /healthz
              port: 8080
            initialDelaySeconds: 5
            periodSeconds: 10

  imagePullSecrets:
    - name: my-registry-secret

  autoscaling:
    enabled: true
    minReplicas: 2
    maxReplicas: 8
    targetCPUUtilizationPercentage: 70

  domains:
    - name: my-app.example.com
      path: /
      tls: true
      redirect_tls: true
      certIssuer: letsencrypt-prod
      portName: http

  disk:
    sizeInGi: 20
    storageClassName: standard

  configMaps:
    - name: app-config
      mountPath: /etc/config

  secrets:
    - name: db-credentials
      mountPath: /etc/db

  lifecycle:
    preStop:
      exec:
        command: ["/bin/sh", "-c", "sleep 5"]

  securityContext:
    runAsNonRoot: true
    runAsUser: 1000
    fsGroup: 1000
```

---

## Getting Started

### Prerequisites

- Go v1.24+
- Docker 17.03+
- kubectl v1.11.3+
- Access to a Kubernetes v1.11.3+ cluster

### Run locally (against current kubeconfig)

```sh
make install   # installs the CRD
make run       # runs the controller from your host
```

### Deploy to cluster

```sh
make docker-build docker-push IMG=<registry>/app-operator:tag
make deploy IMG=<registry>/app-operator:tag
kubectl apply -k config/samples/
```

### Uninstall

```sh
kubectl delete -k config/samples/
make undeploy
make uninstall
```

---

## Development

```sh
make generate    # regenerate DeepCopy methods
make manifests   # regenerate CRDs and RBAC
make fmt vet     # format and vet code
make test        # run unit tests with envtest
make lint        # run golangci-lint
make test-e2e    # run e2e tests against a Kind cluster (spins up/tears down automatically)
```

### Build a consolidated install bundle

```sh
make build-installer IMG=<registry>/app-operator:tag
# outputs dist/install.yaml — apply with:
kubectl apply -f dist/install.yaml
```

---

## Architecture

```
AppDefinition CR
       │
       ▼
AppDefinitionReconciler (internal/controller/appdefinition_controller.go)
       │
       ├── reconcileDeployment()   → apps/v1 Deployment  (volumes, mounts, lifecycle, imagePullSecrets)
       ├── reconcileService()      → core/v1 Service      (exposed ports only)
       ├── reconcilePVC()          → core/v1 PVC          (creation only; size is immutable)
       ├── reconcileIngress()      → networking.k8s.io/v1 Ingress  (per-domain TLS secrets)
       ├── reconcileHPA()          → autoscaling/v2 HPA   (created/deleted based on autoscaling.enabled)
       └── updateStatus()          → status subresource   (phase, readyReplicas, conditions)
```

The controller uses `ctrl.CreateOrUpdate` for idempotent reconciliation. Owner references are set on all child resources so they are garbage-collected with the parent `AppDefinition`. The first reconcile adds a finalizer and requeues; actual resource reconciliation happens on the second pass.

### Reconcile phases

1. **Finalizer pass** — adds `appdefinition.abexamir.me/finalizer`, requeues
2. **Deletion** — if `DeletionTimestamp` is set, removes finalizer and exits
3. **Paused guard** — if `spec.paused: true`, sets status to `Paused` and exits without touching resources
4. **Resource reconciliation** — Deployment → Service → PVC → Ingress → HPA
5. **Status update** — reads Deployment ready replicas, sets phase and conditions

---

## RBAC

The operator requires the following permissions (generated in `config/rbac/`):

- `appdefinitions` — get, list, watch, create, update, patch, delete
- `appdefinitions/status` — get, update, patch
- `apps/deployments` — get, list, watch, create, update, patch, delete
- `core/services` — get, list, watch, create, update, patch, delete
- `core/pods` — get, list, watch
- `networking.k8s.io/ingresses` — get, list, watch, create, update, patch, delete
- `core/persistentvolumeclaims` — get, list, watch, create, update, patch, delete
- `autoscaling/horizontalpodautoscalers` — get, list, watch, create, update, patch, delete

---

## License

Copyright 2025. Licensed under the [Apache License, Version 2.0](LICENSE).
