import { createServer, type Server } from "node:http";
import type { AddressInfo } from "node:net";
import Anthropic from "@anthropic-ai/sdk";
import { SpanStatusCode } from "@opentelemetry/api";
import { InMemorySpanExporter, type ReadableSpan } from "@opentelemetry/sdk-trace-base";
import { afterAll, beforeAll, beforeEach, describe, expect, it } from "vitest";
import { InvestigationError, investigate } from "../src/agent/investigate.js";
import { executeJob, type JobDeps } from "../src/job.js";
import { initTelemetry, type Telemetry } from "../src/telemetry.js";
import { sampleIncident, sampleReport } from "./support/fixtures.js";
import { MemoryStore } from "./support/memory-store.js";

const exporter = new InMemorySpanExporter();
let telemetry: Telemetry;

beforeAll(() => {
  telemetry = initTelemetry({ serviceName: "sre-agent-test", exporter, env: {} });
});
afterAll(async () => {
  await telemetry.shutdown();
});
beforeEach(() => exporter.reset());

const usage = { inputTokens: 100, outputTokens: 20 };

function deps(store: MemoryStore, overrides: Partial<JobDeps> = {}): JobDeps {
  return {
    store,
    connect: async () => ({ tools: [], sourcesSummary: "", close: async () => {} }),
    investigate: async () => ({ report: sampleReport, usage }),
    publish: async () => ({ kind: "shadow" }),
    publishMode: "shadow",
    tracer: telemetry.tracer,
    ...overrides,
  };
}

function rootSpan(): ReadableSpan {
  const root = exporter.getFinishedSpans().find((s) => s.name === "rca.investigation");
  if (!root) throw new Error("span rca.investigation não encontrado");
  return root;
}

describe("rastreio do job", () => {
  it("abre o span raiz com os atributos do Langfuse e guarda o trace_id no run", async () => {
    const store = new MemoryStore();
    const result = await executeJob(sampleIncident, deps(store));
    const root = rootSpan();

    expect(root.status.code).toBe(SpanStatusCode.OK);
    expect(root.attributes).toMatchObject({
      "langfuse.trace.name": "rca-investigation",
      "langfuse.session.id": "webhook:INC-1042",
      "langfuse.trace.tags": ["checkout", "sev2", "shadow"],
      "rca.status": "PRELIMINARY",
      "rca.delivery": "shadow",
    });
    expect(result).toMatchObject({ kind: "succeeded", traceId: root.spanContext().traceId });
    expect(store.runs[0]?.traceId).toBe(root.spanContext().traceId);
  });

  it("modo sombra: relatório salvo como shadow e nada publicado", async () => {
    const store = new MemoryStore();
    await executeJob(sampleIncident, deps(store));
    expect(store.reports[0]).toMatchObject({ delivery: "shadow" });
    expect(store.reports[0]?.slackTs).toBeUndefined();
  });

  it("falha marca o span com ERROR e o failure_code", async () => {
    const store = new MemoryStore();
    const failing = deps(store, {
      investigate: async () => {
        throw new InvestigationError("no_report", "sem relatório", usage);
      },
    });
    await expect(executeJob(sampleIncident, failing)).rejects.toThrow();
    const root = rootSpan();
    expect(root.status.code).toBe(SpanStatusCode.ERROR);
    expect(root.attributes["rca.failure_code"]).toBe("no_report");
  });

  it("sem conteúdo por padrão: incidente e relatório não vão para o trace", async () => {
    await executeJob(sampleIncident, deps(new MemoryStore()));
    expect(rootSpan().attributes["langfuse.observation.output"]).toBeUndefined();

    exporter.reset();
    await executeJob({ ...sampleIncident, id: "INC-2000" }, deps(new MemoryStore(), { contentMode: "content" }));
    expect(String(rootSpan().attributes["langfuse.observation.output"])).toContain("revisão 42");
  });
});

/**
 * O loop completo do agente, offline: um servidor HTTP local faz o papel da API da Anthropic e
 * responde, na ordem, "chame submit_rca_report" e depois "fim". Testa o Tool Runner de verdade
 * (validação do relatório, execução da tool) e a árvore de spans, sem chave nem custo.
 */
describe("loop do agente com API falsa", () => {
  let server: Server;
  let baseURL: string;
  const requests: unknown[] = [];

  beforeAll(async () => {
    const replies = [
      {
        content: [{ type: "tool_use", id: "toolu_01", name: "submit_rca_report", input: sampleReport }],
        stop_reason: "tool_use",
      },
      { content: [{ type: "text", text: "Relatório entregue." }], stop_reason: "end_turn" },
    ];
    server = createServer((req, res) => {
      let body = "";
      req.on("data", (chunk) => (body += chunk));
      req.on("end", () => {
        requests.push(JSON.parse(body));
        const reply = replies[Math.min(requests.length - 1, replies.length - 1)]!;
        res.writeHead(200, { "content-type": "application/json" });
        res.end(
          JSON.stringify({
            id: `msg_${requests.length}`,
            type: "message",
            role: "assistant",
            model: "claude-opus-5-5",
            stop_sequence: null,
            usage: { input_tokens: 50, output_tokens: 10 },
            ...reply,
          }),
        );
      });
    });
    await new Promise<void>((resolve) => server.listen(0, "127.0.0.1", resolve));
    baseURL = `http://127.0.0.1:${(server.address() as AddressInfo).port}`;
  });
  afterAll(() => new Promise<void>((resolve) => server.close(() => resolve())));

  it("entrega o relatório e os spans do SDK nascem dentro do span da investigação", async () => {
    const client = new Anthropic({
      apiKey: "test",
      baseURL,
      maxRetries: 0,
      openTelemetry: { tracerProvider: telemetry.provider, traces: { contentMode: "metadata_only" } },
    });
    const store = new MemoryStore();
    const result = await executeJob(
      sampleIncident,
      deps(store, { investigate: (incident, toolset) => investigate(incident, toolset, client) }),
    );

    expect(result.kind).toBe("succeeded");
    expect(store.reports[0]?.report.rootCause.statement).toContain("revisão 42");

    const spans = exporter.getFinishedSpans();
    const root = rootSpan();
    const names = spans.map((s) => s.name);
    expect(names).toContain("anthropic.messages.tool_runner");
    expect(names).toContain("anthropic.messages.create");
    expect(names).toContain("anthropic.custom_tool_use submit_rca_report");
    // Tudo no mesmo trace da investigação.
    for (const span of spans) expect(span.spanContext().traceId).toBe(root.spanContext().traceId);
  });
});
