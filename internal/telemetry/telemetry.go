// Package telemetry configura o rastreio com OpenTelemetry padrão. O código só fala OTel; o
// destino é escolhido por ambiente:
//
//   - Langfuse: LANGFUSE_PUBLIC_KEY + LANGFUSE_SECRET_KEY (+ LANGFUSE_BASE_URL, padrão cloud EU)
//   - Qualquer backend OTLP/HTTP (Jaeger, Tempo, Datadog Agent...): OTEL_EXPORTER_OTLP_ENDPOINT
//   - Console (debug local): OTEL_TRACES_EXPORTER=console
//   - Nada configurado: rastreio desligado, sem custo e sem trace_id falso no banco.
//
// O SDK da Anthropic emite sozinho os spans do loop, de cada chamada ao modelo e de cada tool.
// Nós só abrimos o span raiz da investigação em volta.
package telemetry

import (
	"context"
	"encoding/base64"
	"strings"

	"github.com/anthropics/anthropic-sdk-go/v2/option"
	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/exporters/otlp/otlptrace/otlptracehttp"
	"go.opentelemetry.io/otel/exporters/stdout/stdouttrace"
	"go.opentelemetry.io/otel/propagation"
	"go.opentelemetry.io/otel/sdk/resource"
	sdktrace "go.opentelemetry.io/otel/sdk/trace"
	"go.opentelemetry.io/otel/trace"
	"go.opentelemetry.io/otel/trace/noop"
)

type DestinationKind string

const (
	DestNone     DestinationKind = "none"
	DestConsole  DestinationKind = "console"
	DestLangfuse DestinationKind = "langfuse"
	DestOTLP     DestinationKind = "otlp" // o exporter lê OTEL_EXPORTER_OTLP_* sozinho
)

type Destination struct {
	Kind    DestinationKind
	URL     string
	Headers map[string]string
}

func ResolveDestination(getenv func(string) string) Destination {
	pk, sk := getenv("LANGFUSE_PUBLIC_KEY"), getenv("LANGFUSE_SECRET_KEY")
	if pk != "" && sk != "" {
		base := strings.TrimRight(getenv("LANGFUSE_BASE_URL"), "/")
		if base == "" {
			base = "https://cloud.langfuse.com"
		}
		return Destination{
			Kind: DestLangfuse,
			URL:  base + "/api/public/otel/v1/traces",
			Headers: map[string]string{
				"Authorization": "Basic " + base64.StdEncoding.EncodeToString([]byte(pk+":"+sk)),
				// Sem este cabeçalho o Langfuse pode atrasar a ingestão em até 10 minutos.
				"x-langfuse-ingestion-version": "4",
			},
		}
	}
	if getenv("OTEL_EXPORTER_OTLP_ENDPOINT") != "" || getenv("OTEL_EXPORTER_OTLP_TRACES_ENDPOINT") != "" {
		return Destination{Kind: DestOTLP}
	}
	if getenv("OTEL_TRACES_EXPORTER") == "console" {
		return Destination{Kind: DestConsole}
	}
	return Destination{Kind: DestNone}
}

// ContentMode: metadata_only = ids, tempos, tokens. content = também prompts, respostas e
// argumentos das tools. Só num backend que você controla: logs de produção vão junto.
func ResolveContentMode(getenv func(string) string) option.OpenTelemetryContentMode {
	if getenv("TRACE_CONTENT") == "content" {
		return option.OpenTelemetryContentModeContent
	}
	return option.OpenTelemetryContentModeMetadataOnly
}

type Telemetry struct {
	Provider    trace.TracerProvider
	Tracer      trace.Tracer
	ContentMode option.OpenTelemetryContentMode
	Destination DestinationKind
	shutdown    func(context.Context) error
}

// Shutdown exporta o que estiver no buffer. O job é efêmero: sem isto, os últimos spans se perdem.
func (t *Telemetry) Shutdown(ctx context.Context) error { return t.shutdown(ctx) }

// Init registra o provider. testExporter != nil (testes) exporta na hora, sem ler o ambiente.
func Init(ctx context.Context, serviceName string, getenv func(string) string, testExporter sdktrace.SpanExporter) (*Telemetry, error) {
	dest := ResolveDestination(getenv)
	contentMode := ResolveContentMode(getenv)

	var processor sdktrace.SpanProcessor
	switch {
	case testExporter != nil:
		processor = sdktrace.NewSimpleSpanProcessor(testExporter)
		dest.Kind = DestOTLP
	case dest.Kind == DestNone:
		p := noop.NewTracerProvider()
		return &Telemetry{Provider: p, Tracer: p.Tracer("sre-agent"), ContentMode: contentMode, Destination: DestNone,
			shutdown: func(context.Context) error { return nil }}, nil
	default:
		exporter, err := newExporter(ctx, dest)
		if err != nil {
			return nil, err
		}
		processor = sdktrace.NewBatchSpanProcessor(exporter)
	}

	version := getenv("APP_VERSION")
	if version == "" {
		version = "dev"
	}
	provider := sdktrace.NewTracerProvider(
		sdktrace.WithSpanProcessor(processor),
		sdktrace.WithResource(resource.NewSchemaless(
			attribute.String("service.name", serviceName),
			attribute.String("service.version", version),
		)),
	)
	otel.SetTracerProvider(provider)
	// O Go não registra propagador por padrão; sem ele o SDK não manda traceparent para a API.
	otel.SetTextMapPropagator(propagation.TraceContext{})

	return &Telemetry{Provider: provider, Tracer: provider.Tracer("sre-agent"), ContentMode: contentMode,
		Destination: dest.Kind, shutdown: provider.Shutdown}, nil
}

func newExporter(ctx context.Context, dest Destination) (sdktrace.SpanExporter, error) {
	switch dest.Kind {
	case DestLangfuse:
		return otlptracehttp.New(ctx, otlptracehttp.WithEndpointURL(dest.URL), otlptracehttp.WithHeaders(dest.Headers))
	case DestConsole:
		return stdouttrace.New()
	default:
		return otlptracehttp.New(ctx)
	}
}

// TraceURL monta o link do trace a partir de TRACE_URL_TEMPLATE,
// ex.: https://cloud.langfuse.com/project/<id>/traces/{traceId}
func TraceURL(template, traceID string) string {
	if traceID == "" || !strings.Contains(template, "{traceId}") {
		return ""
	}
	return strings.ReplaceAll(template, "{traceId}", traceID)
}
