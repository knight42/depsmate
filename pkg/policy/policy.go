// Package policy decides whether a dependabot PR is safe to auto-merge.
package policy

import (
	"fmt"
	"path"
	"regexp"
	"strings"

	"sigs.k8s.io/yaml"
)

// DependabotLogin is the GitHub login dependabot authors PRs under.
const DependabotLogin = "dependabot[bot]"

// Config is the per-repo policy, read from .github/depsmate.yml on the PR's
// base branch. Zero-value fields fall back to the defaults in Default().
type Config struct {
	// Enabled turns depsmate off for the repo when set to false.
	Enabled *bool `json:"enabled"`
	// Ecosystems sets the largest semver bump auto-merged ("patch",
	// "minor", or "major") per dependabot ecosystem (the branch-name
	// form: github_actions, go_modules, npm_and_yarn, pip, ...), with
	// "*" as the fallback for unlisted ecosystems. Entries are merged
	// over the defaults {github_actions: major, "*": minor}, so listing
	// one ecosystem leaves the others at their defaults.
	Ecosystems map[string]string `json:"ecosystems"`
	// Ignore lists dependency-name globs (path.Match syntax) that are
	// never auto-merged.
	Ignore []string `json:"ignore"`
}

// Default returns the built-in policy: minor/patch library bumps, any
// github-actions bump.
func Default() Config {
	enabled := true
	return Config{
		Enabled:    &enabled,
		Ecosystems: map[string]string{"github_actions": "major", "*": "minor"},
	}
}

// ParseConfig parses .github/depsmate.yml and merges it over Default(),
// rejecting invalid values.
func ParseConfig(data []byte) (Config, error) {
	var user Config
	// Strict: unknown (typoed) keys are an error, so they fail closed
	// instead of silently applying defaults.
	if err := yaml.UnmarshalStrict(data, &user); err != nil {
		return Config{}, fmt.Errorf("parsing depsmate.yml: %w", err)
	}
	cfg := Default()
	if user.Enabled != nil {
		cfg.Enabled = user.Enabled
	}
	for eco, ut := range user.Ecosystems {
		if _, ok := updateTypeRank[ut]; !ok {
			return Config{}, fmt.Errorf("invalid update type %q for ecosystem %q", ut, eco)
		}
		cfg.Ecosystems[eco] = ut
	}
	cfg.Ignore = user.Ignore
	return cfg, nil
}

// PR carries the pull-request fields the merge policy inspects.
type PR struct {
	AuthorLogin string
	HeadRef     string // e.g. "dependabot/go_modules/github.com/foo/bar-1.2.3"
	Title       string
	// HeadCommitMessage is the full message of the PR head commit, where
	// dependabot embeds an updated-dependencies YAML block with
	// "dependency-name:" and "update-type: version-update:semver-*" lines
	// per bumped dependency.
	HeadCommitMessage string
}

// Decision is the policy outcome for one PR.
type Decision struct {
	Merge  bool
	Reason string
}

// Update is one bumped dependency extracted from dependabot metadata.
type Update struct {
	Name string
	Type string // "major", "minor", or "patch"
}

var updateTypeRank = map[string]int{"patch": 1, "minor": 2, "major": 3}

var (
	depNameRe    = regexp.MustCompile(`(?m)^\s*-\s*dependency-name:\s*"?([^"\s]+)"?\s*$`)
	updateTypeRe = regexp.MustCompile(`(?m)^\s*update-type:\s*version-update:semver-(major|minor|patch)\s*$`)
	titleBumpRe  = regexp.MustCompile(`(?i)\bbumps? (\S+) from v?(\S+) to v?(\S+)`)
)

