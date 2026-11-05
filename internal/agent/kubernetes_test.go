package agent

import (
	"context"
	"encoding/json"
	"strings"
	"testing"
	"time"

	batchv1 "k8s.io/api/batch/v1"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/kubernetes/fake"

	"github.com/bsteigleder/firstlume/internal/api"
)

func testConfig() KubernetesConfig {
	return KubernetesConfig{
		Namespace:        "firstlume",
		AgentName:        "default",
		Image:            "ghcr.io/bsteigleder/firstlume:test",
		ExecutorAPI:      "http://firstlume:8080",
		ServiceAccount:   "firstlume-executor",
		ActiveDeadline:   2 * time.Hour,
		TTLAfterFinished: time.Hour,
	}
}

var testAssignment = &api.Assignment{
	RunID: "0b9c7c1e-6f3a-4c1e-9d47-2a7f5e1c0d11",
	Phase: api.PhasePlan,
	Token: "flr_secret",
}

func TestBuildJob(t *testing.T) {
	secret, job := BuildJob(testConfig(), testAssignment)

	if len(job.Name) > 52 {
		t.Fatalf("job name %q too long for pod name suffixes", job.Name)
	}
	if *job.Spec.BackoffLimit != 0 {
		t.Fatal("jobs must never be retried")
	}
	pod := job.Spec.Template.Spec
	if pod.RestartPolicy != corev1.RestartPolicyNever || *pod.AutomountServiceAccountToken {
		t.Fatal("unexpected pod policy")
	}
	if pod.ServiceAccountName != "firstlume-executor" {
		t.Fatalf("service account = %q", pod.ServiceAccountName)
	}
	c := pod.Containers[0]
	if !*c.SecurityContext.ReadOnlyRootFilesystem || *c.SecurityContext.AllowPrivilegeEscalation {
		t.Fatal("container must be locked down")
	}
	for _, e := range c.Env {
		if e.Value == testAssignment.Token {
			t.Fatal("run token must come from the secret, not a literal env value")
		}
		if e.Name == "FIRSTLUME_RUN_TOKEN" && (e.ValueFrom == nil || e.ValueFrom.SecretKeyRef.Name != secret.Name) {
			t.Fatal("run token must reference the token secret")
		}
	}
	if secret.StringData[tokenKey] != testAssignment.Token {
		t.Fatal("secret does not hold the token")
	}
}

