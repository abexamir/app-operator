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

> **Single source of truth**: if the operator is already deployed in-cluster (e.g. via `kubectl apply -f dist/install.yaml`), scale it down before running locally to avoid two controllers fighting over the same resources:
> ```sh
> kubectl scale deployment appoperator-controller-manager -n appoperator-system --replicas=0
> # run locally, test, then restore:
> kubectl scale deployment appoperator-controller-manager -n appoperator-system --replicas=1
> ```

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
       ├── reconcileConfigMaps()     → ConfigMap      (one per configMaps[], named <app>-<name>)
       ├── reconcileSecrets()        → Secret        (one per inline secrets[]; secretRef entries skipped)
       ├── reconcileExternalSecrets()→ ExternalSecret (one per externalSecrets[]; skipped if ESO absent)
       ├── reconcileDeployment()     → Deployment    (init containers, containers, volumes, probes,
       │                                            lifecycle, config-hash annotation, scheduling)
       ├── reconcileService()        → Service     (expose:true ports; serviceType)
       ├── reconcilePVC()            → PVC         (create; expand when sizeInGi increases)
       ├── reconcileIngress()        → Ingress     (per-domain rules and TLS blocks)
       ├── reconcileHPA()            → HPA         (created/deleted based on autoscaling.enabled)
       ├── reconcileServiceMonitor() → ServiceMonitor (skipped gracefully when CRD absent)
       └── updateStatus()            → status subresource (phase, readyReplicas,
                                       Ready / DiskReady / IngressReady / HPAActive conditions)
