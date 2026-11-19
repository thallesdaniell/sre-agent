import { z } from "zod";

/**
 * Contrato do relatório de RCA.
 *
 * O agente NÃO escreve texto livre para o Slack: ele chama a tool `submit_rca_report` com este
 * objeto, o servidor valida e só então renderiza a mensagem. Assim a forma da mensagem é
 * determinística e as regras abaixo valem sempre, não importa o que o modelo "quis" escrever.
 */

export const EvidenceRole = z.enum(["root_cause", "trigger", "contributing", "symptom", "contradicts"]);

export const Evidence = z.object({
  tool: z.string().describe("Nome da tool que produziu a evidência, ex.: rollout_history"),
  finding: z.string().min(1).describe("O que foi observado, com nomes, números e horários"),
  role: EvidenceRole.describe("Papel da evidência na cadeia causal"),
});

export const ChangeAttribution = z.object({
  kind: z.enum(["deploy", "pull_request", "config", "none_found"]),
  ref: z.string().optional().describe("PR, revisão do rollout ou commit, quando houver"),
  evidenceStrength: z
    .enum(["direct", "temporal_only"])
    .describe("direct = a evidência liga a mudança ao sintoma; temporal_only = só aconteceu antes"),
});

export const RcaReport = z
  .object({
    incidentId: z.string().min(1),
    status: z
      .enum(["PRELIMINARY", "INCONCLUSIVE"])
      .describe("Nunca CONFIRMED: quem confirma a causa é uma pessoa"),
    summary: z.string().min(1).max(600).describe("Resumo em PT-BR, 2 a 4 frases"),
    rootCause: z.object({
      statement: z.string().min(1),
      confidence: z.number().min(0).max(1),
      reasoning: z.string().min(1),
    }),
    trigger: z.string().optional(),
    causalChain: z.array(z.string().min(1)).min(1).describe("Do gatilho ao sintoma, em ordem"),
    evidence: z.array(Evidence),
    discardedHypotheses: z.array(z.object({ statement: z.string(), reason: z.string() })),
    changeAttribution: ChangeAttribution,
    limitations: z.array(z.string()),
    nextSteps: z.array(z.string()).min(1),
  })
  .superRefine((report, ctx) => {
    const confidence = report.rootCause.confidence;

    // "O deploy veio antes" não é causa. Sem evidência direta, confiança baixa.
    const isChange = report.changeAttribution.kind !== "none_found";
    if (isChange && report.changeAttribution.evidenceStrength === "temporal_only" && confidence > 0.5) {
      ctx.addIssue({
        code: "custom",
        path: ["rootCause", "confidence"],
        message: "Mudança atribuída só por proximidade temporal: confiança máxima 0.5",
      });
    }

    if (report.status === "PRELIMINARY") {
      if (!report.evidence.some((e) => e.role === "root_cause")) {
        ctx.addIssue({
          code: "custom",
          path: ["evidence"],
          message: "PRELIMINARY exige ao menos uma evidência com role=root_cause",
        });
      }
    } else if (confidence >= 0.5) {
      ctx.addIssue({
        code: "custom",
        path: ["rootCause", "confidence"],
        message: "INCONCLUSIVE exige confiança abaixo de 0.5",
      });
    }

    if (confidence >= 0.8 && report.limitations.length === 0) {
      ctx.addIssue({
        code: "custom",
        path: ["limitations"],
        message: "Confiança alta sem nenhuma limitação declarada: diga o que não foi verificado",
      });
    }
  });

export type RcaReport = z.infer<typeof RcaReport>;
