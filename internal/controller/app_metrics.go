package controller

import (
	"context"
	"fmt"
	"sort"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/prometheus/client_golang/prometheus"
	appsv1 "k8s.io/api/apps/v1"
	autoscalingv2 "k8s.io/api/autoscaling/v2"
	corev1 "k8s.io/api/core/v1"
	apimeta "k8s.io/apimachinery/pkg/api/meta"
	"k8s.io/apimachinery/pkg/api/resource"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/labels"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/apimachinery/pkg/selection"
	"k8s.io/apimachinery/pkg/types"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/cache"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/log"
	crmetrics "sigs.k8s.io/controller-runtime/pkg/metrics"

	v1 "github.com/abexamir/app-operator/api/v1"
)

// Per-AppDefinition metrics are produced at scrape time by AppMetricsCollector, in the style of
// kube-state-metrics, rather than pushed into GaugeVecs during reconciliation. Reading the
// informer cache on every scrape means a deleted app's series disappear with it (no stale
// series to forget), the values never lag behind an event the reconciler hasn't processed yet,
// and allowlisted AppDefinition labels can be attached to every series so apps can be filtered
// and grouped by them directly, e.g. appoperator_app_ready{label_team="payments"}.
//
// Every app metric carries namespace and name (the AppDefinition's), plus one label_<key> per
// allowlisted metadata.labels key (see AppMetricsOptions.LabelAllowlist). An app that lacks an
// allowlisted key gets an empty value, which Prometheus treats the same as an absent label, so
// enabling a new key never changes the identity of series for apps that don't set it.

const (
	appMetricsCollectTimeout = 10 * time.Second
	// appNameLabel is the pod label every AppDefinition's pods carry (see selectorLabels). The
	// Pod informer cache is restricted to pods that have it, see PodCacheByObject.
	appNameLabel = "app.kubernetes.io/name"
)

var podMetricsGVK = schema.GroupVersionKind{Group: "metrics.k8s.io", Version: "v1beta1", Kind: "PodMetricsList"}

// AppMetricsOptions configures AppMetricsCollector.
type AppMetricsOptions struct {
	// LabelAllowlist lists AppDefinition metadata.labels keys exported as label_<key> on every
	// app metric. "*" exports all keys. Empty exports none. Each allowlisted key multiplies
	// series only by the number of distinct values it takes, but avoid high-cardinality keys.
	LabelAllowlist []string
	// AnnotationAllowlist lists AppDefinition metadata.annotations keys exported as
	// annotation_<key> on appoperator_app_annotations only. "*" exports all keys.
	AnnotationAllowlist []string
	// ResourceUsage queries the metrics.k8s.io API (metrics-server) on every scrape to export
	// per-container CPU and memory usage. Skipped when that API isn't served.
	ResourceUsage bool
}

// ParseAllowlist splits a comma-separated flag value into its trimmed, non-empty entries.
func ParseAllowlist(s string) []string {
	var out []string
	for _, entry := range strings.Split(s, ",") {
		if entry = strings.TrimSpace(entry); entry != "" {
			out = append(out, entry)
		}
	}
	return out
}

// PodCacheByObject restricts the manager's Pod informer to pods carrying the label every
// AppDefinition pod has, and drops managedFields, so the per-app pod metrics don't cost a
// cluster-wide cache of every pod.
func PodCacheByObject() cache.ByObject {
	req, err := labels.NewRequirement(appNameLabel, selection.Exists, nil)
	if err != nil {
		panic(err) // static input; cannot fail
	}
	return cache.ByObject{
		Label:     labels.NewSelector().Add(*req),
		Transform: cache.TransformStripManagedFields(),
	}
}

// SetupAppMetrics registers an AppMetricsCollector with the controller-runtime metrics registry
// and adds it to the manager. It only emits while this replica holds the leader lease, so
// running several controller replicas doesn't duplicate every app series.
func SetupAppMetrics(mgr ctrl.Manager, opts AppMetricsOptions) error {
	c := newAppMetricsCollector(mgr.GetClient(), mgr.GetAPIReader(), opts)
	c.informers = mgr.GetCache()
	if err := crmetrics.Registry.Register(c); err != nil {
		return fmt.Errorf("registering app metrics collector: %w", err)
	}
	return mgr.Add(c)
}

// AppMetricsCollector is a prometheus.Collector exporting per-AppDefinition metrics read from
// the manager's cache (AppDefinitions, Deployments, Pods, HPAs, PVCs) and, optionally, the
// metrics.k8s.io API. It is also a leader-election manager.Runnable that enables collection.
type AppMetricsCollector struct {
	reader    client.Reader
	apiReader client.Reader
	informers cache.Informers
	opts      AppMetricsOptions
	active    atomic.Bool
}

func newAppMetricsCollector(reader, apiReader client.Reader, opts AppMetricsOptions) *AppMetricsCollector {
	return &AppMetricsCollector{reader: reader, apiReader: apiReader, opts: opts}
}

// Start warms the Pod informer, so the first scrape doesn't block on its initial list, then
// enables collection until this replica loses leadership.
func (c *AppMetricsCollector) Start(ctx context.Context) error {
	if c.informers != nil {
		if _, err := c.informers.GetInformer(ctx, &corev1.Pod{}); err != nil {
			return fmt.Errorf("starting Pod informer for app metrics: %w", err)
		}
	}
	c.active.Store(true)
	<-ctx.Done()
	c.active.Store(false)
	return nil
}

