package controller

import (
	"context"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
	"github.com/prometheus/client_golang/prometheus"
	dto "github.com/prometheus/client_model/go"
	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/resource"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/utils/ptr"
	"sigs.k8s.io/controller-runtime/pkg/reconcile"

	appdefinitionv1 "github.com/abexamir/app-operator/api/v1"
)

// gatherApp collects c through a private registry and returns every sample of every family.
func gatherApp(c prometheus.Collector) []*dto.MetricFamily {
	reg := prometheus.NewPedanticRegistry()
	Expect(reg.Register(c)).To(Succeed())
	families, err := reg.Gather()
	Expect(err).NotTo(HaveOccurred())
	return families
}

// findSample returns the sample of family name whose labels include all of want.
func findSample(families []*dto.MetricFamily, name string, want map[string]string) (*dto.Metric, bool) {
	for _, f := range families {
		if f.GetName() != name {
			continue
		}
	metrics:
		for _, m := range f.GetMetric() {
			got := map[string]string{}
			for _, lp := range m.GetLabel() {
				got[lp.GetName()] = lp.GetValue()
			}
			for k, v := range want {
				if got[k] != v {
					continue metrics
				}
			}
			return m, true
		}
	}
	return nil, false
}

func sampleValue(m *dto.Metric) float64 {
	switch {
	case m.GetGauge() != nil:
		return m.GetGauge().GetValue()
	case m.GetCounter() != nil:
		return m.GetCounter().GetValue()
	}
	return 0
}

