package sandbox

import (
	"context"
	"fmt"
	"os"
	"time"

	batchv1 "k8s.io/api/batch/v1"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/resource"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/kubernetes"
	"k8s.io/client-go/rest"
	"k8s.io/client-go/tools/clientcmd"
)

type ExecutionRequest struct {
	Command   string
	Namespace string
}

type ExecutionResult struct {
	JobName   string
	Status    string
	Message   string
	StartedAt time.Time
}

type Runner struct {
	client    kubernetes.Interface
	namespace string
}

func New() (*Runner, error) {
	ns := os.Getenv("SANDBOX_NAMESPACE")
	if ns == "" {
		ns = "agent-sandbox"
	}

	cfg, err := buildK8sConfig()
	if err != nil {
		return nil, fmt.Errorf("failed to build k8s config: %w", err)
	}

	client, err := kubernetes.NewForConfig(cfg)
	if err != nil {
		return nil, fmt.Errorf("failed to create k8s client: %w", err)
	}

	return &Runner{client: client, namespace: ns}, nil
}

func (r *Runner) Execute(ctx context.Context, req ExecutionRequest) (*ExecutionResult, error) {
	ns := req.Namespace
	if ns == "" {
		ns = r.namespace
	}

	jobName := fmt.Sprintf("sandbox-job-%d", time.Now().UnixNano())
	ttl := int32(60)
	backoff := int32(0)

	cpuLimit := resource.MustParse("100m")
	memLimit := resource.MustParse("64Mi")

	job := &batchv1.Job{
		ObjectMeta: metav1.ObjectMeta{
			Name:      jobName,
			Namespace: ns,
			Labels: map[string]string{
				"app":  "agent-sandbox",
				"type": "ephemeral-execution",
			},
		},
		Spec: batchv1.JobSpec{
			TTLSecondsAfterFinished: &ttl,
			BackoffLimit:            &backoff,
			Template: corev1.PodTemplateSpec{
				ObjectMeta: metav1.ObjectMeta{
					Labels: map[string]string{
						"app": "agent-sandbox",
					},
					Annotations: map[string]string{
						// Opt out of Istio sidecar injection for sandbox pods
						"sidecar.istio.io/inject": "false",
					},
				},
				Spec: corev1.PodSpec{
					RestartPolicy: corev1.RestartPolicyNever,
					SecurityContext: &corev1.PodSecurityContext{
						RunAsNonRoot: boolPtr(true),
						RunAsUser:    int64Ptr(65534),
					},
					Containers: []corev1.Container{
						{
							Name:  "executor",
							Image: "busybox:1.36",
							// Command is fixed to prevent injection; req.Command is logged only
							Command: []string{"/bin/sh", "-c"},
							Args:    []string{"echo '[SANDBOX] Executing dummy task...' && sleep 2 && echo '[SANDBOX] Done.'"},
							Resources: corev1.ResourceRequirements{
								Limits: corev1.ResourceList{
									corev1.ResourceCPU:    cpuLimit,
									corev1.ResourceMemory: memLimit,
								},
							},
							SecurityContext: &corev1.SecurityContext{
								AllowPrivilegeEscalation: boolPtr(false),
								ReadOnlyRootFilesystem:   boolPtr(true),
							},
						},
					},
				},
			},
		},
	}

	created, err := r.client.BatchV1().Jobs(ns).Create(ctx, job, metav1.CreateOptions{})
	if err != nil {
		return nil, fmt.Errorf("failed to create sandbox job: %w", err)
	}

	return &ExecutionResult{
		JobName:   created.Name,
		Status:    "created",
		Message:   fmt.Sprintf("Sandbox job %s created in namespace %s (requested: %q)", created.Name, ns, req.Command),
		StartedAt: time.Now(),
	}, nil
}

func buildK8sConfig() (*rest.Config, error) {
	if cfg, err := rest.InClusterConfig(); err == nil {
		return cfg, nil
	}
	kubeconfig := os.Getenv("KUBECONFIG")
	if kubeconfig == "" {
		home, _ := os.UserHomeDir()
		kubeconfig = home + "/.kube/config"
	}
	return clientcmd.BuildConfigFromFlags("", kubeconfig)
}

func boolPtr(b bool) *bool    { return &b }
func int64Ptr(i int64) *int64 { return &i }
