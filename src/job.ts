import { readFileSync } from "node:fs";
import { pathToFileURL } from "node:url";
import { investigate } from "./agent/investigate.js";
import { loadAgentConfig } from "./agent-config.js";
import { loadJobEnv } from "./config.js";
import { Incident } from "./incident.js";
import { connectIntegrations, resolveIntegrations } from "./integrations/registry.js";
import { publishReport } from "./slack/post.js";

/**
 * Entrada do job efêmero: um processo, um incidente.
 * Lê o incidente de INCIDENT_JSON (ou de um arquivo passado como argumento), investiga, publica e sai.
 */
export async function runJob(incident: Incident): Promise<void> {
  const env = loadJobEnv();
  const { config, scopes } = loadAgentConfig(env.AGENT_CONFIG_FILE);
  scopes.get(incident.product); // recusa cedo se o produto não está no inventário

  const startedAt = Date.now();
  const toolset = await connectIntegrations(resolveIntegrations(config), { scopes, env: process.env });
  let result;
  try {
    result = await investigate(incident, toolset);
  } finally {
    await toolset.close();
  }
  const { report, usage } = result;
  const published = await publishReport(report, incident.slackChannel, env.SLACK_BOT_TOKEN);

  console.error(
    JSON.stringify({
      event: "rca.finished",
      incidentId: incident.id,
      status: report.status,
      confidence: report.rootCause.confidence,
      published: published.kind,
      durationMs: Date.now() - startedAt,
      ...usage,
    }),
  );
}

function readIncident(): Incident {
  const fromFile = process.argv[2];
  const raw = fromFile ? readFileSync(fromFile, "utf8") : process.env["INCIDENT_JSON"];
  if (!raw) throw new Error("Passe o incidente em INCIDENT_JSON ou como caminho de arquivo");
  return Incident.parse(JSON.parse(raw));
}

if (import.meta.url === pathToFileURL(process.argv[1] ?? "").href) {
  runJob(readIncident()).catch((error: unknown) => {
    console.error(JSON.stringify({ event: "rca.failed", error: error instanceof Error ? error.message : String(error) }));
    process.exit(1);
  });
}
