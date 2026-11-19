import { pathToFileURL } from "node:url";
import { serve } from "@hono/node-server";
import { Hono } from "hono";
import { loadEnv, type Env } from "../config.js";
import { loadAgentConfig } from "../agent-config.js";
import type { ScopeRegistry } from "../scope.js";
import { createPool } from "../db/pool.js";
import { Incident } from "../incident.js";
import { runJob } from "../job.js";
import { PgStore } from "../store/pg-store.js";
import type { Store } from "../store/types.js";
import { KubernetesJobDispatcher, jobName, type Dispatcher } from "./dispatch.js";
import { verifySignature } from "./hmac.js";

const MAX_BODY_BYTES = 64 * 1024;

/** Dev: roda a investigação no próprio processo, sem esperar (responde 202 na hora). */
export class LocalDispatcher implements Dispatcher {
  constructor(private readonly store: Store) {}

  async dispatch(incident: Incident) {
    const ref = jobName(incident.id);
    void runJob(incident, this.store).catch((error: unknown) => console.error("investigação falhou", ref, error));
    return { kind: "dispatched" as const, ref };
  }
}

/**
 * O único componente sempre ligado. Faz o mínimo: autentica, valida, filtra e dispara.
 * Nada de LLM aqui; a investigação roda no job.
 */
export function buildApp(deps: { secret: string; scopes: ScopeRegistry; store: Store; dispatcher: Dispatcher }) {
  const app = new Hono();

  app.get("/healthz", (c) => c.text("ok"));

  app.post("/webhooks/incident", async (c) => {
    const rawBody = await c.req.text();
    if (Buffer.byteLength(rawBody) > MAX_BODY_BYTES) return c.json({ error: "payload_too_large" }, 413);

    const check = verifySignature({
      secret: deps.secret,
      rawBody,
      timestamp: c.req.header("x-timestamp"),
      signature: c.req.header("x-signature"),
    });
    if (check.kind === "invalid") return c.json({ error: check.reason }, 401);

    let json: unknown;
    try {
      json = JSON.parse(rawBody);
    } catch {
      return c.json({ error: "invalid_json" }, 400);
    }
    const parsed = Incident.safeParse(json);
    if (!parsed.success) {
      return c.json({ error: "invalid_payload", paths: parsed.error.issues.map((i) => i.path.join(".")) }, 400);
    }

    const incident = parsed.data;
    if (!deps.scopes.products().includes(incident.product)) {
      // Aceita (200) para o remetente não reenviar, mas não investiga produto fora do inventário.
      return c.json({ status: "ignored", reason: "product_not_in_scope" }, 200);
    }

    // O banco é quem deduplica: funciona com várias réplicas do intake e sobrevive a restart.
    const claim = await deps.store.claimCase(incident);
    if (claim.kind === "already_dispatched") return c.json({ status: "duplicate", ref: claim.ref }, 200);

    // Se o disparo falhar, o caso fica sem dispatched_at e o retry do remetente tenta de novo.
    const result = await deps.dispatcher.dispatch(incident);
    await deps.store.markDispatched(claim.caseId, result.ref);
    return c.json({ status: result.kind, ref: result.ref }, result.kind === "dispatched" ? 202 : 200);
  });

  return app;
}

function dispatcherFor(env: Env, store: Store): Dispatcher {
  if (env.DISPATCH_MODE === "local") return new LocalDispatcher(store);
  return new KubernetesJobDispatcher({
    namespace: env.JOB_NAMESPACE,
    image: env.JOB_IMAGE,
    serviceAccount: env.JOB_SERVICE_ACCOUNT,
    secretName: env.JOB_SECRET_NAME,
  });
}

if (import.meta.url === pathToFileURL(process.argv[1] ?? "").href) {
  const env = loadEnv();
  const store = new PgStore(createPool(env.DATABASE_URL));
  const app = buildApp({
    secret: env.WEBHOOK_SECRET,
    scopes: loadAgentConfig(env.AGENT_CONFIG_FILE).scopes,
    store,
    dispatcher: dispatcherFor(env, store),
  });
  serve({ fetch: app.fetch, port: env.PORT }, (info) => console.log(`intake ouvindo em :${info.port} (${env.DISPATCH_MODE})`));
}
