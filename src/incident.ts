import { z } from "zod";

/**
 * Formato interno de um incidente, independente de quem avisou (incident.io, PagerDuty, Datadog).
 * Cada fonte ganha um adaptador que converte o payload dela para isto.
 */
export const Incident = z.object({
  source: z.string().regex(/^[a-z0-9._-]{1,40}$/).default("webhook").describe("Quem avisou: incident.io, pagerduty..."),
  id: z.string().regex(/^[A-Za-z0-9._-]{1,64}$/),
  title: z.string().min(1).max(300),
  product: z.string().regex(/^[a-z0-9-]+$/),
  severity: z.enum(["sev1", "sev2", "sev3", "sev4"]),
  declaredAt: z.iso.datetime(),
  summary: z.string().max(4000).default(""),
  slackChannel: z.string().optional().describe("Canal onde o relatório é publicado"),
});
export type Incident = z.infer<typeof Incident>;
