import { enqueue, processJob, type Credentials, type Job } from "./github";

export interface Env extends Credentials {
  DEPSMATE_WEBHOOK_SECRET: string;
  JOBS: Queue<Job>;
}
const actions = new Set(["opened", "reopened", "synchronize", "ready_for_review"]);

export async function validSignature(body: ArrayBuffer, signature: string | null, secret: string): Promise<boolean> {
  if (!signature || !/^sha256=[a-f0-9]{64}$/.test(signature)) return false;
  const bytes = Uint8Array.from(signature.slice(7).match(/../g)!, hex => Number.parseInt(hex, 16));
  const key = await crypto.subtle.importKey("raw", new TextEncoder().encode(secret), { name: "HMAC", hash: "SHA-256" }, false, ["verify"]);
  return crypto.subtle.verify("HMAC", key, bytes, body);
}
function record(value: unknown): Record<string, unknown> {
  if (!value || typeof value !== "object" || Array.isArray(value)) throw new Error("expected object");
  return value as Record<string, unknown>;
}
function id(value: unknown): number {
  if (!Number.isSafeInteger(value) || Number(value) <= 0) throw new Error("expected positive integer ID");
  return Number(value);
}
function repo(value: unknown): string {
  const name = record(value).full_name;
  if (typeof name !== "string" || !/^[^/]+\/[^/]+$/.test(name)) throw new Error("expected repository full_name");
  return name;
}
export function jobsForEvent(event: string, payload: unknown): Job[] {
  const data = record(payload);
  const action = String(data.action);
  if (event === "pull_request" && actions.has(action)) {
    return [{ kind: "pr", installation: id(record(data.installation).id), repo: repo(data.repository), number: id(record(data.pull_request).number) }];
  }
  if ((event === "installation" && action === "created") || (event === "installation_repositories" && action === "added")) {
    const repos = event === "installation" ? data.repositories : data.repositories_added;
    if (!Array.isArray(repos)) throw new Error("expected repositories");
    const installation = id(record(data.installation).id);
    return repos.map(value => ({ kind: "scan", installation, repo: repo(value), page: 1 }));
  }
  if (event === "check_suite" && action === "completed") {
    const suite = record(data.check_suite);
    if (suite.conclusion !== "success" || typeof suite.head_branch !== "string" || !suite.head_branch.startsWith("dependabot/")) return [];
    return [{ kind: "check", installation: id(record(data.installation).id), repo: repo(data.repository), suite: id(suite.id) }];
  }
  return [];
}
export default {
  async fetch(request: Request, env: Env): Promise<Response> {
    const path = new URL(request.url).pathname;
    if (path === "/healthz" && (request.method === "GET" || request.method === "HEAD")) return new Response("ok\n");
    if (path !== "/webhook") return new Response("Not found", { status: 404 });
    if (request.method !== "POST") return new Response("Method not allowed", { status: 405, headers: { Allow: "POST" } });
    if (!env.DEPSMATE_APP_ID || !env.DEPSMATE_PRIVATE_KEY || !env.DEPSMATE_WEBHOOK_SECRET) return new Response("GitHub App not configured", { status: 503 });
    const body = await request.arrayBuffer();
    if (!await validSignature(body, request.headers.get("x-hub-signature-256"), env.DEPSMATE_WEBHOOK_SECRET)) return new Response("invalid signature", { status: 401 });
    let jobs: Job[];
    try {
      const text = new TextDecoder().decode(body);
      const json = request.headers.get("content-type")?.startsWith("application/x-www-form-urlencoded") ? new URLSearchParams(text).get("payload") ?? "" : text;
      jobs = jobsForEvent(request.headers.get("x-github-event") ?? "", JSON.parse(json));
    } catch { return new Response("unparseable payload", { status: 400 }); }
    if (!jobs.length) return new Response(null, { status: 204 });
    // Acknowledge only after durable enqueue succeeds; no detached background work.
    await enqueue(env.JOBS, jobs);
    return new Response(null, { status: 202 });
  },
  async queue(batch: MessageBatch<Job>, env: Env): Promise<void> {
    for (const message of batch.messages) {
      try {
        await processJob(message.body, env, env.JOBS);
        message.ack();
      } catch (error) {
        console.error("job failed", { job: message.body, error: error instanceof Error ? error.message : String(error) });
        message.retry({ delaySeconds: 60 });
      }
    }
  },
} satisfies ExportedHandler<Env, Job>;
