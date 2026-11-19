package telemetry_test

import (
	"encoding/base64"
	"testing"

	"github.com/anthropics/anthropic-sdk-go/v2/option"

	"github.com/thallesdaniell/sre-agent/internal/telemetry"
)

func env(m map[string]string) func(string) string { return func(k string) string { return m[k] } }

func TestLangfuseEndpointBasicAuthEVersaoDeIngestao(t *testing.T) {
	d := telemetry.ResolveDestination(env(map[string]string{
		"LANGFUSE_PUBLIC_KEY": "pk-lf-1", "LANGFUSE_SECRET_KEY": "sk-lf-2", "LANGFUSE_BASE_URL": "http://localhost:3000/",
	}))
	if d.Kind != telemetry.DestLangfuse || d.URL != "http://localhost:3000/api/public/otel/v1/traces" {
		t.Fatalf("destino errado: %+v", d)
	}
	if d.Headers["Authorization"] != "Basic "+base64.StdEncoding.EncodeToString([]byte("pk-lf-1:sk-lf-2")) || d.Headers["x-langfuse-ingestion-version"] != "4" {
		t.Fatalf("cabeçalhos errados: %v", d.Headers)
	}
}

func TestLangfuseSemBaseURLUsaCloudEU(t *testing.T) {
	d := telemetry.ResolveDestination(env(map[string]string{"LANGFUSE_PUBLIC_KEY": "pk", "LANGFUSE_SECRET_KEY": "sk"}))
	if d.URL != "https://cloud.langfuse.com/api/public/otel/v1/traces" {
		t.Fatal(d.URL)
	}
}

func TestOutrosDestinos(t *testing.T) {
	cases := map[telemetry.DestinationKind]map[string]string{
		telemetry.DestOTLP:    {"OTEL_EXPORTER_OTLP_ENDPOINT": "http://jaeger:4318"},
		telemetry.DestConsole: {"OTEL_TRACES_EXPORTER": "console"},
		telemetry.DestNone:    {},
	}
	for want, vars := range cases {
		if got := telemetry.ResolveDestination(env(vars)).Kind; got != want {
			t.Errorf("%v: veio %s", vars, got)
		}
	}
}

func TestConteudoSoQuandoPedido(t *testing.T) {
	if telemetry.ResolveContentMode(env(nil)) != option.OpenTelemetryContentModeMetadataOnly {
		t.Fatal("padrão deveria ser metadata_only")
	}
	if telemetry.ResolveContentMode(env(map[string]string{"TRACE_CONTENT": "content"})) != option.OpenTelemetryContentModeContent {
		t.Fatal("content não respeitado")
	}
}

func TestTraceURL(t *testing.T) {
	if got := telemetry.TraceURL("https://lf/project/p1/traces/{traceId}", "abc"); got != "https://lf/project/p1/traces/abc" {
		t.Fatal(got)
	}
	if telemetry.TraceURL("https://lf", "abc") != "" || telemetry.TraceURL("x/{traceId}", "") != "" {
		t.Fatal("template sem marcador ou sem trace deveria dar vazio")
	}
}

func TestSemDestinoNaoGeraTraceIDFalso(t *testing.T) {
	tel, err := telemetry.Init(t.Context(), "teste", env(nil), nil)
	if err != nil {
		t.Fatal(err)
	}
	_, span := tel.Tracer.Start(t.Context(), "x")
	defer span.End()
	if span.SpanContext().IsValid() {
		t.Fatal("rastreio desligado não deveria gerar trace_id")
	}
}
