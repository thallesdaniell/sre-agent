import { readFileSync } from "node:fs";
import { pathToFileURL } from "node:url";
import Anthropic from "@anthropic-ai/sdk";
import { SpanStatusCode, isSpanContextValid, trace, type Span, type Tracer } from "@opentelemetry/api";
import { InvestigationError, MODEL, investigate } from "./agent/investigate.js";
import { loadAgentConfig } from "./agent-config.js";
import { loadJobEnv } from "./config.js";
import { createPool } from "./db/pool.js";
import { Incident } from "./incident.js";
import { connectIntegrations, resolveIntegrations, type ConnectedToolset } from "./integrations/registry.js";
import type { RcaReport } from "./report/schema.js";
import { publishReport, type PublishMode } from "./slack/post.js";
import { PgStore } from "./store/pg-store.js";
import type { Delivery, RunId, Store } from "./store/types.js";
import { initTelemetry, traceUrl, type ContentMode, type Telemetry } from "./telemetry.js";

/** Peças trocáveis do job: em produção são as reais, nos testes são falsas. */
export interface JobDeps {
  store: Store;
  connect: () => Promise<ConnectedToolset>;
  investigate: (incident: Incident, toolset: ConnectedToolset) => ReturnType<typeof investigate>;
  publish: (report: RcaReport, context: { traceUrl: string | undefined }) => Promise<Delivery>;
  publishMode: PublishMode;
  tracer?: Tracer;
  contentMode?: ContentMode;
  traceUrlFor?: (traceId: string) => string | undefined;
}

export type JobResult =
  | { kind: "succeeded"; runId: RunId; delivery: Delivery["kind"]; traceId: string | undefined }
  | { kind: "skipped"; reason: "already_running" };

const NO_USAGE = { inputTokens: 0, outputTokens: 0 };

/** Erro ao conectar uma integração (credencial, rede, allowlist): vira failure_code próprio. */
class IntegrationConnectError extends Error {
  override readonly name = "IntegrationConnectError";
}

/**
 * Atributos do span raiz. Os `langfuse.*` viram nome, sessão, tags e metadados do trace no
 * Langfuse; outros backends só os mostram como atributos comuns, sem prejuízo.
 */
function rootAttributes(incident: Incident, publishMode: PublishMode) {
  return {
    "langfuse.trace.name": "rca-investigation",
    // A sessão agrupa todas as tentativas do mesmo incidente.
    "langfuse.session.id": `${incident.source}:${incident.id}`,
    "langfuse.trace.tags": [incident.product, incident.severity, publishMode],
    "langfuse.trace.metadata.product": incident.product,
    "langfuse.trace.metadata.severity": incident.severity,
    "langfuse.trace.metadata.publish_mode": publishMode,
    "incident.id": incident.id,
    "incident.source": incident.source,
    "incident.product": incident.product,
  };
}

/**
 * Um processo, um incidente. Todo desfecho fica registrado no banco e no trace:
 * o run sempre termina SUCCEEDED ou FAILED com motivo, e o relatório é salvo ANTES de publicar,
 * para não se perder se o Slack falhar.
 */
export async function executeJob(incident: Incident, deps: JobDeps): Promise<JobResult> {
  const tracer = deps.tracer ?? trace.getTracer("sre-agent");
  return tracer.startActiveSpan("rca.investigation", { attributes: rootAttributes(incident, deps.publishMode) }, async (span) => {
    try {
      const result = await investigateAndRecord(incident, deps, span);
      span.setStatus({ code: SpanStatusCode.OK });
      return result;
    } catch (error) {
      span.recordException(error instanceof Error ? error : new Error(String(error)));
      span.setStatus({ code: SpanStatusCode.ERROR, message: errorMessage(error) });
      throw error;
    } finally {
      span.end();
    }
  });
}

