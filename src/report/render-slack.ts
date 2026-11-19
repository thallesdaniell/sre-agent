import type { RcaReport } from "./schema.js";

/** Bloco do Slack Block Kit (subconjunto que usamos). */
export type SlackBlock =
  | { type: "header"; text: { type: "plain_text"; text: string } }
  | { type: "section"; text: { type: "mrkdwn"; text: string } }
  | { type: "context"; elements: { type: "mrkdwn"; text: string }[] }
  | { type: "divider" };

const SECTION_LIMIT = 2900; // o Slack recusa seção acima de 3000 caracteres

function clip(text: string, limit = SECTION_LIMIT): string {
  return text.length <= limit ? text : `${text.slice(0, limit - 1)}…`;
}

function section(text: string): SlackBlock {
  return { type: "section", text: { type: "mrkdwn", text: clip(text) } };
}

function bullets(items: string[], max = 5): string {
  const shown = items.slice(0, max).map((item) => `• ${item}`);
  if (items.length > max) shown.push(`_…e mais ${items.length - max}_`);
  return shown.join("\n");
}

/**
 * O servidor, e não o agente, decide a forma da mensagem: ordem, títulos e o aviso de que a
 * hipótese é preliminar. O agente só fornece o conteúdo validado.
 */
export function renderSlackMessage(
  report: RcaReport,
  options: { traceUrl?: string | undefined } = {},
): { text: string; blocks: SlackBlock[] } {
  const pct = Math.round(report.rootCause.confidence * 100);
  const statusLabel =
    report.status === "PRELIMINARY" ? "Hipótese preliminar" : "Investigação inconclusiva";

  const blocks: SlackBlock[] = [
    { type: "header", text: { type: "plain_text", text: clip(`RCA ${report.incidentId}: ${statusLabel}`, 150) } },
    {
      type: "context",
      elements: [{ type: "mrkdwn", text: "⚠️ Gerado por agente. Uma pessoa precisa confirmar a causa." }],
    },
    section(report.summary),
    section(`*Hipótese de causa raiz* (confiança ${pct}%)\n${report.rootCause.statement}\n_${report.rootCause.reasoning}_`),
  ];

  if (report.trigger) blocks.push(section(`*Gatilho*\n${report.trigger}`));
  blocks.push(section(`*Cadeia causal*\n${report.causalChain.map((step, i) => `${i + 1}. ${step}`).join("\n")}`));

  if (report.evidence.length > 0) {
    blocks.push(section(`*Evidências*\n${bullets(report.evidence.map((e) => `[${e.role}] ${e.finding} _(${e.tool})_`))}`));
  }
  if (report.discardedHypotheses.length > 0) {
    blocks.push(section(`*Hipóteses descartadas*\n${bullets(report.discardedHypotheses.map((h) => `${h.statement}: ${h.reason}`))}`));
  }

  blocks.push({ type: "divider" });
  blocks.push(section(`*Próximos passos*\n${bullets(report.nextSteps)}`));
  if (report.limitations.length > 0) blocks.push(section(`*Limitações*\n${bullets(report.limitations)}`));
  if (options.traceUrl) {
    // Quem for validar a hipótese vê cada passo do agente: o que consultou e o que recebeu.
    blocks.push({ type: "context", elements: [{ type: "mrkdwn", text: `<${options.traceUrl}|Ver a investigação passo a passo>` }] });
  }

  return { text: `RCA ${report.incidentId}: ${statusLabel}. ${report.rootCause.statement}`, blocks };
}
