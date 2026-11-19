import { WebClient } from "@slack/web-api";
import { renderSlackMessage } from "../report/render-slack.js";
import type { RcaReport } from "../report/schema.js";
import type { Delivery } from "../store/types.js";

/**
 * shadow = modo sombra: o agente investiga e o relatório fica salvo (e no trace), mas nada é
 * publicado. É como se começa: o time compara as conclusões do agente com o que de fato
 * aconteceu, sem o agente influenciar o incidente, até confiar nele.
 */
export type PublishMode = "shadow" | "slack";

export interface PublishOptions {
  mode: PublishMode;
  channel: string | undefined;
  token: string | undefined;
  traceUrl?: string | undefined;
}

/**
 * Publica o relatório e diz como ele foi entregue. O token do bot fica no job, nunca nas tools
 * do agente: o modelo não tem como postar outra coisa nem em outro canal.
 */
export async function publishReport(report: RcaReport, options: PublishOptions): Promise<Delivery> {
  if (options.mode === "shadow") return { kind: "shadow" };

  const message = renderSlackMessage(report, { traceUrl: options.traceUrl });
  if (!options.token || !options.channel) {
    console.log(JSON.stringify({ channel: options.channel ?? "(sem canal)", ...message }, null, 2));
    return { kind: "stdout" };
  }
  const result = await new WebClient(options.token).chat.postMessage({
    channel: options.channel,
    text: message.text,
    blocks: message.blocks,
  });
  return { kind: "slack", ts: result.ts ?? "" };
}