// NeedLeaderElection makes the manager start this runnable only on the leader.
func (c *AppMetricsCollector) NeedLeaderElection() bool { return true }

// Describe sends nothing, making this an unchecked collector: the set of label_<key> labels
// depends on the AppDefinitions present at scrape time.
func (c *AppMetricsCollector) Describe(chan<- *prometheus.Desc) {}

// Collect implements prometheus.Collector.
func (c *AppMetricsCollector) Collect(ch chan<- prometheus.Metric) {
	if !c.active.Load() {
		return
	}
	ctx, cancel := context.WithTimeout(context.Background(), appMetricsCollectTimeout)
	defer cancel()

	started := time.Now()
	success := 1.0
	if err := c.collect(ctx, ch); err != nil {
		success = 0
		log.Log.WithName("app-metrics").Error(err, "collecting AppDefinition metrics")
	}
	ch <- prometheus.MustNewConstMetric(collectSuccessDesc, prometheus.GaugeValue, success)
	ch <- prometheus.MustNewConstMetric(collectDurationDesc, prometheus.GaugeValue, time.Since(started).Seconds())
}

var (
	collectSuccessDesc = prometheus.NewDesc("appoperator_app_metrics_collect_success",
		"Whether the last collection of AppDefinition metrics succeeded (1) or failed (0).", nil, nil)
	collectDurationDesc = prometheus.NewDesc("appoperator_app_metrics_collect_duration_seconds",
		"Time taken to collect AppDefinition metrics.", nil, nil)
	resourceUsageUpDesc = prometheus.NewDesc("appoperator_app_metrics_resource_usage_up",
		"Whether container resource usage could be read from the metrics.k8s.io API (1) or not (0).", nil, nil)
)

// clusterState is everything one scrape reads, indexed by namespace/name.
type clusterState struct {
	deployments map[types.NamespacedName]*appsv1.Deployment
	hpas        map[types.NamespacedName]*autoscalingv2.HorizontalPodAutoscaler
	pvcs        map[types.NamespacedName]*corev1.PersistentVolumeClaim
	// pods is keyed by namespace and app.kubernetes.io/name label value.
	pods map[types.NamespacedName][]*corev1.Pod
	// usage is keyed by namespace and pod name; nil when resource usage isn't available.
	usage map[types.NamespacedName]map[string]containerUsage
}

type containerUsage struct {
	cpuCores    float64
	memoryBytes float64
}

func (c *AppMetricsCollector) collect(ctx context.Context, ch chan<- prometheus.Metric) error {
	apps := &v1.AppDefinitionList{}
	if err := c.reader.List(ctx, apps); err != nil {
		return fmt.Errorf("listing AppDefinitions: %w", err)
	}
	state, err := c.readClusterState(ctx)
	if err != nil {
		return err
	}
	if c.opts.ResourceUsage {
		up := 0.0
		if state.usage != nil {
			up = 1
		}
		ch <- prometheus.MustNewConstMetric(resourceUsageUpDesc, prometheus.GaugeValue, up)
	}

	w := newAppMetricWriter(ch, apps.Items, c.opts)
	for i := range apps.Items {
		w.writeApp(&apps.Items[i], state)
	}
	return nil
}

func (c *AppMetricsCollector) readClusterState(ctx context.Context) (*clusterState, error) {
	state := &clusterState{
		deployments: map[types.NamespacedName]*appsv1.Deployment{},
		hpas:        map[types.NamespacedName]*autoscalingv2.HorizontalPodAutoscaler{},
		pvcs:        map[types.NamespacedName]*corev1.PersistentVolumeClaim{},
		pods:        map[types.NamespacedName][]*corev1.Pod{},
	}

	deployments := &appsv1.DeploymentList{}
	if err := c.reader.List(ctx, deployments, client.MatchingLabels{"app.kubernetes.io/managed-by": "app-operator"}); err != nil {
		return nil, fmt.Errorf("listing Deployments: %w", err)
	}
	for i := range deployments.Items {
		state.deployments[client.ObjectKeyFromObject(&deployments.Items[i])] = &deployments.Items[i]
	}

	hpas := &autoscalingv2.HorizontalPodAutoscalerList{}
	if err := c.reader.List(ctx, hpas, client.MatchingLabels{"app.kubernetes.io/managed-by": "app-operator"}); err != nil {
		return nil, fmt.Errorf("listing HorizontalPodAutoscalers: %w", err)
	}
	for i := range hpas.Items {
		state.hpas[client.ObjectKeyFromObject(&hpas.Items[i])] = &hpas.Items[i]
	}

	pvcs := &corev1.PersistentVolumeClaimList{}
	if err := c.reader.List(ctx, pvcs, client.MatchingLabels{"app.kubernetes.io/managed-by": "app-operator"}); err != nil {
		return nil, fmt.Errorf("listing PersistentVolumeClaims: %w", err)
	}
	for i := range pvcs.Items {
		state.pvcs[client.ObjectKeyFromObject(&pvcs.Items[i])] = &pvcs.Items[i]
	}

	pods := &corev1.PodList{}
	if err := c.reader.List(ctx, pods, client.HasLabels{appNameLabel}); err != nil {
		return nil, fmt.Errorf("listing Pods: %w", err)
	}
	for i := range pods.Items {
		pod := &pods.Items[i]
		key := types.NamespacedName{Namespace: pod.Namespace, Name: pod.Labels[appNameLabel]}
		state.pods[key] = append(state.pods[key], pod)
	}

	if c.opts.ResourceUsage {
		state.usage = c.readResourceUsage(ctx)
	}
	return state, nil
}

