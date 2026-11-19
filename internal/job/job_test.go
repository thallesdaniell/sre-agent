package job_test

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"slices"
	"strings"
	"sync"
	"testing"

	"github.com/anthropics/anthropic-sdk-go/v2"
	"github.com/anthropics/anthropic-sdk-go/v2/option"
	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/codes"
	"go.opentelemetry.io/otel/sdk/trace/tracetest"

	"github.com/thallesdaniell/sre-agent/internal/agent"
	"github.com/thallesdaniell/sre-agent/internal/config"
	"github.com/thallesdaniell/sre-agent/internal/incident"
	"github.com/thallesdaniell/sre-agent/internal/integrations"
	"github.com/thallesdaniell/sre-agent/internal/job"
	"github.com/thallesdaniell/sre-agent/internal/report"
	"github.com/thallesdaniell/sre-agent/internal/store"
	"github.com/thallesdaniell/sre-agent/internal/store/memstore"
	"github.com/thallesdaniell/sre-agent/internal/telemetry"
	"github.com/thallesdaniell/sre-agent/internal/testfixtures"
)

var usage = store.Usage{InputTokens: 100, OutputTokens: 20}

func setup(t *testing.T) (*tracetest.InMemoryExporter, *telemetry.Telemetry) {
	t.Helper()
	exp := tracetest.NewInMemoryExporter()
	tel, err := telemetry.Init(t.Context(), "sre-agent-test", func(string) string { return "" }, exp)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = tel.Shutdown(context.Background()) })
	return exp, tel
}

func deps(st store.Store, tel *telemetry.Telemetry) job.Deps {
	return job.Deps{
		Store:   st,
		Connect: func(context.Context) (*integrations.Toolset, error) { return &integrations.Toolset{}, nil },
		Investigate: func(context.Context, incident.Incident, *integrations.Toolset) (agent.Result, error) {
			return agent.Result{Report: testfixtures.Report(), Usage: usage}, nil
		},
		Publish: func(context.Context, report.RcaReport, string) (store.Delivery, error) {
			return store.Delivery{Kind: store.DeliveryShadow}, nil
		},
		PublishMode: config.PublishShadow,
		Tracer:      tel.Tracer,
		ContentMode: tel.ContentMode,
	}
}

func root(t *testing.T, exp *tracetest.InMemoryExporter) tracetest.SpanStub {
	t.Helper()
	for _, s := range exp.GetSpans() {
		if s.Name == "rca.investigation" {
			return s
		}
	}
	t.Fatal("span rca.investigation não encontrado")
	return tracetest.SpanStub{}
}

func attr(s tracetest.SpanStub, key string) attribute.Value {
	for _, a := range s.Attributes {
		if string(a.Key) == key {
			return a.Value
		}
	}
	return attribute.Value{}
}

func TestRegistraRunSalvaRelatorioEGuardaTraceID(t *testing.T) {
	exp, tel := setup(t)
	st := memstore.New()
	res, err := job.Execute(t.Context(), testfixtures.Incident(), deps(st, tel))
	if err != nil {
		t.Fatal(err)
	}
	r := root(t, exp)
	if r.Status.Code != codes.Ok || attr(r, "langfuse.session.id").AsString() != "webhook:INC-1042" ||
		!slices.Equal(attr(r, "langfuse.trace.tags").AsStringSlice(), []string{"checkout", "sev2", "shadow"}) ||
		attr(r, "rca.delivery").AsString() != "shadow" {
		t.Fatalf("span raiz errado: %+v", r.Attributes)
	}
	if res.TraceID != r.SpanContext.TraceID().String() || st.Runs[0].TraceID != res.TraceID {
		t.Fatal("trace_id não guardado no run")
	}
	if st.Runs[0].Status != "SUCCEEDED" || st.Runs[0].Outcome.Usage != usage || st.Reports[0].Delivery.Kind != store.DeliveryShadow {
		t.Fatalf("run ou relatório errado: %+v %+v", st.Runs[0], st.Reports[0])
	}
}

func TestFalhaDoAgenteViraFailedComCodigoTokensEErrorNoSpan(t *testing.T) {
	exp, tel := setup(t)
	st := memstore.New()
	d := deps(st, tel)
	d.Investigate = func(context.Context, incident.Incident, *integrations.Toolset) (agent.Result, error) {
		return agent.Result{}, &agent.InvestigationError{Code: store.FailureMaxIterations, Msg: "30 voltas", Usage: usage}
	}
	if _, err := job.Execute(t.Context(), testfixtures.Incident(), d); err == nil {
		t.Fatal("esperava erro")
	}
	if st.Runs[0].Status != "FAILED" || st.Runs[0].Outcome.Failure != store.FailureMaxIterations || st.Runs[0].Outcome.Usage != usage {
		t.Fatalf("%+v", st.Runs[0])
	}
	if r := root(t, exp); r.Status.Code != codes.Error || attr(r, "rca.failure_code").AsString() != "max_iterations" {
		t.Fatal("span sem erro")
	}
}

func TestFalhaAoConectarIntegracaoViraIntegrationError(t *testing.T) {
	_, tel := setup(t)
	st := memstore.New()
	d := deps(st, tel)
	d.Connect = func(context.Context) (*integrations.Toolset, error) { return nil, io.ErrUnexpectedEOF }
	_, _ = job.Execute(t.Context(), testfixtures.Incident(), d)
	if st.Runs[0].Outcome.Failure != store.FailureIntegrationError {
		t.Fatal(st.Runs[0].Outcome)
	}
}

