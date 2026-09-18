// Package server implements the GitHub App webhook server that approves and
// auto-merges dependabot PRs allowed by the policy package.
package server

import (
	"context"
	"fmt"
	"log/slog"
	"net/http"
	"strings"
	"time"

	"github.com/bradleyfalzon/ghinstallation/v2"
	"github.com/google/go-github/v91/github"
	"github.com/shurcooL/githubv4"

	"github.com/knight42/depsmate/pkg/policy"
)

// Config holds everything the webhook server needs at runtime.
type Config struct {
	WebhookSecret []byte
	AppsTransport *ghinstallation.AppsTransport
	DryRun        bool
	Logger        *slog.Logger
}

// Server handles GitHub webhook deliveries.
type Server struct {
	cfg Config
}

func New(cfg Config) *Server {
	return &Server{cfg: cfg}
}

// Handler returns the HTTP handler exposing /webhook and /healthz.
func (s *Server) Handler() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("GET /healthz", func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusOK)
	})
	mux.HandleFunc("POST /webhook", s.handleWebhook)
	return mux
}

func (s *Server) handleWebhook(w http.ResponseWriter, r *http.Request) {
	payload, err := github.ValidatePayload(r, s.cfg.WebhookSecret)
	if err != nil {
		http.Error(w, "invalid signature", http.StatusUnauthorized)
		return
	}
	event, err := github.ParseWebHook(github.WebHookType(r), payload)
	if err != nil {
		http.Error(w, "unparseable payload", http.StatusBadRequest)
		return
	}

	// Ack immediately: GitHub times deliveries out at 10s, and processing
	// makes several API calls (an installation scan, many).
	switch e := event.(type) {
	case *github.PullRequestEvent:
		go s.processPullRequestEvent(e)
	case *github.InstallationEvent:
		if e.GetAction() == "created" {
			go s.scanRepos(e.GetInstallation().GetID(), e.Repositories)
		}
	case *github.InstallationRepositoriesEvent:
		if e.GetAction() == "added" {
			go s.scanRepos(e.GetInstallation().GetID(), e.RepositoriesAdded)
		}
	case *github.CheckSuiteEvent:
		// Re-evaluate when CI finishes on a dependabot branch: on branches
		// without required checks, auto-merge can't be armed, so the PR
		// was approved but left unmerged while checks were pending.
		if e.GetAction() == "completed" &&
			e.GetCheckSuite().GetConclusion() == "success" &&
			strings.HasPrefix(e.GetCheckSuite().GetHeadBranch(), "dependabot/") {
			go s.processCheckSuite(e)
		}
	default:
		w.WriteHeader(http.StatusNoContent)
		return
	}
	w.WriteHeader(http.StatusAccepted)
}

// handledActions are the PR lifecycle points where a merge decision is due;
// "synchronize" covers dependabot rebases, which dismiss earlier approvals.
var handledActions = map[string]bool{
	"opened":           true,
	"reopened":         true,
	"synchronize":      true,
	"ready_for_review": true,
}

func (s *Server) processPullRequestEvent(e *github.PullRequestEvent) {
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()

	if !handledActions[e.GetAction()] {
		return
	}
	log := s.cfg.Logger.With("action", e.GetAction())

	c, err := s.newClients(e.GetInstallation().GetID())
	if err != nil {
		log.Error("building installation clients failed", "err", err,
			"repo", e.GetRepo().GetFullName(), "pr", e.GetPullRequest().GetNumber())
		return
	}
	s.evaluateAndMerge(ctx, log, c, e.GetRepo().GetOwner().GetLogin(), e.GetRepo().GetName(), e.GetPullRequest())
}

func (s *Server) processCheckSuite(e *github.CheckSuiteEvent) {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Minute)
	defer cancel()

	log := s.cfg.Logger.With("action", "check_suite_completed")

	c, err := s.newClients(e.GetInstallation().GetID())
	if err != nil {
		log.Error("building installation clients failed", "err", err)
		return
	}

	owner := e.GetRepo().GetOwner().GetLogin()
	name := e.GetRepo().GetName()
	for _, ref := range e.GetCheckSuite().PullRequests {
		// The event's PR objects are skeletal (no author/title/draft).
		pr, _, err := c.rest.PullRequests.Get(ctx, owner, name, ref.GetNumber())
		if err != nil {
			log.Error("fetching PR failed", "repo", owner+"/"+name, "pr", ref.GetNumber(), "err", err)
			continue
		}
		s.evaluateAndMerge(ctx, log, c, owner, name, pr)
	}
}