// readResourceUsage lists PodMetrics for every pod carrying the app label in one call. It
// returns nil when metrics.k8s.io isn't served or the call fails; usage metrics are then
// omitted and appoperator_app_metrics_resource_usage_up reports 0.
func (c *AppMetricsCollector) readResourceUsage(ctx context.Context) map[types.NamespacedName]map[string]containerUsage {
	list := &unstructured.UnstructuredList{}
	list.SetGroupVersionKind(podMetricsGVK)
	if err := c.apiReader.List(ctx, list, client.HasLabels{appNameLabel}); err != nil {
		if !apimeta.IsNoMatchError(err) {
			log.Log.WithName("app-metrics").V(1).Info("reading pod resource usage from metrics.k8s.io", "error", err.Error())
		}
		return nil
	}
	return podUsageFromList(list)
}

// podUsageFromList parses a metrics.k8s.io PodMetricsList into per-pod, per-container usage.
func podUsageFromList(list *unstructured.UnstructuredList) map[types.NamespacedName]map[string]containerUsage {
	usage := map[types.NamespacedName]map[string]containerUsage{}
	for _, item := range list.Items {
		containers, _, _ := unstructured.NestedSlice(item.Object, "containers")
		perContainer := map[string]containerUsage{}
		for _, raw := range containers {
			container, ok := raw.(map[string]interface{})
			if !ok {
				continue
			}
			name, _, _ := unstructured.NestedString(container, "name")
			cpu, _, _ := unstructured.NestedString(container, "usage", "cpu")
			memory, _, _ := unstructured.NestedString(container, "usage", "memory")
			var u containerUsage
			if q, err := resource.ParseQuantity(cpu); err == nil {
				u.cpuCores = q.AsApproximateFloat64()
			}
			if q, err := resource.ParseQuantity(memory); err == nil {
				u.memoryBytes = q.AsApproximateFloat64()
			}
			perContainer[name] = u
		}
		usage[types.NamespacedName{Namespace: item.GetNamespace(), Name: item.GetName()}] = perContainer
	}
	return usage
}

// metricDef describes one exported metric family. labels are its own variable labels, between
// the leading namespace/name and the trailing label_<key> labels.
type metricDef struct {
	name   string
	help   string
	typ    prometheus.ValueType
	labels []string
}

func gaugeDef(name, help string, labelNames ...string) metricDef {
	return metricDef{name: name, help: help, typ: prometheus.GaugeValue, labels: labelNames}
}

func counterDef(name, help string, labelNames ...string) metricDef {
	return metricDef{name: name, help: help, typ: prometheus.CounterValue, labels: labelNames}
}

