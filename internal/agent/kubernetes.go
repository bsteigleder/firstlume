package agent

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"time"

	batchv1 "k8s.io/api/batch/v1"
	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/api/resource"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/client-go/kubernetes"

	"github.com/bsteigleder/firstlume/internal/api"
	"github.com/bsteigleder/firstlume/internal/placement"
)

const (
	labelManagedBy = "app.kubernetes.io/managed-by"
	labelAgent     = "firstlume.io/agent"
	labelRunID     = "firstlume.io/run-id"
	labelPhase     = "firstlume.io/phase"
	labelStack     = "firstlume.io/stack-id"
	// LabelRunType tells command runs' pods apart, for the egress policy the
	// chart creates (spec 0009, E3).
	LabelRunType   = "firstlume.io/run-type"
	annotReported  = "firstlume.io/failure-reported"
	managedByValue = "firstlume"
	tokenKey       = "token"
	executorUID    = 65532
)

type KubernetesConfig struct {
	Namespace        string
	AgentName        string // distinguishes Jobs of different agents sharing a namespace
	Image            string
	ImagePullPolicy  corev1.PullPolicy
	ServiceAccount   string
	AutomountToken   bool
	ExecutorAPI      string
	Resources        corev1.ResourceRequirements
	ActiveDeadline   time.Duration
	TTLAfterFinished time.Duration
	NodeSelector     map[string]string
	Tolerations      []corev1.Toleration
	Affinity         *corev1.Affinity
	PodAnnotations   map[string]string
	// AllowPlacement accepts placement set in First Lume: on the agent, the
	// organization, folders and stacks (spec 0007, R3). The fields above always apply.
	AllowPlacement bool
	// UnschedulableTimeout fails a run no node can take for this long. Zero never does.
	UnschedulableTimeout time.Duration
}

// withPlacement returns the configuration of a run: each field set in First
// Lume replaces the installation's, without adding to it.
func (cfg KubernetesConfig) withPlacement(p api.Placement) (KubernetesConfig, error) {
	if p.IsZero() {
		return cfg, nil
	}
	if !cfg.AllowPlacement {
		return cfg, fmt.Errorf("this agent doesn't accept placement (agent.executor.allowPlacement)")
	}
	parsed, err := placement.Parse(p)
	if err != nil {
		return cfg, err
	}
	if len(p.NodeSelector) > 0 {
		cfg.NodeSelector = parsed.NodeSelector
	}
	if len(p.Tolerations) > 0 {
		cfg.Tolerations = parsed.Tolerations
	}
	aff := corev1.Affinity{}
	if cfg.Affinity != nil {
		aff = *cfg.Affinity
	}
	if len(p.NodeAffinity) > 0 {
		aff.NodeAffinity = parsed.NodeAffinity
	}
	if len(p.PodAffinity) > 0 {
		aff.PodAffinity = parsed.PodAffinity
	}
	if len(p.PodAntiAffinity) > 0 {
		aff.PodAntiAffinity = parsed.PodAntiAffinity
	}
	cfg.Affinity = &aff
	return cfg, nil
}

// installPlacement is what the Helm values set, as the screens show it.
func (cfg KubernetesConfig) installPlacement() (api.Placement, error) {
	var p api.Placement
	var aff corev1.Affinity
	if cfg.Affinity != nil {
		aff = *cfg.Affinity
	}
	for _, f := range []struct {
		into *json.RawMessage
		set  bool
		v    any
	}{
		{&p.NodeSelector, len(cfg.NodeSelector) > 0, cfg.NodeSelector},
		{&p.Tolerations, len(cfg.Tolerations) > 0, cfg.Tolerations},
		{&p.NodeAffinity, aff.NodeAffinity != nil, aff.NodeAffinity},
		{&p.PodAffinity, aff.PodAffinity != nil, aff.PodAffinity},
		{&p.PodAntiAffinity, aff.PodAntiAffinity != nil, aff.PodAntiAffinity},
	} {
		if !f.set {
			continue
		}
		raw, err := json.Marshal(f.v)
		if err != nil {
			return p, err
		}
		*f.into = raw
	}
	return p, nil
}

