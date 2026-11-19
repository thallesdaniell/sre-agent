import type { Tracer } from "@opentelemetry/api";
import { OTLPTraceExporter } from "@opentelemetry/exporter-trace-otlp-proto";
import { resourceFromAttributes } from "@opentelemetry/resources";
import {
  BatchSpanProcessor,
  ConsoleSpanExporter,
  SimpleSpanProcessor,
  type SpanExporter,
  type SpanProcessor,
} from "@opentelemetry/sdk-trace-base";
import { NodeTracerProvider } from "@opentelemetry/sdk-trace-node";
import { ATTR_SERVICE_NAME, ATTR_SERVICE_VERSION } from "@opentelemetry/semantic-conventions";

/**
 * Rastreio com OpenTelemetry padrão. O código só fala OTel; o destino é escolhido por ambiente:
 *
 * - Langfuse: LANGFUSE_PUBLIC_KEY + LANGFUSE_SECRET_KEY (+ LANGFUSE_BASE_URL, padrão cloud EU)
 * - Qualquer backend OTLP/HTTP (Jaeger, Grafana Tempo, Honeycomb, Datadog...): OTEL_EXPORTER_OTLP_ENDPOINT
 * - Console (debug local): OTEL_TRACES_EXPORTER=console
 * - Nada configurado: rastreio desligado, sem custo.
 *
 * O SDK da Anthropic emite sozinho os spans do loop (anthropic.messages.tool_runner), de cada
 * chamada ao modelo (anthropic.messages.create) e de cada tool executada
 * (anthropic.custom_tool_use <nome>). Nós só abrimos o span raiz da investigação em volta.
 */
export type TraceDestination =
  | { kind: "none" }
  | { kind: "console" }
  | { kind: "langfuse"; url: string; headers: Record<string, string> }
  | { kind: "otlp" }; // o exporter lê OTEL_EXPORTER_OTLP_* sozinho

/** metadata_only = ids, tempos, tokens. content = também prompts, respostas e argumentos de tools. */
export type ContentMode = "metadata_only" | "content";

export function resolveDestination(env: NodeJS.ProcessEnv): TraceDestination {
  const publicKey = env["LANGFUSE_PUBLIC_KEY"];
  const secretKey = env["LANGFUSE_SECRET_KEY"];
  if (publicKey && secretKey) {
    const base = (env["LANGFUSE_BASE_URL"] ?? "https://cloud.langfuse.com").replace(/\/+$/, "");
    return {
      kind: "langfuse",
      url: `${base}/api/public/otel/v1/traces`,
      headers: {
        Authorization: `Basic ${Buffer.from(`${publicKey}:${secretKey}`).toString("base64")}`,
        // Sem este cabeçalho o Langfuse pode atrasar a ingestão em até 10 minutos.
        "x-langfuse-ingestion-version": "4",
      },
    };
  }
  if (env["OTEL_EXPORTER_OTLP_ENDPOINT"] || env["OTEL_EXPORTER_OTLP_TRACES_ENDPOINT"]) return { kind: "otlp" };
  if (env["OTEL_TRACES_EXPORTER"] === "console") return { kind: "console" };
  return { kind: "none" };
}

export function resolveContentMode(env: NodeJS.ProcessEnv): ContentMode {
  return env["TRACE_CONTENT"] === "content" ? "content" : "metadata_only";
}

export interface Telemetry {
  readonly provider: NodeTracerProvider;
  readonly tracer: Tracer;
  readonly destination: TraceDestination["kind"];
  readonly contentMode: ContentMode;
  /** Exporta o que estiver no buffer e fecha. O job é efêmero: sem isto, os últimos spans se perdem. */
  shutdown(): Promise<void>;
}

function exporterFor(destination: TraceDestination): SpanExporter | undefined {
  switch (destination.kind) {
    case "none":
      return undefined;
    case "console":
      return new ConsoleSpanExporter();
    case "langfuse":
      return new OTLPTraceExporter({ url: destination.url, headers: destination.headers });
    case "otlp":
      return new OTLPTraceExporter();
  }
}

export function initTelemetry(options: {
  serviceName: string;
  env?: NodeJS.ProcessEnv;
  /** Testes injetam um exporter em memória. */
  exporter?: SpanExporter;
}): Telemetry {
  const env = options.env ?? process.env;
  const destination = resolveDestination(env);
  const exporter = options.exporter ?? exporterFor(destination);

  const spanProcessors: SpanProcessor[] = [];
  if (exporter) {
    // Em teste, exporta na hora; em produção, em lote.
    spanProcessors.push(options.exporter ? new SimpleSpanProcessor(exporter) : new BatchSpanProcessor(exporter));
  }

  const provider = new NodeTracerProvider({
    resource: resourceFromAttributes({
      [ATTR_SERVICE_NAME]: options.serviceName,
      [ATTR_SERVICE_VERSION]: env["APP_VERSION"] ?? "dev",
    }),
    spanProcessors,
  });
  // register() instala o context manager: é o que faz os spans do SDK nascerem dentro do nosso.
  provider.register();

  return {
    provider,
    tracer: provider.getTracer("sre-agent"),
    destination: options.exporter ? "otlp" : destination.kind,
    contentMode: resolveContentMode(env),
    shutdown: () => provider.shutdown(),
  };
}

/** Monta o link do trace a partir de TRACE_URL_TEMPLATE, ex.: https://cloud.langfuse.com/project/<id>/traces/{traceId} */
export function traceUrl(template: string | undefined, traceId: string): string | undefined {
  return template?.includes("{traceId}") ? template.replace("{traceId}", traceId) : undefined;
}
