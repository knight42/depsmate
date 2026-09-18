import { createHmac } from "node:crypto";
import { describe, expect, it, vi } from "vitest";
import worker, { jobsForEvent, type Env } from "../src/index";
import type { Job } from "../src/github";

function env() {
  return { DEPSMATE_APP_ID: "1", DEPSMATE_PRIVATE_KEY: "test", DEPSMATE_WEBHOOK_SECRET: "test-secret", JOBS: { sendBatch: vi.fn().mockResolvedValue(undefined), send: vi.fn() } } as unknown as Env;
}
function request(body: string, event = "pull_request", secret = "test-secret") {
  return new Request("https://depsmate.zejian.me/webhook", { method: "POST", body, headers: {
    "x-github-event": event, "x-hub-signature-256": `sha256=${createHmac("sha256", secret).update(body).digest("hex")}`, "content-type": "application/json",
  } });
}
const payload = { action: "opened", installation: { id: 1 }, repository: { full_name: "owner/repo" }, pull_request: { number: 42 } };
describe("webhook ingress", () => {
  it("rejects a forged signature without queueing", async () => {
    const e = env(); expect((await worker.fetch(request(JSON.stringify(payload), "pull_request", "wrong"), e)).status).toBe(401);
    expect(e.JOBS.sendBatch).not.toHaveBeenCalled();
  });
  it("only acknowledges after durable enqueue", async () => {
    const e = env(); expect((await worker.fetch(request(JSON.stringify(payload)), e)).status).toBe(202);
    expect(e.JOBS.sendBatch).toHaveBeenCalledWith([{ body: { kind: "pr", installation: 1, repo: "owner/repo", number: 42 } }]);
  });
  it("does not acknowledge queue failures", async () => {
    const e = env(); vi.mocked(e.JOBS.sendBatch).mockRejectedValue(new Error("unavailable"));
    await expect(worker.fetch(request(JSON.stringify(payload)), e)).rejects.toThrow("unavailable");
  });
  it("rejects malformed signed payloads", async () => {
    expect((await worker.fetch(request("{"), env())).status).toBe(400);
    expect((await worker.fetch(request(JSON.stringify({ ...payload, installation: null })), env())).status).toBe(400);
  });
  it("accepts ping without enqueuing", async () => {
    const e = env(); expect((await worker.fetch(request("{}", "ping"), e)).status).toBe(204);
    expect(e.JOBS.sendBatch).not.toHaveBeenCalled();
  });
  it("fails closed when credentials are missing", async () => {
    const e = env(); e.DEPSMATE_PRIVATE_KEY = "";
    expect((await worker.fetch(request(JSON.stringify(payload)), e)).status).toBe(503);
  });
});
it("splits installation scans into repository jobs", () => {
  expect(jobsForEvent("installation", { action: "created", installation: { id: 3 }, repositories: [{ full_name: "a/b" }, { full_name: "a/c" }] })).toEqual([
    { kind: "scan", installation: 3, repo: "a/b", page: 1 }, { kind: "scan", installation: 3, repo: "a/c", page: 1 },
  ]);
});
it("only handles successful check suites on dependabot branches", () => {
  const p = { ...payload, action: "completed", check_suite: { id: 7, head_branch: "dependabot/npm/foo", conclusion: "success" } };
  expect(jobsForEvent("check_suite", p)[0]).toMatchObject({ kind: "check", suite: 7 });
  expect(jobsForEvent("check_suite", { ...p, check_suite: { ...p.check_suite, conclusion: "failure" } })).toEqual([]);
});
