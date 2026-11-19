// Package kubernetes é a integração nativa com a API do Kubernetes. Vale para qualquer cluster
// (GKE, EKS, AKS, kind): o que muda é só a credencial. Não usa kubectl nem shell.
//
// Só usa get/list nos recursos que o RBAC do Job permite (deploy/k8s/rbac-readonly.yaml):
// pods, pods/log, events, replicasets, deployments. O RBAC é a garantia; este código é só a
// forma de pedir.
package kubernetes

import (
	"context"
	"fmt"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/anthropics/anthropic-sdk-go/v2"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/kubernetes"
	"k8s.io/client-go/rest"
	"k8s.io/client-go/tools/clientcmd"

	"github.com/thallesdaniell/sre-agent/internal/integrations"
)

// k8sName é o formato DNS-1123 de nomes de recurso. Recusa qualquer outra coisa.
var k8sName = regexp.MustCompile(`^[a-z0-9]([-a-z0-9.]{0,251}[a-z0-9])?$`)

func checkName(field, value string) error {
	if value != "" && !k8sName.MatchString(value) {
		return fmt.Errorf("%s inválido: %q", field, value)
	}
	return nil
}

// NewClient: dentro do cluster usa a ServiceAccount do pod; fora, o ~/.kube/config (dev).
func NewClient() (kubernetes.Interface, error) {
	cfg, err := rest.InClusterConfig()
	if err != nil {
		cfg, err = clientcmd.NewNonInteractiveDeferredLoadingClientConfig(
			clientcmd.NewDefaultClientConfigLoadingRules(), &clientcmd.ConfigOverrides{}).ClientConfig()
		if err != nil {
			return nil, fmt.Errorf("sem credencial do Kubernetes: %w", err)
		}
	}
	return kubernetes.NewForConfig(cfg)
}

