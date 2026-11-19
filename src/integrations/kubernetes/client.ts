import { AppsV1Api, CoreV1Api, KubeConfig } from "@kubernetes/client-node";
import { LIMITS, minutesAgo } from "../bounds.js";

/**
 * Leitura do cluster. Só usa verbos get/list e só nos recursos que o RBAC do Job permite
 * (deploy/k8s/rbac-readonly.yaml): pods, pods/log, events, replicasets, deployments.
 * Sem secrets, sem exec, sem nenhum verbo que altera algo. O RBAC é a garantia;
 * este código é só a forma de pedir.
 */
export class K8sEvidence {
  private readonly core: CoreV1Api;
  private readonly apps: AppsV1Api;

  constructor(kubeConfig?: KubeConfig) {
    const kc = kubeConfig ?? K8sEvidence.defaultConfig();
    this.core = kc.makeApiClient(CoreV1Api);
    this.apps = kc.makeApiClient(AppsV1Api);
  }

  /** Dentro do cluster usa a ServiceAccount do pod; fora, o ~/.kube/config (dev). */
  static defaultConfig(): KubeConfig {
    const kc = new KubeConfig();
    kc.loadFromDefault();
    return kc;
  }

  async listPods(namespace: string, labelSelector?: string) {
    const list = await this.core.listNamespacedPod({ namespace, labelSelector, limit: LIMITS.maxListItems });
    return list.items.map((pod) => {
      const statuses = pod.status?.containerStatuses ?? [];
      return {
        name: pod.metadata?.name,
        phase: pod.status?.phase,
        node: pod.spec?.nodeName,
        startedAt: pod.status?.startTime,
        containers: statuses.map((c) => ({
          name: c.name,
          ready: c.ready,
          restarts: c.restartCount,
          image: c.image,
          waitingReason: c.state?.waiting?.reason,
          lastTermination: c.lastState?.terminated
            ? {
                reason: c.lastState.terminated.reason,
                exitCode: c.lastState.terminated.exitCode,
                finishedAt: c.lastState.terminated.finishedAt,
              }
            : undefined,
        })),
      };
    });
  }

  async podLogs(args: {
    namespace: string;
    pod: string;
    container?: string;
    sinceMinutes: number;
    tailLines: number;
    previous: boolean;
  }): Promise<string> {
    return this.core.readNamespacedPodLog({
      namespace: args.namespace,
      name: args.pod,
      container: args.container,
      sinceSeconds: args.sinceMinutes * 60,
      tailLines: args.tailLines,
      previous: args.previous,
      timestamps: true,
    });
  }

  async events(namespace: string, sinceMinutes: number) {
    const since = minutesAgo(sinceMinutes);
    const list = await this.core.listNamespacedEvent({ namespace });
    return list.items
      .map((e) => ({
        type: e.type,
        reason: e.reason,
        object: `${e.involvedObject.kind}/${e.involvedObject.name}`,
        message: e.message,
        count: e.count,
        lastSeen: e.lastTimestamp ?? e.eventTime ?? e.metadata.creationTimestamp,
      }))
      .filter((e) => e.lastSeen !== undefined && new Date(e.lastSeen) >= since)
      .sort((a, b) => Number(b.type === "Warning") - Number(a.type === "Warning"))
      .slice(0, LIMITS.maxListItems);
  }

  /**
   * Histórico de rollout de um Deployment: cada ReplicaSet é uma revisão, com imagem e horário.
   * É a evidência direta de "mudou algo aqui", e não só "algo aconteceu antes".
   */
  async rolloutHistory(namespace: string, deployment: string) {
    const deploy = await this.apps.readNamespacedDeployment({ namespace, name: deployment });
    const selector = Object.entries(deploy.spec?.selector.matchLabels ?? {})
      .map(([k, v]) => `${k}=${v}`)
      .join(",");
    const sets = await this.apps.listNamespacedReplicaSet({ namespace, labelSelector: selector });
    return sets.items
      .filter((rs) => rs.metadata?.ownerReferences?.some((o) => o.kind === "Deployment" && o.name === deployment))
      .map((rs) => ({
        revision: Number(rs.metadata?.annotations?.["deployment.kubernetes.io/revision"] ?? 0),
        replicaSet: rs.metadata?.name,
        createdAt: rs.metadata?.creationTimestamp,
        replicas: rs.status?.replicas ?? 0,
        images: rs.spec?.template?.spec?.containers.map((c) => c.image) ?? [],
        changeCause: rs.metadata?.annotations?.["kubernetes.io/change-cause"],
      }))
      .sort((a, b) => b.revision - a.revision)
      .slice(0, 10);
  }
}
