import Anthropic from "@anthropic-ai/sdk";
import type { Incident } from "../incident.js";
import type { RcaReport } from "../report/schema.js";
import type { ConnectedToolset } from "../integrations/registry.js";
import { buildSystemPrompt, incidentMessage } from "./prompt.js";
import { ReportInbox, submitReportTool } from "./tools.js";

export const MODEL = "claude-opus-5-5";
const MAX_ITERATIONS = 30; // teto de voltas do loop: protege custo e tempo

export class InvestigationError extends Error {
  override readonly name = "InvestigationError";
  constructor(
    readonly code: "no_report" | "refused" | "max_iterations",
    message: string,
    /** Tokens gastos até a falha: investigação que falha também custa. */
    readonly usage: { inputTokens: number; outputTokens: number } = { inputTokens: 0, outputTokens: 0 },
  ) {
    super(message);
  }
}

export interface InvestigationResult {
  report: RcaReport;
  usage: { inputTokens: number; outputTokens: number };
}

/**
 * Um run = uma investigação. O Tool Runner do SDK cuida do loop:
 * chama o modelo, executa as tools que ele pedir, devolve os resultados e repete
 * até o modelo parar de pedir tools (ou bater MAX_ITERATIONS).
 */
export async function investigate(
  incident: Incident,
  toolset: Pick<ConnectedToolset, "tools" | "sourcesSummary">,
  client: Anthropic = new Anthropic(),
): Promise<InvestigationResult> {
  const inbox = new ReportInbox();

  const runner = client.beta.messages.toolRunner({
    model: MODEL,
    max_tokens: 16000,
    max_iterations: MAX_ITERATIONS,
    thinking: { type: "adaptive" },
    output_config: { effort: "high" },
    // Se o modelo recusar por política, a API refaz a mesma chamada num modelo de fallback.
    betas: ["server-side-fallback-2026-07-01"],
    fallbacks: "default",
    system: [{ type: "text", text: buildSystemPrompt(toolset.sourcesSummary), cache_control: { type: "ephemeral" } }],
    tools: [...toolset.tools, submitReportTool(inbox)],
    messages: [{ role: "user", content: incidentMessage(incident) }],
  });

  const usage = { inputTokens: 0, outputTokens: 0 };
  let last: Anthropic.Beta.BetaMessage | undefined;
  for await (const message of runner) {
    last = message;
    usage.inputTokens += message.usage.input_tokens;
    usage.outputTokens += message.usage.output_tokens;
    if (inbox.report) break; // relatório já entregue: não deixa o loop seguir chamando tools
  }

  if (inbox.report) return { report: inbox.report, usage };
  if (last?.stop_reason === "refusal") {
    throw new InvestigationError("refused", `Modelo recusou: ${last.stop_details?.category ?? "sem categoria"}`, usage);
  }
  if (last?.stop_reason === "tool_use") {
    throw new InvestigationError("max_iterations", `Atingiu ${MAX_ITERATIONS} voltas sem entregar relatório`, usage);
  }
  throw new InvestigationError("no_report", "O agente terminou sem chamar submit_rca_report", usage);
}