// scanRepos processes the open PRs of repositories that were just added to an
// installation, so PRs dependabot opened before the app was installed are not
// missed while waiting for their next webhook event.
func (s *Server) scanRepos(instID int64, repos []*github.Repository) {
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Minute)
	defer cancel()

	log := s.cfg.Logger.With("action", "installation_scan")

	c, err := s.newClients(instID)
	if err != nil {
		log.Error("building installation clients failed", "err", err)
		return
	}

	for _, repo := range repos {
		// Installation payloads carry only full_name, not a nested owner.
		owner, name, ok := strings.Cut(repo.GetFullName(), "/")
		if !ok {
			log.Error("repository has no usable full_name", "full_name", repo.GetFullName())
			continue
		}
		rlog := log.With("repo", repo.GetFullName())

		opts := &github.PullRequestListOptions{
			State:       "open",
			ListOptions: github.ListOptions{PerPage: 100},
		}
		scanned := 0
		for {
			prs, resp, err := c.rest.PullRequests.List(ctx, owner, name, opts)
			if err != nil {
				rlog.Error("listing open PRs failed", "err", err)
				break
			}
			for _, pr := range prs {
				if pr.GetUser().GetLogin() != policy.DependabotLogin {
					continue
				}
				scanned++
				// evaluateAndMerge attaches repo/pr itself; rlog would duplicate them.
				s.evaluateAndMerge(ctx, log, c, owner, name, pr)
			}
			if resp.NextPage == 0 {
				break
			}
			opts.Page = resp.NextPage
		}
		rlog.Info("scan finished", "dependabot_prs", scanned)
	}
}

type clients struct {
	rest *github.Client
	gql  *githubv4.Client
}

func (s *Server) newClients(instID int64) (*clients, error) {
	hc := &http.Client{
		Transport: ghinstallation.NewFromAppsTransport(s.cfg.AppsTransport, instID),
		Timeout:   30 * time.Second,
	}
	rest, err := github.NewClient(github.WithHTTPClient(hc))
	if err != nil {
		return nil, err
	}
	return &clients{rest: rest, gql: githubv4.NewClient(hc)}, nil
}

// evaluateAndMerge runs the policy against one PR and, when allowed, approves
// it and arms auto-merge (or merges directly when checks are already green).
func (s *Server) evaluateAndMerge(ctx context.Context, log *slog.Logger, c *clients, owner, name string, pr *github.PullRequest) {
	log = log.With("repo", owner+"/"+name, "pr", pr.GetNumber())

	if pr.GetDraft() || pr.GetState() != "open" {
		return
	}
	if pr.GetUser().GetLogin() != policy.DependabotLogin {
		return
	}

	cfg, err := s.repoConfig(ctx, c, owner, name, pr.GetBase().GetRef())
	if err != nil {
		// Fail closed: a present-but-broken config must not widen the policy.
		log.Error("loading .github/depsmate.yml failed; skipping PR", "err", err)
		return
	}

	commit, _, err := c.rest.Git.GetCommit(ctx, owner, name, pr.GetHead().GetSHA())
	if err != nil {
		log.Error("fetching head commit failed", "err", err)
		return
	}

	decision := policy.Evaluate(cfg, policy.PR{
		AuthorLogin:       pr.GetUser().GetLogin(),
		HeadRef:           pr.GetHead().GetRef(),
		Title:             pr.GetTitle(),
		HeadCommitMessage: commit.GetMessage(),
	})
	if !decision.Merge {
		log.Info("skipping", "reason", decision.Reason)
		return
	}
	if s.cfg.DryRun {
		log.Info("dry-run: would approve and auto-merge", "reason", decision.Reason)
		return
	}

	// A PR is re-evaluated on several triggers (PR events, check suites,
	// scans); don't stack up duplicate approval reviews.
	approved, err := s.alreadyApproved(ctx, c, owner, name, pr)
	if err != nil {
		log.Warn("listing reviews failed; approving anyway", "err", err)
	}
	if !approved {
		approve := "APPROVE"
		body := "depsmate: auto-approving — " + decision.Reason
		if _, _, err := c.rest.PullRequests.CreateReview(ctx, owner, name, pr.GetNumber(), &github.PullRequestReviewRequest{
			Event: &approve,
			Body:  &body,
		}); err != nil {
			log.Error("approving failed", "err", err)
			return
		}
	}

	queued, err := s.mergeQueueEnabled(ctx, c.gql, owner, name, pr.GetBase().GetRef())
	if err != nil {
		// Non-fatal: fall back to the auto-merge path, which also arms
		// queue entry on merge-queue branches once requirements are met.
		log.Warn("checking for a merge queue failed", "err", err)
	}
	if queued {
		s.enqueueOrArm(ctx, log, c, pr.GetNodeID(), decision.Reason)
		return
	}

	mergeMethod, err := s.detectMergeMethod(ctx, c, owner, name)
	if err != nil {
		log.Error("detecting merge method failed", "err", err)
		return
	}

	err = s.enableAutoMerge(ctx, c.gql, pr.GetNodeID(), mergeMethod)
	switch {
	case err == nil:
		log.Info("auto-merge enabled", "reason", decision.Reason)
	case strings.Contains(err.Error(), "clean status"):
		// Auto-merge can't be armed on an unblocked PR. That's either a
		// PR whose required checks already passed, or a branch with no
		// required checks at all, where a PR is "clean" the moment it
		// opens. Merge directly only with proof of verification: checks
		// exist on the head commit and all of them succeeded.
		passed, cerr := s.checksPassed(ctx, c.gql, owner, name, pr.GetHead().GetSHA())
		if cerr != nil {
			log.Error("reading status-check rollup failed; approved only, not merging", "err", cerr)
			return
		}
		if !passed {
			log.Warn("head commit has no successful check rollup; approved only, not merging")
			return
		}
		if _, _, merr := c.rest.PullRequests.Merge(ctx, owner, name, pr.GetNumber(), "", &github.PullRequestOptions{
			MergeMethod: mergeMethod,
		}); merr != nil {
			log.Error("direct merge failed", "err", merr)
			return
		}
		log.Info("merged directly", "reason", decision.Reason)
	case strings.Contains(strings.ToLower(err.Error()), "already enabled"):
		log.Info("auto-merge already enabled")
	case isWorkflowsPermissionError(err):
		log.Error(workflowsPermissionHint, "err", err)
	case strings.Contains(err.Error(), "not allowed for this repository"):
		log.Error(`auto-merge is disabled on the repository; enable "Allow auto-merge" in repo settings`, "err", err)
	default:
		log.Error("enabling auto-merge failed", "err", err)
	}
}