var _ = Describe("AppMetricsCollector", func() {
	ctx := context.Background()
	const namespace = "default"

	It("exports per-app metrics carrying allowlisted labels, counting only the app's own pods", func() {
		nn := types.NamespacedName{Name: "metrics-app", Namespace: namespace}
		app := &appdefinitionv1.AppDefinition{
			ObjectMeta: metav1.ObjectMeta{
				Name: nn.Name, Namespace: namespace,
				Labels:      map[string]string{"team": "payments", "env": "prod"},
				Annotations: map[string]string{"owner.example.com/slack": "#payments"},
			},
			Spec: appdefinitionv1.AppDefinitionSpec{
				Containers: []appdefinitionv1.ContainerSpec{{
					Name:  "web",
					Image: "nginx:1.27",
					Resources: corev1.ResourceRequirements{
						Requests: corev1.ResourceList{
							corev1.ResourceCPU:    resource.MustParse("250m"),
							corev1.ResourceMemory: resource.MustParse("128Mi"),
						},
					},
				}},
				Domains: []appdefinitionv1.DomainSpec{{Name: "metrics.example.com", TLS: true}},
			},
		}
		Expect(k8sClient.Create(ctx, app)).To(Succeed())
		DeferCleanup(func() { _ = k8sClient.Delete(ctx, app) })

		r := &AppDefinitionReconciler{Client: k8sClient, Scheme: k8sClient.Scheme(), APIReader: k8sClient}
		for range 2 {
			_, err := r.Reconcile(ctx, reconcile.Request{NamespacedName: nn})
			Expect(err).NotTo(HaveOccurred())
		}

		// One pod created by the app's ReplicaSet, one unrelated pod sharing the well-known label.
		newPod := func(name, owner, hash string) {
			pod := &corev1.Pod{
				ObjectMeta: metav1.ObjectMeta{
					Name: name, Namespace: namespace,
					Labels: map[string]string{appNameLabel: nn.Name, appsv1.DefaultDeploymentUniqueLabelKey: hash},
					OwnerReferences: []metav1.OwnerReference{{
						APIVersion: "apps/v1", Kind: "ReplicaSet", Name: owner, UID: "rs-uid", Controller: ptr.To(true),
					}},
				},
				Spec: corev1.PodSpec{Containers: []corev1.Container{{Name: "web", Image: "nginx:1.27"}}},
			}
			Expect(k8sClient.Create(ctx, pod)).To(Succeed())
			DeferCleanup(func() { _ = k8sClient.Delete(ctx, pod) })
			pod.Status = corev1.PodStatus{
				Phase: corev1.PodRunning,
				ContainerStatuses: []corev1.ContainerStatus{{
					Name: "web", RestartCount: 3,
					State: corev1.ContainerState{Waiting: &corev1.ContainerStateWaiting{Reason: "CrashLoopBackOff"}},
					LastTerminationState: corev1.ContainerState{
						Terminated: &corev1.ContainerStateTerminated{Reason: "OOMKilled", ExitCode: 137},
					},
				}},
			}
			Expect(k8sClient.Status().Update(ctx, pod)).To(Succeed())
		}
		newPod("metrics-app-7d9f8b6c5-abcde", "metrics-app-7d9f8b6c5", "7d9f8b6c5")
		newPod("metrics-app-other-x1", "metrics-app-other-5f4d", "5f4d")

		c := newAppMetricsCollector(k8sClient, k8sClient, AppMetricsOptions{
			LabelAllowlist:      []string{"team"},
			AnnotationAllowlist: []string{"owner.example.com/slack"},
			ResourceUsage:       true,
		})

		By("emitting nothing until started as leader")
		Expect(gatherApp(c)).To(BeEmpty())

		c.active.Store(true)
		families := gatherApp(c)
		app1 := map[string]string{"namespace": namespace, "name": nn.Name}
		with := func(extra map[string]string) map[string]string {
			out := map[string]string{}
			for k, v := range app1 {
				out[k] = v
			}
			for k, v := range extra {
				out[k] = v
			}
			return out
		}
		expectValue := func(name string, labels map[string]string, value float64) {
			GinkgoHelper()
			m, ok := findSample(families, name, labels)
			Expect(ok).To(BeTrue(), "missing %s%v", name, labels)
			Expect(sampleValue(m)).To(Equal(value), "%s%v", name, labels)
		}

		By("attaching allowlisted labels to every app series and leaving others off")
		m, ok := findSample(families, "appoperator_app_ready", with(map[string]string{"label_team": "payments"}))
		Expect(ok).To(BeTrue())
		for _, lp := range m.GetLabel() {
			Expect(lp.GetName()).NotTo(Equal("label_env"))
		}
		expectValue("appoperator_app_labels", with(map[string]string{"label_team": "payments"}), 1)
		expectValue("appoperator_app_annotations", with(map[string]string{"annotation_owner_example_com_slack": "#payments"}), 1)
		expectValue("appoperator_app_metrics_collect_success", nil, 1)
		expectValue("appoperator_app_metrics_resource_usage_up", nil, 0) // envtest serves no metrics.k8s.io

		By("reporting metadata, status and spec")
		expectValue("appoperator_app_info", with(map[string]string{"stateful": "false", "autoscaling": "false", "uid": string(app.UID)}), 1)
		expectValue("appoperator_app_metadata_generation", app1, 1)
		expectValue("appoperator_app_observed_generation", app1, 1)
		expectValue("appoperator_app_status_phase", with(map[string]string{"phase": "Progressing"}), 1)
		expectValue("appoperator_app_status_phase", with(map[string]string{"phase": "Available"}), 0)
		expectValue("appoperator_app_status_condition", with(map[string]string{"condition": "Ready", "status": "false"}), 1)
		expectValue("appoperator_app_status_condition", with(map[string]string{"condition": "Ready", "status": "true"}), 0)
		expectValue("appoperator_app_spec_replicas", app1, 1)
		expectValue("appoperator_app_container_info", with(map[string]string{"container": "web", "type": "main", "image": "nginx:1.27"}), 1)
		expectValue("appoperator_app_container_resource_requests", with(map[string]string{"container": "web", "resource": "cpu", "unit": "core"}), 0.25)
		expectValue("appoperator_app_container_resource_requests", with(map[string]string{"container": "web", "resource": "memory", "unit": "byte"}), 128*1024*1024)
		expectValue("appoperator_app_domain_info", with(map[string]string{"domain": "metrics.example.com", "path": "/", "tls": "true"}), 1)

		By("reporting the Deployment rollout")
		expectValue("appoperator_app_replicas_desired", app1, 1)
		expectValue("appoperator_app_replicas_ready", app1, 0)
		expectValue("appoperator_app_rollout_in_progress", app1, 1)
		expectValue("appoperator_app_rollout_stalled", app1, 0)

		By("aggregating only the app's own pods")
		expectValue("appoperator_app_pods", with(map[string]string{"phase": "Running"}), 1)
		expectValue("appoperator_app_pods", with(map[string]string{"phase": "Pending"}), 0)
		expectValue("appoperator_app_container_restarts_total", with(map[string]string{"container": "web"}), 3)
		expectValue("appoperator_app_container_waiting", with(map[string]string{"container": "web", "reason": "CrashLoopBackOff"}), 1)
		expectValue("appoperator_app_container_last_terminated", with(map[string]string{"container": "web", "reason": "OOMKilled"}), 1)
		expectValue("appoperator_app_container_ready", with(map[string]string{"container": "web"}), 0)

		By("counting this process's reconciles")
		m, ok = findSample(families, "appoperator_app_reconcile_total", with(map[string]string{"result": "success"}))
		Expect(ok).To(BeTrue())
		Expect(sampleValue(m)).To(BeNumerically(">=", 2))
		_, ok = findSample(families, "appoperator_app_last_successful_reconcile_timestamp_seconds", app1)
		Expect(ok).To(BeTrue())

		By("dropping the app's series, including reconcile stats, once it is deleted")
		Expect(k8sClient.Delete(ctx, app)).To(Succeed())
		for range 2 {
			_, err := r.Reconcile(ctx, reconcile.Request{NamespacedName: nn})
			Expect(err).NotTo(HaveOccurred())
		}
		_, tracked := appReconcileStats.get(nn)
		Expect(tracked).To(BeFalse())
		_, ok = findSample(gatherApp(c), "appoperator_app_info", app1)
		Expect(ok).To(BeFalse())
	})

	It("reports HPA-driven state for autoscaled apps", func() {
		nn := types.NamespacedName{Name: "metrics-hpa-app", Namespace: namespace}
		app := &appdefinitionv1.AppDefinition{
			ObjectMeta: metav1.ObjectMeta{Name: nn.Name, Namespace: namespace},
			Spec: appdefinitionv1.AppDefinitionSpec{
				Containers: []appdefinitionv1.ContainerSpec{{Name: "web", Image: "nginx"}},
				Replicas:   ptr.To(int32(2)),
				Autoscaling: &appdefinitionv1.AutoscalingSpec{
					Enabled: true, MaxReplicas: 6, TargetCPUUtilizationPercentage: ptr.To(int32(75)),
				},
			},
		}
		Expect(k8sClient.Create(ctx, app)).To(Succeed())
		DeferCleanup(func() { _ = k8sClient.Delete(ctx, app) })
		r := &AppDefinitionReconciler{Client: k8sClient, Scheme: k8sClient.Scheme(), APIReader: k8sClient}
		for range 2 {
			_, err := r.Reconcile(ctx, reconcile.Request{NamespacedName: nn})
			Expect(err).NotTo(HaveOccurred())
		}

		c := newAppMetricsCollector(k8sClient, k8sClient, AppMetricsOptions{})
		c.active.Store(true)
		families := gatherApp(c)
		app1 := map[string]string{"namespace": namespace, "name": nn.Name}

		for name, want := range map[string]float64{
			"appoperator_app_autoscaling_min_replicas": 2,
			"appoperator_app_autoscaling_max_replicas": 6,
			"appoperator_app_hpa_current_replicas":     0,
			"appoperator_app_replicas_desired":         2,
		} {
			m, ok := findSample(families, name, app1)
			Expect(ok).To(BeTrue(), name)
			Expect(sampleValue(m)).To(Equal(want), name)
		}
		m, ok := findSample(families, "appoperator_app_autoscaling_target_utilization_ratio",
			map[string]string{"namespace": namespace, "name": nn.Name, "resource": "cpu"})
		Expect(ok).To(BeTrue())
		Expect(sampleValue(m)).To(Equal(0.75))

		By("not exporting resource usage when it is disabled")
		_, ok = findSample(families, "appoperator_app_metrics_resource_usage_up", nil)
		Expect(ok).To(BeFalse())
	})
})