func TestKubernetesDriverLifecycle(t *testing.T) {
	ctx := context.Background()
	client := fake.NewClientset()
	d := NewKubernetesDriver(testConfig(), client)

	if err := d.Start(ctx, testAssignment); err != nil {
		t.Fatal(err)
	}
	// Starting again (e.g. after an agent restart) is idempotent.
	if err := d.Start(ctx, testAssignment); err != nil {
		t.Fatal(err)
	}
	if n, _ := d.Active(ctx); n != 1 {
		t.Fatalf("active = %d, want 1", n)
	}

	name := jobName(testAssignment)
	job, err := client.BatchV1().Jobs("firstlume").Get(ctx, name, metav1.GetOptions{})
	if err != nil {
		t.Fatal(err)
	}
	job.Status.Conditions = []batchv1.JobCondition{{Type: batchv1.JobFailed, Status: corev1.ConditionTrue, Reason: "DeadlineExceeded"}}
	if _, err := client.BatchV1().Jobs("firstlume").UpdateStatus(ctx, job, metav1.UpdateOptions{}); err != nil {
		t.Fatal(err)
	}

	failures, err := d.Failures(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if len(failures) != 1 || failures[0].RunID != testAssignment.RunID || failures[0].Phase != api.PhasePlan {
		t.Fatalf("failures = %+v", failures)
	}
	if again, _ := d.Failures(ctx); len(again) != 0 {
		t.Fatalf("failure reported twice: %+v", again)
	}
	if n, _ := d.Active(ctx); n != 0 {
		t.Fatalf("active = %d, want 0", n)
	}
}

func TestParseScheduling(t *testing.T) {
	// What the Helm chart renders with toJson.
	ns, tol, aff, err := ParseScheduling(`{"topology.kubernetes.io/zone":"sp1"}`,
		`[{"effect":"NoSchedule","key":"dedicated","operator":"Equal","value":"infra"}]`,
		`{"podAntiAffinity":{"preferredDuringSchedulingIgnoredDuringExecution":[{"podAffinityTerm":{"labelSelector":{"matchLabels":{"app.kubernetes.io/managed-by":"firstlume"}},"topologyKey":"kubernetes.io/hostname"},"weight":100}]}}`)
	if err != nil {
		t.Fatal(err)
	}
	cfg := testConfig()
	cfg.NodeSelector, cfg.Tolerations, cfg.Affinity = ns, tol, aff
	_, job := BuildJob(cfg, testAssignment)
	pod := job.Spec.Template.Spec
	if pod.NodeSelector["topology.kubernetes.io/zone"] != "sp1" {
		t.Fatalf("node selector %v", pod.NodeSelector)
	}
	if len(pod.Tolerations) != 1 || pod.Tolerations[0].Effect != corev1.TaintEffectNoSchedule || pod.Tolerations[0].Value != "infra" {
		t.Fatalf("tolerations %v", pod.Tolerations)
	}
	terms := pod.Affinity.PodAntiAffinity.PreferredDuringSchedulingIgnoredDuringExecution
	if len(terms) != 1 || terms[0].Weight != 100 || terms[0].PodAffinityTerm.TopologyKey != "kubernetes.io/hostname" {
		t.Fatalf("affinity %+v", pod.Affinity)
	}

	if ns, tol, aff, err := ParseScheduling("", " ", ""); err != nil || ns != nil || tol != nil || aff != nil {
		t.Fatalf("empty values: %v %v %v %v", ns, tol, aff, err)
	}
	if _, job := BuildJob(testConfig(), testAssignment); job.Spec.Template.Spec.Affinity != nil {
		t.Fatal("no affinity unless configured")
	}
	// A typo must not silently drop the constraint.
	if _, _, _, err := ParseScheduling("", "", `{"podAntiAfinity":{}}`); err == nil {
		t.Fatal("unknown affinity field accepted")
	}
	if _, _, _, err := ParseScheduling(`["sp1"]`, "", ""); err == nil {
		t.Fatal("malformed node selector accepted")
	}
}

func TestPlacementReplacesEachField(t *testing.T) {
	cfg := testConfig()
	cfg.AllowPlacement = true
	cfg.NodeSelector = map[string]string{"zone": "install"}
	cfg.Tolerations = []corev1.Toleration{{Key: "install", Operator: corev1.TolerationOpExists}}
	cfg.Affinity = &corev1.Affinity{PodAffinity: &corev1.PodAffinity{}}

	a := *testAssignment
	a.StackID = "5f3b6f0e-77b2-4f55-8a21-0b1f6a0b4d20"
	a.Placement = api.Placement{
		NodeSelector:    json.RawMessage(`{"zone":"east"}`),
		PodAntiAffinity: json.RawMessage(`{"preferredDuringSchedulingIgnoredDuringExecution":[{"weight":100,"podAffinityTerm":{"topologyKey":"kubernetes.io/hostname"}}]}`),
	}
	merged, err := cfg.withPlacement(a.Placement)
	if err != nil {
		t.Fatal(err)
	}
	_, job := BuildJob(merged, &a)
	pod := job.Spec.Template.Spec
	if len(pod.NodeSelector) != 1 || pod.NodeSelector["zone"] != "east" {
		t.Fatalf("a field replaces the installation's, without adding: %v", pod.NodeSelector)
	}
	if len(pod.Tolerations) != 1 || pod.Tolerations[0].Key != "install" {
		t.Fatalf("a field not set keeps the installation's: %v", pod.Tolerations)
	}
	if pod.Affinity.PodAffinity == nil || pod.Affinity.PodAntiAffinity == nil {
		t.Fatalf("affinity fields are independent: %+v", pod.Affinity)
	}
	if cfg.Affinity.PodAntiAffinity != nil {
		t.Fatal("a run's placement must not leak into the agent's configuration")
	}
	if job.Spec.Template.Labels[labelStack] != a.StackID {
		t.Fatalf("labels = %v", job.Spec.Template.Labels)
	}

	// An empty field cancels the installation's.
	merged, err = cfg.withPlacement(api.Placement{NodeSelector: json.RawMessage(`{}`), Tolerations: json.RawMessage(`[]`)})
	if err != nil {
		t.Fatal(err)
	}
	if _, job := BuildJob(merged, &a); len(job.Spec.Template.Spec.NodeSelector) != 0 || len(job.Spec.Template.Spec.Tolerations) != 0 {
		t.Fatalf("not canceled: %+v", job.Spec.Template.Spec)
	}
}

func TestPlacementNeedsPermission(t *testing.T) {
	cfg := testConfig() // allowPlacement off
	cfg.NodeSelector = map[string]string{"zone": "install"}
	d := NewKubernetesDriver(cfg, fake.NewClientset())

	a := *testAssignment
	a.Placement = api.Placement{NodeSelector: json.RawMessage(`{"zone":"east"}`)}
	if err := d.Start(context.Background(), &a); err == nil {
		t.Fatal("an agent that doesn't accept placement must refuse it")
	}

	// The installation's value still applies, and is what the agent reports.
	a.Placement = api.Placement{}
	if err := d.Start(context.Background(), &a); err != nil {
		t.Fatal(err)
	}
	info, err := d.ClaimInfo()
	if err != nil {
		t.Fatal(err)
	}
	if info.AcceptsPlacement || string(info.InstallPlacement.NodeSelector) != `{"zone":"install"}` || len(info.InstallPlacement.Tolerations) != 0 {
		t.Fatalf("claim info = %+v", info)
	}
}

func TestUnschedulableFailsTheRun(t *testing.T) {
	ctx := context.Background()
	const scheduler = "0/7 nodes are available: 3 node(s) had untolerated taint {dedicated: gpu}."
	run := func(phase api.Phase, stuckFor time.Duration, status corev1.PodPhase, reason string) ([]Failure, *fake.Clientset) {
		t.Helper()
		client := fake.NewClientset()
		cfg := testConfig()
		cfg.UnschedulableTimeout = 5 * time.Minute
		d := NewKubernetesDriver(cfg, client)
		a := *testAssignment
		a.Phase = phase
		if err := d.Start(ctx, &a); err != nil {
			t.Fatal(err)
		}
		_, err := client.CoreV1().Pods("firstlume").Create(ctx, &corev1.Pod{
			ObjectMeta: metav1.ObjectMeta{Name: "p", Namespace: "firstlume", Labels: map[string]string{
				labelManagedBy: managedByValue, labelAgent: "default", labelRunID: a.RunID, labelPhase: string(phase),
			}},
			Status: corev1.PodStatus{Phase: status, Conditions: []corev1.PodCondition{{
				Type: corev1.PodScheduled, Status: corev1.ConditionFalse, Reason: reason, Message: scheduler,
				LastTransitionTime: metav1.NewTime(time.Now().Add(-stuckFor)),
			}}},
		}, metav1.CreateOptions{})
		if err != nil {
			t.Fatal(err)
		}
		failures, err := d.Failures(ctx)
		if err != nil {
			t.Fatal(err)
		}
		return failures, client
	}
	jobs := func(client *fake.Clientset) int {
		list, err := client.BatchV1().Jobs("firstlume").List(ctx, metav1.ListOptions{})
		if err != nil {
			t.Fatal(err)
		}
		return len(list.Items)
	}

	// Still within the timeout: a node may show up.
	failures, client := run(api.PhasePlan, time.Minute, corev1.PodPending, corev1.PodReasonUnschedulable)
	if len(failures) != 0 || jobs(client) != 1 {
		t.Fatalf("too early: %+v", failures)
	}
	// Scheduled pods and other reasons aren't this case.
	failures, client = run(api.PhasePlan, time.Hour, corev1.PodRunning, corev1.PodReasonUnschedulable)
	if len(failures) != 0 || jobs(client) != 1 {
		t.Fatalf("running: %+v", failures)
	}
	failures, client = run(api.PhasePlan, time.Hour, corev1.PodPending, "SchedulingGated")
	if len(failures) != 0 || jobs(client) != 1 {
		t.Fatalf("other reason: %+v", failures)
	}

	failures, client = run(api.PhasePlan, 6*time.Minute, corev1.PodPending, corev1.PodReasonUnschedulable)
	if len(failures) != 1 || failures[0].Phase != api.PhasePlan || jobs(client) != 0 {
		t.Fatalf("stuck plan: %+v, jobs %d", failures, jobs(client))
	}
	if !strings.Contains(failures[0].Reason, "for 5m.") || !strings.Contains(failures[0].Reason, "Nothing ran") || !strings.Contains(failures[0].Reason, scheduler) {
		t.Fatalf("reason = %q", failures[0].Reason)
	}

	failures, _ = run(api.PhaseApply, 6*time.Minute, corev1.PodPending, corev1.PodReasonUnschedulable)
	if len(failures) != 1 || !strings.Contains(failures[0].Reason, "The apply never started") || !strings.Contains(failures[0].Reason, "Nothing was applied") {
		t.Fatalf("stuck apply: %+v", failures)
	}
}
