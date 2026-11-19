import type { AgentConfig } from "../agent-config.js";
import { truncateOutput } from "./bounds.js";
import { githubIntegration } from "./github/index.js";
import { kubernetesIntegration } from "./kubernetes/index.js";
import { mcpIntegration, type McpClientFactory } from "./mcp/index.js";
import { EVIDENCE_CATEGORIES, type EvidenceCategory, type Integration, type IntegrationContext, type RunnableTool } from "./types.js";

/**
 * Integrações nativas conhecidas. Para criar uma nova: pasta em src/integrations/<nome>/ com um
 * index.ts que devolve um Integration, e uma linha aqui. Para um servidor MCP pronto, nem isso:
 * basta declarar no agent.config.json.
 */
export const NATIVE_INTEGRATIONS: Record<string, () => Integration> = {
  kubernetes: () => kubernetesIntegration(),
  github: () => githubIntegration(),
};

export function resolveIntegrations(config: AgentConfig, mcpFactory?: McpClientFactory): Integration[] {
  return Object.entries(config.integrations)
    .filter(([, entry]) => entry.enabled)
    .map(([name, entry]) => {
      if (entry.type === "mcp") return mcpIntegration(name, entry, mcpFactory);
      const factory = NATIVE_INTEGRATIONS[name];
      if (!factory) throw new Error(`Integração nativa desconhecida: ${name}. Conhecidas: ${Object.keys(NATIVE_INTEGRATIONS).join(", ")}`);
      return factory();
    });
}

/** Toda tool, nativa ou MCP, passa pelo mesmo teto de saída. Servidores MCP não conhecem nossos limites. */
export function limitToolOutput(tool: RunnableTool): RunnableTool {
  return {
    ...tool,
    run: async (input, context) => {
      const result = await tool.run(input, context);
      if (typeof result === "string") return truncateOutput(result);
      return result.map((block) => (block.type === "text" ? { ...block, text: truncateOutput(block.text) } : block));
    },
  };
}

export interface ConnectedToolset {
  tools: RunnableTool[];
  /** Texto para o prompt: fontes agrupadas por categoria. */
  sourcesSummary: string;
  close(): Promise<void>;
}

export async function connectIntegrations(integrations: Integration[], ctx: IntegrationContext): Promise<ConnectedToolset> {
  const closers: Array<() => Promise<void>> = [];
  const tools: RunnableTool[] = [];
  const owner = new Map<string, string>();

  try {
    for (const integration of integrations) {
      const connected = await integration.connect(ctx);
      if (connected.close) closers.push(connected.close);
      for (const tool of connected.tools) {
        const previous = owner.get(tool.name);
        if (previous) throw new Error(`Tool ${tool.name} existe em ${previous} e ${integration.name}: renomeie ou tire da allowlist`);
        owner.set(tool.name, integration.name);
        tools.push(limitToolOutput(tool));
      }
    }
  } catch (error) {
    await Promise.allSettled(closers.map((close) => close()));
    throw error;
  }

  return {
    tools,
    sourcesSummary: summarize(integrations, owner),
    close: async () => void (await Promise.allSettled(closers.map((close) => close()))),
  };
}

function summarize(integrations: Integration[], owner: Map<string, string>): string {
  const toolsOf = (name: string) => [...owner].filter(([, o]) => o === name).map(([tool]) => tool);
  return (Object.keys(EVIDENCE_CATEGORIES) as EvidenceCategory[])
    .map((category) => {
      const sources = integrations.filter((i) => i.categories.includes(category));
      if (sources.length === 0) return `${EVIDENCE_CATEGORIES[category]}: nenhuma fonte disponível`;
      const lines = sources.map((i) => `  - ${i.name}: ${i.purpose} (tools: ${toolsOf(i.name).join(", ")})`);
      return `${EVIDENCE_CATEGORIES[category]}:\n${lines.join("\n")}`;
    })
    .join("\n");
}
