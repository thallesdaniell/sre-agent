import { randomUUID } from "node:crypto";
import type { Incident } from "../../src/incident.js";
import type { RcaReport } from "../../src/report/schema.js";
import type {
  CaseClaim,
  CaseId,
  PastIncident,
  PostmortemHit,
  PostmortemInput,
  ReportId,
  RunId,
  RunOutcome,
  StartRun,
  Store,
  Verdict,
} from "../../src/store/types.js";

interface CaseRow {
  id: CaseId;
  incident: Incident;
  dispatchedAt?: Date;
  ref?: string;
}
interface RunRow {
  id: RunId;
  caseId: CaseId;
  status: "RUNNING" | "SUCCEEDED" | "FAILED";
  outcome?: RunOutcome;
}
interface ReportRow {
  id: ReportId;
  runId: RunId;
  caseId: CaseId;
  report: RcaReport;
  slackTs?: string;
}

/** Store em memória com as mesmas regras do Postgres, para testes unitários sem banco. */
export class MemoryStore implements Store {
  readonly cases: CaseRow[] = [];
  readonly runs: RunRow[] = [];
  readonly reports: ReportRow[] = [];
  readonly postmortems: PostmortemInput[] = [];
  readonly feedback: { reportId: ReportId; verdict: Verdict; author: string; actualCause?: string | undefined }[] = [];

  async claimCase(incident: Incident): Promise<CaseClaim> {
    let row = this.cases.find((c) => c.incident.source === incident.source && c.incident.id === incident.id);
    if (!row) {
      row = { id: randomUUID() as CaseId, incident };
      this.cases.push(row);
    }
    return row.dispatchedAt ? { kind: "already_dispatched", caseId: row.id, ref: row.ref ?? "" } : { kind: "needs_dispatch", caseId: row.id };
  }

  async markDispatched(caseId: CaseId, ref: string): Promise<void> {
    const row = this.cases.find((c) => c.id === caseId)!;
    row.dispatchedAt ??= new Date();
    row.ref ??= ref;
  }

  async startRun(caseId: CaseId): Promise<StartRun> {
    if (this.runs.some((r) => r.caseId === caseId && r.status === "RUNNING")) return { kind: "already_running", caseId };
    const run: RunRow = { id: randomUUID() as RunId, caseId, status: "RUNNING" };
    this.runs.push(run);
    return { kind: "started", runId: run.id };
  }

  async finishRun(runId: RunId, outcome: RunOutcome): Promise<void> {
    const run = this.runs.find((r) => r.id === runId && r.status === "RUNNING");
    if (!run) return;
    run.status = outcome.kind === "failed" ? "FAILED" : "SUCCEEDED";
    run.outcome = outcome;
  }

  async saveReport(args: { runId: RunId; caseId: CaseId; report: RcaReport }): Promise<ReportId> {
    const row: ReportRow = { id: randomUUID() as ReportId, runId: args.runId, caseId: args.caseId, report: args.report };
    this.reports.push(row);
    return row.id;
  }

  async markPublished(reportId: ReportId, slackTs: string): Promise<void> {
    this.reports.find((r) => r.id === reportId)!.slackTs = slackTs;
  }

  async recordFeedback(args: { reportId: ReportId; verdict: Verdict; author: string; actualCause?: string | undefined }): Promise<void> {
    this.feedback.push(args);
  }

  async upsertPostmortem(pm: PostmortemInput): Promise<void> {
    const at = this.postmortems.findIndex((p) => p.slug === pm.slug);
    if (at >= 0) this.postmortems[at] = pm;
    else this.postmortems.push(pm);
  }

  async searchPostmortems(args: { query: string; product?: string | undefined; limit: number }): Promise<PostmortemHit[]> {
    const terms = args.query.toLowerCase().split(/\s+/);
    return this.postmortems
      .filter((p) => !args.product || p.product === args.product)
      .filter((p) => terms.some((t) => `${p.title} ${p.rootCause ?? ""} ${p.body}`.toLowerCase().includes(t)))
      .slice(0, args.limit)
      .map((p) => ({
        slug: p.slug,
        product: p.product,
        title: p.title,
        incidentRef: p.incidentRef ?? null,
        occurredAt: p.occurredAt ?? null,
        rootCause: p.rootCause ?? null,
        snippet: p.body.slice(0, 120),
        rank: 1,
      }));
  }

  async getPostmortem(slug: string) {
    return this.postmortems.find((p) => p.slug === slug) ?? null;
  }

  async recentIncidents(args: { product: string; excludeExternalId: string; limit: number }): Promise<PastIncident[]> {
    return this.cases
      .filter((c) => c.incident.product === args.product && c.incident.id !== args.excludeExternalId)
      .slice(0, args.limit)
      .map((c) => {
        const report = this.reports.filter((r) => r.caseId === c.id).at(-1);
        const fb = report ? this.feedback.filter((f) => f.reportId === report.id).at(-1) : undefined;
        return {
          externalId: c.incident.id,
          title: c.incident.title,
          declaredAt: c.incident.declaredAt,
          reportStatus: report?.report.status ?? null,
          rootCause: report?.report.rootCause.statement ?? null,
          confidence: report?.report.rootCause.confidence ?? null,
          feedback: fb?.verdict ?? null,
          actualCause: fb?.actualCause ?? null,
        };
      });
  }

  async close(): Promise<void> {}
}
