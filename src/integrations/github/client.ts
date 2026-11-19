import { Octokit } from "@octokit/rest";
import { LIMITS } from "../bounds.js";

/**
 * Mudanças recentes no repositório do produto: PRs mergeados e deployments do GitHub.
 * O token precisa ser SÓ de leitura (GitHub App com contents:read e pull_requests:read,
 * ou um PAT fine-grained equivalente).
 */
export class DeployEvidence {
  private readonly octokit: Octokit;

  constructor(token: string | undefined) {
    this.octokit = new Octokit({ auth: token });
  }

  async recentChanges(repository: string, sinceHours: number) {
    const [owner, repo] = repository.split("/") as [string, string];
    const since = new Date(Date.now() - sinceHours * 3_600_000);

    const pulls = await this.octokit.rest.pulls.list({
      owner,
      repo,
      state: "closed",
      sort: "updated",
      direction: "desc",
      per_page: 50,
    });
    const merged = pulls.data
      .filter((pr) => pr.merged_at !== null && new Date(pr.merged_at) >= since)
      .map((pr) => ({
        number: pr.number,
        title: pr.title,
        mergedAt: pr.merged_at,
        author: pr.user?.login,
        baseBranch: pr.base.ref,
        url: pr.html_url,
      }));

    const deployments = await this.octokit.rest.repos.listDeployments({ owner, repo, per_page: 30 });
    const recentDeploys = deployments.data
      .filter((d) => new Date(d.created_at) >= since)
      .map((d) => ({ environment: d.environment, ref: d.ref, sha: d.sha.slice(0, 12), createdAt: d.created_at }));

    return {
      window: { since: since.toISOString(), hours: sinceHours },
      mergedPullRequests: merged.slice(0, LIMITS.maxListItems),
      deployments: recentDeploys.slice(0, LIMITS.maxListItems),
    };
  }
}