```

Reconciliation order is intentional: ConfigMaps and Secrets are created before the Deployment so all mounts are available when pods start.

### Reconcile phases

1. **Finalizer** — `appdefinition.abexamir.me/finalizer` added on first encounter; requeues
2. **Deletion guard** — if `DeletionTimestamp` is set, removes finalizer and exits
3. **Paused guard** — if `spec.paused: true`, sets phase to `Paused` and exits
4. **Resource reconciliation** — runs the chain above in order
5. **Status update** — reads Deployment ready replicas, fetches PVC/Ingress/HPA for per-resource conditions

`ctrl.CreateOrUpdate` is used for all resources — the reconcile loop is fully idempotent. The loop also runs every 30 seconds in addition to event-driven triggers, so drift is corrected automatically.

### Init containers

`spec.initContainers[]` maps directly to `podSpec.InitContainers`. Init containers share the same volumes (disk partitions, ConfigMaps, Secrets with `mountPath`) and the same `envFrom` Secret injection (Secrets with `asEnvVars: true`) as the main containers. They do not receive ports or lifecycle hooks.

### Config hash rollout

Whenever inline `configMaps[].data` or `secrets[].data` (inline only, not `secretRef`) changes, a new SHA-256 hash is computed over the sorted keys and values and stored in the pod template annotation `appdefinition.abexamir.me/config-hash`. Kubernetes treats a changed pod template annotation as a pod spec change and performs a rolling restart automatically — no manual rollout needed.

### PVC expansion

When `spec.disk.sizeInGi` increases, `reconcilePVC` patches `pvc.spec.resources.requests.storage` to the new value. The storage class must have `allowVolumeExpansion: true`. The actual filesystem resize is performed by the storage class's CSI driver; until it completes, the `DiskReady` condition shows `bound (Xgi, expanding to Ygi)`. Shrinking is not supported by Kubernetes.

The `DiskReady` condition message always reflects `pvc.status.capacity` (the size actually granted by the provisioner), not `pvc.spec.resources.requests` (the size requested). This distinction matters during an in-progress resize. The PVC is read via `APIReader` (bypasses the informer cache) to avoid a k3s-specific transient state where the cache briefly holds an intermediate capacity value during resize processing.

### ExternalSecrets Operator integration

`spec.externalSecrets[]` creates `ExternalSecret` objects (external-secrets.io/v1) via `unstructured.Unstructured` — same pattern as `ServiceMonitor`. If the ESO CRDs are not installed, `apimeta.IsNoMatchError` is caught and the step is silently skipped.

The existence check uses `r.APIReader.Get` (bypasses the informer cache) rather than `r.Get`, because the informer cache only has informers for types registered in the scheme. Since `ExternalSecret` is not in the scheme (unstructured access to avoid a hard dependency), the cached client returns `NoMatchError` even when the CRD is present. `APIReader` goes directly to the API server and performs discovery on each call.

Each `ExternalSecret` is named `<app>-<name>`, carries standard labels, and has an owner reference pointing to the `AppDefinition` — so it is garbage-collected when the AppDefinition is deleted. ESO owns the resulting Kubernetes Secret (via `creationPolicy: Owner`).

The Deployment mounts or injects the resulting Secret through `buildVolumes` and `buildVolumeMounts` exactly like any other Secret — `mountPath` for file mounting, `asEnvVars` for environment variable injection.

Install ESO CRDs with `--server-side` to avoid the kubectl annotation size limit:
```sh
kubectl apply -f https://raw.githubusercontent.com/external-secrets/external-secrets/v2.6.0/deploy/crds/bundle.yaml --server-side --force-conflicts
```

### External Secret references

A `SecretMount` with `secretRef` set tells the operator to use an existing Secret by name instead of creating one. The operator:
- Skips Secret creation in `reconcileSecrets` for that entry
- Routes `envFrom` and volume mounts to the referenced Secret name via `resolvedSecretName()`
- Does not set an owner reference on the referenced Secret (it is not garbage-collected with the AppDefinition)

This works with any source that creates Kubernetes Secrets: External Secrets Operator, Vault agent sidecar, Sealed Secrets, or plain `kubectl create secret`. The `data` and `secretRef` fields are mutually exclusive and enforced by a CEL validation rule on the CRD — no webhook needed.

### Per-resource status conditions

`updateStatus` sets four conditions on every reconcile pass:

| Condition | Set when | `True` when |
|---|---|---|
| `Ready` | Always | `readyReplicas >= desiredReplicas` |
| `DiskReady` | `spec.disk` is set | PVC phase is `Bound` |
| `IngressReady` | `spec.domains` is non-empty | Ingress has at least one LoadBalancer address assigned |
| `HPAActive` | `spec.autoscaling.enabled: true` | HPA resource exists |

### ServiceMonitor

`ServiceMonitor` (monitoring.coreos.com/v1) is created via `unstructured.Unstructured` to avoid a hard dependency on the prometheus-operator Go types. If the CRD is not installed, `apimeta.IsNoMatchError` is caught and the step is silently skipped — the operator installs and runs fine without Prometheus.

### CEL validation

Three validation rules are baked into the CRD schema (no admission webhook needed):

| Rule | Reason |
|---|---|
| `disk + replicas > 1` rejected | `ReadWriteOnce` PVCs allow one writer; multiple pods corrupt the volume |
| `disk + autoscaling.enabled` rejected | HPA would scale past 1, violating the rule above |
| `secrets[].secretRef + secrets[].data` rejected | The two are mutually exclusive; you either manage the Secret or reference an existing one |

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
| `external-secrets.io` | `externalsecrets` | full |

---

## Known Limitations

- **Secrets in plain text**: `secrets[].data` is stored unencrypted in the AppDefinition spec and in etcd. For production, use `secretRef` to point to a Secret managed by [External Secrets Operator](https://external-secrets.io) or [Sealed Secrets](https://github.com/bitnami-labs/sealed-secrets).

- **PVC shrink is not supported**: the operator expands the PVC when `disk.sizeInGi` increases (storage class must have `allowVolumeExpansion: true`), but Kubernetes rejects shrink requests. To downsize: delete the AppDefinition (which deletes the PVC) and recreate with the smaller size.

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

- **Stale child cleanup**: track which ConfigMaps and Secrets were last created by the operator and delete those no longer in the spec.

- **Admission webhook**: CEL rules cover basics but a webhook can validate things CEL cannot — image format, port name uniqueness across containers, checking that a referenced StorageClass or Secret exists — and return richer errors before any resources are created.

- **Log shipping integration**: when `loggingConfig` is present, inject a Promtail scrape annotation on the pod, or create an OTel `PodLogs` object targeting it — same unstructured pattern as `ServiceMonitor`.

- **KEDA / custom metrics HPA**: current autoscaling supports CPU and memory only. A `ScaledObject` would allow scaling on Prometheus metrics, queue length, or any external signal.