// detectMergeMethod picks the merge method from the repository's allowed
// methods, preferring squash, then merge, then rebase.
func (s *Server) detectMergeMethod(ctx context.Context, c *clients, owner, name string) (string, error) {
	repo, _, err := c.rest.Repositories.Get(ctx, owner, name)
	if err != nil {
		return "", err
	}
	switch {
	case repo.GetAllowSquashMerge():
		return "squash", nil
	case repo.GetAllowMergeCommit():
		return "merge", nil
	case repo.GetAllowRebaseMerge():
		return "rebase", nil
	}
	return "", fmt.Errorf("repository %s/%s allows no merge method", owner, name)
}

// repoConfig loads .github/depsmate.yml from the PR's base branch; a missing
// file means the default policy, a broken one is an error (fail closed).
func (s *Server) repoConfig(ctx context.Context, c *clients, owner, name, ref string) (policy.Config, error) {
	fc, _, resp, err := c.rest.Repositories.GetContents(ctx, owner, name, ".github/depsmate.yml", &github.RepositoryContentGetOptions{Ref: ref})
	if resp != nil && resp.StatusCode == http.StatusNotFound {
		return policy.Default(), nil
	}
	if err != nil {
		return policy.Config{}, err
	}
	content, err := fc.GetContent()
	if err != nil {
		return policy.Config{}, err
	}
	return policy.ParseConfig([]byte(content))
}

// alreadyApproved reports whether the PR's current head commit already has an
// active approving review — from depsmate or anyone else.
func (s *Server) alreadyApproved(ctx context.Context, c *clients, owner, name string, pr *github.PullRequest) (bool, error) {
	opts := &github.ListOptions{PerPage: 100}
	for {
		reviews, resp, err := c.rest.PullRequests.ListReviews(ctx, owner, name, pr.GetNumber(), opts)
		if err != nil {
			return false, err
		}
		for _, r := range reviews {
			if r.GetState() == "APPROVED" && r.GetCommitID() == pr.GetHead().GetSHA() {
				return true, nil
			}
		}
		if resp.NextPage == 0 {
			return false, nil
		}
		opts.Page = resp.NextPage
	}
}