// Exported per-app metric families. docs/metrics.md documents each one with example queries;
// keep the two in sync.
var (
	// Metadata and status.
	mAppInfo = gaugeDef("appoperator_app_info",
		"Information about an AppDefinition. Always 1.",
		"uid", "stateful", "autoscaling", "paused", "service_account", "service_type", "ingress_class")
	mAppLabels = gaugeDef("appoperator_app_labels",
		"Allowlisted AppDefinition labels, as label_<key>. Always 1.")
	mAppAnnotations = gaugeDef("appoperator_app_annotations",
		"Allowlisted AppDefinition annotations, as annotation_<key>. Always 1. Unlike label_<key>, not attached to other metrics.")
	mAppCreated = gaugeDef("appoperator_app_created_timestamp_seconds",
		"Unix creation timestamp of the AppDefinition.")
	mAppGeneration = gaugeDef("appoperator_app_metadata_generation",
		"Current metadata.generation of the AppDefinition.")
	mAppObservedGeneration = gaugeDef("appoperator_app_observed_generation",
		"Latest AppDefinition generation observed by the controller (status.observedGeneration).")
	mAppReady = gaugeDef("appoperator_app_ready",
		"Whether the AppDefinition's Ready condition is True (1) or not (0).")
	mAppPhase = gaugeDef("appoperator_app_status_phase",
		"The AppDefinition's current status.phase. 1 for the current phase, 0 for the others.", "phase")
	mAppCondition = gaugeDef("appoperator_app_status_condition",
		"The AppDefinition's status conditions. 1 for the condition's current status, 0 for the others.",
		"condition", "status")
	mAppConditionTransition = gaugeDef("appoperator_app_status_condition_last_transition_timestamp_seconds",
		"Unix timestamp of a status condition's last transition.", "condition")

	// Desired state from the spec.
	mSpecReplicas = gaugeDef("appoperator_app_spec_replicas",
		"Replica count requested by spec.replicas (defaults to 1). Under an HPA this is only the starting count.")
	mContainerInfo = gaugeDef("appoperator_app_container_info",
		"Information about a container in the AppDefinition's pod template. Always 1. type is main, sidecar or init.",
		"container", "type", "image")
	mContainerRequests = gaugeDef("appoperator_app_container_resource_requests",
		"Resource requests of a container per pod, from the spec. CPU in cores, memory and storage in bytes.",
		"container", "resource", "unit")
	mContainerLimits = gaugeDef("appoperator_app_container_resource_limits",
		"Resource limits of a container per pod, from the spec. CPU in cores, memory and storage in bytes.",
		"container", "resource", "unit")
	mDomainInfo = gaugeDef("appoperator_app_domain_info",
		"A domain routed to the app through an Ingress. Always 1.", "domain", "path", "tls")
	mAutoscalingMin = gaugeDef("appoperator_app_autoscaling_min_replicas",
		"Effective HPA minReplicas when autoscaling is enabled.")
	mAutoscalingMax = gaugeDef("appoperator_app_autoscaling_max_replicas",
		"HPA maxReplicas when autoscaling is enabled.")
	mAutoscalingTarget = gaugeDef("appoperator_app_autoscaling_target_utilization_ratio",
		"HPA target average utilization, as a ratio of requests (0.8 = 80%).", "resource")

	// Deployment rollout state.
	mReplicasDesired = gaugeDef("appoperator_app_replicas_desired",
		"Replica count the Deployment is currently asked for (set by the HPA when autoscaling is enabled).")
	mReplicasCurrent = gaugeDef("appoperator_app_replicas_current",
		"Pods currently created by the Deployment.")
	mReplicasReady = gaugeDef("appoperator_app_replicas_ready",
		"Ready pods of the Deployment.")
	mReplicasAvailable = gaugeDef("appoperator_app_replicas_available",
		"Available pods of the Deployment (ready for at least minReadySeconds).")
	mReplicasUnavailable = gaugeDef("appoperator_app_replicas_unavailable",
		"Pods the Deployment still needs before it is fully available.")
	mReplicasUpdated = gaugeDef("appoperator_app_replicas_updated",
		"Pods running the latest pod template.")
	mRolloutInProgress = gaugeDef("appoperator_app_rollout_in_progress",
		"Whether a Deployment rollout is in progress (1) or complete (0).")
	mRolloutStalled = gaugeDef("appoperator_app_rollout_stalled",
		"Whether the Deployment rollout exceeded its progress deadline (1) or not (0).")

	// Pods.
	mPods = gaugeDef("appoperator_app_pods",
		"Number of the app's pods in each phase.", "phase")
	mPodsReady = gaugeDef("appoperator_app_pods_ready",
		"Number of the app's pods with a Ready condition.")
	mContainerReady = gaugeDef("appoperator_app_container_ready",
		"Number of ready instances of a container across the app's pods.", "container")
	mContainerRestarts = counterDef("appoperator_app_container_restarts_total",
		"Container restarts summed across the app's current pods. Drops when pods are replaced, which rate() treats as a counter reset.",
		"container")
	mContainerWaiting = gaugeDef("appoperator_app_container_waiting",
		"Number of instances of a container waiting, by reason (CrashLoopBackOff, ImagePullBackOff, ...).",
		"container", "reason")
	mContainerLastTerminated = gaugeDef("appoperator_app_container_last_terminated",
		"Number of instances of a container whose previous run terminated, by reason (OOMKilled, Error, ...).",
		"container", "reason")
	mContainerCPUUsage = gaugeDef("appoperator_app_container_cpu_usage_cores",
		"CPU usage of a container summed across the app's pods, from metrics.k8s.io.", "container")
	mContainerMemoryUsage = gaugeDef("appoperator_app_container_memory_working_set_bytes",
		"Memory working set of a container summed across the app's pods, from metrics.k8s.io.", "container")

	// HPA runtime state.
	mHPACurrentReplicas = gaugeDef("appoperator_app_hpa_current_replicas",
		"Current replica count observed by the HPA.")
	mHPADesiredReplicas = gaugeDef("appoperator_app_hpa_desired_replicas",
		"Replica count the HPA last computed.")
	mHPACurrentUtilization = gaugeDef("appoperator_app_hpa_current_utilization_ratio",
		"Current average utilization the HPA observed, as a ratio of requests (0.8 = 80%).", "resource")
	mHPACondition = gaugeDef("appoperator_app_hpa_condition",
		"The HPA's status conditions (AbleToScale, ScalingActive, ScalingLimited). 1 for the current status, 0 for the others.",
		"condition", "status")
	mHPALastScale = gaugeDef("appoperator_app_hpa_last_scale_timestamp_seconds",
		"Unix timestamp of the HPA's last scale event.")

	// Persistent disk.
	mPVCRequested = gaugeDef("appoperator_app_disk_requested_bytes",
		"Storage requested by the app's PVC.")
	mPVCCapacity = gaugeDef("appoperator_app_disk_capacity_bytes",
		"Storage capacity provisioned for the app's PVC.")
	mPVCBound = gaugeDef("appoperator_app_disk_bound",
		"Whether the app's PVC is Bound (1) or not (0).")
	mPVCResizing = gaugeDef("appoperator_app_disk_resize_pending",
		"Whether the app's PVC has a resize in progress (1) or not (0).")

	// Reconciliation of this AppDefinition by this controller process.
	mReconcileTotal = counterDef("appoperator_app_reconcile_total",
		"Reconciliations of the AppDefinition by this controller process, by result.", "result")
	mLastReconcile = gaugeDef("appoperator_app_last_reconcile_timestamp_seconds",
		"Unix timestamp of the AppDefinition's last reconciliation.")
	mLastSuccessfulReconcile = gaugeDef("appoperator_app_last_successful_reconcile_timestamp_seconds",
		"Unix timestamp of the AppDefinition's last successful reconciliation.")
	mLastReconcileDuration = gaugeDef("appoperator_app_last_reconcile_duration_seconds",
		"Duration of the AppDefinition's last reconciliation.")
)

