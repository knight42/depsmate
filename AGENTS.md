# depsmate — project conventions

GitHub App (Go) that auto-merges safe dependabot PRs. User-facing docs live
in README.md; this file is for people (and agents) working on the code.

## Layout

- `main.go` — env wiring only; no business logic.
- `pkg/policy` — pure decision engine. No network, no GitHub types; takes a
  `Config` + `PR` struct, returns a `Decision`. All policy behavior is
  covered by table-driven tests here.
- `pkg/server` — webhook handling and every GitHub API side effect
  (approve, auto-merge, enqueue, direct merge, config fetch).

Keep that boundary: new policy rules go in `pkg/policy` with tests; new API
interactions go in `pkg/server`.

## Commands

```bash
make build   # go build ./...
make test    # go test ./...
make lint    # golangci-lint run — required before pushing
```

## Design rules

- **Fail closed.** Anything indeterminate means "don't merge": a broken or
  unknown-key `depsmate.yml`, a dependency-name/update-type count mismatch
  in commit metadata, an ecosystem missing from the config with no `"*"`
  entry. Never let an error path widen the policy.
- **Config is read from the PR base branch**, never the head — the head is
  attacker-influenceable in principle. Keep it that way.
- **CI is never bypassed**: merging always goes through auto-merge, the
  merge queue, or a direct merge gated on the head commit's status-check
  rollup being SUCCESS — a PR with no checks at all is approved, never
  merged. The gate is evidence-based (did checks pass?), deliberately not
  configuration-based (are checks marked required?): a repo whose CI isn't
  marked required still only merges verified commits.
- Config semantics: `ecosystems` entries merge over the defaults
  `{github_actions: major, "*": minor}`; parsing is strict
  (`yaml.UnmarshalStrict`), so a typoed key is an error by design.

## Gotchas

- **go-github v91**: `github.NewClient` takes option funcs and returns an
  error (`github.WithHTTPClient(...)`). Avoid `github.Ptr(...)` inside
  composite literals — golangci-lint's govet inline check rejects it; use a
  local variable and take its address.
- **sigs.k8s.io/yaml** converts YAML to JSON first: struct tags are
  `json:`, not `yaml:`.
- **Merge queue**: `enablePullRequestAutoMerge` ignores the merge method on
  queue branches (the queue's own method applies), so pass none there. Try
  `enqueuePullRequest` first; arm auto-merge only when the PR isn't
  queue-eligible yet.
- **Auto-merge errors are string-matched** ("clean status", "already
  enabled", "not allowed for this repository") — GitHub's GraphQL API gives
  no error codes for these. If a case stops matching, GitHub changed the
  message.
- The webhook handler ACKs immediately and processes in a goroutine:
  GitHub times deliveries out at 10s, and an installation scan can take
  minutes.
- A PR gets re-evaluated from several triggers (PR events, successful
  check suites, installation scans), so every side effect must be
  idempotent: approval is skipped when the head commit already has an
  active approving review, re-enabling auto-merge / re-enqueueing treats
  "already enabled/queued" as success.
- Merge method is auto-detected per repo (squash > merge commit > rebase
  among the allowed methods); there is deliberately no configuration knob.
- Dependabot's update-type metadata lives in the **head commit message**
  (`updated-dependencies:` YAML block), not in the PR body. The PR-title
  parse is only a fallback for single-dependency bumps.

## GitHub App setup

Create the app under the org: Settings → Developer settings → GitHub Apps →
New GitHub App.

**Repository permissions** (least privilege — nothing else is needed):

| Permission | Level | Used for |
|------------|-------|----------|
| Pull requests | Read & write | list/read PRs, submit the approving review, merge, arm auto-merge |
| Contents | Read & write | read `.github/depsmate.yml` and head commit messages; merging writes to the base branch |
| Merge queues | Read & write | `enqueuePullRequest` on merge-queue branches (only needed if any repo uses a merge queue) |
| Workflows | Read & write | GitHub refuses any App merge of a PR touching `.github/workflows` without it — which is most github-actions bumps |
| Checks | Read | status-check rollup gating the direct-merge path |
| Commit statuses | Read | same rollup (it combines check runs and commit statuses) |
| Metadata | Read | implicit/mandatory; covers `GET /repos` for merge-method detection |

No organization or account permissions.

**Webhook**: set Active, URL `https://<host>/webhook`, and a secret (the
same value as `DEPSMATE_WEBHOOK_SECRET` — signature validation rejects
everything else). Subscribe to the **Pull request** and **Check suite**
events; `installation` / `installation_repositories` events (which trigger
the existing-PR scan) are always delivered to apps without subscribing.

After creating: note the **App ID** (`DEPSMATE_APP_ID`) and generate a
**private key** — GitHub downloads a `.pem`; mount it and point
`DEPSMATE_PRIVATE_KEY_PATH` at it. Then install the app on the target
repositories (installation immediately scans their open PRs, so consider
`DEPSMATE_DRY_RUN=true` for the first install).

**Per-repo prerequisites**:

- Non-merge-queue repos: enable "Allow auto-merge"
  (Settings → General → Pull Requests).
- Required status checks are still strongly recommended. Without them the
  direct-merge path only fires when the head commit's status-check rollup
  is SUCCESS (checks exist and all passed — required or not); a PR with
  pending, failing, or no checks is approved but left unmerged. Successful
  `check_suite` completions on `dependabot/*` branches re-trigger
  evaluation, so such PRs merge once their CI finishes green; a repo with
  no CI at all stays approve-only forever.
- The app's approval counts as a normal review, but does NOT satisfy a
  "require review from Code Owners" rule.

## Deploying

Environment variables (a `.env` in the working directory is loaded first;
real environment wins):

| Variable | Required | Default |
|----------|----------|---------|
| `DEPSMATE_APP_ID` | yes | — |
| `DEPSMATE_PRIVATE_KEY_PATH` | yes | — |
| `DEPSMATE_WEBHOOK_SECRET` | yes | — |
| `DEPSMATE_LISTEN_ADDR` | no | `:8080` |
| `DEPSMATE_DRY_RUN` | no | `false` |

Roll out new installations with `DEPSMATE_DRY_RUN=true` first and read the
logs. The Dockerfile is distroless; runtime config is env-only — never bake
config into the image.
