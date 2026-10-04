# App metrics

The controller exports Prometheus metrics for every AppDefinition on its metrics endpoint (the
controller-runtime metrics server, `--metrics-bind-address`). They cover the app's metadata and
status, the desired state from its spec, the Deployment rollout, its pods and containers,
resource usage, autoscaling, disk, and how the controller is reconciling it.

The metrics are computed at scrape time from the controller's informer cache, the way
kube-state-metrics does it, and are not pushed during reconciliation. So:

- A deleted AppDefinition's series disappear on the next scrape. No stale series are left behind.
- Values reflect the cluster as the cache sees it, even for changes the reconciler hasn't
  processed yet.
- Only the leader replica exports them. Running several controller replicas does not
  duplicate series.

For scraping setup, see `config/prometheus` (ServiceMonitor plus PrometheusRule alerts). For a
ready-made dashboard, see `config/grafana`.

## Filtering apps by label

Every `appoperator_app_*` series carries `namespace` and `name` (the AppDefinition's). Allowlist
AppDefinition `metadata.labels` keys and each one is attached as `label_<key>` to **every**
per-app series. Characters outside `[a-zA-Z0-9_]` become `_`, as in kube-state-metrics.

```sh
/manager --app-metrics-labels-allowlist=team,environment,app.kubernetes.io/part-of
```

```yaml
apiVersion: appdefinition.abexamir.me/v1
kind: AppDefinition
metadata:
  name: checkout
  labels:
    team: payments
    environment: production
    app.kubernetes.io/part-of: shop
```

```promql
# Every not-ready production app owned by the payments team
appoperator_app_ready{label_team="payments", label_environment="production"} == 0

# CPU used per team
sum by (label_team) (appoperator_app_container_cpu_usage_cores)

# Restarts in the last hour per product
sum by (label_app_kubernetes_io_part_of) (increase(appoperator_app_container_restarts_total[1h]))
```

An app without an allowlisted key gets an empty value. Prometheus treats an empty label the same
as a missing one, so `label_team=""` matches apps with no `team` label. Adding a key to the
allowlist does not change the identity of existing series for apps that don't set it. Changing
an app's label value does start new series for that app, just as relabeling would.

`--app-metrics-labels-allowlist=*` exports every label key. Each key multiplies the series count
only by the number of distinct values it takes, but avoid high-cardinality keys such as commit
SHAs or timestamps.

Annotations work the same way via `--app-metrics-annotations-allowlist`, exported as
`annotation_<key>`. They go on `appoperator_app_annotations` only, not on every series, because
annotations tend to be long and high-cardinality. Join them in when needed:

```promql
appoperator_app_ready == 0
  * on (namespace, name) group_left (annotation_owner_example_com_slack)
  appoperator_app_annotations
```

## Flags

| Flag | Default | Description |
|---|---|---|
| `--app-metrics-labels-allowlist` | empty | AppDefinition label keys to attach as `label_<key>` to every app series. `*` for all. |
| `--app-metrics-annotations-allowlist` | empty | AppDefinition annotation keys exported on `appoperator_app_annotations`. `*` for all. |
| `--app-metrics-resource-usage` | `true` | Read per-container CPU/memory usage from the `metrics.k8s.io` API (metrics-server) on every scrape, in one cluster-wide list call. Skipped if the API isn't served. |

## Metric reference

All series below also carry `namespace`, `name` and any allowlisted `label_<key>` labels.

### Metadata and status

| Metric | Type | Labels | Description |
|---|---|---|---|
| `appoperator_app_info` | gauge | `uid`, `stateful`, `autoscaling`, `paused`, `service_account`, `service_type`, `ingress_class` | Always 1. |
| `appoperator_app_labels` | gauge | | Always 1. Carries only the allowlisted `label_<key>` labels; useful for listing apps by label. |
| `appoperator_app_annotations` | gauge | `annotation_<key>` | Always 1. |
| `appoperator_app_created_timestamp_seconds` | gauge | | Creation time. |
| `appoperator_app_metadata_generation` | gauge | | `metadata.generation`. |
| `appoperator_app_observed_generation` | gauge | | `status.observedGeneration`. Lags `metadata_generation` until the controller processes a spec change. |
| `appoperator_app_ready` | gauge | | 1 when the `Ready` condition is True. |
| `appoperator_app_status_phase` | gauge | `phase` | One-hot over `Available`, `Progressing`, `Failed`, `Paused`. |
| `appoperator_app_status_condition` | gauge | `condition`, `status` | One-hot over `true`/`false`/`unknown` for each status condition (`Ready`, `DiskReady`, `IngressReady`, `HPAActive`, `ExternalSecretsReady`, `MonitoringReady`, `MiddlewaresReady`, ...). |
| `appoperator_app_status_condition_last_transition_timestamp_seconds` | gauge | `condition` | When each condition last changed. |

### Desired state (spec)

| Metric | Type | Labels | Description |
|---|---|---|---|
| `appoperator_app_spec_replicas` | gauge | | `spec.replicas` (default 1). Under an HPA it is only the starting count. |
| `appoperator_app_container_info` | gauge | `container`, `type`, `image` | Always 1. `type` is `main` (`containers[0]`), `sidecar` or `init`. |
| `appoperator_app_container_resource_requests` | gauge | `container`, `resource`, `unit` | Per-pod requests. CPU in cores, memory/storage in bytes. |
| `appoperator_app_container_resource_limits` | gauge | `container`, `resource`, `unit` | Per-pod limits, same units. |
| `appoperator_app_domain_info` | gauge | `domain`, `path`, `tls` | Always 1, one per `spec.domains` entry. |
| `appoperator_app_autoscaling_min_replicas` | gauge | | Effective HPA minimum (defaults to `spec.replicas`). Only when autoscaling is enabled. |
| `appoperator_app_autoscaling_max_replicas` | gauge | | HPA maximum. |
| `appoperator_app_autoscaling_target_utilization_ratio` | gauge | `resource` | Target utilization, 0.75 = 75%. |

### Deployment rollout

| Metric | Type | Description |
|---|---|---|
| `appoperator_app_replicas_desired` | gauge | Deployment `spec.replicas`, which the HPA sets when autoscaling is enabled. |
| `appoperator_app_replicas_current` | gauge | Pods the Deployment has created. |
| `appoperator_app_replicas_ready` | gauge | Ready pods. |
| `appoperator_app_replicas_available` | gauge | Available pods. |
| `appoperator_app_replicas_unavailable` | gauge | Pods still needed before the Deployment is fully available. |
| `appoperator_app_replicas_updated` | gauge | Pods running the latest pod template. |
| `appoperator_app_rollout_in_progress` | gauge | 1 while a rollout is in progress, using the same rules as `kubectl rollout status`. |
| `appoperator_app_rollout_stalled` | gauge | 1 when the rollout exceeded its progress deadline (`ProgressDeadlineExceeded`). |

### Pods and containers

These are aggregated across the app's current pods, so cardinality doesn't grow with replica
count. A pod counts as the app's only if its controlling ReplicaSet is `<app>-<pod-template-hash>`.
That excludes other workloads that happen to reuse the `app.kubernetes.io/name` label.

| Metric | Type | Labels | Description |
|---|---|---|---|
| `appoperator_app_pods` | gauge | `phase` | Pods per phase (`Pending`, `Running`, `Succeeded`, `Failed`, `Unknown`). |
| `appoperator_app_pods_ready` | gauge | | Pods with a `Ready` condition. |
| `appoperator_app_container_ready` | gauge | `container` | Ready instances of each container. |
| `appoperator_app_container_restarts_total` | counter | `container` | Restarts summed across current pods. It drops when pods are replaced, and `rate()`/`increase()` treat that as a counter reset. |
| `appoperator_app_container_waiting` | gauge | `container`, `reason` | Instances waiting, by reason: `CrashLoopBackOff`, `ImagePullBackOff`, `CreateContainerConfigError`, ... |
| `appoperator_app_container_last_terminated` | gauge | `container`, `reason` | Instances whose previous run terminated, by reason: `OOMKilled`, `Error`, `Completed`, ... |
| `appoperator_app_container_cpu_usage_cores` | gauge | `container` | CPU usage summed across pods (metrics-server). |
| `appoperator_app_container_memory_working_set_bytes` | gauge | `container` | Memory working set summed across pods (metrics-server). |

### Autoscaling (HPA status)

| Metric | Type | Labels | Description |
|---|---|---|---|
| `appoperator_app_hpa_current_replicas` | gauge | | Replicas the HPA currently observes. |
| `appoperator_app_hpa_desired_replicas` | gauge | | Replicas the HPA last computed. |
| `appoperator_app_hpa_current_utilization_ratio` | gauge | `resource` | Observed average utilization, 0.8 = 80%. |
| `appoperator_app_hpa_condition` | gauge | `condition`, `status` | One-hot status of `AbleToScale`, `ScalingActive`, `ScalingLimited`. |
| `appoperator_app_hpa_last_scale_timestamp_seconds` | gauge | | Time of the last scale event. |

### Disk (`spec.disk`)

| Metric | Type | Description |
|---|---|---|
| `appoperator_app_disk_requested_bytes` | gauge | Storage the PVC requests. |
| `appoperator_app_disk_capacity_bytes` | gauge | Storage provisioned. It lags `requested` during an expansion. |
| `appoperator_app_disk_bound` | gauge | 1 when the PVC is `Bound`. |
| `appoperator_app_disk_resize_pending` | gauge | 1 while a volume or filesystem resize is in progress. |

### Reconciliation

These count reconciles by the controller process currently exporting metrics, and they reset
when the leader changes.

| Metric | Type | Labels | Description |
|---|---|---|---|
| `appoperator_app_reconcile_total` | counter | `result` | Reconciles by result (`success`, `error`). |
| `appoperator_app_last_reconcile_timestamp_seconds` | gauge | | Time of the last reconcile. |
| `appoperator_app_last_successful_reconcile_timestamp_seconds` | gauge | | Time of the last successful reconcile. |
| `appoperator_app_last_reconcile_duration_seconds` | gauge | | Duration of the last reconcile. |

An `Available` app is only reconciled again when something changes, so an old
`last_reconcile_timestamp` alone is not a problem. To detect a stuck controller, compare
`metadata_generation` with `observed_generation` instead.

### Collector health

These series carry no app labels.

| Metric | Type | Description |
|---|---|---|
| `appoperator_app_metrics_collect_success` | gauge | 1 if the last scrape-time collection succeeded. |
| `appoperator_app_metrics_collect_duration_seconds` | gauge | Time the last collection took. |
| `appoperator_app_metrics_resource_usage_up` | gauge | 1 if usage could be read from `metrics.k8s.io`. Absent when `--app-metrics-resource-usage=false`. |

The controller also keeps its existing operator-level metrics (`appoperator_reconcile_step_duration_seconds`,
`appoperator_reconcile_step_errors_total`, `appoperator_managed_resource_prunes_total`) plus the
standard controller-runtime and Go runtime metrics.

## Example queries

```promql
# Apps not ready, excluding paused ones
appoperator_app_ready == 0 unless on (namespace, name) appoperator_app_status_phase{phase="Paused"} == 1

# Availability ratio per app
appoperator_app_replicas_available / appoperator_app_replicas_desired

# Memory usage as a fraction of limits, per container
sum by (namespace, name, container) (appoperator_app_container_memory_working_set_bytes)
  /
(sum by (namespace, name, container) (appoperator_app_container_resource_limits{resource="memory"})
  * on (namespace, name) group_left sum by (namespace, name) (appoperator_app_pods{phase="Running"}))

# CPU usage relative to requests (over 1 means bursting above requests)
sum by (namespace, name, container) (appoperator_app_container_cpu_usage_cores)
  /
(sum by (namespace, name, container) (appoperator_app_container_resource_requests{resource="cpu"})
  * on (namespace, name) group_left sum by (namespace, name) (appoperator_app_pods{phase="Running"}))

# Containers OOMKilled in the last 15 minutes
increase(appoperator_app_container_restarts_total[15m]) > 0
  and on (namespace, name, container) appoperator_app_container_last_terminated{reason="OOMKilled"} > 0

# Apps pinned at their HPA maximum
appoperator_app_hpa_current_replicas >= appoperator_app_autoscaling_max_replicas

# Images running per app
count by (namespace, name, image) (appoperator_app_container_info)
```

## Alerts

`config/prometheus/operator_alerts.yaml` ships these rules alongside the operator-level ones:

| Alert | Fires when |
|---|---|
| `AppDefinitionNotReady` | Not ready for 10m, excluding paused apps. |
| `AppDefinitionReconcileFailing` | Phase `Failed` for 10m. |
| `AppDefinitionSpecNotObserved` | `metadata_generation > observed_generation` for 15m. |
| `AppDefinitionRolloutStalled` | Rollout exceeded its progress deadline. |
| `AppDefinitionReplicasUnavailable` | Fewer available replicas than desired for 15m. |
| `AppDefinitionContainerCrashLooping` | A container in `CrashLoopBackOff` for 10m. |
| `AppDefinitionImagePullFailing` | A container stuck pulling its image for 10m. |
| `AppDefinitionContainerOOMKilled` | A container restarted after an OOM kill in the last 15m. |
| `AppDefinitionMemoryNearLimit` | Working set above 90% of limits for 15m (info). |
| `AppDefinitionAutoscalingAtMax` | HPA pinned at `maxReplicas` and `ScalingLimited` for 30m (info). |
| `AppDefinitionDiskNotBound` | PVC unbound for 15m. |
| `AppOperatorAppMetricsCollectFailing` | Per-app metrics collection failing for 10m. |

Every rule except `AppDefinitionMemoryNearLimit` (which aggregates) keeps the allowlisted
`label_<key>` labels on its alerts. That lets Alertmanager route on them directly, for example by
`label_team`.

## Dashboard

`config/grafana/appoperator-apps-dashboard.json` is a Grafana dashboard with a fleet overview,
an apps table, and rows for rollout, pods, resource usage, autoscaling, disk and reconciliation.
It has namespace and app variables plus an ad-hoc filter, which works with any exported
`label_<key>`. `kubectl apply -k config/grafana -n <grafana-namespace>` renders it as a ConfigMap
labeled `grafana_dashboard: "1"` for the kube-prometheus-stack Grafana sidecar. You can also
import the JSON through the Grafana UI.

## Cost

Each scrape lists AppDefinitions, Deployments, HPAs, PVCs and Pods from the in-memory cache, and
it makes no API calls apart from the single `metrics.k8s.io` list. The Pod informer only caches
pods labeled `app.kubernetes.io/name`, with managedFields stripped. It is only started on the
leader.