// ParseScheduling reads where run Jobs go: a node selector, tolerations and an
// affinity (node affinity, pod affinity and anti-affinity), each as the JSON of
// its Kubernetes field. Empty ones are skipped; unknown fields are an error, so
// a typo doesn't silently put runs on any node.
func ParseScheduling(nodeSelector, tolerations, affinity string) (map[string]string, []corev1.Toleration, *corev1.Affinity, error) {
	var (
		ns  map[string]string
		tol []corev1.Toleration
		aff *corev1.Affinity
	)
	for _, f := range []struct {
		name, text string
		into       any
	}{
		{"node selector", nodeSelector, &ns},
		{"tolerations", tolerations, &tol},
		{"affinity", affinity, &aff},
	} {
		if strings.TrimSpace(f.text) == "" {
			continue
		}
		dec := json.NewDecoder(strings.NewReader(f.text))
		dec.DisallowUnknownFields()
		if err := dec.Decode(f.into); err != nil {
			return nil, nil, nil, fmt.Errorf("executor %s: %w", f.name, err)
		}
	}
	return ns, tol, aff, nil
}

// ParseResources builds resource requirements from quantity strings; empty ones are skipped.
func ParseResources(cpuReq, memReq, cpuLim, memLim string) (corev1.ResourceRequirements, error) {
	rr := corev1.ResourceRequirements{Requests: corev1.ResourceList{}, Limits: corev1.ResourceList{}}
	for _, q := range []struct {
		list corev1.ResourceList
		name corev1.ResourceName
		val  string
	}{
		{rr.Requests, corev1.ResourceCPU, cpuReq},
		{rr.Requests, corev1.ResourceMemory, memReq},
		{rr.Limits, corev1.ResourceCPU, cpuLim},
		{rr.Limits, corev1.ResourceMemory, memLim},
	} {
		if q.val == "" {
			continue
		}
		parsed, err := resource.ParseQuantity(q.val)
		if err != nil {
			return rr, fmt.Errorf("invalid quantity %q for %s: %w", q.val, q.name, err)
		}
		q.list[q.name] = parsed
	}
	return rr, nil
}

// KubernetesDriver runs each executor as a Kubernetes Job.
type KubernetesDriver struct {
	cfg    KubernetesConfig
	client kubernetes.Interface
}

func NewKubernetesDriver(cfg KubernetesConfig, client kubernetes.Interface) *KubernetesDriver {
	return &KubernetesDriver{cfg: cfg, client: client}
}

func jobName(a *api.Assignment) string {
	return jobNameOf(a.RunID, string(a.Phase))
}

func jobNameOf(runID, phase string) string {
	return "flr-" + strings.ReplaceAll(runID, "-", "") + "-" + phase
}

// BuildJob returns the Secret holding the run token and the Job that uses it.
func BuildJob(cfg KubernetesConfig, a *api.Assignment) (*corev1.Secret, *batchv1.Job) {
	name := jobName(a)
	labels := map[string]string{
		labelManagedBy: managedByValue,
		labelAgent:     cfg.AgentName,
		labelRunID:     a.RunID,
		labelPhase:     string(a.Phase),
	}
	if a.StackID != "" {
		labels[labelStack] = a.StackID // so a rule can name a stack (R2.5)
	}
	if a.Type != "" {
		labels[LabelRunType] = string(a.Type)
	}
	secret := &corev1.Secret{
		ObjectMeta: metav1.ObjectMeta{Name: name + "-token", Namespace: cfg.Namespace, Labels: labels},
		Type:       corev1.SecretTypeOpaque,
		StringData: map[string]string{tokenKey: a.Token},
	}

	uid := int64(executorUID)
	yes, no := true, false
	backoff := int32(0) // never retry: re-running an apply is not safe
	deadline := int64(cfg.ActiveDeadline.Seconds())
	ttl := int32(cfg.TTLAfterFinished.Seconds())
	automount := cfg.AutomountToken

	job := &batchv1.Job{
		ObjectMeta: metav1.ObjectMeta{Name: name, Namespace: cfg.Namespace, Labels: labels},
		Spec: batchv1.JobSpec{
			BackoffLimit:            &backoff,
			ActiveDeadlineSeconds:   &deadline,
			TTLSecondsAfterFinished: &ttl,
			Template: corev1.PodTemplateSpec{
				ObjectMeta: metav1.ObjectMeta{Labels: labels, Annotations: cfg.PodAnnotations},
				Spec: corev1.PodSpec{
					RestartPolicy:                corev1.RestartPolicyNever,
					ServiceAccountName:           cfg.ServiceAccount,
					AutomountServiceAccountToken: &automount,
					NodeSelector:                 cfg.NodeSelector,
					Tolerations:                  cfg.Tolerations,
					Affinity:                     cfg.Affinity,
					SecurityContext: &corev1.PodSecurityContext{
						RunAsNonRoot:   &yes,
						RunAsUser:      &uid,
						RunAsGroup:     &uid,
						FSGroup:        &uid,
						SeccompProfile: &corev1.SeccompProfile{Type: corev1.SeccompProfileTypeRuntimeDefault},
					},
					Containers: []corev1.Container{{
						Name:            "executor",
						Image:           cfg.Image,
						ImagePullPolicy: cfg.ImagePullPolicy,
						Args:            []string{"exec"},
						Env: []corev1.EnvVar{
							{Name: "FIRSTLUME_API_URL", Value: cfg.ExecutorAPI},
							{Name: "FIRSTLUME_WORK_DIR", Value: "/workspace/run"},
							{Name: "FIRSTLUME_TOOLS_DIR", Value: "/workspace/tools"},
							{Name: "FIRSTLUME_RUN_TOKEN", ValueFrom: &corev1.EnvVarSource{SecretKeyRef: &corev1.SecretKeySelector{
								LocalObjectReference: corev1.LocalObjectReference{Name: secret.Name},
								Key:                  tokenKey,
							}}},
						},
						Resources: cfg.Resources,
						SecurityContext: &corev1.SecurityContext{
							AllowPrivilegeEscalation: &no,
							ReadOnlyRootFilesystem:   &yes,
							Capabilities:             &corev1.Capabilities{Drop: []corev1.Capability{"ALL"}},
						},
						VolumeMounts: []corev1.VolumeMount{
							{Name: "workspace", MountPath: "/workspace"},
							{Name: "tmp", MountPath: "/tmp"},
						},
					}},
					Volumes: []corev1.Volume{
						{Name: "workspace", VolumeSource: corev1.VolumeSource{EmptyDir: &corev1.EmptyDirVolumeSource{}}},
						{Name: "tmp", VolumeSource: corev1.VolumeSource{EmptyDir: &corev1.EmptyDirVolumeSource{}}},
					},
				},
			},
		},
	}
	return secret, job
}