var _ = Describe("app metrics helpers", func() {
	It("drops duplicate series instead of failing the scrape", func() {
		ch := make(chan prometheus.Metric, 10)
		w := newAppMetricWriter(ch, nil, AppMetricsOptions{})
		w.appValues = []string{"ns", "app"}
		w.write(mDomainInfo, 1, "a.example.com", "/", "true")
		w.write(mDomainInfo, 1, "a.example.com", "/", "true")
		Expect(ch).To(HaveLen(1))
	})

	It("sanitizes label keys and keeps the first of colliding names", func() {
		apps := []appdefinitionv1.AppDefinition{
			{ObjectMeta: metav1.ObjectMeta{Labels: map[string]string{"app.kubernetes.io/part-of": "shop", "team": "x"}}},
			{ObjectMeta: metav1.ObjectMeta{Labels: map[string]string{"app_kubernetes_io/part-of": "y"}}},
		}
		keys, names := unionKeys(apps, "label_", newAllowlist([]string{"*"}),
			func(a *appdefinitionv1.AppDefinition) map[string]string { return a.Labels })
		Expect(keys).To(Equal([]string{"app.kubernetes.io/part-of", "team"}))
		Expect(names).To(Equal([]string{"label_app_kubernetes_io_part_of", "label_team"}))

		keys, _ = unionKeys(apps, "label_", newAllowlist(nil),
			func(a *appdefinitionv1.AppDefinition) map[string]string { return a.Labels })
		Expect(keys).To(BeEmpty())
	})

	It("parses metrics.k8s.io PodMetrics", func() {
		list := &unstructured.UnstructuredList{Items: []unstructured.Unstructured{{Object: map[string]interface{}{
			"metadata": map[string]interface{}{"name": "pod-1", "namespace": "ns"},
			"containers": []interface{}{
				map[string]interface{}{"name": "web", "usage": map[string]interface{}{"cpu": "250000000n", "memory": "64Mi"}},
			},
		}}}}
		usage := podUsageFromList(list)
		u := usage[types.NamespacedName{Namespace: "ns", Name: "pod-1"}]["web"]
		Expect(u.cpuCores).To(BeNumerically("~", 0.25, 1e-9))
		Expect(u.memoryBytes).To(Equal(float64(64 * 1024 * 1024)))
	})

	It("parses comma-separated allowlists", func() {
		Expect(ParseAllowlist(" team, env ,,")).To(Equal([]string{"team", "env"}))
		Expect(ParseAllowlist("")).To(BeEmpty())
	})
})
