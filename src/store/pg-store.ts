import type pg from "pg";
import type { Incident } from "../incident.js";
import type { RcaReport } from "../report/schema.js";
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
} from "./types.js";

const UNIQUE_VIOLATION = "23505";

function isUniqueViolation(error: unknown): boolean {
  return typeof error === "object" && error !== null && (error as { code?: unknown }).code === UNIQUE_VIOLATION;
}

/** Implementação com Postgres. Todo SQL do projeto mora aqui, sempre parametrizado ($1, $2...). */
export class PgStore implements Store {
  constructor(private readonly pool: pg.Pool) {}

  async claimCase(incident: Incident): Promise<CaseClaim> {
    const { rows } = await this.pool.query<{ id: CaseId; dispatched_at: Date | null; dispatch_ref: string | null }>(
      `INSERT INTO incident_case (source, external_id, product, title, severity, declared_at, slack_channel, payload)
       VALUES ($1, $2, $3, $4, $5, $6, $7, $8)
       ON CONFLICT (source, external_id) DO UPDATE SET last_seen_at = now()
       RETURNING id, dispatched_at, dispatch_ref`,
      [incident.source, incident.id, incident.product, incident.title, incident.severity, incident.declaredAt, incident.slackChannel ?? null, incident],
    );
    const row = rows[0]!;
    return row.dispatched_at
      ? { kind: "already_dispatched", caseId: row.id, ref: row.dispatch_ref ?? "" }
      : { kind: "needs_dispatch", caseId: row.id };
  }

  async markDispatched(caseId: CaseId, ref: string): Promise<void> {
    await this.pool.query(
      `UPDATE incident_case SET dispatched_at = coalesce(dispatched_at, now()), dispatch_ref = coalesce(dispatch_ref, $2) WHERE id = $1`,
      [caseId, ref],
    );
  }

  async startRun(caseId: CaseId, model: string): Promise<StartRun> {
    try {
      const { rows } = await this.pool.query<{ id: RunId }>(
        `INSERT INTO investigation_run (case_id, status, model) VALUES ($1, 'RUNNING', $2) RETURNING id`,
        [caseId, model],
      );
      return { kind: "started", runId: rows[0]!.id };
    } catch (error) {
      // O índice único parcial impede duas investigações simultâneas do mesmo caso.
      if (isUniqueViolation(error)) return { kind: "already_running", caseId };
      throw error;
    }
  }

  async finishRun(runId: RunId, outcome: RunOutcome): Promise<void> {
    const failed = outcome.kind === "failed";
    await this.pool.query(
      `UPDATE investigation_run
          SET status = $2, failure_code = $3, failure_detail = $4, input_tokens = $5, output_tokens = $6, finished_at = now()
        WHERE id = $1 AND status = 'RUNNING'`,
      [
        runId,
        failed ? "FAILED" : "SUCCEEDED",
        failed ? outcome.code : null,
        failed ? outcome.detail.slice(0, 2000) : null,
        outcome.inputTokens,
        outcome.outputTokens,
      ],
    );
  }

  async saveReport(args: { runId: RunId; caseId: CaseId; report: RcaReport; slackChannel?: string | undefined }): Promise<ReportId> {
    const { report } = args;
    const { rows } = await this.pool.query<{ id: ReportId }>(
      `INSERT INTO rca_report (run_id, case_id, status, confidence, root_cause, change_kind, report, slack_channel)
       VALUES ($1, $2, $3, $4, $5, $6, $7, $8) RETURNING id`,
      [args.runId, args.caseId, report.status, report.rootCause.confidence, report.rootCause.statement, report.changeAttribution.kind, report, args.slackChannel ?? null],
    );
    return rows[0]!.id;
  }

  async markPublished(reportId: ReportId, slackTs: string): Promise<void> {
    await this.pool.query(`UPDATE rca_report SET slack_ts = $2 WHERE id = $1`, [reportId, slackTs]);
  }

  async recordFeedback(args: { reportId: ReportId; verdict: Verdict; author: string; actualCause?: string | undefined }): Promise<void> {
    await this.pool.query(
      `INSERT INTO rca_feedback (report_id, verdict, actual_cause, author) VALUES ($1, $2, $3, $4)
       ON CONFLICT (report_id, author) DO UPDATE SET verdict = EXCLUDED.verdict, actual_cause = EXCLUDED.actual_cause, created_at = now()`,
      [args.reportId, args.verdict, args.actualCause ?? null, args.author],
    );
  }