// Integration. client nil = cria o cliente real na conexão; testes passam um fake.
func Integration(client kubernetes.Interface) integrations.Integration {
	return integrations.Integration{
		Name:       "kubernetes",
		Categories: []integrations.Category{integrations.Symptoms, integrations.Changes},
		Purpose:    "estado dos pods, logs, eventos e histórico de rollout dos Deployments",
		Connect: func(_ context.Context, deps integrations.Deps) (integrations.Connected, error) {
			k8s := client
			if k8s == nil {
				var err error
				if k8s, err = NewClient(); err != nil {
					return integrations.Connected{}, err
				}
			}
			e := evidence{k8s: k8s}
			productDesc := "Produto do inventário: " + strings.Join(deps.Scopes.Products(), ", ")
			ns := func(product, requested string) (string, error) {
				if err := checkName("namespace", requested); err != nil {
					return "", err
				}
				return deps.Scopes.Namespace(product, requested)
			}

			type listPodsIn struct {
				Product       string `json:"product" jsonschema:"required"`
				Namespace     string `json:"namespace,omitempty" jsonschema:"description=Namespace do produto; omita para o principal"`
				LabelSelector string `json:"labelSelector,omitempty" jsonschema:"maxLength=200,description=ex.: app=checkout"`
			}
			listPods := integrations.NewTool("list_pods",
				"Lista pods de um produto com fase, restarts, imagem e o motivo do último término (ex.: OOMKilled). Ponto de partida para sintomas de disponibilidade. "+productDesc,
				func(ctx context.Context, in listPodsIn) (string, error) {
					namespace, err := ns(in.Product, in.Namespace)
					if err != nil {
						return "", err
					}
					pods, err := e.listPods(ctx, namespace, in.LabelSelector)
					if err != nil {
						return "", err
					}
					return integrations.JSON(map[string]any{"namespace": namespace, "pods": pods})
				})

			type podLogsIn struct {
				Product      string `json:"product" jsonschema:"required"`
				Namespace    string `json:"namespace,omitempty"`
				Pod          string `json:"pod" jsonschema:"required"`
				Container    string `json:"container,omitempty"`
				SinceMinutes int    `json:"sinceMinutes,omitempty" jsonschema:"minimum=1,maximum=360,description=Janela em minutos (padrão 60)"`
				TailLines    int    `json:"tailLines,omitempty" jsonschema:"minimum=1,maximum=400,description=Linhas finais (padrão 200)"`
				Previous     bool   `json:"previous,omitempty" jsonschema:"description=true para o container que reiniciou (o log de antes do crash)"`
			}
			podLogs := integrations.NewTool("pod_logs",
				"Lê logs de um pod numa janela limitada. Use previous=true para o container que reiniciou. "+productDesc,
				func(ctx context.Context, in podLogsIn) (string, error) {
					namespace, err := ns(in.Product, in.Namespace)
					if err != nil {
						return "", err
					}
					if err := checkName("pod", in.Pod); err != nil || in.Pod == "" {
						return "", fmt.Errorf("pod inválido: %q", in.Pod)
					}
					if err := checkName("container", in.Container); err != nil {
						return "", err
					}
					logs, err := e.podLogs(ctx, namespace, in.Pod, in.Container,
						integrations.Clamp(in.SinceMinutes, 60, 1, integrations.MaxWindowMinutes),
						integrations.Clamp(in.TailLines, 200, 1, integrations.MaxLogLines), in.Previous)
					if err != nil {
						return "", err
					}
					if logs == "" {
						return "(sem linhas de log na janela)", nil
					}
					return integrations.Truncate(logs), nil
				})

			type eventsIn struct {
				Product      string `json:"product" jsonschema:"required"`
				Namespace    string `json:"namespace,omitempty"`
				SinceMinutes int    `json:"sinceMinutes,omitempty" jsonschema:"minimum=1,maximum=360,description=Janela em minutos (padrão 120)"`
			}
			events := integrations.NewTool("k8s_events",
				"Eventos recentes do namespace (Warnings primeiro): falhas de scheduling, probes, OOM, pull de imagem. "+productDesc,
				func(ctx context.Context, in eventsIn) (string, error) {
					namespace, err := ns(in.Product, in.Namespace)
					if err != nil {
						return "", err
					}
					evs, err := e.events(ctx, namespace, integrations.Clamp(in.SinceMinutes, 120, 1, integrations.MaxWindowMinutes))
					if err != nil {
						return "", err
					}
					return integrations.JSON(evs)
				})

			type rolloutIn struct {
				Product    string `json:"product" jsonschema:"required"`
				Namespace  string `json:"namespace,omitempty"`
				Deployment string `json:"deployment" jsonschema:"required"`
			}
			rollout := integrations.NewTool("rollout_history",
				"Revisões de um Deployment com imagem e horário. Consulte sempre que houver suspeita de mudança: é a evidência direta de que algo mudou. "+productDesc,
				func(ctx context.Context, in rolloutIn) (string, error) {
					namespace, err := ns(in.Product, in.Namespace)
					if err != nil {
						return "", err
					}
					if err := checkName("deployment", in.Deployment); err != nil || in.Deployment == "" {
						return "", fmt.Errorf("deployment inválido: %q", in.Deployment)
					}
					revs, err := e.rolloutHistory(ctx, namespace, in.Deployment)
					if err != nil {
						return "", err
					}
					return integrations.JSON(revs)
				})

			return integrations.Connected{Tools: []anthropic.BetaTool{listPods, podLogs, events, rollout}}, nil
		},
	}
}

type evidence struct{ k8s kubernetes.Interface }

type containerState struct {
	Name            string       `json:"name"`
	Ready           bool         `json:"ready"`
	Restarts        int32        `json:"restarts"`
	Image           string       `json:"image"`
	WaitingReason   string       `json:"waitingReason,omitempty"`
	LastTermination *termination `json:"lastTermination,omitempty"`
}

type termination struct {
	Reason     string    `json:"reason"`
	ExitCode   int32     `json:"exitCode"`
	FinishedAt time.Time `json:"finishedAt"`
}

type podState struct {
	Name       string           `json:"name"`
	Phase      string           `json:"phase"`
	Node       string           `json:"node"`
	StartedAt  *time.Time       `json:"startedAt,omitempty"`
	Containers []containerState `json:"containers"`
}

func (e evidence) listPods(ctx context.Context, namespace, selector string) ([]podState, error) {
	list, err := e.k8s.CoreV1().Pods(namespace).List(ctx, metav1.ListOptions{LabelSelector: selector, Limit: integrations.MaxListItems})
	if err != nil {
		return nil, err
	}
	out := make([]podState, 0, len(list.Items))
	for _, p := range list.Items {
		ps := podState{Name: p.Name, Phase: string(p.Status.Phase), Node: p.Spec.NodeName}
		if p.Status.StartTime != nil {
			t := p.Status.StartTime.Time
			ps.StartedAt = &t
		}
		for _, c := range p.Status.ContainerStatuses {
			cs := containerState{Name: c.Name, Ready: c.Ready, Restarts: c.RestartCount, Image: c.Image}
			if c.State.Waiting != nil {
				cs.WaitingReason = c.State.Waiting.Reason
			}
			if t := c.LastTerminationState.Terminated; t != nil {
				cs.LastTermination = &termination{Reason: t.Reason, ExitCode: t.ExitCode, FinishedAt: t.FinishedAt.Time}
			}
			ps.Containers = append(ps.Containers, cs)
		}
		out = append(out, ps)
	}
	return out, nil
}

