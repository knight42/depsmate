import { Octokit } from "@octokit/rest";
import { createAppAuth } from "@octokit/auth-app";
import { defaultConfig, dependabotLogin, evaluate, parseConfig } from "./policy";

export interface Credentials {
  DEPSMATE_APP_ID: string;
  DEPSMATE_PRIVATE_KEY: string;
  DEPSMATE_DRY_RUN?: string;
}
export type Job =
  | { kind: "pr"; installation: number; repo: string; number: number }
  | { kind: "scan"; installation: number; repo: string; page: number }
  | { kind: "check"; installation: number; repo: string; suite: number };
export function githubClient(env: Credentials, installation: number): Octokit {
  return new Octokit({
    authStrategy: createAppAuth,
    auth: { appId: env.DEPSMATE_APP_ID, privateKey: env.DEPSMATE_PRIVATE_KEY, installationId: installation },
    userAgent: "depsmate",
    request: { timeout: 30_000 },
  });
}
function status(error: unknown): number | undefined {
  return typeof error === "object" && error !== null && "status" in error ? Number(error.status) : undefined;
}
function message(error: unknown): string { return error instanceof Error ? error.message : String(error); }
function repository(repo: string) {
  const parts = repo.split("/");
  if (parts.length !== 2 || !parts.every(Boolean)) throw new Error("invalid repository name");
  return { owner: parts[0], repo: parts[1] };
}
export async function processJob(job: Job, env: Credentials, queue: Queue<Job>, client = githubClient(env, job.installation)): Promise<void> {
  const ref = repository(job.repo);
  if (job.kind === "scan") {
    const { data: prs } = await client.rest.pulls.list({ ...ref, state: "open", per_page: 100, page: job.page });
    await enqueue(queue, prs.filter(pr => pr.user?.login === dependabotLogin).map(pr => ({
      kind: "pr", installation: job.installation, repo: job.repo, number: pr.number,
    })));
    if (prs.length === 100) await queue.send({ ...job, page: job.page + 1 });
    return;
  }
  if (job.kind === "check") {
    // Refresh the suite to obtain its associated PR references.
    const { data } = await client.rest.checks.getSuite({ ...ref, check_suite_id: job.suite });
    await enqueue(queue, (data.pull_requests ?? []).map(pr => ({
      kind: "pr", installation: job.installation, repo: job.repo, number: pr.number,
    })));
    return;
  }
  const { data: pr } = await client.rest.pulls.get({ ...ref, pull_number: job.number });
  if (pr.draft || pr.state !== "open" || pr.user?.login !== dependabotLogin) return;
  let cfg = defaultConfig();
  try {
    const { data } = await client.rest.repos.getContent({ ...ref, path: ".github/depsmate.yml", ref: pr.base.ref });
    if (Array.isArray(data) || data.type !== "file" || !("content" in data)) throw new Error("policy is not a file");
    const text = new TextDecoder().decode(Uint8Array.from(atob(data.content.replace(/\s/g, "")), c => c.charCodeAt(0)));
    try { cfg = parseConfig(text); }
    catch (error) {
      console.error("invalid policy; skipping PR", { repo: job.repo, number: job.number, error: message(error) });
      return;
    }
  } catch (error) {
    if (status(error) !== 404) throw error;
  }
  const { data: commit } = await client.rest.git.getCommit({ ...ref, commit_sha: pr.head.sha });
  const decision = evaluate(cfg, { authorLogin: pr.user.login, headRef: pr.head.ref, title: pr.title, headCommitMessage: commit.message });
  console.log("policy decision", { repo: job.repo, number: job.number, ...decision });
  if (!decision.merge || env.DEPSMATE_DRY_RUN === "true") return;

  let approved = false;
  for await (const { data: reviews } of client.paginate.iterator(client.rest.pulls.listReviews, { ...ref, pull_number: pr.number, per_page: 100 })) {
    if (reviews.some(review => review.state === "APPROVED" && review.commit_id === pr.head.sha)) {
      approved = true;
      break;
    }
  }
  if (!approved) await client.rest.pulls.createReview({ ...ref, pull_number: pr.number, commit_id: pr.head.sha, event: "APPROVE", body: `depsmate: auto-approving — ${decision.reason}` });

  const queueState = await client.graphql<{ repository: { mergeQueue: { id: string } | null } }>(
    `query($owner:String!,$repo:String!,$branch:String!){repository(owner:$owner,name:$repo){mergeQueue(branch:$branch){id}}}`,
    { ...ref, branch: pr.base.ref },
  );
  if (queueState.repository.mergeQueue) {
    try {
      await client.graphql(`mutation($id:ID!){enqueuePullRequest(input:{pullRequestId:$id}){mergeQueueEntry{id}}}`, { id: pr.node_id });
    } catch (error) {
      if (/already/i.test(message(error))) return;
      if (message(error).includes("without `workflows` permission")) throw error;
      await enableAutoMerge(client, pr.node_id);
    }
    return;
  }
  const { data: repo } = await client.rest.repos.get(ref);
  const method = repo.allow_squash_merge ? "squash" : repo.allow_merge_commit ? "merge" : repo.allow_rebase_merge ? "rebase" : undefined;
  if (!method) throw new Error("repository allows no merge method");
  try {
    await enableAutoMerge(client, pr.node_id, method.toUpperCase());
  } catch (error) {
    if (/already enabled/i.test(message(error))) return;
    if (!message(error).includes("clean status")) throw error;
    const result = await client.graphql<{ repository: { object: { statusCheckRollup: { state: string } | null } | null } }>(
      `query($owner:String!,$repo:String!,$sha:GitObjectID!){repository(owner:$owner,name:$repo){object(oid:$sha){... on Commit{statusCheckRollup{state}}}}}`,
      { ...ref, sha: pr.head.sha },
    );
    if (result.repository.object?.statusCheckRollup?.state !== "SUCCESS") {
      console.log("approved only: no successful check rollup", { repo: job.repo, number: pr.number });
      return;
    }
    // Pin the verified head: GitHub rejects a merge if a new commit arrived.
    await client.rest.pulls.merge({ ...ref, pull_number: pr.number, merge_method: method, sha: pr.head.sha });
  }
}
async function enableAutoMerge(client: Octokit, id: string, method?: string): Promise<void> {
  await client.graphql(`mutation($input:EnablePullRequestAutoMergeInput!){enablePullRequestAutoMerge(input:$input){pullRequest{number}}}`, {
    input: { pullRequestId: id, ...(method ? { mergeMethod: method } : {}) },
  });
}
export async function enqueue(queue: Queue<Job>, jobs: Job[]): Promise<void> {
  for (let i = 0; i < jobs.length; i += 100) await queue.sendBatch(jobs.slice(i, i + 100).map(body => ({ body })));
}
