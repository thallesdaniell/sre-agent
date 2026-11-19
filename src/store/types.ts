import type { Incident } from "../incident.js";
import type { RcaReport } from "../report/schema.js";

export type CaseId = string & { readonly __brand: "CaseId" };
export type RunId = string & { readonly __brand: "RunId" };
export type ReportId = string & { readonly __brand: "ReportId" };

/** Resultado de receber um incidente: já disparado antes ou ainda precisa disparar. */
export type CaseClaim = { kind: "needs_dispatch"; caseId: CaseId } | { kind: "already_dispatched"; caseId: CaseId; ref: string };

export type StartRun = { kind: "started"; runId: RunId } | { kind: "already_running"; caseId: CaseId };

export type FailureCode = "no_report" | "refused" | "max_iterations" | "integration_error" | "publish_error" | "unknown";

export type RunOutcome =
  | { kind: "succeeded"; inputTokens: number; outputTokens: number }
  | { kind: "failed"; code: FailureCode; detail: string; inputTokens: number; outputTokens: number };

export type Verdict = "correct" | "partially_correct" | "wrong";

export interface PostmortemInput {
  slug: string;
  product: string;
  title: string;
  incidentRef?: string | undefined;
  occurredAt?: string | undefined; // YYYY-MM-DD
  rootCause?: string | undefined;
  tags: string[];
  body: string;
}

export interface PostmortemHit {
  slug: string;
  product: string;
  title: string;
  incidentRef: string | null;
  occurredAt: string | null;
  rootCause: string | null;
  snippet: string;
  rank: number;
}

export interface PastIncident {
  externalId: string;
  title: string;
  declaredAt: string;
  reportStatus: string | null;
  rootCause: string | null;
  confidence: number | null;
  feedback: Verdict | null;
  actualCause: string | null;
}

/**
 * Tudo que o agente persiste passa por aqui. A interface deixa os testes rodarem com uma
 * implementação em memória e mantém o SQL num lugar só (pg-store.ts).
 */
export interface Store {
  /** Upsert do caso; diz se a investigação ainda precisa sair. Idempotente por (source, externalId). */
  claimCase(incident: Incident): Promise<CaseClaim>;
  markDispatched(caseId: CaseId, ref: string): Promise<void>;

  startRun(caseId: CaseId, model: string): Promise<StartRun>;
  finishRun(runId: RunId, outcome: RunOutcome): Promise<void>;

  saveReport(args: { runId: RunId; caseId: CaseId; report: RcaReport; slackChannel?: string | undefined }): Promise<ReportId>;
  markPublished(reportId: ReportId, slackTs: string): Promise<void>;
  recordFeedback(args: { reportId: ReportId; verdict: Verdict; author: string; actualCause?: string | undefined }): Promise<void>;

  upsertPostmortem(postmortem: PostmortemInput): Promise<void>;
  searchPostmortems(args: { query: string; product?: string | undefined; limit: number }): Promise<PostmortemHit[]>;
  getPostmortem(slug: string): Promise<(PostmortemInput & { body: string }) | null>;

  /** Incidentes anteriores do produto com o que o agente concluiu e o que o time disse. */
  recentIncidents(args: { product: string; excludeExternalId: string; limit: number }): Promise<PastIncident[]>;

  close(): Promise<void>;
}
