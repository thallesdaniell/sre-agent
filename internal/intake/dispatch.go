package intake

import (
	"context"
	"regexp"
	"strings"

	batchv1 "k8s.io/api/batch/v1"
	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/api/resource"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/kubernetes"

	"github.com/thallesdaniell/sre-agent/internal/incident"
)

// Dispatcher executa a investigação. O intake não sabe nem quer saber onde ela roda.
type Dispatcher interface {
	Dispatch(ctx context.Context, inc incident.Incident) (DispatchResult, error)
}

type DispatchResult struct {
	Ref       string
	Duplicate bool // o Job já existia
}

var nonSlug = regexp.MustCompile(`[^a-z0-9-]+`)

// JobName vem do id do incidente: o mesmo incidente nunca gera dois Jobs.
func JobName(incidentID string) string {
	slug := nonSlug.ReplaceAllString(strings.ToLower(incidentID), "-")
	slug = regexp.MustCompile(`-+`).ReplaceAllString(slug, "-")
	if len(slug) > 50 {
		slug = slug[:50]
	}
	return strings.TrimRight("rca-"+slug, "-")
}

// ForwardedJobEnv é a configuração (não segredo) que o intake repassa ao Job. Segredos chegam pelo
// Secret (envFrom); isto aqui é o que muda o comportamento: publicação e destino do rastreio.
var ForwardedJobEnv = []string{
	"PUBLISH_MODE", "TRACE_CONTENT", "TRACE_URL_TEMPLATE", "LANGFUSE_BASE_URL", "OTEL_EXPORTER_OTLP_ENDPOINT", "APP_VERSION",
}

func ForwardedEnv(getenv func(string) string) []corev1.EnvVar {
	var out []corev1.EnvVar
	for _, name := range ForwardedJobEnv {
		if v := getenv(name); v != "" {
			out = append(out, corev1.EnvVar{Name: name, Value: v})
		}
	}
	return out
}

// KubernetesDispatcher cria um Job efêmero por incidente, com a ServiceAccount só de leitura.
// O Job some sozinho depois (TTLSecondsAfterFinished).
type KubernetesDispatcher struct {
	Client         kubernetes.Interface
	Namespace      string
	Image          string
	ServiceAccount string
	SecretName     string
	Env            []corev1.EnvVar
}

func (k KubernetesDispatcher) Dispatch(ctx context.Context, inc incident.Incident) (DispatchResult, error) {
	name := JobName(inc.ID)
	_, err := k.Client.BatchV1().Jobs(k.Namespace).Create(ctx, k.buildJob(name, inc), metav1.CreateOptions{})
	if apierrors.IsAlreadyExists(err) {
		return DispatchResult{Ref: name, Duplicate: true}, nil
	}
	if err != nil {
		return DispatchResult{}, err
	}
	return DispatchResult{Ref: name}, nil
}

func ptr[T any](v T) *T { return &v }

func (k KubernetesDispatcher) buildJob(name string, inc incident.Incident) *batchv1.Job {
	payload := mustJSON(inc)
	env := append([]corev1.EnvVar{
		{Name: "INCIDENT_JSON", Value: payload},
		{Name: "AGENT_CONFIG_FILE", Value: "/config/agent.config.json"},
	}, k.Env...)
	return &batchv1.Job{
		ObjectMeta: metav1.ObjectMeta{Name: name, Labels: map[string]string{"app": "sre-agent", "incident": strings.TrimPrefix(name, "rca-")}},
		Spec: batchv1.JobSpec{
			BackoffLimit:            ptr[int32](0), // não repete sozinho: um RCA duplicado no canal é pior que um que falhou
			ActiveDeadlineSeconds:   ptr[int64](1800),
			TTLSecondsAfterFinished: ptr[int32](3600),
			Template: corev1.PodTemplateSpec{
				ObjectMeta: metav1.ObjectMeta{Labels: map[string]string{"app": "sre-agent"}},
				Spec: corev1.PodSpec{
					RestartPolicy:      corev1.RestartPolicyNever,
					ServiceAccountName: k.ServiceAccount,
					Containers: []corev1.Container{{
						Name:         "agent",
						Image:        k.Image,
						Command:      []string{"/app/sre-agent", "job"},
						Env:          env,
						EnvFrom:      []corev1.EnvFromSource{{SecretRef: &corev1.SecretEnvSource{LocalObjectReference: corev1.LocalObjectReference{Name: k.SecretName}}}},
						VolumeMounts: []corev1.VolumeMount{{Name: "config", MountPath: "/config", ReadOnly: true}},
						Resources: corev1.ResourceRequirements{
							Requests: corev1.ResourceList{corev1.ResourceCPU: resource.MustParse("100m"), corev1.ResourceMemory: resource.MustParse("64Mi")},
							Limits:   corev1.ResourceList{corev1.ResourceMemory: resource.MustParse("256Mi")},
						},
						SecurityContext: &corev1.SecurityContext{
							RunAsNonRoot:             ptr(true),
							AllowPrivilegeEscalation: ptr(false),
							ReadOnlyRootFilesystem:   ptr(true),
							Capabilities:             &corev1.Capabilities{Drop: []corev1.Capability{"ALL"}},
						},
					}},
					Volumes: []corev1.Volume{{Name: "config", VolumeSource: corev1.VolumeSource{
						ConfigMap: &corev1.ConfigMapVolumeSource{LocalObjectReference: corev1.LocalObjectReference{Name: "sre-agent-config"}},
					}}},
				},
			},
		},
	}
}
