import { readFileSync } from "node:fs";
import { pathToFileURL } from "node:url";
import { InvestigationError, MODEL, investigate } from "./agent/investigate.js";
import { loadAgentConfig } from "./agent-config.js";
import { loadJobEnv } from "./config.js";
import { createPool } from "./db/pool.js";
import { Incident } from "./incident.js";
import { connectIntegrations, resolveIntegrations, type ConnectedToolset } from "./integrations/registry.js";
import { publishReport, type PublishResult } from "./slack/post.js";
import { PgStore } from "./store/pg-store.js";
import type { RunId, Store } from "./store/types.js";

/** Peças trocáveis do job: em produção são as reais, nos testes são falsas. */
export interface JobDeps {
  store: Store;
  connect: () => Promise<ConnectedToolset>;
  investigate: typeof investigate;
  publish: (report: Parameters<typeof publishReport>[0], channel: string | undefined) => Promise<PublishResult>;
}

export type JobResult =
  | { kind: "succeeded"; runId: RunId; published: PublishResult["kind"] }
  | { kind: "skipped"; reason: "already_running" };

const NO_USAGE = { inputTokens: 0, outputTokens: 0 };

/** Erro ao conectar uma integração (credencial, rede, allowlist): vira failure_code próprio. */
class IntegrationConnectError extends Error {
  override readonly name = "IntegrationConnectError";
}

/**
 * Um processo, um incidente. Todo desfecho fica registrado no banco:
 * o run sempre termina SUCCEEDED ou FAILED com motivo, e o relatório é salvo ANTES de publicar,
 * para não se perder se o Slack falhar.
 */
export async function executeJob(incident: Incident, deps: JobDeps): Promise<JobResult> {
  const { caseId } = await deps.store.claimCase(incident);
  const start = await deps.store.startRun(caseId, MODEL);
  if (start.kind === "already_running") return { kind: "skipped", reason: "already_running" };
  const { runId } = start;

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
    await deps.store.finishRun(runId, { kind: "failed", code, detail: errorMessage(error), ...usage });
    throw error;
  }

  const { report, usage } = result;
  const reportId = await deps.store.saveReport({ runId, caseId, report, slackChannel: incident.slackChannel });

  let published: PublishResult;
  try {
    published = await deps.publish(report, incident.slackChannel);
  } catch (error) {
    await deps.store.finishRun(runId, { kind: "failed", code: "publish_error", detail: errorMessage(error), ...usage });
    throw error;
  }
  if (published.kind === "posted") await deps.store.markPublished(reportId, published.ts);

  await deps.store.finishRun(runId, { kind: "succeeded", ...usage });
  return { kind: "succeeded", runId, published: published.kind };
}

/** Monta as dependências reais a partir do ambiente e executa. */
export async function runJob(incident: Incident, sharedStore?: Store): Promise<JobResult> {
  const env = loadJobEnv();
  const { config, scopes } = loadAgentConfig(env.AGENT_CONFIG_FILE);
  scopes.get(incident.product); // recusa cedo se o produto não está no inventário

  const store = sharedStore ?? new PgStore(createPool(env.DATABASE_URL, 2));
  const startedAt = Date.now();
  try {
    const result = await executeJob(incident, {
      store,
      connect: () => connectIntegrations(resolveIntegrations(config), { scopes, store, currentIncidentId: incident.id, env: process.env }),
      investigate,
      publish: (report, channel) => publishReport(report, channel, env.SLACK_BOT_TOKEN),
    });
    console.error(JSON.stringify({ event: "rca.finished", incidentId: incident.id, ...result, durationMs: Date.now() - startedAt }));
    return result;
  } finally {
    if (!sharedStore) await store.close();
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
