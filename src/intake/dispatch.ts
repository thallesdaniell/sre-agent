import { BatchV1Api, KubeConfig, type V1Job } from "@kubernetes/client-node";
import type { Incident } from "../incident.js";

export type DispatchResult = { kind: "dispatched"; ref: string } | { kind: "duplicate"; ref: string };

/** Quem executa a investigação. O intake não sabe nem quer saber onde ela roda. */
export interface Dispatcher {
  dispatch(incident: Incident): Promise<DispatchResult>;
}

/** Nome do Job derivado do id do incidente: o mesmo incidente nunca gera dois Jobs. */
/**
 * Configuração (não segredo) que o intake repassa ao Job. Segredos chegam pelo Secret (envFrom);
 * isto aqui é o que muda o comportamento: modo de publicação e destino do rastreio.
 */
export const FORWARDED_JOB_ENV = [
  "PUBLISH_MODE",
  "TRACE_CONTENT",
  "TRACE_URL_TEMPLATE",
  "LANGFUSE_BASE_URL",
  "OTEL_EXPORTER_OTLP_ENDPOINT",
  "APP_VERSION",
] as const;

export function forwardedEnv(source: NodeJS.ProcessEnv): { name: string; value: string }[] {
  return FORWARDED_JOB_ENV.flatMap((name) => (source[name] ? [{ name, value: source[name]! }] : []));
}

export function jobName(incidentId: string): string {
  const slug = incidentId.toLowerCase().replace(/[^a-z0-9-]/g, "-").replace(/-+/g, "-").slice(0, 50);
  return `rca-${slug}`.replace(/-$/, "");
}

/**
 * Produção: cria um Kubernetes Job efêmero por incidente. O Job roda `node dist/job.js` com a
 * ServiceAccount só de leitura e some sozinho depois (ttlSecondsAfterFinished).
 */
export class KubernetesJobDispatcher implements Dispatcher {
  private readonly batch: BatchV1Api;

  constructor(
    private readonly options: {
      namespace: string;
      image: string;
      serviceAccount: string;
      secretName: string;
      env?: { name: string; value: string }[];
    },
    kubeConfig?: KubeConfig,
  ) {
    const kc = kubeConfig ?? new KubeConfig();
    if (!kubeConfig) kc.loadFromDefault();
    this.batch = kc.makeApiClient(BatchV1Api);
  }

  async dispatch(incident: Incident): Promise<DispatchResult> {
    const name = jobName(incident.id);
    try {
      await this.batch.createNamespacedJob({ namespace: this.options.namespace, body: this.buildJob(name, incident) });
      return { kind: "dispatched", ref: name };
    } catch (error) {
      if (isConflict(error)) return { kind: "duplicate", ref: name };
      throw error;
    }
  }

  private buildJob(name: string, incident: Incident): V1Job {
    return {
      apiVersion: "batch/v1",
      kind: "Job",
      metadata: { name, labels: { app: "sre-agent", incident: jobName(incident.id).slice(4) } },
      spec: {
        backoffLimit: 0, // não repete sozinho: um RCA duplicado no canal é pior que um que falhou
        activeDeadlineSeconds: 1800,
        ttlSecondsAfterFinished: 3600,
        template: {
          metadata: { labels: { app: "sre-agent" } },
          spec: {
            restartPolicy: "Never",
            serviceAccountName: this.options.serviceAccount,
            automountServiceAccountToken: true,
            containers: [
              {
                name: "agent",
                image: this.options.image,
                command: ["node", "dist/job.js"],
                env: [
                  { name: "INCIDENT_JSON", value: JSON.stringify(incident) },
                  { name: "AGENT_CONFIG_FILE", value: "/config/agent.config.json" },
                  ...(this.options.env ?? []),
                ],
                envFrom: [{ secretRef: { name: this.options.secretName } }],
                volumeMounts: [{ name: "config", mountPath: "/config", readOnly: true }],
                resources: { requests: { cpu: "100m", memory: "256Mi" }, limits: { memory: "512Mi" } },
                securityContext: {
                  runAsNonRoot: true,
                  allowPrivilegeEscalation: false,
                  readOnlyRootFilesystem: true,
                  capabilities: { drop: ["ALL"] },
                },
              },
            ],
            volumes: [{ name: "config", configMap: { name: "sre-agent-config" } }],
          },
        },
      },
    };
  }
}

function isConflict(error: unknown): boolean {
  return typeof error === "object" && error !== null && "code" in error && (error as { code: unknown }).code === 409;
}
