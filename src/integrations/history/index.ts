import { betaZodTool } from "@anthropic-ai/sdk/helpers/beta/zod";
import { z } from "zod";
import { toolOutput, truncateOutput } from "../bounds.js";
import type { Integration } from "../types.js";

/**
 * Memória do time: post-mortems escritos por pessoas e incidentes anteriores com o que o agente
 * concluiu e o que o time respondeu. Lê só do nosso Postgres; não fala com nenhum sistema externo.
 */
export function historyIntegration(): Integration {
  return {
    name: "history",
    categories: ["context"],
    purpose: "post-mortems anteriores e incidentes passados do mesmo produto, com o feedback do time",
    async connect({ scopes, store, currentIncidentId }) {
      const product = z.string().describe(`Produto do inventário: ${scopes.products().join(", ")}`);

      const searchPostmortems = betaZodTool({
        name: "search_postmortems",
        description:
          "Busca textual em post-mortems (português). Use termos do sintoma ou do componente, ex.: \"OOMKilled checkout\" ou \"timeout banco\". Ajuda a reconhecer um padrão que já aconteceu.",
        inputSchema: z.object({
          query: z.string().min(2).max(200),
          product: product.optional(),
          limit: z.number().int().min(1).max(10).default(5),
        }),
        run: async (input) => {
          if (input.product) scopes.get(input.product);
          const hits = await store.searchPostmortems(input);
          return hits.length ? toolOutput(hits) : "Nenhum post-mortem encontrado para esses termos.";
        },
      });

      const getPostmortem = betaZodTool({
        name: "get_postmortem",
        description: "Lê um post-mortem completo pelo slug retornado em search_postmortems.",
        inputSchema: z.object({ slug: z.string().regex(/^[a-z0-9-]{1,120}$/) }),
        run: async ({ slug }) => {
          const pm = await store.getPostmortem(slug);
          if (!pm) throw new Error(`Post-mortem não encontrado: ${slug}`);
          return truncateOutput(JSON.stringify(pm));
        },
      });

      const pastIncidents = betaZodTool({
        name: "past_incidents",
        description:
          "Incidentes anteriores do produto com a conclusão do agente e o veredito do time (correct, partially_correct, wrong) e a causa real quando o agente errou. Use para não repetir um erro já apontado.",
        inputSchema: z.object({ product, limit: z.number().int().min(1).max(20).default(10) }),
        run: async (input) => {
          scopes.get(input.product);
          const incidents = await store.recentIncidents({ ...input, excludeExternalId: currentIncidentId ?? "" });
          return incidents.length ? toolOutput(incidents) : "Nenhum incidente anterior registrado para este produto.";
        },
      });

      return { tools: [searchPostmortems, getPostmortem, pastIncidents] };
    },
  };
}
