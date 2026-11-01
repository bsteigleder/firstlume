package agent

import (
	"context"
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