// Evaluate applies cfg to one dependabot PR: every updated dependency must be
// outside the ignore list and within the configured max update type for the
// PR's ecosystem.
func Evaluate(cfg Config, pr PR) Decision {
	if cfg.Enabled != nil && !*cfg.Enabled {
		return Decision{Reason: "disabled by .github/depsmate.yml"}
	}
	if pr.AuthorLogin != DependabotLogin {
		return Decision{Reason: fmt.Sprintf("author %q is not %s", pr.AuthorLogin, DependabotLogin)}
	}

	eco := ecosystem(pr.HeadRef)
	if eco == "" {
		return Decision{Reason: fmt.Sprintf("head ref %q is not a dependabot branch", pr.HeadRef)}
	}
	maxType, ok := cfg.Ecosystems[eco]
	if !ok {
		// An absent "*" ranks as 0, denying everything: fail closed.
		maxType = cfg.Ecosystems["*"]
	}

	updates, err := parseUpdates(pr.HeadCommitMessage)
	if err != nil {
		return Decision{Reason: err.Error()}
	}
	if len(updates) == 0 {
		u, ok := updateFromTitle(pr.Title)
		if !ok {
			return Decision{Reason: fmt.Sprintf("%s bump has no update-type metadata and title is not a single \"bump X from A to B\"", eco)}
		}
		updates = []Update{u}
	}

	for _, u := range updates {
		if ignored(cfg.Ignore, u.Name) {
			return Decision{Reason: fmt.Sprintf("dependency %s is in the ignore list", u.Name)}
		}
		if updateTypeRank[u.Type] > updateTypeRank[maxType] {
			return Decision{Reason: fmt.Sprintf("%s bump of %s is semver-%s, above the allowed semver-%s", eco, u.Name, u.Type, maxType)}
		}
	}
	return Decision{Merge: true, Reason: fmt.Sprintf("%s bump, all %d update(s) within semver-%s", eco, len(updates), maxType)}
}

// ecosystem extracts the package ecosystem from a dependabot branch name
// ("dependabot/<ecosystem>/..."); empty when the branch is not dependabot's.
func ecosystem(headRef string) string {
	rest, ok := strings.CutPrefix(headRef, "dependabot/")
	if !ok {
		return ""
	}
	eco, _, _ := strings.Cut(rest, "/")
	return eco
}

// parseUpdates extracts the updated-dependencies metadata from the head
// commit message. A name count that disagrees with the update-type count
// means some dependency's bump kind is unknown — fail closed.
func parseUpdates(commitMessage string) ([]Update, error) {
	names := depNameRe.FindAllStringSubmatch(commitMessage, -1)
	types := updateTypeRe.FindAllStringSubmatch(commitMessage, -1)
	if len(names) == 0 && len(types) == 0 {
		return nil, nil
	}
	if len(names) != len(types) {
		return nil, fmt.Errorf("commit metadata lists %d dependencies but %d update types", len(names), len(types))
	}
	updates := make([]Update, len(names))
	for i := range names {
		updates[i] = Update{Name: names[i][1], Type: types[i][1]}
	}
	return updates, nil
}

// updateFromTitle handles PRs whose head commit carries no metadata: it
// accepts only a single-dependency "bump X from A to B" title and derives the
// update type by comparing the two versions.
func updateFromTitle(title string) (Update, bool) {
	m := titleBumpRe.FindAllStringSubmatch(title, -1)
	if len(m) != 1 {
		return Update{}, false
	}
	return Update{Name: m[0][1], Type: compareVersions(m[0][2], m[0][3])}, true
}

// compareVersions classifies a version transition by the first differing
// dot-separated component.
func compareVersions(from, to string) string {
	f := strings.Split(from, ".")
	t := strings.Split(to, ".")
	if f[0] != t[0] {
		return "major"
	}
	if len(f) > 1 && len(t) > 1 && f[1] != t[1] {
		return "minor"
	}
	return "patch"
}

func ignored(patterns []string, name string) bool {
	for _, p := range patterns {
		if ok, err := path.Match(p, name); err == nil && ok {
			return true
		}
	}
	return false
}
