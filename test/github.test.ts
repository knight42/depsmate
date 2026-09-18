import { expect, it, vi } from "vitest";
import { Octokit } from "@octokit/rest";
import { processJob, type Job } from "../src/github";

const job: Job = { kind: "pr", installation: 1, repo: "owner/repo", number: 42 };
const credentials = { DEPSMATE_APP_ID: "1", DEPSMATE_PRIVATE_KEY: "unused" };
function harness(options: { checks?: string | null; config?: string; approved?: boolean; queue?: boolean; dryRun?: boolean } = {}) {
  const calls: { url: string; method: string; body: Record<string, unknown> }[] = [];
  const fetch = vi.fn(async (input: string | URL | Request, init?: RequestInit) => {
    const url = String(input), method = init?.method ?? "GET";
    const body = init?.body ? JSON.parse(String(init.body)) : {};
    calls.push({ url, method, body });
    let data: unknown = {};
    let status = 200;
    if (url.endsWith("/pulls/42") && method === "GET") data = { number: 42, node_id: "PR_42", state: "open", draft: false, user: { login: "dependabot[bot]" }, base: { ref: "main" }, head: { ref: "dependabot/npm_and_yarn/foo", sha: "verified-sha" }, title: "Bump foo from 1.0.0 to 1.1.0" };
    else if (url.includes("/contents/")) {
      if (options.config !== undefined) data = { type: "file", content: btoa(options.config) };
      else { status = 404; data = { message: "Not found" }; }
    } else if (url.includes("/git/commits/")) data = { message: "Bump foo from 1.0.0 to 1.1.0" };
    else if (url.includes("/reviews") && method === "GET") data = options.approved ? [{ state: "APPROVED", commit_id: "verified-sha" }] : [];
    else if (url.endsWith("/graphql")) {
      const query = String(body.query);
      if (query.includes("mergeQueue(branch:")) data = { data: { repository: { mergeQueue: options.queue ? { id: "QUEUE" } : null } } };
      else if (query.includes("enablePullRequestAutoMerge")) data = { errors: [{ message: "Pull request is in clean status" }] };
      else if (query.includes("statusCheckRollup")) data = { data: { repository: { object: { statusCheckRollup: options.checks ? { state: options.checks } : null } } } };
      else data = { data: { enqueuePullRequest: { mergeQueueEntry: { id: "ENTRY" } } } };
    } else if (url.endsWith("/repos/owner/repo")) data = { allow_squash_merge: true };
    return new Response(JSON.stringify(data), { status, headers: { "content-type": "application/json" } });
  });
  const client = new Octokit({ auth: "test", request: { fetch }, log: { debug(){}, info(){}, warn(){}, error(){} } });
  const queue = { send: vi.fn(), sendBatch: vi.fn() } as unknown as Queue<Job>;
  return { calls, client, queue, run: () => processJob(job, { ...credentials, DEPSMATE_DRY_RUN: options.dryRun ? "true" : "false" }, queue, client) };
}
it.each([null, "PENDING", "FAILURE"])("never directly merges with check state %s", async checks => {
  const h = harness({ checks }); await h.run();
  expect(h.calls.some(c => c.url.endsWith("/merge"))).toBe(false);
  expect(h.calls.some(c => c.url.endsWith("/reviews") && c.method === "POST")).toBe(true);
});
it("pins approval and direct merge to the verified head", async () => {
  const h = harness({ checks: "SUCCESS" }); await h.run();
  expect(h.calls.find(c => c.url.endsWith("/merge"))?.body).toMatchObject({ sha: "verified-sha", merge_method: "squash" });
  expect(h.calls.find(c => c.url.endsWith("/reviews") && c.method === "POST")?.body.commit_id).toBe("verified-sha");
  expect(h.calls.find(c => c.url.includes("/contents/"))?.url).toContain("ref=main");
});
it("invalid policy causes no GitHub writes", async () => {
  const h = harness({ config: "enable: true" }); await h.run();
  expect(h.calls.every(c => c.method === "GET")).toBe(true);
});
it("dry run causes no GitHub writes", async () => {
  const h = harness({ dryRun: true }); await h.run();
  expect(h.calls.every(c => c.method === "GET")).toBe(true);
});
it("skips an existing approval for this head", async () => {
  const h = harness({ approved: true }); await h.run();
  expect(h.calls.some(c => c.url.endsWith("/reviews") && c.method === "POST")).toBe(false);
});
it("uses the merge queue without direct merge", async () => {
  const h = harness({ queue: true }); await h.run();
  expect(h.calls.some(c => String(c.body.query).includes("enqueuePullRequest"))).toBe(true);
  expect(h.calls.some(c => c.url.endsWith("/merge"))).toBe(false);
});
