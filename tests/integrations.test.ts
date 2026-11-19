import { describe, expect, it } from "vitest";
import { AgentConfig } from "../src/agent-config.js";
import { mcpIntegration, McpServerConfig, type McpClientFactory } from "../src/integrations/mcp/index.js";
import { connectIntegrations, resolveIntegrations } from "../src/integrations/registry.js";
import type { Integration, RunnableTool } from "../src/integrations/types.js";
import { ScopeRegistry } from "../src/scope.js";
import { MemoryStore } from "./support/memory-store.js";

const ctx = {
  scopes: new ScopeRegistry([{ product: "checkout", namespaces: ["checkout"], repository: "org/checkout-api", environment: "production" }]),
  store: new MemoryStore(),
  env: {},
};

const serverConfig = McpServerConfig.parse({
  type: "mcp",
  purpose: "métricas do serviço",
  categories: ["symptoms"],
  transport: { kind: "http", url: "https://mcp.example.com/mcp" },
  allowTools: ["query_metrics"],
});

/** Servidor MCP falso: expõe uma tool de leitura e uma de escrita. */
function fakeServer(output = "ok"): { factory: McpClientFactory; called: string[] } {
  const called: string[] = [];
  const schema = { type: "object" as const, properties: {} };
  const factory: McpClientFactory = async () => ({
    listTools: async () => ({
      tools: [
        { name: "query_metrics", description: "lê métricas", inputSchema: schema },
        { name: "mute_monitor", description: "silencia um monitor", inputSchema: schema },
      ],
    }),
    callTool: async ({ name }: { name: string }) => {
      called.push(name);
      return { content: [{ type: "text" as const, text: output }] };
    },
    close: async () => {},
  }) as never;
  return { factory, called };
}

function nativeWith(name: string, toolName: string): Integration {
  const tool = { name: toolName, input_schema: { type: "object" }, run: async () => "x", parse: (v: unknown) => v } as unknown as RunnableTool;
  return { name, categories: ["changes"], purpose: "teste", connect: async () => ({ tools: [tool] }) };
}

describe("integração MCP", () => {
  it("só expõe as tools da allowlist; as de escrita ficam de fora", async () => {
    const { factory } = fakeServer();
    const toolset = await connectIntegrations([mcpIntegration("metrics", serverConfig, factory)], ctx);
    expect(toolset.tools.map((t) => t.name)).toEqual(["query_metrics"]);
  });

  it("falha se a allowlist cita uma tool que o servidor não tem", async () => {
    const { factory } = fakeServer();
    const config = { ...serverConfig, allowTools: ["query_metrics", "nao_existe"] };
    await expect(connectIntegrations([mcpIntegration("metrics", config, factory)], ctx)).rejects.toThrow(/nao_existe/);
  });

  it("aplica o teto de saída também às respostas do servidor MCP", async () => {
    const { factory } = fakeServer("a".repeat(50_000));
    const toolset = await connectIntegrations([mcpIntegration("metrics", serverConfig, factory)], ctx);
    const result = await toolset.tools[0]!.run({});
    const text = typeof result === "string" ? result : JSON.stringify(result);
    expect(text).toContain("saída truncada");
    expect(text.length).toBeLessThan(21_000);
  });

  it("recusa config sem allowlist", () => {
    expect(McpServerConfig.safeParse({ ...serverConfig, allowTools: [] }).success).toBe(false);
  });
});

describe("registro", () => {
  it("recusa duas integrações com tool de mesmo nome", async () => {
    await expect(connectIntegrations([nativeWith("a", "recent_changes"), nativeWith("b", "recent_changes")], ctx)).rejects.toThrow(/existe em a e b/);
  });

  it("resume as fontes por categoria e aponta categoria sem fonte", async () => {
    const toolset = await connectIntegrations([nativeWith("github", "recent_changes")], ctx);
    expect(toolset.sourcesSummary).toContain("github: teste (tools: recent_changes)");
    expect(toolset.sourcesSummary).toMatch(/Erros de aplicação.*nenhuma fonte disponível/);
  });

  it("recusa integração nativa desconhecida e ignora as desligadas", () => {
    const base = { products: [{ product: "checkout", namespaces: ["checkout"], repository: "org/x", environment: "production" }] };
    const unknown = AgentConfig.parse({ ...base, integrations: { mongo: { type: "native" } } });
    expect(() => resolveIntegrations(unknown)).toThrow(/desconhecida: mongo/);

    const disabled = AgentConfig.parse({ ...base, integrations: { kubernetes: { type: "native", enabled: false } } });
    expect(resolveIntegrations(disabled)).toHaveLength(0);
  });
});