var (
	knownAppPhases = []string{"Available", "Progressing", "Failed", "Paused"}
	knownPodPhases = []corev1.PodPhase{corev1.PodPending, corev1.PodRunning, corev1.PodSucceeded, corev1.PodFailed, corev1.PodUnknown}
)

// appMetricWriter emits metrics for one scrape. It owns the scrape's label_<key> set and drops
// duplicate series: the metrics endpoint fails the whole scrape on a duplicate, so one app with,
// say, two domains entries with the same host and path must not take every other metric down.
type appMetricWriter struct {
	ch chan<- prometheus.Metric

	labelAllow      allowlist
	labelKeys       []string // allowlisted metadata.labels keys, in emission order
	labelNames      []string // label_<key> names, parallel to labelKeys
	annotationAllow allowlist
	annotationKeys  []string
	annotationNames []string

	descs map[string]*prometheus.Desc
	seen  map[string]struct{}

	// Per-app values, set by writeApp.
	appValues []string // namespace, name
	appLabels []string // parallel to labelKeys
}

type allowlist struct {
	all  bool
	keys map[string]struct{}
}

func newAllowlist(entries []string) allowlist {
	a := allowlist{keys: map[string]struct{}{}}
	for _, e := range entries {
		if e == "*" {
			a.all = true
		}
		a.keys[e] = struct{}{}
	}
	return a
}

func (a allowlist) allows(key string) bool {
	if a.all {
		return true
	}
	_, ok := a.keys[key]
	return ok
}

func newAppMetricWriter(ch chan<- prometheus.Metric, apps []v1.AppDefinition, opts AppMetricsOptions) *appMetricWriter {
	w := &appMetricWriter{
		ch:              ch,
		labelAllow:      newAllowlist(opts.LabelAllowlist),
		annotationAllow: newAllowlist(opts.AnnotationAllowlist),
		descs:           map[string]*prometheus.Desc{},
		seen:            map[string]struct{}{},
	}
	w.labelKeys, w.labelNames = unionKeys(apps, "label_", w.labelAllow, func(a *v1.AppDefinition) map[string]string { return a.Labels })
	w.annotationKeys, w.annotationNames = unionKeys(apps, "annotation_", w.annotationAllow, func(a *v1.AppDefinition) map[string]string { return a.Annotations })
	return w
}

// unionKeys returns the allowlisted keys present on any app, sorted, with their Prometheus
// label names. Keys that sanitize to the same name keep only the first in sort order.
func unionKeys(apps []v1.AppDefinition, prefix string, allow allowlist, get func(*v1.AppDefinition) map[string]string) ([]string, []string) {
	if !allow.all && len(allow.keys) == 0 {
		return nil, nil
	}
	set := map[string]struct{}{}
	for i := range apps {
		for k := range get(&apps[i]) {
			if allow.allows(k) {
				set[k] = struct{}{}
			}
		}
	}
	keys := make([]string, 0, len(set))
	for k := range set {
		keys = append(keys, k)
	}
	sort.Strings(keys)

	outKeys := make([]string, 0, len(keys))
	outNames := make([]string, 0, len(keys))
	taken := map[string]struct{}{}
	for _, k := range keys {
		name := prefix + sanitizeLabelName(k)
		if _, dup := taken[name]; dup {
			continue
		}
		taken[name] = struct{}{}
		outKeys = append(outKeys, k)
		outNames = append(outNames, name)
	}
	return outKeys, outNames
}

// sanitizeLabelName maps a Kubernetes label key onto the Prometheus label-name charset the way
// kube-state-metrics does: every character outside [a-zA-Z0-9_] becomes "_".
func sanitizeLabelName(s string) string {
	var b strings.Builder
	for _, r := range s {
		if (r >= 'a' && r <= 'z') || (r >= 'A' && r <= 'Z') || (r >= '0' && r <= '9') || r == '_' {
			b.WriteRune(r)
		} else {
			b.WriteRune('_')
		}
	}
	return b.String()
}

func (w *appMetricWriter) desc(m metricDef, extraNames []string) *prometheus.Desc {
	if d, ok := w.descs[m.name]; ok {
		return d
	}
	names := make([]string, 0, 2+len(m.labels)+len(extraNames))
	names = append(names, "namespace", "name")
	names = append(names, m.labels...)
	names = append(names, extraNames...)
	d := prometheus.NewDesc(m.name, m.help, names, nil)
	w.descs[m.name] = d
	return d
}

func (w *appMetricWriter) emit(m metricDef, extraNames, extraValues []string, value float64, labelValues ...string) {
	values := make([]string, 0, 2+len(labelValues)+len(extraValues))
	values = append(values, w.appValues...)
	values = append(values, labelValues...)
	values = append(values, extraValues...)

	key := m.name + "\xff" + strings.Join(values, "\xff")
	if _, dup := w.seen[key]; dup {
		return
	}
	w.seen[key] = struct{}{}
	w.ch <- prometheus.MustNewConstMetric(w.desc(m, extraNames), m.typ, value, values...)
}

