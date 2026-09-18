# depsmate — project conventions

TypeScript GitHub App on Cloudflare Workers. User-facing docs live in README.md.

## Layout

- `src/policy.ts`: pure policy evaluation and strict YAML config parsing.
- `src/index.ts`: webhook signature validation, event-to-job conversion, queue consumer.
- `src/github.ts`: GitHub App authentication, repository scans, approval and merging.
- `test/`: policy parity fixtures, webhook and GitHub API tests.
- `wrangler.jsonc`: custom domain and queue bindings.

## Commands

- `npm ci`: install locked dependencies.
- `npm run lint`: TypeScript checking; required before pushing.
- `npm test`: run policy, ingress, and GitHub behavior tests.
- `npx wrangler deploy --dry-run`: check the Worker bundle.
- `npm run dev`: local Worker development.
- `npm run deploy`: publish Worker and domain configuration.

## Design rules

- Fail closed on invalid config, unknown config keys, indeterminate update types, or
  GitHub API errors. Read `.github/depsmate.yml` from the PR base branch only.
- Defaults are `{github_actions: major, "*": minor}`; configured ecosystems merge
  over them. Every dependency in a grouped update must pass the policy.
- Validate webhook HMAC over the raw request bytes before parsing or queueing.
  Return 202 only after durable enqueue. Never use detached promises or
  `waitUntil` for repository scans.
- Queue jobs fetch fresh PR state. Repository scans process one page per job
  and enqueue individual PR jobs. Failed jobs retry, then reach the dead-letter
  queue. Delivery is at least once: skip an existing approval on the same head
  and treat already-enabled/queued responses as success.
- Merging uses the merge queue, auto-merge, or a direct merge gated on a SUCCESS
  head-commit check rollup. Missing/pending/failing checks must not directly merge.
  Pin direct merges and reviews to the inspected head SHA.
- Queue branches omit the merge method when arming auto-merge. Other branches
  select squash, merge, then rebase from the repository's allowed methods.
- Dependabot update metadata comes from the head commit message, not the PR
  body. A single dependency title is the fallback when metadata is absent or a
  single dependency omits update-type (as security updates can). In the latter
  case the title name must match metadata. Infer only numeric release versions
  with equal precision; incomplete groups and unknown update types stay denied.
- GitHub GraphQL errors use message matching. Do not hide failures or log
  credentials, JWTs, webhook payloads, or private keys.

## Runtime configuration

Worker secrets: `DEPSMATE_APP_ID`, `DEPSMATE_PRIVATE_KEY` (PKCS#8 PEM contents),
`DEPSMATE_WEBHOOK_SECRET`. Optional `DEPSMATE_DRY_RUN=true` disables writes.
Use Wrangler secrets in production and ignored `.dev.vars` locally.
Never commit `.env`, `.dev.vars`, PEM files, or credentials.

## GitHub App

Webhook: `https://depsmate.zejian.me/webhook`, JSON, SSL verification enabled.
Subscribe to Pull request and Check suite; installation events are automatic.
Repository permissions: contents, pull requests, merge queues, workflows write;
checks, commit statuses, metadata read. No organization permissions.
Enable Allow auto-merge on non-queue repositories. Keep required CI checks in
repository rules; this App's review does not satisfy Code Owners requirements.