// ClaimInfo tells the server what this agent accepts and what it was installed with.
func (d *KubernetesDriver) ClaimInfo() (api.ClaimRequest, error) {
	install, err := d.cfg.installPlacement()
	return api.ClaimRequest{AcceptsPlacement: d.cfg.AllowPlacement, InstallPlacement: install, AcceptsCommands: true}, err
}

func (d *KubernetesDriver) Start(ctx context.Context, a *api.Assignment) error {
	cfg, err := d.cfg.withPlacement(a.Placement)
	if err != nil {
		return err
	}
	secret, job := BuildJob(cfg, a)
	secrets := d.client.CoreV1().Secrets(d.cfg.Namespace)
	jobs := d.client.BatchV1().Jobs(d.cfg.Namespace)

	if _, err := secrets.Create(ctx, secret, metav1.CreateOptions{}); err != nil {
		if !apierrors.IsAlreadyExists(err) {
			return fmt.Errorf("creating token secret: %w", err)
		}
		if _, err := secrets.Update(ctx, secret, metav1.UpdateOptions{}); err != nil {
			return fmt.Errorf("updating token secret: %w", err)
		}
	}
	created, err := jobs.Create(ctx, job, metav1.CreateOptions{})
	if apierrors.IsAlreadyExists(err) {
		return nil // started by a previous attempt
	}
	if err != nil {
		return fmt.Errorf("creating job: %w", err)
	}

	// Make the Job own the Secret so the token is garbage-collected with it.
	patch := fmt.Sprintf(`{"metadata":{"ownerReferences":[{"apiVersion":"batch/v1","kind":"Job","name":%q,"uid":%q}]}}`,
		created.Name, created.UID)
	if _, err := secrets.Patch(ctx, secret.Name, types.MergePatchType, []byte(patch), metav1.PatchOptions{}); err != nil {
		return fmt.Errorf("setting secret owner: %w", err)
	}
	return nil
}

func (d *KubernetesDriver) listJobs(ctx context.Context) ([]batchv1.Job, error) {
	list, err := d.client.BatchV1().Jobs(d.cfg.Namespace).List(ctx, metav1.ListOptions{
		LabelSelector: labelManagedBy + "=" + managedByValue + "," + labelAgent + "=" + d.cfg.AgentName,
	})
	if err != nil {
		return nil, err
	}
	return list.Items, nil
}

func jobFinished(j *batchv1.Job) (failed bool, reason string, done bool) {
	for _, c := range j.Status.Conditions {
		if c.Status != corev1.ConditionTrue {
			continue
		}
		switch c.Type {
		case batchv1.JobComplete:
			return false, "", true
		case batchv1.JobFailed:
			reason := c.Reason
			if c.Message != "" {
				reason += ": " + c.Message
			}
			return true, reason, true
		}
	}
	return false, "", false
}