// write emits one sample of m for the current app, with the app's label_<key> labels.
func (w *appMetricWriter) write(m metricDef, value float64, labelValues ...string) {
	w.emit(m, w.labelNames, w.appLabels, value, labelValues...)
}

func boolValue(b bool) float64 {
	if b {
		return 1
	}
	return 0
}

func (w *appMetricWriter) writeApp(app *v1.AppDefinition, state *clusterState) {
	key := client.ObjectKeyFromObject(app)
	w.appValues = []string{app.Namespace, app.Name}
	w.appLabels = make([]string, len(w.labelKeys))
	for i, k := range w.labelKeys {
		w.appLabels[i] = app.Labels[k]
	}

	w.writeMetadata(app)
	w.writeSpec(app)
	w.writeDeployment(app, ownedBy(state.deployments[key], app))
	w.writePods(app, state)
	w.writeHPA(ownedBy(state.hpas[key], app))
	w.writePVC(ownedBy(state.pvcs[types.NamespacedName{Namespace: app.Namespace, Name: pvcName(app.Name)}], app))
	w.writeReconcileStats(key)
}

// ownedBy returns obj only if it is controlled by app, so a same-named object left over from an
// earlier AppDefinition with the same name, or created by someone else, isn't reported.
func ownedBy[T client.Object](obj T, app *v1.AppDefinition) T {
	var zero T
	if any(obj) == any(zero) {
		return zero
	}
	if owner := metav1.GetControllerOf(obj); owner == nil || owner.UID != app.UID {
		return zero
	}
	return obj
}

func (w *appMetricWriter) writeMetadata(app *v1.AppDefinition) {
	w.write(mAppInfo, 1,
		string(app.UID),
		fmt.Sprint(isStateful(app)),
		fmt.Sprint(autoscalingEnabled(app)),
		fmt.Sprint(app.Spec.Paused),
		app.Spec.ServiceAccountName,
		string(app.Spec.ServiceType),
		app.Spec.IngressClass,
	)
	w.write(mAppLabels, 1)

	annotationValues := make([]string, len(w.annotationKeys))
	for i, k := range w.annotationKeys {
		annotationValues[i] = app.Annotations[k]
	}
	w.emit(mAppAnnotations, w.annotationNames, annotationValues, 1)

	w.write(mAppCreated, float64(app.CreationTimestamp.Unix()))
	w.write(mAppGeneration, float64(app.Generation))
	w.write(mAppObservedGeneration, float64(app.Status.ObservedGeneration))
	w.write(mAppReady, boolValue(apimeta.IsStatusConditionTrue(app.Status.Conditions, v1.ConditionTypeReady)))

	phases := knownAppPhases
	if p := app.Status.Phase; p != "" && !containsString(phases, p) {
		phases = append(append([]string{}, phases...), p)
	}
	for _, p := range phases {
		w.write(mAppPhase, boolValue(app.Status.Phase == p), p)
	}

	for _, cond := range app.Status.Conditions {
		writeConditionStatus(w, mAppCondition, cond.Type, cond.Status)
		w.write(mAppConditionTransition, float64(cond.LastTransitionTime.Unix()), cond.Type)
	}
}

// writeConditionStatus emits a one-hot true/false/unknown triple, kube-state-metrics style.
func writeConditionStatus(w *appMetricWriter, m metricDef, condition string, status metav1.ConditionStatus) {
	for _, s := range []metav1.ConditionStatus{metav1.ConditionTrue, metav1.ConditionFalse, metav1.ConditionUnknown} {
		w.write(m, boolValue(status == s), condition, strings.ToLower(string(s)))
	}
}

func containsString(list []string, s string) bool {
	for _, item := range list {
		if item == s {
			return true
		}
	}
	return false
}

func (w *appMetricWriter) writeSpec(app *v1.AppDefinition) {
	w.write(mSpecReplicas, float64(desiredReplicas(app)))

	for i, c := range app.Spec.Containers {
		containerType := "sidecar"
		if i == 0 {
			containerType = "main"
		}
		w.write(mContainerInfo, 1, c.Name, containerType, c.Image)
		w.writeResources(c.Name, c.Resources)
	}
	for _, c := range app.Spec.InitContainers {
		w.write(mContainerInfo, 1, c.Name, "init", c.Image)
		w.writeResources(c.Name, c.Resources)
	}

	for _, d := range app.Spec.Domains {
		path := d.Path
		if path == "" {
			path = "/"
		}
		w.write(mDomainInfo, 1, d.Name, path, fmt.Sprint(d.TLS))
	}

	if autoscalingEnabled(app) {
		as := app.Spec.Autoscaling
		w.write(mAutoscalingMin, float64(hpaMinReplicas(app)))
		w.write(mAutoscalingMax, float64(as.MaxReplicas))
		if as.TargetCPUUtilizationPercentage != nil {
			w.write(mAutoscalingTarget, float64(*as.TargetCPUUtilizationPercentage)/100, "cpu")
		}
		if as.TargetMemoryUtilizationPercentage != nil {
			w.write(mAutoscalingTarget, float64(*as.TargetMemoryUtilizationPercentage)/100, "memory")
		}
	}
}

