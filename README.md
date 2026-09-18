# depsmate

A GitHub App that auto-merges safe dependabot PRs, so routine dependency
bumps land without anyone clicking through them.

## What gets auto-merged

A dependabot PR is approved and merged when every updated dependency is
within the allowed update type:

| Ecosystem | Allowed by default |
|-----------|--------------------|
| `github_actions` | any bump, including major |
| everything else (Go, npm, pip, ...) | minor and patch only |

Anything else — a major library bump, a grouped PR containing one, or a PR
whose bump kind can't be determined — is left alone for a human.

Only PRs authored by `dependabot[bot]` are ever touched.

## What it does to an allowed PR

1. Submits an approving review (satisfies a required-review rule).
2. Merges via the path that fits your branch:
   - **Merge queue branch**: adds the PR to the queue (or arms
     "merge when ready" if it isn't eligible yet).
   - **Otherwise**: enables GitHub auto-merge, so your required CI checks
     still gate the actual merge; if checks are already green, merges
     directly. The merge method follows your repository's allowed methods
     (squash preferred, then merge commit, then rebase).

CI is never bypassed: a PR only merges once the checks on its head commit
have all passed — even checks you haven't marked as required. A repo with
no CI at all gets approvals only; the merge stays yours.

## Getting started

1. Install the app on your repository.
2. For non-merge-queue repos, enable **"Allow auto-merge"** in
   Settings → General → Pull Requests.

That's it. On installation the app also scans your existing open PRs, so
dependabot PRs opened earlier are handled right away — no need to wait for
dependabot's next run.

## Tuning: `.github/depsmate.yml`

Optional, read from the PR's base branch. Missing file = the defaults above.

```yaml
enabled: true            # false turns depsmate off for this repo
ecosystems:              # max update type per dependabot ecosystem
  github_actions: major  #   (branch-name form); "*" covers unlisted ones.
  "*": minor             #   Entries MERGE over the defaults shown here,
  pip: patch             #   so listing one key leaves the rest at default.
ignore:                  # dependency-name globs (path.Match) never auto-merged
  - react
  - github.com/aws/*
```

A config file that fails to parse (including unknown keys) makes depsmate
skip your PRs entirely — it never falls back to a looser policy. Check the
app's logs if PRs stop being merged after a config change.
