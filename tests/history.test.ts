import { describe, expect, it } from "vitest";
import { historyIntegration } from "../src/integrations/history/index.js";
import { ScopeRegistry } from "../src/scope.js";
import { parsePostmortem } from "../scripts/import-postmortems.js";
import { sampleIncident, sampleReport } from "./support/fixtures.js";
import { MemoryStore } from "./support/memory-store.js";

const scopes = new ScopeRegistry([{ product: "checkout", namespaces: ["checkout"], repository: "org/checkout-api", environment: "production" }]);

const markdown = `---
title: Checkout fora do ar por limite de memória
product: checkout
incident: INC-0981
occurredAt: 2026-08-14
rootCause: Limite de memória reduzido
tags: oom, deploy
---
## Resumo
Pods reiniciando com OOMKilled.`;

describe("parsePostmortem", () => {
  it("lê o cabeçalho e usa o nome do arquivo como slug", () => {
    const pm = parsePostmortem("2026-08-Checkout_OOM.md", markdown);
    expect(pm).toMatchObject({ slug: "2026-08-checkout-oom", product: "checkout", incidentRef: "INC-0981", tags: ["oom", "deploy"] });
    expect(pm.body).toContain("OOMKilled");
  });

  it("recusa arquivo sem cabeçalho", () => {
    expect(() => parsePostmortem("x.md", "# só corpo")).toThrow(/cabeçalho/);
  });
});

describe("integração history", () => {
  async function tools(store: MemoryStore, currentIncidentId?: string) {
    const { tools } = await historyIntegration().connect({ scopes, store, currentIncidentId, env: {} });
    return Object.fromEntries(tools.map((t) => [t.name, t]));
  }

  it("busca post-mortems", async () => {
    const store = new MemoryStore();
    await store.upsertPostmortem(parsePostmortem("checkout-oom.md", markdown));
    const result = await (await tools(store)).search_postmortems!.run({ query: "OOMKilled", limit: 5 });
    expect(result).toContain("checkout-oom");
  });

  it("incidentes anteriores trazem o veredito do time e excluem o incidente atual", async () => {
    const store = new MemoryStore();
    const old = { ...sampleIncident, id: "INC-0981" };
    const { caseId } = await store.claimCase(old);
    const run = await store.startRun(caseId);
    if (run.kind !== "started") throw new Error("esperava started");
    const reportId = await store.saveReport({ runId: run.runId, caseId, report: sampleReport });
    await store.recordFeedback({ reportId, verdict: "wrong", author: "ana", actualCause: "Pool de conexões esgotado" });
    await store.claimCase(sampleIncident); // o incidente atual

    const result = String(await (await tools(store, sampleIncident.id)).past_incidents!.run({ product: "checkout", limit: 10 }));
    expect(result).toContain("INC-0981");
    expect(result).toContain("Pool de conexões esgotado");
    expect(result).not.toContain("INC-1042");
  });

  it("recusa produto fora do inventário", async () => {
    const result = (await tools(new MemoryStore())).past_incidents!.run({ product: "billing", limit: 10 });
    await expect(result).rejects.toThrow(/fora do inventário/);
  });
});
