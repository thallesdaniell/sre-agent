import { mcpTools, type MCPCallToolResultLike, type MCPClientLike, type MCPToolLike } from "@anthropic-ai/sdk/helpers/beta/mcp";
import { Client } from "@modelcontextprotocol/sdk/client/index.js";
import { StdioClientTransport } from "@modelcontextprotocol/sdk/client/stdio.js";
import { StreamableHTTPClientTransport } from "@modelcontextprotocol/sdk/client/streamableHttp.js";
import { z } from "zod";
import { EVIDENCE_CATEGORIES, type EvidenceCategory, type Integration } from "../types.js";

/**
 * Segredos nunca ficam no arquivo de config: o config diz o NOME da variável de ambiente,
 * e o valor é lido na hora de conectar. Ex.: { "DD-API-KEY": "DATADOG_API_KEY" }.
 */
const EnvRefs = z.record(z.string(), z.string().regex(/^[A-Z][A-Z0-9_]*$/)).default({});

export const McpServerConfig = z.object({
  type: z.literal("mcp"),
  enabled: z.boolean().default(true),
  purpose: z.string().min(1),
  categories: z.array(z.enum(Object.keys(EVIDENCE_CATEGORIES) as [EvidenceCategory, ...EvidenceCategory[]])).min(1),
  transport: z.discriminatedUnion("kind", [
    z.object({ kind: z.literal("http"), url: z.url(), headersFromEnv: EnvRefs }),
    z.object({ kind: z.literal("stdio"), command: z.string(), args: z.array(z.string()).default([]), envFromEnv: EnvRefs }),
  ]),
  /**
   * Allowlist obrigatória. Servidores MCP prontos costumam expor tools de escrita (criar monitor,
   * sincronizar app, reiniciar pipeline). Só entram as tools listadas aqui; o resto é descartado.
   */
  allowTools: z.array(z.string()).min(1),
});
export type McpServerConfig = z.infer<typeof McpServerConfig>;

/** O que precisamos de uma conexão MCP: listar tools, chamá-las e fechar. */
export interface McpConnection extends MCPClientLike {
  listTools(): Promise<{ tools: MCPToolLike[] }>;
  close(): Promise<void>;
}

export type McpClientFactory = (name: string, config: McpServerConfig, env: NodeJS.ProcessEnv) => Promise<McpConnection>;

function resolveEnvRefs(refs: Record<string, string>, env: NodeJS.ProcessEnv, server: string): Record<string, string> {
  return Object.fromEntries(
    Object.entries(refs).map(([key, varName]) => {
      const value = env[varName];
      if (!value) throw new Error(`Integração ${server}: variável ${varName} não definida`);
      return [key, value];
    }),
  );
}

export const connectMcpClient: McpClientFactory = async (name, config, env) => {
  const client = new Client({ name: `sre-agent:${name}`, version: "0.1.0" });
  const transport =
    config.transport.kind === "http"
      ? new StreamableHTTPClientTransport(new URL(config.transport.url), {
          requestInit: { headers: resolveEnvRefs(config.transport.headersFromEnv, env, name) },
        })
      : new StdioClientTransport({
          command: config.transport.command,
          args: config.transport.args,
          env: resolveEnvRefs(config.transport.envFromEnv, env, name),
        });
  await client.connect(transport);
  return {
    listTools: async () => ({ tools: (await client.listTools()).tools as MCPToolLike[] }),
    callTool: async (params) => {
      const result = await client.callTool(params);
      // Servidores antigos respondem { toolResult } em vez de { content }: normaliza para texto.
      if ("content" in result && Array.isArray(result.content)) return result as MCPCallToolResultLike;
      return { content: [{ type: "text", text: JSON.stringify(result["toolResult"] ?? null) }] };
    },
    close: () => client.close(),
  };
};

/** Filtra as tools do servidor pela allowlist e avisa sobre nomes da allowlist que não existem. */
export function selectAllowedTools(name: string, available: MCPToolLike[], allow: string[]): MCPToolLike[] {
  const missing = allow.filter((tool) => !available.some((t) => t.name === tool));
  if (missing.length > 0) throw new Error(`Integração ${name}: tools da allowlist não existem no servidor: ${missing.join(", ")}`);
  return available.filter((tool) => allow.includes(tool.name));
}

/** Integração genérica para qualquer servidor MCP (Datadog, Argo CD, Sentry...), declarada só por config. */
export function mcpIntegration(name: string, config: McpServerConfig, factory: McpClientFactory = connectMcpClient): Integration {
  return {
    name,
    categories: config.categories,
    purpose: config.purpose,
    async connect({ env }) {
      const client = await factory(name, config, env);
      const { tools } = await client.listTools();
      const allowed = selectAllowedTools(name, tools, config.allowTools);
      return { tools: mcpTools(allowed, client), close: () => client.close() };
    },
  };
}