func TestSeOSlackFalharORelatorioJaEstaSalvo(t *testing.T) {
	_, tel := setup(t)
	st := memstore.New()
	d := deps(st, tel)
	d.Publish = func(context.Context, report.RcaReport, string) (store.Delivery, error) {
		return store.Delivery{}, io.ErrClosedPipe
	}
	_, _ = job.Execute(t.Context(), testfixtures.Incident(), d)
	if len(st.Reports) != 1 || st.Reports[0].Delivery.Kind != store.DeliveryPending || st.Runs[0].Outcome.Failure != store.FailurePublishError {
		t.Fatal("relatório perdido ou código errado")
	}
}

func TestNaoRodaDuasInvestigacoesDoMesmoIncidente(t *testing.T) {
	_, tel := setup(t)
	st := memstore.New()
	claim, _ := st.ClaimCase(t.Context(), testfixtures.Incident())
	_, _ = st.StartRun(t.Context(), claim.CaseID, "m", "")
	called := false
	d := deps(st, tel)
	d.Investigate = func(context.Context, incident.Incident, *integrations.Toolset) (agent.Result, error) {
		called = true
		return agent.Result{}, nil
	}
	res, err := job.Execute(t.Context(), testfixtures.Incident(), d)
	if err != nil || !res.Skipped || called {
		t.Fatal(res, err, called)
	}
}

func TestSemConteudoPorPadrao(t *testing.T) {
	exp, tel := setup(t)
	if _, err := job.Execute(t.Context(), testfixtures.Incident(), deps(memstore.New(), tel)); err != nil {
		t.Fatal(err)
	}
	if attr(root(t, exp), "langfuse.observation.output").AsString() != "" {
		t.Fatal("relatório vazou para o trace sem TRACE_CONTENT=content")
	}
	exp.Reset()
	d := deps(memstore.New(), tel)
	d.ContentMode = option.OpenTelemetryContentModeContent
	if _, err := job.Execute(t.Context(), testfixtures.Incident(), d); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(attr(root(t, exp), "langfuse.observation.output").AsString(), "revisão 42") {
		t.Fatal("conteúdo não exportado com content")
	}
}

// fakeAnthropic faz o papel da API: responde "chame submit_rca_report" e depois "fim".
func fakeAnthropic(t *testing.T, submitted any) (*httptest.Server, *[]string) {
	var mu sync.Mutex
	var paths []string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = io.ReadAll(r.Body)
		mu.Lock()
		paths = append(paths, r.URL.Path)
		n := len(paths)
		mu.Unlock()
		reply := map[string]any{"content": []any{map[string]any{"type": "text", "text": "Relatório entregue."}}, "stop_reason": "end_turn"}
		if n == 1 {
			reply = map[string]any{"content": []any{map[string]any{"type": "tool_use", "id": "toolu_01", "name": "submit_rca_report", "input": submitted}}, "stop_reason": "tool_use"}
		}
		reply["id"], reply["type"], reply["role"], reply["model"] = "msg_x", "message", "assistant", "claude-opus-5-5"
		reply["stop_sequence"], reply["usage"] = nil, map[string]any{"input_tokens": 50, "output_tokens": 10}
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(reply)
	}))
	t.Cleanup(srv.Close)
	return srv, &paths
}

func TestLoopCompletoDoAgenteComAPIFalsaESpansNoMesmoTrace(t *testing.T) {
	exp, tel := setup(t)
	srv, _ := fakeAnthropic(t, testfixtures.Report())
	client := anthropic.NewClient(option.WithAPIKey("test"), option.WithBaseURL(srv.URL), option.WithMaxRetries(0),
		option.WithTracerProvider(tel.Provider), option.WithOpenTelemetryTracesContentMode(option.OpenTelemetryContentModeMetadataOnly))

	st := memstore.New()
	d := deps(st, tel)
	d.Investigate = func(ctx context.Context, inc incident.Incident, ts *integrations.Toolset) (agent.Result, error) {
		return agent.Investigate(ctx, client, inc, ts)
	}
	if _, err := job.Execute(t.Context(), testfixtures.Incident(), d); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(st.Reports[0].Report.RootCause.Statement, "revisão 42") {
		t.Fatal("relatório não chegou")
	}

	r := root(t, exp)
	var names []string
	for _, s := range exp.GetSpans() {
		names = append(names, s.Name)
		if s.SpanContext.TraceID() != r.SpanContext.TraceID() {
			t.Errorf("span %s fora do trace da investigação", s.Name)
		}
	}
	for _, want := range []string{"anthropic.messages.tool_runner", "anthropic.messages.create", "anthropic.custom_tool_use submit_rca_report"} {
		if !slices.Contains(names, want) {
			t.Errorf("faltou o span %s; spans: %v", want, names)
		}
	}
}

func TestRelatorioInvalidoVoltaParaOModeloComoErro(t *testing.T) {
	_, tel := setup(t)
	bad := testfixtures.Report()
	bad.Status = "CONFIRMED"
	srv, paths := fakeAnthropic(t, bad)
	client := anthropic.NewClient(option.WithAPIKey("test"), option.WithBaseURL(srv.URL), option.WithMaxRetries(0), option.WithTracerProvider(tel.Provider))
	_, err := agent.Investigate(t.Context(), client, testfixtures.Incident(), &integrations.Toolset{})
	var ie *agent.InvestigationError
	if err == nil || !errors.As(err, &ie) || ie.Code != store.FailureNoReport {
		t.Fatalf("esperava no_report (relatório recusado e modelo encerrou), veio %v", err)
	}
	if len(*paths) != 2 {
		t.Fatalf("esperava 2 chamadas ao modelo (o erro voltou para ele), vieram %d", len(*paths))
	}
}
