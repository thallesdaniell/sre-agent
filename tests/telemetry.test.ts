import { describe, expect, it } from "vitest";
import { resolveContentMode, resolveDestination, traceUrl } from "../src/telemetry.js";

describe("resolveDestination", () => {
  it("Langfuse: endpoint OTLP de traces, Basic auth e versão de ingestão", () => {
    const dest = resolveDestination({ LANGFUSE_PUBLIC_KEY: "pk-lf-1", LANGFUSE_SECRET_KEY: "sk-lf-2", LANGFUSE_BASE_URL: "http://localhost:3000/" });
    expect(dest).toEqual({
      kind: "langfuse",
      url: "http://localhost:3000/api/public/otel/v1/traces",
      headers: {
        Authorization: `Basic ${Buffer.from("pk-lf-1:sk-lf-2").toString("base64")}`,
        "x-langfuse-ingestion-version": "4",
      },
    });
  });

  it("Langfuse sem base URL usa o cloud EU", () => {
    const dest = resolveDestination({ LANGFUSE_PUBLIC_KEY: "pk", LANGFUSE_SECRET_KEY: "sk" });
    expect(dest.kind === "langfuse" && dest.url).toBe("https://cloud.langfuse.com/api/public/otel/v1/traces");
  });

  it("qualquer backend OTLP pelas variáveis padrão do OpenTelemetry", () => {
    expect(resolveDestination({ OTEL_EXPORTER_OTLP_ENDPOINT: "http://jaeger:4318" })).toEqual({ kind: "otlp" });
  });

  it("console para debug e desligado quando nada está configurado", () => {
    expect(resolveDestination({ OTEL_TRACES_EXPORTER: "console" })).toEqual({ kind: "console" });
    expect(resolveDestination({})).toEqual({ kind: "none" });
  });

  it("só exporta conteúdo (prompts, respostas) quando pedido explicitamente", () => {
    expect(resolveContentMode({})).toBe("metadata_only");
    expect(resolveContentMode({ TRACE_CONTENT: "content" })).toBe("content");
  });
});

describe("traceUrl", () => {
  it("preenche o id no template e ignora template sem marcador", () => {
    expect(traceUrl("https://lf.example.com/project/p1/traces/{traceId}", "abc")).toBe("https://lf.example.com/project/p1/traces/abc");
    expect(traceUrl("https://lf.example.com", "abc")).toBeUndefined();
    expect(traceUrl(undefined, "abc")).toBeUndefined();
  });
});
