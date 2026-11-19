package intake_test

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/kubernetes/fake"

	"github.com/thallesdaniell/sre-agent/internal/incident"
	"github.com/thallesdaniell/sre-agent/internal/intake"
	"github.com/thallesdaniell/sre-agent/internal/store/memstore"
	"github.com/thallesdaniell/sre-agent/internal/testfixtures"
)

const secret = "segredo-de-teste-com-mais-de-24-caracteres"

var now = time.Date(2026, 11, 19, 14, 5, 0, 0, time.UTC)

type recording struct{ calls []incident.Incident }

func (r *recording) Dispatch(_ context.Context, inc incident.Incident) (intake.DispatchResult, error) {
	r.calls = append(r.calls, inc)
	return intake.DispatchResult{Ref: intake.JobName(inc.ID)}, nil
}

func signed(t *testing.T, body any, key string) *http.Request {
	raw, _ := json.Marshal(body)
	ts := fmt.Sprint(now.Unix())
	req := httptest.NewRequest(http.MethodPost, "/webhooks/incident", bytes.NewReader(raw))
	req.Header.Set("X-Timestamp", ts)
	req.Header.Set("X-Signature", intake.Sign(key, ts, raw))
	return req
}

func serve(s *intake.Server, req *http.Request) (int, map[string]any) {
	rec := httptest.NewRecorder()
	s.Handler().ServeHTTP(rec, req)
	var body map[string]any
	_ = json.Unmarshal(rec.Body.Bytes(), &body)
	return rec.Code, body
}

func server(d intake.Dispatcher) *intake.Server {
	return &intake.Server{Secret: secret, Scopes: testfixtures.Scopes(), Store: memstore.New(), Dispatcher: d, Now: func() time.Time { return now }}
}

func TestVerifySignature(t *testing.T) {
	ts := fmt.Sprint(now.Unix())
	body := []byte("{}")
	if got := intake.VerifySignature(secret, body, ts, intake.Sign(secret, ts, body), now); got != intake.SignatureValid {
		t.Fatal(got)
	}
	if got := intake.VerifySignature(secret, []byte(`{"a":1}`), ts, intake.Sign(secret, ts, body), now); got != intake.SignatureBad {
		t.Fatal(got)
	}
	old := fmt.Sprint(now.Unix() - 600)
	if got := intake.VerifySignature(secret, body, old, intake.Sign(secret, old, body), now); got != intake.SignatureStale {
		t.Fatal(got)
	}
}

func TestDisparaParaIncidenteValido(t *testing.T) {
	d := &recording{}
	code, body := serve(server(d), signed(t, testfixtures.Incident(), secret))
	if code != http.StatusAccepted || body["ref"] != "rca-inc-1042" || len(d.calls) != 1 {
		t.Fatalf("%d %v %d", code, body, len(d.calls))
	}
}

func TestRecusaSegredoErradoSemDisparar(t *testing.T) {
	d := &recording{}
	code, _ := serve(server(d), signed(t, testfixtures.Incident(), "outro-segredo-qualquer-bem-longo"))
	if code != http.StatusUnauthorized || len(d.calls) != 0 {
		t.Fatalf("%d %d", code, len(d.calls))
	}
}

func TestPayloadInvalidoDevolveSoOsCampos(t *testing.T) {
	bad := testfixtures.Incident()
	bad.Severity = "urgente"
	code, body := serve(server(&recording{}), signed(t, bad, secret))
	if code != http.StatusBadRequest || fmt.Sprint(body["fields"]) != "[severity]" {
		t.Fatalf("%d %v", code, body)
	}
}

func TestIgnoraProdutoForaDoInventario(t *testing.T) {
	inc := testfixtures.Incident()
	inc.Product = "billing"
	d := &recording{}
	_, body := serve(server(d), signed(t, inc, secret))
	if body["status"] != "ignored" || len(d.calls) != 0 {
		t.Fatal(body)
	}
}

func TestMesmaEntregaRepetidaDisparaUmaVez(t *testing.T) {
	d := &recording{}
	s := server(d)
	serve(s, signed(t, testfixtures.Incident(), secret))
	_, body := serve(s, signed(t, testfixtures.Incident(), secret))
	if body["status"] != "duplicate" || len(d.calls) != 1 {
		t.Fatalf("%v %d", body, len(d.calls))
	}
}

type flaky struct{ attempts int }

func (f *flaky) Dispatch(_ context.Context, inc incident.Incident) (intake.DispatchResult, error) {
	f.attempts++
	if f.attempts == 1 {
		return intake.DispatchResult{}, errors.New("API do Kubernetes indisponível")
	}
	return intake.DispatchResult{Ref: intake.JobName(inc.ID)}, nil
}

func TestRetryDoRemetenteDisparaDeNovoSeODisparoFalhou(t *testing.T) {
	f := &flaky{}
	s := server(f)
	if code, _ := serve(s, signed(t, testfixtures.Incident(), secret)); code != http.StatusInternalServerError {
		t.Fatal(code)
	}
	if code, _ := serve(s, signed(t, testfixtures.Incident(), secret)); code != http.StatusAccepted || f.attempts != 2 {
		t.Fatalf("%d %d", code, f.attempts)
	}
}

func TestKubernetesDispatcherCriaJobUmaVez(t *testing.T) {
	client := fake.NewSimpleClientset()
	d := intake.KubernetesDispatcher{Client: client, Namespace: "sre-agent", Image: "img", ServiceAccount: "sa", SecretName: "sec",
		Env: intake.ForwardedEnv(func(k string) string { return map[string]string{"PUBLISH_MODE": "shadow"}[k] })}
	first, err := d.Dispatch(t.Context(), testfixtures.Incident())
	if err != nil || first.Duplicate {
		t.Fatal(first, err)
	}
	second, err := d.Dispatch(t.Context(), testfixtures.Incident())
	if err != nil || !second.Duplicate {
		t.Fatal("segundo disparo deveria ser duplicado", second, err)
	}
	job, _ := client.BatchV1().Jobs("sre-agent").Get(t.Context(), "rca-inc-1042", metav1GetOptions())
	c := job.Spec.Template.Spec.Containers[0]
	if *job.Spec.BackoffLimit != 0 || !*c.SecurityContext.ReadOnlyRootFilesystem || job.Spec.Template.Spec.ServiceAccountName != "sa" {
		t.Fatal("Job sem as proteções esperadas")
	}
	found := false
	for _, e := range c.Env {
		found = found || (e.Name == "PUBLISH_MODE" && e.Value == "shadow")
	}
	if !found {
		t.Fatal("PUBLISH_MODE não repassado ao Job")
	}
}

func TestRepassaSoConfiguracaoConhecidaNuncaSegredo(t *testing.T) {
	vars := map[string]string{"PUBLISH_MODE": "shadow", "TRACE_CONTENT": "content", "LANGFUSE_SECRET_KEY": "sk", "DATABASE_URL": "postgres://x"}
	env := intake.ForwardedEnv(func(k string) string { return vars[k] })
	if len(env) != 2 || env[0].Name != "PUBLISH_MODE" || env[1].Name != "TRACE_CONTENT" {
		t.Fatal(env)
	}
}

func TestJobNameEstavel(t *testing.T) {
	if intake.JobName("INC-1042") != "rca-inc-1042" || intake.JobName("INC_1042") != intake.JobName("INC-1042") {
		t.Fatal("nome instável")
	}
}

func metav1GetOptions() metav1.GetOptions { return metav1.GetOptions{} }