func (w *appMetricWriter) writeResources(container string, res corev1.ResourceRequirements) {
	for _, name := range sortedResourceNames(res.Requests) {
		q := res.Requests[name]
		w.write(mContainerRequests, quantityValue(name, q), container, string(name), resourceUnit(name))
	}
	for _, name := range sortedResourceNames(res.Limits) {
		q := res.Limits[name]
		w.write(mContainerLimits, quantityValue(name, q), container, string(name), resourceUnit(name))
	}
}

func sortedResourceNames(list corev1.ResourceList) []corev1.ResourceName {
	names := make([]corev1.ResourceName, 0, len(list))
	for name := range list {
		names = append(names, name)
	}
	sort.Slice(names, func(i, j int) bool { return names[i] < names[j] })
	return names
}

func quantityValue(name corev1.ResourceName, q resource.Quantity) float64 {
	if name == corev1.ResourceCPU {
		return float64(q.MilliValue()) / 1000
	}
	return q.AsApproximateFloat64()
}

func resourceUnit(name corev1.ResourceName) string {
	switch name {
	case corev1.ResourceCPU:
		return "core"
	case corev1.ResourceMemory, corev1.ResourceEphemeralStorage, corev1.ResourceStorage:
		return "byte"
	default:
		return "integer"
	}
}

func (w *appMetricWriter) writeDeployment(app *v1.AppDefinition, d *appsv1.Deployment) {
	if d == nil {
		return
	}
	desired := int32(1)
	if d.Spec.Replicas != nil {
		desired = *d.Spec.Replicas
	}
	st := d.Status
	w.write(mReplicasDesired, float64(desired))
	w.write(mReplicasCurrent, float64(st.Replicas))
	w.write(mReplicasReady, float64(st.ReadyReplicas))
	w.write(mReplicasAvailable, float64(st.AvailableReplicas))
	w.write(mReplicasUnavailable, float64(st.UnavailableReplicas))
	w.write(mReplicasUpdated, float64(st.UpdatedReplicas))

	// Mirrors `kubectl rollout status`: the rollout is done once the controller has observed the
	// latest spec and every pod is updated and available with no old pods left over.
	inProgress := d.Generation > st.ObservedGeneration ||
		st.UpdatedReplicas < desired ||
		st.Replicas > st.UpdatedReplicas ||
		st.AvailableReplicas < st.UpdatedReplicas
	w.write(mRolloutInProgress, boolValue(inProgress && !app.Spec.Paused))

	stalled := false
	for _, cond := range st.Conditions {
		if cond.Type == appsv1.DeploymentProgressing && cond.Status == corev1.ConditionFalse && cond.Reason == "ProgressDeadlineExceeded" {
			stalled = true
		}
	}
	w.write(mRolloutStalled, boolValue(stalled))
}

// podBelongsToApp reports whether pod was created by the app's Deployment: its controller is a
// ReplicaSet named <app>-<pod-template-hash>. The app.kubernetes.io/name label alone isn't
// enough, since other workloads in the namespace may carry the same well-known label.
func podBelongsToApp(pod *corev1.Pod, appName string) bool {
	owner := metav1.GetControllerOf(pod)
	if owner == nil || owner.Kind != "ReplicaSet" {
		return false
	}
	hash, ok := strings.CutPrefix(owner.Name, appName+"-")
	return ok && hash != "" && hash == pod.Labels[appsv1.DefaultDeploymentUniqueLabelKey]
}

type containerPodStats struct {
	ready, restarts float64
	waiting         map[string]float64
	lastTerminated  map[string]float64
	usage           containerUsage
}

func (w *appMetricWriter) writePods(app *v1.AppDefinition, state *clusterState) {
	phaseCounts := map[corev1.PodPhase]float64{}
	var ready float64
	stats := map[string]*containerPodStats{}
	statsFor := func(name string) *containerPodStats {
		s, ok := stats[name]
		if !ok {
			s = &containerPodStats{waiting: map[string]float64{}, lastTerminated: map[string]float64{}}
			stats[name] = s
		}
		return s
	}
	// Seed every spec container so a container with no pods yet still reports zeros.
	for _, c := range app.Spec.Containers {
		statsFor(c.Name)
	}

	usageSeen := false
	for _, pod := range state.pods[client.ObjectKeyFromObject(app)] {
		if !podBelongsToApp(pod, app.Name) {
			continue
		}
		phaseCounts[pod.Status.Phase]++
		for _, cond := range pod.Status.Conditions {
			if cond.Type == corev1.PodReady && cond.Status == corev1.ConditionTrue {
				ready++
			}
		}
		statuses := append(append([]corev1.ContainerStatus{}, pod.Status.InitContainerStatuses...), pod.Status.ContainerStatuses...)
		for _, cs := range statuses {
			s := statsFor(cs.Name)
			s.restarts += float64(cs.RestartCount)
			if cs.Ready {
				s.ready++
			}
			if cs.State.Waiting != nil && cs.State.Waiting.Reason != "" {
				s.waiting[cs.State.Waiting.Reason]++
			}
			if t := cs.LastTerminationState.Terminated; t != nil && t.Reason != "" {
				s.lastTerminated[t.Reason]++
			}
		}
		if state.usage != nil {
			if perContainer, ok := state.usage[client.ObjectKeyFromObject(pod)]; ok {
				usageSeen = true
				for name, u := range perContainer {
					s := statsFor(name)
					s.usage.cpuCores += u.cpuCores
					s.usage.memoryBytes += u.memoryBytes
				}
			}
		}
	}

	for _, phase := range knownPodPhases {
		w.write(mPods, phaseCounts[phase], string(phase))
	}
	w.write(mPodsReady, ready)

	names := make([]string, 0, len(stats))
	for name := range stats {
		names = append(names, name)
	}
	sort.Strings(names)
	for _, name := range names {
		s := stats[name]
		w.write(mContainerReady, s.ready, name)
		w.write(mContainerRestarts, s.restarts, name)
		for _, reason := range sortedKeys(s.waiting) {
			w.write(mContainerWaiting, s.waiting[reason], name, reason)
		}
		for _, reason := range sortedKeys(s.lastTerminated) {
			w.write(mContainerLastTerminated, s.lastTerminated[reason], name, reason)
		}
		if usageSeen {
			w.write(mContainerCPUUsage, s.usage.cpuCores, name)
			w.write(mContainerMemoryUsage, s.usage.memoryBytes, name)
		}
	}
}

