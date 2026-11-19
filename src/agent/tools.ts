import { betaZodTool } from "@anthropic-ai/sdk/helpers/beta/zod";
import { RcaReport } from "../report/schema.js";

/** Guarda o relatório entregue pelo agente; o loop termina quando ele chega. */
export class ReportInbox {
  report: RcaReport | undefined;
}

/**
 * A única tool do núcleo: entregar o relatório. As de evidência vêm das integrações.
 * O schema é validado antes de chegar aqui; se o modelo errar, recebe os campos a corrigir.
 */
export function submitReportTool(inbox: ReportInbox) {
  return betaZodTool({
    name: "submit_rca_report",
    description:
      "Entrega o relatório final de RCA. Chame UMA vez, ao terminar. Se a validação recusar, corrija os campos apontados e chame de novo.",
    inputSchema: RcaReport,
    run: async (report) => {
      inbox.report = report;
      return "Relatório recebido e validado. Encerre a investigação sem chamar outras tools.";
    },
  });
}
