import { readdirSync, readFileSync } from "node:fs";
import { join } from "node:path";
import pg from "pg";
import { afterAll, beforeAll, beforeEach, describe, expect, it } from "vitest";
import { migrate } from "../src/db/migrate.js";
import { PgStore } from "../src/store/pg-store.js";
import { parsePostmortem } from "../scripts/import-postmortems.js";
import { sampleIncident, sampleReport } from "./support/fixtures.js";

/**
 * Testes contra um Postgres de verdade: constraints, índices e busca textual não dá para
 * simular em memória. Rodam só com TEST_DATABASE_URL (ver README); sem ela, são pulados.
 */
const url = process.env["TEST_DATABASE_URL"];

describe.skipIf(!url)("PgStore (Postgres real)", () => {
  let pool: pg.Pool;
  let store: PgStore;

  beforeAll(async () => {
    pool = new pg.Pool({ connectionString: url });
    await pool.query("DROP SCHEMA public CASCADE; CREATE SCHEMA public;");
    await migrate(pool);
    store = new PgStore(pool);
  });

  beforeEach(async () => {
    await pool.query("TRUNCATE incident_case, postmortem CASCADE");
  });

  afterAll(async () => {
    await pool?.end();
  });

  it("migrate é idempotente", async () => {
    expect(await migrate(pool)).toEqual([]);
  });

  it("o mesmo incidente cai no mesmo caso e só dispara uma vez", async () => {
    const first = await store.claimCase(sampleIncident);
    expect(first.kind).toBe("needs_dispatch");
    await store.markDispatched(first.caseId, "rca-inc-1042");

    const again = await store.claimCase(sampleIncident);
    expect(again).toEqual({ kind: "already_dispatched", caseId: first.caseId, ref: "rca-inc-1042" });

    const otherSource = await store.claimCase({ ...sampleIncident, source: "pagerduty" });
    expect(otherSource.caseId).not.toBe(first.caseId);
  });

  it("o banco impede duas investigações simultâneas do mesmo caso", async () => {
    const { caseId } = await store.claimCase(sampleIncident);
    const run = await store.startRun(caseId, "claude-opus-5-5");
    expect(run.kind).toBe("started");
    expect((await store.startRun(caseId, "claude-opus-5-5")).kind).toBe("already_running");

    if (run.kind !== "started") return;
    await store.finishRun(run.runId, { kind: "failed", code: "no_report", detail: "x", inputTokens: 10, outputTokens: 2 });
    expect((await store.startRun(caseId, "claude-opus-5-5")).kind).toBe("started");
  });

  it("run FAILED sem failure_code é recusado pela constraint", async () => {
    const { caseId } = await store.claimCase(sampleIncident);
    await expect(
      pool.query(`INSERT INTO investigation_run (case_id, status, model, finished_at) VALUES ($1, 'FAILED', 'm', now())`, [caseId]),
    ).rejects.toThrow(/check constraint/);
  });

  it("salva relatório, feedback e devolve no histórico do produto", async () => {
    const old = { ...sampleIncident, id: "INC-0981", declaredAt: "2026-08-14T10:12:00Z" };
    const { caseId } = await store.claimCase(old);
    const run = await store.startRun(caseId, "claude-opus-5-5");
    if (run.kind !== "started") throw new Error("esperava started");
    const reportId = await store.saveReport({ runId: run.runId, caseId, report: sampleReport, slackChannel: "#inc" });
    await store.markPublished(reportId, "1731.0001");
    await store.recordFeedback({ reportId, verdict: "partially_correct", author: "ana", actualCause: "Memória e também pool de conexões" });
    await store.recordFeedback({ reportId, verdict: "correct", author: "ana" }); // mesma pessoa: atualiza

    const history = await store.recentIncidents({ product: "checkout", excludeExternalId: "INC-1042", limit: 5 });
    expect(history).toEqual([
      expect.objectContaining({ externalId: "INC-0981", reportStatus: "PRELIMINARY", confidence: 0.7, feedback: "correct" }),
    ]);
  });

  it("busca post-mortems em português, ranqueando pelo título e pela causa", async () => {
    const dir = join(import.meta.dirname, "../fixtures/postmortems");
    for (const file of readdirSync(dir)) await store.upsertPostmortem(parsePostmortem(file, readFileSync(join(dir, file), "utf8")));

    const memory = await store.searchPostmortems({ query: "memória OOMKilled", limit: 5 });
    expect(memory[0]?.slug).toBe("2026-08-checkout-oom");

    const pool2 = await store.searchPostmortems({ query: "conexões banco", product: "checkout", limit: 5 });
    expect(pool2[0]?.slug).toBe("2026-09-checkout-db-pool");
    expect(pool2[0]?.snippet.length).toBeGreaterThan(0);

    expect(await store.searchPostmortems({ query: "kafka", limit: 5 })).toEqual([]);
    expect((await store.getPostmortem("2026-09-checkout-db-pool"))?.tags).toContain("banco");
  });
});