// checksPassed reports whether the commit has a status-check rollup and it
// is SUCCESS — i.e. checks exist and every one of them passed.
func (s *Server) checksPassed(ctx context.Context, gql *githubv4.Client, owner, name, sha string) (bool, error) {
	var q struct {
		Repository struct {
			Object struct {
				Commit struct {
					StatusCheckRollup struct {
						State githubv4.String
					}
				} `graphql:"... on Commit"`
			} `graphql:"object(oid: $oid)"`
		} `graphql:"repository(owner: $owner, name: $name)"`
	}
	vars := map[string]any{
		"owner": githubv4.String(owner),
		"name":  githubv4.String(name),
		"oid":   githubv4.GitObjectID(sha),
	}
	if err := gql.Query(ctx, &q, vars); err != nil {
		return false, err
	}
	// A commit with no checks at all has a null rollup (empty state).
	return q.Repository.Object.Commit.StatusCheckRollup.State == "SUCCESS", nil
}

// mergeQueueEnabled reports whether the given base branch requires a merge
// queue, in which case PRs must be enqueued rather than auto-merged.
func (s *Server) mergeQueueEnabled(ctx context.Context, gql *githubv4.Client, owner, name, branch string) (bool, error) {
	var q struct {
		Repository struct {
			MergeQueue struct {
				ID githubv4.ID
			} `graphql:"mergeQueue(branch: $branch)"`
		} `graphql:"repository(owner: $owner, name: $name)"`
	}
	vars := map[string]any{
		"owner":  githubv4.String(owner),
		"name":   githubv4.String(name),
		"branch": githubv4.String(branch),
	}
	if err := gql.Query(ctx, &q, vars); err != nil {
		return false, err
	}
	return q.Repository.MergeQueue.ID != nil, nil
}

// GitHub refuses any App merge of a PR that touches .github/workflows files
// unless the App holds the Workflows read & write permission — common for
// github-actions bumps. Retrying or falling back cannot help; only granting
// the permission (and approving it on the installation) does.
const workflowsPermissionHint = `app lacks the "Workflows" read & write permission required to merge PRs touching .github/workflows; grant it in the App settings and approve the update on the installation`

func isWorkflowsPermissionError(err error) bool {
	return err != nil && strings.Contains(err.Error(), "without `workflows` permission")
}

// enqueueOrArm adds the PR to the base branch's merge queue; when it isn't
// eligible yet (e.g. checks still pending), it arms auto-merge instead, which
// on merge-queue branches enqueues the PR once requirements are met.
func (s *Server) enqueueOrArm(ctx context.Context, log *slog.Logger, c *clients, prNodeID, reason string) {
	var m struct {
		EnqueuePullRequest struct {
			MergeQueueEntry struct {
				ID githubv4.ID
			}
		} `graphql:"enqueuePullRequest(input: $input)"`
	}
	err := c.gql.Mutate(ctx, &m, githubv4.EnqueuePullRequestInput{
		PullRequestID: githubv4.ID(prNodeID),
	}, nil)
	switch {
	case err == nil:
		log.Info("added to merge queue", "reason", reason)
	case strings.Contains(strings.ToLower(err.Error()), "already"):
		log.Info("already in merge queue")
	case isWorkflowsPermissionError(err):
		// Arming auto-merge would hit the same refusal; don't bother.
		log.Error(workflowsPermissionHint, "err", err)
	default:
		log.Info("enqueue not possible yet; arming auto-merge", "err", err)
		// Merge method is the queue's own on merge-queue branches; GitHub
		// ignores any value passed here, so pass none.
		if aerr := s.enableAutoMerge(ctx, c.gql, prNodeID, ""); aerr != nil {
			log.Error("arming auto-merge for merge queue failed", "err", aerr)
			return
		}
		log.Info("auto-merge armed for merge queue", "reason", reason)
	}
}

// enableAutoMerge arms GitHub auto-merge; an empty method lets GitHub pick
// (required on merge-queue branches, where the queue's method applies).
func (s *Server) enableAutoMerge(ctx context.Context, gql *githubv4.Client, prNodeID, mergeMethod string) error {
	var m struct {
		EnablePullRequestAutoMerge struct {
			PullRequest struct {
				Number githubv4.Int
			}
		} `graphql:"enablePullRequestAutoMerge(input: $input)"`
	}
	input := githubv4.EnablePullRequestAutoMergeInput{
		PullRequestID: githubv4.ID(prNodeID),
	}
	if mergeMethod != "" {
		method := githubv4.PullRequestMergeMethod(strings.ToUpper(mergeMethod))
		input.MergeMethod = &method
	}
	return gql.Mutate(ctx, &m, input, nil)
}