func (e evidence) podLogs(ctx context.Context, namespace, pod, container string, sinceMinutes, tailLines int, previous bool) (string, error) {
	since, tail := int64(sinceMinutes*60), int64(tailLines)
	raw, err := e.k8s.CoreV1().Pods(namespace).GetLogs(pod, &corev1.PodLogOptions{
		Container: container, SinceSeconds: &since, TailLines: &tail, Previous: previous, Timestamps: true,
	}).DoRaw(ctx)
	return string(raw), err
}

type event struct {
	Type     string    `json:"type"`
	Reason   string    `json:"reason"`
	Object   string    `json:"object"`
	Message  string    `json:"message"`
	Count    int32     `json:"count"`
	LastSeen time.Time `json:"lastSeen"`
}

func (e evidence) events(ctx context.Context, namespace string, sinceMinutes int) ([]event, error) {
	list, err := e.k8s.CoreV1().Events(namespace).List(ctx, metav1.ListOptions{})
	if err != nil {
		return nil, err
	}
	since := time.Now().Add(-time.Duration(sinceMinutes) * time.Minute)
	var out []event
	for _, ev := range list.Items {
		last := ev.LastTimestamp.Time
		if last.IsZero() {
			last = ev.EventTime.Time
		}
		if last.IsZero() {
			last = ev.CreationTimestamp.Time
		}
		if last.Before(since) {
			continue
		}
		out = append(out, event{Type: ev.Type, Reason: ev.Reason, Object: ev.InvolvedObject.Kind + "/" + ev.InvolvedObject.Name,
			Message: ev.Message, Count: ev.Count, LastSeen: last})
	}
	sort.SliceStable(out, func(i, j int) bool { return out[i].Type == "Warning" && out[j].Type != "Warning" })
	if len(out) > integrations.MaxListItems {
		out = out[:integrations.MaxListItems]
	}
	return out, nil
}

type revision struct {
	Revision    int       `json:"revision"`
	ReplicaSet  string    `json:"replicaSet"`
	CreatedAt   time.Time `json:"createdAt"`
	Replicas    int32     `json:"replicas"`
	Images      []string  `json:"images"`
	ChangeCause string    `json:"changeCause,omitempty"`
}

// rolloutHistory: cada ReplicaSet do Deployment é uma revisão, com imagem e horário. É a evidência
// direta de "mudou algo aqui", e não só "algo aconteceu antes".
func (e evidence) rolloutHistory(ctx context.Context, namespace, deployment string) ([]revision, error) {
	d, err := e.k8s.AppsV1().Deployments(namespace).Get(ctx, deployment, metav1.GetOptions{})
	if err != nil {
		return nil, err
	}
	selector, err := metav1.LabelSelectorAsSelector(d.Spec.Selector)
	if err != nil {
		return nil, err
	}
	sets, err := e.k8s.AppsV1().ReplicaSets(namespace).List(ctx, metav1.ListOptions{LabelSelector: selector.String()})
	if err != nil {
		return nil, err
	}
	var out []revision
	for _, rs := range sets.Items {
		owned := false
		for _, o := range rs.OwnerReferences {
			if o.Kind == "Deployment" && o.Name == deployment {
				owned = true
			}
		}
		if !owned {
			continue
		}
		rev, _ := strconv.Atoi(rs.Annotations["deployment.kubernetes.io/revision"])
		r := revision{Revision: rev, ReplicaSet: rs.Name, CreatedAt: rs.CreationTimestamp.Time, Replicas: rs.Status.Replicas,
			ChangeCause: rs.Annotations["kubernetes.io/change-cause"]}
		for _, c := range rs.Spec.Template.Spec.Containers {
			r.Images = append(r.Images, c.Image)
		}
		out = append(out, r)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Revision > out[j].Revision })
	if len(out) > 10 {
		out = out[:10]
	}
	return out, nil
}