func sortedKeys(m map[string]float64) []string {
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	return keys
}

func (w *appMetricWriter) writeHPA(hpa *autoscalingv2.HorizontalPodAutoscaler) {
	if hpa == nil {
		return
	}
	w.write(mHPACurrentReplicas, float64(hpa.Status.CurrentReplicas))
	w.write(mHPADesiredReplicas, float64(hpa.Status.DesiredReplicas))
	for _, m := range hpa.Status.CurrentMetrics {
		if m.Type == autoscalingv2.ResourceMetricSourceType && m.Resource != nil && m.Resource.Current.AverageUtilization != nil {
			w.write(mHPACurrentUtilization, float64(*m.Resource.Current.AverageUtilization)/100, string(m.Resource.Name))
		}
	}
	for _, cond := range hpa.Status.Conditions {
		writeConditionStatus(w, mHPACondition, string(cond.Type), metav1.ConditionStatus(cond.Status))
	}
	if hpa.Status.LastScaleTime != nil {
		w.write(mHPALastScale, float64(hpa.Status.LastScaleTime.Unix()))
	}
}

func (w *appMetricWriter) writePVC(pvc *corev1.PersistentVolumeClaim) {
	if pvc == nil {
		return
	}
	if q, ok := pvc.Spec.Resources.Requests[corev1.ResourceStorage]; ok {
		w.write(mPVCRequested, q.AsApproximateFloat64())
	}
	if q, ok := pvc.Status.Capacity[corev1.ResourceStorage]; ok {
		w.write(mPVCCapacity, q.AsApproximateFloat64())
	}
	w.write(mPVCBound, boolValue(pvc.Status.Phase == corev1.ClaimBound))
	resizing := false
	for _, cond := range pvc.Status.Conditions {
		if (cond.Type == corev1.PersistentVolumeClaimResizing || cond.Type == corev1.PersistentVolumeClaimFileSystemResizePending) &&
			cond.Status == corev1.ConditionTrue {
			resizing = true
		}
	}
	w.write(mPVCResizing, boolValue(resizing))
}

func (w *appMetricWriter) writeReconcileStats(key types.NamespacedName) {
	rec, ok := appReconcileStats.get(key)
	if !ok {
		return
	}
	w.write(mReconcileTotal, rec.successes, "success")
	w.write(mReconcileTotal, rec.errors, "error")
	w.write(mLastReconcile, float64(rec.last.UnixNano())/1e9)
	if !rec.lastSuccess.IsZero() {
		w.write(mLastSuccessfulReconcile, float64(rec.lastSuccess.UnixNano())/1e9)
	}
	w.write(mLastReconcileDuration, rec.lastDuration.Seconds())
}

// reconcileStatsTracker keeps per-AppDefinition reconcile counters in memory for the collector.
// Entries are removed when the AppDefinition is gone (forgetAppMetrics).
type reconcileStatsTracker struct {
	mu    sync.Mutex
	byApp map[types.NamespacedName]reconcileRecord
}

type reconcileRecord struct {
	successes, errors float64
	last, lastSuccess time.Time
	lastDuration      time.Duration
}

var appReconcileStats = &reconcileStatsTracker{byApp: map[types.NamespacedName]reconcileRecord{}}

func (t *reconcileStatsTracker) observe(key types.NamespacedName, started time.Time, err error) {
	now := time.Now()
	t.mu.Lock()
	defer t.mu.Unlock()
	rec := t.byApp[key]
	rec.last = now
	rec.lastDuration = now.Sub(started)
	if err != nil {
		rec.errors++
	} else {
		rec.successes++
		rec.lastSuccess = now
	}
	t.byApp[key] = rec
}

func (t *reconcileStatsTracker) get(key types.NamespacedName) (reconcileRecord, bool) {
	t.mu.Lock()
	defer t.mu.Unlock()
	rec, ok := t.byApp[key]
	return rec, ok
}

func (t *reconcileStatsTracker) forget(key types.NamespacedName) {
	t.mu.Lock()
	defer t.mu.Unlock()
	delete(t.byApp, key)
}