  async upsertPostmortem(pm: PostmortemInput): Promise<void> {
    await this.pool.query(
      `INSERT INTO postmortem (slug, product, title, incident_ref, occurred_at, root_cause, tags, body)
       VALUES ($1, $2, $3, $4, $5, $6, $7, $8)
       ON CONFLICT (slug) DO UPDATE SET product = EXCLUDED.product, title = EXCLUDED.title, incident_ref = EXCLUDED.incident_ref,
         occurred_at = EXCLUDED.occurred_at, root_cause = EXCLUDED.root_cause, tags = EXCLUDED.tags, body = EXCLUDED.body, updated_at = now()`,
      [pm.slug, pm.product, pm.title, pm.incidentRef ?? null, pm.occurredAt ?? null, pm.rootCause ?? null, pm.tags, pm.body],
    );
  }

  async searchPostmortems(args: { query: string; product?: string | undefined; limit: number }): Promise<PostmortemHit[]> {
    const { rows } = await this.pool.query<{
      slug: string;
      product: string;
      title: string;
      incident_ref: string | null;
      occurred_at: string | null;
      root_cause: string | null;
      snippet: string;
      rank: number;
    }>(
      `WITH q AS (SELECT websearch_to_tsquery('portuguese', $1) AS query)
       SELECT slug, product, title, incident_ref, to_char(occurred_at, 'YYYY-MM-DD') AS occurred_at, root_cause,
              ts_headline('portuguese', body, q.query, 'MaxWords=40, MinWords=15, MaxFragments=2') AS snippet,
              ts_rank(search, q.query)::float AS rank
         FROM postmortem, q
        WHERE search @@ q.query AND ($2::text IS NULL OR product = $2)
        ORDER BY rank DESC, occurred_at DESC NULLS LAST
        LIMIT $3`,
      [args.query, args.product ?? null, args.limit],
    );
    return rows.map((r) => ({
      slug: r.slug,
      product: r.product,
      title: r.title,
      incidentRef: r.incident_ref,
      occurredAt: r.occurred_at,
      rootCause: r.root_cause,
      snippet: r.snippet,
      rank: Number(r.rank),
    }));
  }

  async getPostmortem(slug: string) {
    const { rows } = await this.pool.query<{
      slug: string;
      product: string;
      title: string;
      incident_ref: string | null;
      occurred_at: string | null;
      root_cause: string | null;
      tags: string[];
      body: string;
    }>(
      `SELECT slug, product, title, incident_ref, to_char(occurred_at, 'YYYY-MM-DD') AS occurred_at, root_cause, tags, body
         FROM postmortem WHERE slug = $1`,
      [slug],
    );
    const r = rows[0];
    if (!r) return null;
    return {
      slug: r.slug,
      product: r.product,
      title: r.title,
      incidentRef: r.incident_ref ?? undefined,
      occurredAt: r.occurred_at ?? undefined,
      rootCause: r.root_cause ?? undefined,
      tags: r.tags,
      body: r.body,
    };
  }

  async recentIncidents(args: { product: string; excludeExternalId: string; limit: number }): Promise<PastIncident[]> {
    const { rows } = await this.pool.query<{
      external_id: string;
      title: string;
      declared_at: Date;
      status: string | null;
      root_cause: string | null;
      confidence: string | null;
      verdict: Verdict | null;
      actual_cause: string | null;
    }>(
      `SELECT c.external_id, c.title, c.declared_at, r.status, r.root_cause, r.confidence, f.verdict, f.actual_cause
         FROM incident_case c
         LEFT JOIN LATERAL (
           SELECT id, status, root_cause, confidence FROM rca_report WHERE case_id = c.id ORDER BY created_at DESC LIMIT 1
         ) r ON true
         LEFT JOIN LATERAL (
           SELECT verdict, actual_cause FROM rca_feedback WHERE report_id = r.id ORDER BY created_at DESC LIMIT 1
         ) f ON true
        WHERE c.product = $1 AND c.external_id <> $2
        ORDER BY c.declared_at DESC
        LIMIT $3`,
      [args.product, args.excludeExternalId, args.limit],
    );
    return rows.map((r) => ({
      externalId: r.external_id,
      title: r.title,
      declaredAt: r.declared_at.toISOString(),
      reportStatus: r.status,
      rootCause: r.root_cause,
      confidence: r.confidence === null ? null : Number(r.confidence),
      feedback: r.verdict,
      actualCause: r.actual_cause,
    }));
  }

  async close(): Promise<void> {
    await this.pool.end();
  }
}
