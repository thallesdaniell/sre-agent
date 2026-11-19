import { z } from "zod";

/** Toda configuração entra por variável de ambiente e é validada no boot: falha cedo, não no meio do incidente. */
const Env = z.object({
  WEBHOOK_SECRET: z.string().min(24, "WEBHOOK_SECRET precisa de ao menos 24 caracteres"),
  DATABASE_URL: z.string().regex(/^postgres(ql)?:\/\//, "DATABASE_URL precisa ser postgres://..."),
  GITHUB_TOKEN: z.string().optional(),
  SLACK_BOT_TOKEN: z.string().optional(),
  AGENT_CONFIG_FILE: z.string().default("./agent.config.json"),
  DISPATCH_MODE: z.enum(["local", "kubernetes"]).default("local"),
  JOB_NAMESPACE: z.string().default("sre-agent"),
  JOB_IMAGE: z.string().default("sre-agent:dev"),
  JOB_SERVICE_ACCOUNT: z.string().default("sre-agent-readonly"),
  JOB_SECRET_NAME: z.string().default("sre-agent-secrets"),
  PORT: z.coerce.number().int().default(8080),
  // Modo sombra por padrão: publicar no canal é decisão explícita, depois que o time confiar no agente.
  PUBLISH_MODE: z.enum(["shadow", "slack"]).default("shadow"),
  TRACE_URL_TEMPLATE: z.string().optional(),
});
export type Env = z.infer<typeof Env>;

export function loadEnv(source: NodeJS.ProcessEnv = process.env): Env {
  return Env.parse(source);
}

/** O job não recebe webhook, então não precisa do segredo. */
export function loadJobEnv(source: NodeJS.ProcessEnv = process.env) {
  return Env.omit({ WEBHOOK_SECRET: true }).parse(source);
}
