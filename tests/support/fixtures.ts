import { Incident } from "../../src/incident.js";
import { RcaReport } from "../../src/report/schema.js";

export const sampleIncident = Incident.parse({
  id: "INC-1042",
  title: "Checkout 502",
  product: "checkout",
  severity: "sev2",
  declaredAt: "2026-11-19T14:05:00Z",
  slackChannel: "#inc-1042",
});

export const sampleReport = RcaReport.parse({
  incidentId: "INC-1042",
  status: "PRELIMINARY",
  summary: "O checkout passou a responder 502 após a revisão 42 reduzir o limite de memória.",
  rootCause: {
    statement: "Limite de memória do checkout-api reduzido de 1Gi para 512Mi na revisão 42",
    confidence: 0.7,
    reasoning: "Pods da revisão 42 terminam com OOMKilled; a revisão 41 não tinha nenhum.",
  },
  causalChain: ["Revisão 42 sobe com 512Mi", "Pods estouram memória no pico", "Ingress devolve 502"],
  evidence: [{ tool: "list_pods", finding: "12 restarts OOMKilled só na revisão 42", role: "root_cause" }],
  discardedHypotheses: [{ statement: "Banco lento", reason: "Latência do banco estável na janela" }],
  changeAttribution: { kind: "deploy", ref: "revisão 42", evidenceStrength: "direct" },
  limitations: ["Não houve acesso às métricas do banco"],
  nextSteps: ["Voltar o limite para 1Gi e acompanhar os restarts"],
});