async function investigateAndRecord(incident: Incident, deps: JobDeps, span: Span): Promise<JobResult> {
  const context = span.spanContext();
  const traceId = isSpanContextValid(context) ? context.traceId : undefined;

  const { caseId } = await deps.store.claimCase(incident);
  const start = await deps.store.startRun(caseId, MODEL, traceId);
  if (start.kind === "already_running") {
    span.setAttribute("rca.outcome", "skipped_already_running");
    return { kind: "skipped", reason: "already_running" };
  }
  const { runId } = start;
  span.setAttributes({ "langfuse.trace.metadata.run_id": runId, "langfuse.trace.metadata.case_id": caseId });
  if (deps.contentMode === "content") span.setAttribute("langfuse.observation.input", JSON.stringify(incident));

  let result: Awaited<ReturnType<typeof investigate>>;
  try {
    const toolset = await deps.connect().catch((error: unknown) => {
      throw new IntegrationConnectError(error instanceof Error ? error.message : String(error));
    });
    try {
      result = await deps.investigate(incident, toolset);
    } finally {
      await toolset.close();
    }
  } catch (error) {
    const code =
      error instanceof InvestigationError ? error.code : error instanceof IntegrationConnectError ? "integration_error" : "unknown";
    const usage = error instanceof InvestigationError ? error.usage : NO_USAGE;
    span.setAttributes({ "rca.outcome": "failed", "rca.failure_code": code });
    await deps.store.finishRun(runId, { kind: "failed", code, detail: errorMessage(error), ...usage });
    throw error;
  }

  const { report, usage } = result;
  span.setAttributes({
    "rca.outcome": "succeeded",
    "rca.status": report.status,
    "rca.confidence": report.rootCause.confidence,
    "rca.change_kind": report.changeAttribution.kind,
  });
  if (deps.contentMode === "content") span.setAttribute("langfuse.observation.output", JSON.stringify(report));

  const reportId = await deps.store.saveReport({ runId, caseId, report, slackChannel: incident.slackChannel });

  let delivery: Delivery;
  try {
    delivery = await deps.publish(report, { traceUrl: traceId ? deps.traceUrlFor?.(traceId) : undefined });
  } catch (error) {
    span.setAttributes({ "rca.outcome": "failed", "rca.failure_code": "publish_error" });
    await deps.store.finishRun(runId, { kind: "failed", code: "publish_error", detail: errorMessage(error), ...usage });
    throw error;
  }
  await deps.store.markDelivered(reportId, delivery);
  span.setAttribute("rca.delivery", delivery.kind);

  await deps.store.finishRun(runId, { kind: "succeeded", ...usage });
  return { kind: "succeeded", runId, delivery: delivery.kind, traceId };
}

/** Monta as dependências reais a partir do ambiente e executa. */
export async function runJob(incident: Incident, shared: { store?: Store; telemetry?: Telemetry } = {}): Promise<JobResult> {
  const env = loadJobEnv();
  const { config, scopes } = loadAgentConfig(env.AGENT_CONFIG_FILE);
  scopes.get(incident.product); // recusa cedo se o produto não está no inventário

  // Registrado antes da primeira chamada ao Claude, senão os spans do SDK não nascem dentro do nosso.
  const telemetry = shared.telemetry ?? initTelemetry({ serviceName: "sre-agent-job" });
  const store = shared.store ?? new PgStore(createPool(env.DATABASE_URL, 2));
  const client = new Anthropic({
    openTelemetry: { tracerProvider: telemetry.provider, traces: { contentMode: telemetry.contentMode } },
  });

  const startedAt = Date.now();
  try {
    const result = await executeJob(incident, {
      store,
      connect: () => connectIntegrations(resolveIntegrations(config), { scopes, store, currentIncidentId: incident.id, env: process.env }),
      investigate: (i, toolset) => investigate(i, toolset, client),
      publish: (report, context) =>
        publishReport(report, { mode: env.PUBLISH_MODE, channel: incident.slackChannel, token: env.SLACK_BOT_TOKEN, traceUrl: context.traceUrl }),
      publishMode: env.PUBLISH_MODE,
      tracer: telemetry.tracer,
      contentMode: telemetry.contentMode,
      traceUrlFor: (traceId) => traceUrl(env.TRACE_URL_TEMPLATE, traceId),
    });
    console.error(JSON.stringify({ event: "rca.finished", incidentId: incident.id, ...result, durationMs: Date.now() - startedAt }));
    return result;
  } finally {
    if (!shared.store) await store.close();
    if (!shared.telemetry) await telemetry.shutdown(); // exporta os spans pendentes antes do processo sair
  }
}

function errorMessage(error: unknown): string {
  return error instanceof Error ? error.message : String(error);
}

function readIncident(): Incident {
  const fromFile = process.argv[2];
  const raw = fromFile ? readFileSync(fromFile, "utf8") : process.env["INCIDENT_JSON"];
  if (!raw) throw new Error("Passe o incidente em INCIDENT_JSON ou como caminho de arquivo");
  return Incident.parse(JSON.parse(raw));
}

if (import.meta.url === pathToFileURL(process.argv[1] ?? "").href) {
  runJob(readIncident()).catch((error: unknown) => {
    console.error(JSON.stringify({ event: "rca.failed", error: errorMessage(error) }));
    process.exit(1);
  });
}
