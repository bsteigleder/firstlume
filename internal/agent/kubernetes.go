package agent

import (
	"context"
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
)

const (
	labelManagedBy = "app.kubernetes.io/managed-by"
	labelAgent     = "firstlume.io/agent"
	labelRunID     = "firstlume.io/run-id"
	labelPhase     = "firstlume.io/phase"
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
	PodAnnotations   map[string]string
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
	return "flr-" + strings.ReplaceAll(a.RunID, "-", "") + "-" + string(a.Phase)
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

func (d *KubernetesDriver) Start(ctx context.Context, a *api.Assignment) error {
	secret, job := BuildJob(d.cfg, a)
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
	return out, nil
}
