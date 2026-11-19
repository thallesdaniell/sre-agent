import { betaZodTool } from "@anthropic-ai/sdk/helpers/beta/zod";
import { z } from "zod";
import { LIMITS, toolOutput } from "../bounds.js";
import type { Integration } from "../types.js";
import { DeployEvidence } from "./client.js";

/** Integração nativa com o GitHub: o que mudou no repositório do produto. Token só de leitura. */
export function githubIntegration(client?: DeployEvidence): Integration {
  return {
    name: "github",
    categories: ["changes"],
    purpose: "PRs mergeados e deployments registrados no repositório do produto",
    async connect({ scopes, env }) {
      const github = client ?? new DeployEvidence(env["GITHUB_TOKEN"]);

      const recentChanges = betaZodTool({
        name: "recent_changes",
        description:
          "PRs mergeados e deployments do GitHub no repositório do produto. Consulte quando houver suspeita de mudança. Lembre: ter acontecido antes não prova causa.",
        inputSchema: z.object({
          product: z.string().describe(`Produto do inventário: ${scopes.products().join(", ")}`),
          sinceHours: z.number().int().min(1).max(LIMITS.maxRecentChangesHours).default(24),
        }),
        run: async (input) => toolOutput(await github.recentChanges(scopes.get(input.product).repository, input.sinceHours)),
      });

      return { tools: [recentChanges] };
    },
  };
}
