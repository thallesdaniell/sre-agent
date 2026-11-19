import { WebClient } from "@slack/web-api";
import { renderSlackMessage } from "../report/render-slack.js";
import type { RcaReport } from "../report/schema.js";

export type PublishResult = { kind: "posted"; ts: string } | { kind: "printed" };

/**
 * Publica o relatório. Sem token do Slack (dev), imprime a mensagem renderizada no stdout.
 * O token do bot fica no job, nunca nas tools do agente: o modelo não tem como postar
 * outra coisa nem em outro canal.
 */
export async function publishReport(report: RcaReport, channel: string | undefined, token: string | undefined): Promise<PublishResult> {
  const message = renderSlackMessage(report);
  if (!token || !channel) {
    console.log(JSON.stringify({ channel: channel ?? "(sem canal)", ...message }, null, 2));
    return { kind: "printed" };
  }
  const result = await new WebClient(token).chat.postMessage({ channel, text: message.text, blocks: message.blocks });
  return { kind: "posted", ts: result.ts ?? "" };
}
