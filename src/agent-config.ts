import { readFileSync } from "node:fs";
import { z } from "zod";
import { McpServerConfig } from "./integrations/mcp/index.js";
import { ProductScope, ScopeRegistry } from "./scope.js";

const NativeIntegrationConfig = z.object({
  type: z.literal("native"),
  enabled: z.boolean().default(true),
});

export const IntegrationConfig = z.discriminatedUnion("type", [NativeIntegrationConfig, McpServerConfig]);
export type IntegrationConfig = z.infer<typeof IntegrationConfig>;

/**
 * agent.config.json: o que o agente pode investigar (products) e com quais fontes (integrations).
 * Sem segredos: credenciais vêm de variáveis de ambiente.
 */
export const AgentConfig = z.object({
  products: z.array(ProductScope).min(1),
  integrations: z.record(z.string().regex(/^[a-z0-9-]+$/), IntegrationConfig),
});
export type AgentConfig = z.infer<typeof AgentConfig>;

export function loadAgentConfig(path: string): { config: AgentConfig; scopes: ScopeRegistry } {
  const config = AgentConfig.parse(JSON.parse(readFileSync(path, "utf8")));
  return { config, scopes: new ScopeRegistry(config.products) };
}
