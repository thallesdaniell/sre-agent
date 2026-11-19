package kubernetes_test

import (
	"context"
	"encoding/json"
	"strings"
	"testing"
	"time"

	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/resource"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/kubernetes/fake"

	"github.com/anthropics/anthropic-sdk-go/v2"

	"github.com/thallesdaniell/sre-agent/internal/integrations"
	k8sint "github.com/thallesdaniell/sre-agent/internal/integrations/kubernetes"
	"github.com/thallesdaniell/sre-agent/internal/store/memstore"
	"github.com/thallesdaniell/sre-agent/internal/testfixtures"
)

func tools(t *testing.T) map[string]anthropic.BetaTool {
	t.Helper()
	labels := map[string]string{"app": "checkout-api"}
	created := metav1.NewTime(time.Now().Add(-time.Hour))
	client := fake.NewSimpleClientset(
		&corev1.Pod{
			ObjectMeta: metav1.ObjectMeta{Name: "checkout-api-7d9f", Namespace: "checkout", Labels: labels},
			Status: corev1.PodStatus{Phase: corev1.PodRunning, ContainerStatuses: []corev1.ContainerStatus{{
				Name: "api", RestartCount: 12, Image: "checkout-api:42",
				LastTerminationState: corev1.ContainerState{Terminated: &corev1.ContainerStateTerminated{Reason: "OOMKilled", ExitCode: 137}},
			}}},
		},
		&appsv1.Deployment{ObjectMeta: metav1.ObjectMeta{Name: "checkout-api", Namespace: "checkout"},
			Spec: appsv1.DeploymentSpec{Selector: &metav1.LabelSelector{MatchLabels: labels}}},
		replicaSet("checkout-api-41", "41", "checkout-api:41", labels, created, "1Gi"),
		replicaSet("checkout-api-42", "42", "checkout-api:42", labels, created, "512Mi"),
	)
	conn, err := k8sint.Integration(client).Connect(t.Context(), integrations.Deps{Scopes: testfixtures.Scopes(), Store: memstore.New()})
	if err != nil {
		t.Fatal(err)
	}
	out := map[string]anthropic.BetaTool{}
	for _, tool := range conn.Tools {
		out[tool.Name()] = tool
	}
	return out
}

func replicaSet(name, rev, image string, labels map[string]string, created metav1.Time, memLimit ...string) *appsv1.ReplicaSet {
	resources := corev1.ResourceRequirements{}
	if len(memLimit) > 0 {
		resources.Limits = corev1.ResourceList{corev1.ResourceMemory: resource.MustParse(memLimit[0])}
	}
	return &appsv1.ReplicaSet{
		ObjectMeta: metav1.ObjectMeta{Name: name, Namespace: "checkout", Labels: labels, CreationTimestamp: created,
			Annotations:     map[string]string{"deployment.kubernetes.io/revision": rev},
			OwnerReferences: []metav1.OwnerReference{{Kind: "Deployment", Name: "checkout-api"}}},
		Spec: appsv1.ReplicaSetSpec{Template: corev1.PodTemplateSpec{Spec: corev1.PodSpec{Containers: []corev1.Container{{Name: "api", Image: image, Resources: resources}}}}},
	}
}

func run(t *testing.T, tool anthropic.BetaTool, input string) (string, error) {
	t.Helper()
	blocks, err := tool.Execute(context.Background(), json.RawMessage(input))
	if err != nil {
		return "", err
	}
	return blocks[0].OfText.Text, nil
}

func TestListPodsMostraMotivoDoUltimoTermino(t *testing.T) {
	out, err := run(t, tools(t)["list_pods"], `{"product":"checkout"}`)
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{"checkout-api-7d9f", "OOMKilled", `"restarts":12`} {
		if !strings.Contains(out, want) {
			t.Errorf("faltou %s em %s", want, out)
		}
	}
}

func TestRolloutHistoryOrdenaRevisoesDaMaisNova(t *testing.T) {
	out, err := run(t, tools(t)["rollout_history"], `{"product":"checkout","deployment":"checkout-api"}`)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Index(out, `"revision":42`) > strings.Index(out, `"revision":41`) {
		t.Fatalf("ordem errada: %s", out)
	}
	// O limite de memória de cada revisão aparece: é a evidência direta numa falha por OOM.
	if !strings.Contains(out, `"memory":"512Mi"`) || !strings.Contains(out, `"memory":"1Gi"`) {
		t.Fatalf("limites ausentes: %s", out)
	}
}

func TestRecusaNamespaceForaDoProdutoENomeInvalido(t *testing.T) {
	ts := tools(t)
	if _, err := run(t, ts["list_pods"], `{"product":"checkout","namespace":"kube-system"}`); err == nil || !strings.Contains(err.Error(), "não pertence") {
		t.Fatalf("esperava recusa, veio %v", err)
	}
	if _, err := run(t, ts["pod_logs"], `{"product":"checkout","pod":"../etc/passwd"}`); err == nil {
		t.Fatal("esperava recusa de nome inválido")
	}
	if _, err := run(t, ts["list_pods"], `{"product":"billing"}`); err == nil || !strings.Contains(err.Error(), "fora do inventário") {
		t.Fatalf("esperava recusa de produto, veio %v", err)
	}
}
