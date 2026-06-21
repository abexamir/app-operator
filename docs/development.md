# Development Guide

## Prerequisites

- Go 1.24+
- Docker with Buildx (for multi-platform builds)
- kubectl v1.11+
- A Kubernetes cluster for testing (OrbStack, Kind, or any v1.23+ cluster)

## Running locally

```sh
# Install the CRD into your current cluster
make install

# Run the controller from your host against the current kubeconfig
make run
```

The controller reconnects automatically when you restart it. Any AppDefinitions already in the cluster are reconciled on startup.

## Code generation

Run these after changing `api/v1/appdefinition_types.go`:

```sh
make generate   # regenerates DeepCopy methods (zz_generated.deepcopy.go)
make manifests  # regenerates CRD and RBAC from kubebuilder markers
```

Both are also run automatically by `make build`, `make test`, and `make run`.

## Testing

```sh
make test        # unit tests with envtest (spins up a real API server)
make lint        # golangci-lint
make lint-fix    # auto-fix lint issues where possible
```

End-to-end tests require Kind:

```sh
make test-e2e    # creates a Kind cluster, runs tests, tears down
```

## Building the image

```sh
# Single-platform (fast, for local use)
make docker-build IMG=ghcr.io/abexamir/app-operator:dev
make docker-push  IMG=ghcr.io/abexamir/app-operator:dev

# Multi-platform (amd64 + arm64, requires buildx)
make docker-buildx IMG=ghcr.io/abexamir/app-operator:dev
```

## Generating the install bundle

```sh
make build-installer IMG=ghcr.io/abexamir/app-operator:latest
# Writes dist/install.yaml — the single-file install artifact
```

## Deploying to a cluster

```sh
make deploy IMG=ghcr.io/abexamir/app-operator:dev
# Applies config/default via kustomize with the given image

make undeploy   # removes the controller and RBAC
make uninstall  # removes the CRDs (deletes all AppDefinitions)
```

---

## Architecture

```
AppDefinition CR
       │
       ▼
AppDefinitionReconciler  (internal/controller/appdefinition_controller.go)
       │
       ├── reconcileConfigMaps()     → ConfigMap   (one per configMaps[], named <app>-<name>)
       ├── reconcileSecrets()        → Secret      (one per secrets[], named <app>-<name>)
       ├── reconcileDeployment()     → Deployment  (init containers, containers, volumes, probes, lifecycle, config-hash annotation, scheduling)
       ├── reconcileService()        → Service     (expose:true ports; serviceType)
       ├── reconcilePVC()            → PVC         (create; expand if sizeInGi increases)
       ├── reconcileIngress()        → Ingress     (per-domain rules and TLS blocks)
       ├── reconcileHPA()            → HPA         (created/deleted based on autoscaling.enabled)
       ├── reconcileServiceMonitor() → ServiceMonitor (skipped gracefully when CRD absent)
       └── updateStatus()            → status subresource (phase, readyReplicas, Ready/DiskReady/IngressReady/HPAActive conditions)
```

Reconciliation order is intentional: ConfigMaps and Secrets are created before the Deployment so all mounts are available when pods start.

### Reconcile phases

1. **Finalizer** — `appdefinition.abexamir.me/finalizer` added on first encounter; requeues
2. **Deletion guard** — if `DeletionTimestamp` is set, removes finalizer and exits
3. **Paused guard** — if `spec.paused: true`, sets phase to `Paused` and exits
4. **Resource reconciliation** — runs the chain above in order
5. **Status update** — reads Deployment ready replicas, sets phase and conditions

`ctrl.CreateOrUpdate` is used for all resources — the reconcile loop is fully idempotent. The loop also runs every 30 seconds in addition to event-driven triggers, so drift is corrected automatically.

### ServiceMonitor

`ServiceMonitor` (monitoring.coreos.com/v1) is created via `unstructured.Unstructured` to avoid a hard dependency on the prometheus-operator Go types. If the CRD is not installed, `apimeta.IsNoMatchError` is caught and the step is silently skipped — the operator installs and runs fine without Prometheus.

### CEL validation

Two validation rules are baked into the CRD schema (no admission webhook needed):

