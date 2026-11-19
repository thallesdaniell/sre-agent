import type { BetaRunnableTool } from "@anthropic-ai/sdk/lib/tools/BetaRunnableTool";
import type { ScopeRegistry } from "../scope.js";
import type { Store } from "../store/types.js";

/**
 * Categoria da evidência que uma integração fornece. O prompt agrupa as fontes por categoria
 * para o agente saber onde procurar cada coisa, e principalmente onde procurar MUDANÇAS:
 * agentes de RCA tendem a parar no sintoma e nunca atribuir a causa a um deploy ou config.
 */
export const EVIDENCE_CATEGORIES = {
  symptoms: "Sintomas (estado, saúde, métricas, logs)",
  changes: "Mudanças (deploys, rollouts, PRs, alterações de config)",
  errors: "Erros de aplicação (exceções, stack traces, contagens)",
  context: "Contexto (incidentes anteriores, conversa do incidente, documentação)",
} as const;
export type EvidenceCategory = keyof typeof EVIDENCE_CATEGORIES;

export type RunnableTool = BetaRunnableTool<any>;

export interface IntegrationContext {
  scopes: ScopeRegistry;
  /** Acesso ao histórico (incidentes anteriores, post-mortems) para integrações de contexto. */
  store: Store;
  /** Id do incidente investigado, para as consultas de histórico não devolverem ele mesmo. */
  currentIncidentId?: string | undefined;
  env: NodeJS.ProcessEnv;
}

export interface ConnectedIntegration {
  tools: RunnableTool[];
  close?: () => Promise<void>;
}

/**
 * Contrato de toda integração, nativa ou MCP. Adicionar uma fonte nova = implementar isto
 * (ou só declarar um servidor MCP no agent.config.json) e registrar. O resto do agente não muda.
 */
export interface Integration {
  readonly name: string;
  readonly categories: EvidenceCategory[];
  /** Uma frase que vai para o prompt: o que esta fonte responde. */
  readonly purpose: string;
  connect(ctx: IntegrationContext): Promise<ConnectedIntegration>;
}
