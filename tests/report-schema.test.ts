import { describe, expect, it } from "vitest";
import { renderSlackMessage } from "../src/report/render-slack.js";
import { RcaReport } from "../src/report/schema.js";

const base = {
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
} as const;

describe("RcaReport", () => {
  it("aceita um relatório preliminar bem fundamentado", () => {
    expect(RcaReport.safeParse(base).success).toBe(true);
  });

  it("limita a confiança quando a mudança só aconteceu antes (temporal_only)", () => {
    const result = RcaReport.safeParse({
      ...base,
      changeAttribution: { kind: "deploy", evidenceStrength: "temporal_only" },
    });
    expect(result.success).toBe(false);
    expect(result.error?.issues[0]?.path).toEqual(["rootCause", "confidence"]);
  });

  it("exige evidência de causa raiz para PRELIMINARY", () => {
    const result = RcaReport.safeParse({
      ...base,
      evidence: [{ tool: "k8s_events", finding: "BackOff", role: "symptom" }],
    });
    expect(result.success).toBe(false);
  });

  it("não aceita CONFIRMED: quem confirma é uma pessoa", () => {
    expect(RcaReport.safeParse({ ...base, status: "CONFIRMED" }).success).toBe(false);
  });

  it("INCONCLUSIVE exige confiança baixa", () => {
    expect(RcaReport.safeParse({ ...base, status: "INCONCLUSIVE" }).success).toBe(false);
  });
});

describe("renderSlackMessage", () => {
  it("sempre avisa que a hipótese precisa de confirmação humana", () => {
    const report = RcaReport.parse(base);
    const { blocks, text } = renderSlackMessage(report);
    expect(JSON.stringify(blocks)).toContain("Uma pessoa precisa confirmar a causa");
    expect(text).toContain("Hipótese preliminar");
  });

  it("corta seções acima do limite do Slack", () => {
    const report = RcaReport.parse({ ...base, summary: "x".repeat(600), rootCause: { ...base.rootCause, reasoning: "y".repeat(5000) } });
    for (const block of renderSlackMessage(report).blocks) {
      if (block.type === "section") expect(block.text.text.length).toBeLessThanOrEqual(2900);
    }
  });
});
