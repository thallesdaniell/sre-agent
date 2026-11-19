import { betaZodTool } from "@anthropic-ai/sdk/helpers/beta/zod";
import { z } from "zod";
import { K8S_NAME, LIMITS, toolOutput, truncateOutput } from "../bounds.js";
import type { Integration } from "../types.js";
import { K8sEvidence } from "./client.js";

/**
 * Integração nativa com a API do Kubernetes. Vale para qualquer cluster (GKE, EKS, AKS, k3d):
 * o que muda é só a credencial. Não usa kubectl nem shell.
 */
export function kubernetesIntegration(client?: K8sEvidence): Integration {
  return {
    name: "kubernetes",
    categories: ["symptoms", "changes"],
    purpose: "estado dos pods, logs, eventos e histórico de rollout dos Deployments",
    async connect({ scopes }) {
      const k8s = client ?? new K8sEvidence();
      const product = z.string().describe(`Produto do inventário: ${scopes.products().join(", ")}`);
      const namespace = z.string().regex(K8S_NAME).optional().describe("Namespace do produto; omita para o principal");

      const listPods = betaZodTool({
        name: "list_pods",
        description:
          "Lista pods de um produto com fase, restarts, imagem e o motivo do último término (ex.: OOMKilled). Ponto de partida para sintomas de disponibilidade.",
        inputSchema: z.object({ product, namespace, labelSelector: z.string().max(200).optional().describe("ex.: app=checkout") }),
        run: async (input) => {
          const ns = scopes.namespace(input.product, input.namespace);
          return toolOutput({ namespace: ns, pods: await k8s.listPods(ns, input.labelSelector) });
        },
      });

      const podLogs = betaZodTool({
        name: "pod_logs",
        description: "Lê logs de um pod numa janela limitada. Use previous=true para o container que reiniciou (o log de antes do crash).",
        inputSchema: z.object({
          product,
          namespace,
          pod: z.string().regex(K8S_NAME),
          container: z.string().regex(K8S_NAME).optional(),
          sinceMinutes: z.number().int().min(1).max(LIMITS.maxWindowMinutes).default(60),
          tailLines: z.number().int().min(1).max(LIMITS.maxLogLines).default(200),
          previous: z.boolean().default(false),
        }),
        run: async (input) => {
          const ns = scopes.namespace(input.product, input.namespace);
          return truncateOutput((await k8s.podLogs({ ...input, namespace: ns })) || "(sem linhas de log na janela)");
        },
      });

      const events = betaZodTool({
        name: "k8s_events",
        description: "Eventos recentes do namespace (Warnings primeiro): falhas de scheduling, probes, OOM, pull de imagem.",
        inputSchema: z.object({ product, namespace, sinceMinutes: z.number().int().min(1).max(LIMITS.maxWindowMinutes).default(120) }),
        run: async (input) => {
          const ns = scopes.namespace(input.product, input.namespace);
          return toolOutput(await k8s.events(ns, input.sinceMinutes));
        },
      });

      const rolloutHistory = betaZodTool({
        name: "rollout_history",
        description:
          "Revisões de um Deployment com imagem e horário. Consulte sempre que houver suspeita de mudança: é a evidência direta de que algo mudou.",
        inputSchema: z.object({ product, namespace, deployment: z.string().regex(K8S_NAME) }),
        run: async (input) => {
          const ns = scopes.namespace(input.product, input.namespace);
          return toolOutput(await k8s.rolloutHistory(ns, input.deployment));
        },
      });

      return { tools: [listPods, podLogs, events, rolloutHistory] };
    },
  };
}
