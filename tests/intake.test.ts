import { describe, expect, it } from "vitest";
import { ScopeRegistry } from "../src/scope.js";
import type { Incident } from "../src/incident.js";
import { jobName, type Dispatcher } from "../src/intake/dispatch.js";
import { sign, verifySignature } from "../src/intake/hmac.js";
import { buildApp } from "../src/intake/server.js";

const SECRET = "segredo-de-teste-com-mais-de-24-caracteres";
const scopes = new ScopeRegistry([
  { product: "checkout", namespaces: ["checkout"], repository: "org/checkout-api", environment: "production" },
]);
const incident = {
  id: "INC-1042",
  title: "Checkout 502",
  product: "checkout",
  severity: "sev2",
  declaredAt: "2026-11-19T14:05:00Z",
};

class RecordingDispatcher implements Dispatcher {
  readonly calls: Incident[] = [];
  async dispatch(i: Incident) {
    this.calls.push(i);
    return { kind: "dispatched" as const, ref: jobName(i.id) };
  }
}

function signedRequest(body: unknown, secret = SECRET, timestamp = `${Math.floor(Date.now() / 1000)}`) {
  const raw = JSON.stringify(body);
  return new Request("http://local/webhooks/incident", {
    method: "POST",
    body: raw,
    headers: { "x-timestamp": timestamp, "x-signature": sign(secret, timestamp, raw) },
  });
}

describe("verifySignature", () => {
  const now = new Date("2026-11-19T14:05:00Z");
  const ts = `${Math.floor(now.getTime() / 1000)}`;

  it("aceita assinatura correta", () => {
    expect(verifySignature({ secret: SECRET, rawBody: "{}", timestamp: ts, signature: sign(SECRET, ts, "{}"), now })).toEqual({ kind: "valid" });
  });

  it("recusa corpo alterado", () => {
    const result = verifySignature({ secret: SECRET, rawBody: '{"a":1}', timestamp: ts, signature: sign(SECRET, ts, "{}"), now });
    expect(result).toEqual({ kind: "invalid", reason: "bad_signature" });
  });

  it("recusa entrega velha (replay)", () => {
    const old = `${Math.floor(now.getTime() / 1000) - 600}`;
    const result = verifySignature({ secret: SECRET, rawBody: "{}", timestamp: old, signature: sign(SECRET, old, "{}"), now });
    expect(result).toEqual({ kind: "invalid", reason: "stale_timestamp" });
  });
});

describe("POST /webhooks/incident", () => {
  it("dispara a investigação para incidente válido", async () => {
    const dispatcher = new RecordingDispatcher();
    const res = await buildApp({ secret: SECRET, scopes, dispatcher }).request(signedRequest(incident));
    expect(res.status).toBe(202);
    expect(dispatcher.calls).toHaveLength(1);
  });

  it("recusa assinatura com segredo errado e não dispara nada", async () => {
    const dispatcher = new RecordingDispatcher();
    const res = await buildApp({ secret: SECRET, scopes, dispatcher }).request(signedRequest(incident, "outro-segredo-qualquer-bem-longo"));
    expect(res.status).toBe(401);
    expect(dispatcher.calls).toHaveLength(0);
  });

  it("ignora produto fora do inventário", async () => {
    const dispatcher = new RecordingDispatcher();
    const res = await buildApp({ secret: SECRET, scopes, dispatcher }).request(signedRequest({ ...incident, product: "billing" }));
    expect(await res.json()).toMatchObject({ status: "ignored" });
    expect(dispatcher.calls).toHaveLength(0);
  });
});

describe("escopo e nome do job", () => {
  it("recusa namespace que não pertence ao produto", () => {
    expect(() => scopes.namespace("checkout", "kube-system")).toThrow(/não pertence/);
  });

  it("gera o mesmo nome de Job para o mesmo incidente", () => {
    expect(jobName("INC-1042")).toBe("rca-inc-1042");
    expect(jobName("INC_1042")).toBe(jobName("INC-1042"));
  });
});
