import { describe, expect, it } from "vitest";
import { InvestigationError } from "../src/agent/investigate.js";
import { executeJob, type JobDeps } from "../src/job.js";
import { MemoryStore } from "./support/memory-store.js";
import { sampleIncident, sampleReport } from "./support/fixtures.js";

const usage = { inputTokens: 1200, outputTokens: 300 };

function deps(store: MemoryStore, overrides: Partial<JobDeps> = {}): JobDeps {
  return {
    store,
    connect: async () => ({ tools: [], sourcesSummary: "", close: async () => {} }),
    investigate: async () => ({ report: sampleReport, usage }),
    publish: async () => ({ kind: "posted", ts: "1731.0001" }),
    ...overrides,
  };
}

describe("executeJob", () => {
  it("registra run SUCCEEDED, salva o relatório e o ts da mensagem", async () => {
    const store = new MemoryStore();
    const result = await executeJob(sampleIncident, deps(store));

    expect(result).toMatchObject({ kind: "succeeded", published: "posted" });
    expect(store.runs[0]).toMatchObject({ status: "SUCCEEDED", outcome: { kind: "succeeded", ...usage } });
    expect(store.reports[0]).toMatchObject({ slackTs: "1731.0001" });
  });

  it("falha do agente vira FAILED com o código e os tokens gastos", async () => {
    const store = new MemoryStore();
    const failing = deps(store, {
      investigate: async () => {
        throw new InvestigationError("max_iterations", "30 voltas", usage);
      },
    });

    await expect(executeJob(sampleIncident, failing)).rejects.toThrow("30 voltas");
    expect(store.runs[0]).toMatchObject({ status: "FAILED", outcome: { code: "max_iterations", ...usage } });
    expect(store.reports).toHaveLength(0);
  });

  it("falha ao conectar integração vira integration_error", async () => {
    const store = new MemoryStore();
    const failing = deps(store, {
      connect: async () => {
        throw new Error("DATADOG_API_KEY não definida");
      },
    });

    await expect(executeJob(sampleIncident, failing)).rejects.toThrow();
    expect(store.runs[0]?.outcome).toMatchObject({ kind: "failed", code: "integration_error" });
  });

  it("se o Slack falhar, o relatório já está salvo e o run fica FAILED publish_error", async () => {
    const store = new MemoryStore();
    const failing = deps(store, {
      publish: async () => {
        throw new Error("channel_not_found");
      },
    });

    await expect(executeJob(sampleIncident, failing)).rejects.toThrow("channel_not_found");
    expect(store.reports).toHaveLength(1);
    expect(store.reports[0]?.slackTs).toBeUndefined();
    expect(store.runs[0]?.outcome).toMatchObject({ kind: "failed", code: "publish_error" });
  });

  it("não roda duas investigações ao mesmo tempo para o mesmo incidente", async () => {
    const store = new MemoryStore();
    const { caseId } = await store.claimCase(sampleIncident);
    await store.startRun(caseId);

    let called = false;
    const result = await executeJob(sampleIncident, deps(store, { investigate: async () => ((called = true), { report: sampleReport, usage }) }));
    expect(result).toEqual({ kind: "skipped", reason: "already_running" });
    expect(called).toBe(false);
  });
});
