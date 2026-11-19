import { pathToFileURL } from "node:url";
import { z } from "zod";
import { createPool } from "../src/db/pool.js";
import { PgStore } from "../src/store/pg-store.js";
import type { ReportId, ReportSummary, Store } from "../src/store/types.js";
import { traceUrl } from "../src/telemetry.js";

/**
 * Revisão dos relatórios, principalmente no modo sombra, quando nada vai para o Slack:
 *
 *   npm run review                                   lista os relatórios recentes
 *   npm run review -- feedback <reportId> correct --author ana
 *   npm run review -- feedback <reportId> wrong --author ana --cause "Pool de conexões esgotado"
 *
 * O feedback alimenta a tool past_incidents (o agente vê onde já errou) e é a base do
 * conjunto de avaliação.
 */
const FeedbackArgs = z.object({
  reportId: z.uuid(),
  verdict: z.enum(["correct", "partially_correct", "wrong"]),
  author: z.string().min(1),
  cause: z.string().optional(),
});

export function parseFeedbackArgs(argv: string[]) {
  const [reportId, verdict, ...rest] = argv;
  const flags: Record<string, string> = {};
  for (let i = 0; i < rest.length; i += 2) {
    const key = rest[i]?.replace(/^--/, "");
    const value = rest[i + 1];
    if (key && value !== undefined) flags[key] = value;
  }
  const parsed = FeedbackArgs.parse({ reportId, verdict, author: flags["author"], cause: flags["cause"] });
  if (parsed.verdict !== "correct" && !parsed.cause) {
    throw new Error("Quando o agente errou, diga a causa real com --cause: é isso que ensina o próximo run.");
  }
  return parsed;
}

export function formatReports(reports: ReportSummary[], template?: string): string {
  if (reports.length === 0) return "Nenhum relatório ainda.";
  return reports
    .map((r) => {
      const link = r.traceId ? traceUrl(template, r.traceId) ?? `trace ${r.traceId}` : "sem trace";
      return [
        `${r.reportId}  ${r.externalId} (${r.product})  ${r.status} ${Math.round(r.confidence * 100)}%  entrega=${r.delivery}  feedback=${r.feedback ?? "—"}`,
        `  ${r.title}`,
        `  causa: ${r.rootCause}`,
        `  ${link}`,
      ].join("\n");
    })
    .join("\n\n");
}

export async function runReview(store: Store, argv: string[], template?: string): Promise<string> {
  if (argv[0] === "feedback") {
    const args = parseFeedbackArgs(argv.slice(1));
    await store.recordFeedback({ reportId: args.reportId as ReportId, verdict: args.verdict, author: args.author, actualCause: args.cause });
    return `Feedback registrado: ${args.verdict}`;
  }
  return formatReports(await store.listRecentReports(20), template);
}

if (import.meta.url === pathToFileURL(process.argv[1] ?? "").href) {
  const store = new PgStore(createPool());
  runReview(store, process.argv.slice(2), process.env["TRACE_URL_TEMPLATE"])
    .then((output) => console.log(output))
    .catch((error: unknown) => {
      console.error(error instanceof Error ? error.message : error);
      process.exitCode = 1;
    })
    .finally(() => store.close());
}
