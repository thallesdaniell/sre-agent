import { describe, expect, it } from "vitest";
import { parseFeedbackArgs, runReview } from "../scripts/review.js";
import { sampleIncident, sampleReport } from "./support/fixtures.js";
import { MemoryStore } from "./support/memory-store.js";

async function storeWithReport() {
  const store = new MemoryStore();
  const { caseId } = await store.claimCase(sampleIncident);
  const run = await store.startRun(caseId, "claude-opus-5-5", "4bf92f3577b34da6a3ce929d0e0e4736");
  if (run.kind !== "started") throw new Error("esperava started");
  const reportId = await store.saveReport({ runId: run.runId, caseId, report: sampleReport });
  await store.markDelivered(reportId, { kind: "shadow" });
  return { store, reportId };
}

describe("review", () => {
  it("lista relatórios com entrega, feedback e link do trace", async () => {
    const { store, reportId } = await storeWithReport();
    const out = await runReview(store, [], "https://lf.example.com/project/p1/traces/{traceId}");
    expect(out).toContain(reportId);
    expect(out).toContain("entrega=shadow");
    expect(out).toContain("https://lf.example.com/project/p1/traces/4bf92f3577b34da6a3ce929d0e0e4736");
  });

  it("registra feedback e ele aparece na listagem", async () => {
    const { store, reportId } = await storeWithReport();
    await runReview(store, ["feedback", reportId, "wrong", "--author", "ana", "--cause", "Pool de conexões esgotado"]);
    expect(store.feedback[0]).toMatchObject({ verdict: "wrong", actualCause: "Pool de conexões esgotado" });
    expect(await runReview(store, [])).toContain("feedback=wrong");
  });

  it("exige a causa real quando o agente errou", () => {
    const id = "6f1e2d3c-4b5a-4f6e-8d7c-1a2b3c4d5e6f";
    expect(() => parseFeedbackArgs([id, "wrong", "--author", "ana"])).toThrow(/--cause/);
    expect(parseFeedbackArgs([id, "correct", "--author", "ana"])).toMatchObject({ verdict: "correct" });
  });
});