func (d *KubernetesDriver) Active(ctx context.Context) (int, error) {
	jobs, err := d.listJobs(ctx)
	if err != nil {
		return 0, err
	}
	n := 0
	for i := range jobs {
		if _, _, done := jobFinished(&jobs[i]); !done {
			n++
		}
	}
	return n, nil
}

func (d *KubernetesDriver) Failures(ctx context.Context) ([]Failure, error) {
	jobs, err := d.listJobs(ctx)
	if err != nil {
		return nil, err
	}
	var out []Failure
	for i := range jobs {
		j := &jobs[i]
		failed, reason, _ := jobFinished(j)
		if !failed || j.Annotations[annotReported] == "true" {
			continue
		}
		out = append(out, Failure{
			RunID:  j.Labels[labelRunID],
			Phase:  api.Phase(j.Labels[labelPhase]),
			Reason: "executor job failed (" + reason + ")",
		})
		// Mark it so the failure is reported once. The server ignores reports for
		// runs that already finished, so a duplicate after a crash is harmless.
		patch := fmt.Sprintf(`{"metadata":{"annotations":{%q:"true"}}}`, annotReported)
		if _, err := d.client.BatchV1().Jobs(d.cfg.Namespace).Patch(ctx, j.Name, types.MergePatchType, []byte(patch), metav1.PatchOptions{}); err != nil {
			return out, fmt.Errorf("marking job %s reported: %w", j.Name, err)
		}
	}
	unschedulable, err := d.unschedulable(ctx, jobs)
	return append(out, unschedulable...), err
}

// unschedulable finds runs no node has taken for too long, removes their Jobs
// so they can't start later, and reports them with the scheduler's message.
// A Job left Pending would otherwise fail only by heartbeat, saying nothing about why.
func (d *KubernetesDriver) unschedulable(ctx context.Context, jobs []batchv1.Job) ([]Failure, error) {
	if d.cfg.UnschedulableTimeout <= 0 {
		return nil, nil
	}
	open := map[string]bool{}
	for i := range jobs {
		if _, _, done := jobFinished(&jobs[i]); !done {
			open[jobs[i].Name] = true
		}
	}
	if len(open) == 0 {
		return nil, nil
	}
	pods, err := d.client.CoreV1().Pods(d.cfg.Namespace).List(ctx, metav1.ListOptions{
		LabelSelector: labelManagedBy + "=" + managedByValue + "," + labelAgent + "=" + d.cfg.AgentName,
	})
	if err != nil {
		return nil, fmt.Errorf("listing run pods: %w", err)
	}
	var out []Failure
	for i := range pods.Items {
		pod := &pods.Items[i]
		name := jobNameOf(pod.Labels[labelRunID], pod.Labels[labelPhase])
		message, stuck := d.stuckPending(pod)
		if !open[name] || !stuck {
			continue
		}
		background := metav1.DeletePropagationBackground
		err := d.client.BatchV1().Jobs(d.cfg.Namespace).Delete(ctx, name, metav1.DeleteOptions{PropagationPolicy: &background})
		if err != nil && !apierrors.IsNotFound(err) {
			return out, fmt.Errorf("removing job %s: %w", name, err)
		}
		delete(open, name)
		phase := api.Phase(pod.Labels[labelPhase])
		out = append(out, Failure{RunID: pod.Labels[labelRunID], Phase: phase, Reason: unschedulableReason(phase, d.cfg.UnschedulableTimeout, message)})
	}
	return out, nil
}

// stuckPending says whether the scheduler has been refusing the pod for longer
// than the timeout, and returns its message.
func (d *KubernetesDriver) stuckPending(pod *corev1.Pod) (string, bool) {
	if pod.Status.Phase != corev1.PodPending {
		return "", false
	}
	for _, c := range pod.Status.Conditions {
		if c.Type != corev1.PodScheduled || c.Status != corev1.ConditionFalse || c.Reason != corev1.PodReasonUnschedulable {
			continue
		}
		since := c.LastTransitionTime.Time
		if since.IsZero() {
			since = pod.CreationTimestamp.Time
		}
		return c.Message, time.Since(since) >= d.cfg.UnschedulableTimeout
	}
	return "", false
}

func unschedulableReason(phase api.Phase, timeout time.Duration, message string) string {
	wait := timeout.String()
	if strings.HasSuffix(wait, "m0s") {
		wait = strings.TrimSuffix(wait, "0s")
	}
	if phase == api.PhaseApply {
		return fmt.Sprintf("The apply never started: no node could take it for %s. Nothing was applied.\n%s", wait, message)
	}
	return fmt.Sprintf("No node could take this run for %s. Nothing ran.\n%s", wait, message)
}
