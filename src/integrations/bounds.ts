/**
 * Limites de toda tool de evidência. Duas razões:
 * - segurança: o agente não escolhe janelas ou volumes arbitrários;
 * - contexto: saída grande demais enterra a evidência útil e custa tokens.
 */
export const LIMITS = {
  maxWindowMinutes: 6 * 60,
  maxLogLines: 400,
  maxRecentChangesHours: 72,
  maxListItems: 100,
  maxOutputChars: 20_000,
} as const;

/** Nome de recurso Kubernetes (DNS-1123). Recusa qualquer coisa fora disso. */
export const K8S_NAME = /^[a-z0-9]([-a-z0-9.]{0,251}[a-z0-9])?$/;

export function truncateOutput(text: string, limit: number = LIMITS.maxOutputChars): string {
  if (text.length <= limit) return text;
  const omitted = text.length - limit;
  return `${text.slice(0, limit)}\n[saída truncada: ${omitted} caracteres omitidos; refine a consulta]`;
}

/** Serializa o resultado de uma tool de forma compacta e já truncada. */
export function toolOutput(value: unknown): string {
  return truncateOutput(JSON.stringify(value));
}

export function minutesAgo(minutes: number, now: Date = new Date()): Date {
  return new Date(now.getTime() - minutes * 60_000);
}
