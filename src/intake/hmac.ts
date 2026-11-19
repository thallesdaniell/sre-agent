import { createHmac, timingSafeEqual } from "node:crypto";

export const TOLERANCE_SECONDS = 300;

export type SignatureCheck =
  | { kind: "valid" }
  | { kind: "invalid"; reason: "missing_headers" | "stale_timestamp" | "bad_signature" };

export function sign(secret: string, timestamp: string, rawBody: string): string {
  return `sha256=${createHmac("sha256", secret).update(`${timestamp}.${rawBody}`).digest("hex")}`;
}

/**
 * Verifica a assinatura sobre o corpo BRUTO (antes de qualquer parse) e recusa entregas velhas,
 * o que impede reenviar um webhook capturado. Devolve estado explícito, não boolean.
 */
export function verifySignature(args: {
  secret: string;
  rawBody: string;
  timestamp: string | undefined;
  signature: string | undefined;
  now?: Date;
}): SignatureCheck {
  if (!args.timestamp || !args.signature) return { kind: "invalid", reason: "missing_headers" };

  const ts = Number(args.timestamp);
  const nowSeconds = Math.floor((args.now ?? new Date()).getTime() / 1000);
  if (!Number.isInteger(ts) || Math.abs(nowSeconds - ts) > TOLERANCE_SECONDS) {
    return { kind: "invalid", reason: "stale_timestamp" };
  }

  const expected = Buffer.from(sign(args.secret, args.timestamp, args.rawBody));
  const received = Buffer.from(args.signature);
  if (expected.length !== received.length || !timingSafeEqual(expected, received)) {
    return { kind: "invalid", reason: "bad_signature" };
  }
  return { kind: "valid" };
}