| Rule | Reason |
|---|---|
| `disk + replicas > 1` rejected | `ReadWriteOnce` PVCs allow one writer; multiple pods corrupt the volume |
| `disk + autoscaling.enabled` rejected | HPA would scale past 1, violating the rule above |

### Labels

Every child resource gets these labels:

```
app.kubernetes.io/name: <app-name>
app.kubernetes.io/instance: <app-name>
app.kubernetes.io/managed-by: app-operator
```

The selector label (`app.kubernetes.io/name`) is intentionally kept to one key so the Service and HPA can match pods without ambiguity.

---

## RBAC

Generated from `// +kubebuilder:rbac:...` markers in `appdefinition_controller.go` via `make manifests`.

| API group | Resource | Verbs |
|---|---|---|
| `appdefinition.abexamir.me` | `appdefinitions`, `/status`, `/finalizers` | full |
| `apps` | `deployments` | full |
| `core` | `services`, `pods`, `configmaps`, `secrets`, `persistentvolumeclaims` | full |
| `networking.k8s.io` | `ingresses` | full |
| `autoscaling` | `horizontalpodautoscalers` | full |
| `monitoring.coreos.com` | `servicemonitors` | full |

---

## Known Limitations

- **Secrets in plain text**: `secrets[].data` is stored unencrypted in the AppDefinition spec and in etcd. For production, use [External Secrets Operator](https://external-secrets.io) or [Sealed Secrets](https://github.com/bitnami-labs/sealed-secrets) to manage values externally and reference them.

- **PVC shrink is not supported**: the operator will expand the PVC when `disk.sizeInGi` increases (the storage class must have `allowVolumeExpansion: true`), but shrinking is rejected by Kubernetes. To downsize: delete the AppDefinition (which deletes the PVC) and recreate with the smaller size.

- **Single PVC across all containers**: every container in the pod mounts the same disk at every declared partition. There is no per-container disk assignment.

- **Lifecycle hooks on primary container only**: `postStart` and `preStop` are applied to `containers[0]` only. Sidecars often lack a shell; applying exec hooks to them causes `FailedPostStartHook` crash loops.

- **One cert-manager issuer per AppDefinition**: `certIssuer` maps to a single `cert-manager.io/cluster-issuer` annotation on the Ingress. If two domains need different issuers, use per-domain `annotations` and leave `certIssuer` empty.

- **No stale child cleanup**: removing an entry from `configMaps[]` or `secrets[]` leaves the old resource orphaned until the AppDefinition is deleted. The operator does not diff and prune resources that are no longer in the spec.

- **`loggingConfig` is a no-op**: parsed and stored but not acted on. Log shipping is handled at the cluster infrastructure layer. See `api/v1/appdefinition_types.go` for a full comparison of implementation options (Loki + Promtail, OpenTelemetry `PodLogs` CRD, Fluent Bit / Vector).

---

## Potential Improvements

- **Startup probe**: the recommended way to handle slow-starting containers without inflating liveness `initialDelaySeconds`.

- **Container-level security context**: the current `securityContext` is pod-level only. Per-container `runAsUser`, `allowPrivilegeEscalation`, `readOnlyRootFilesystem` cover the most common hardening requirements.

- **ServiceAccount management**: create a dedicated `ServiceAccount` per AppDefinition with user-provided annotations, enabling AWS IRSA / GCP Workload Identity without users managing the SA separately.

- **PodDisruptionBudget**: automatically create a PDB (`minAvailable: 1`) for apps with `replicas > 1` to prevent outages during node drains.

- **Stale child cleanup**: track which ConfigMaps and Secrets were last created by the operator (via a hash annotation) and delete those no longer in the spec.

- **Admission webhook**: CEL rules cover basics but a webhook can validate things CEL cannot — image format, port name uniqueness across containers, checking that a referenced StorageClass exists — and return richer errors before any resources are created.

- **Log shipping integration**: when `loggingConfig` is present, inject a Promtail scrape annotation on the pod, or create an OTel `PodLogs` object targeting it — same unstructured pattern as `ServiceMonitor`.

- **KEDA / custom metrics HPA**: current autoscaling supports CPU and memory only. A `ScaledObject` would allow scaling on Prometheus metrics, queue length, or any external signal.
