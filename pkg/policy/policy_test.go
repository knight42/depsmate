package policy

import "testing"

const minorBumpCommit = `Bumps [github.com/foo/bar](https://github.com/foo/bar) from 1.2.3 to 1.3.0.

---
updated-dependencies:
- dependency-name: github.com/foo/bar
  dependency-version: 1.3.0
  dependency-type: direct:production
  update-type: version-update:semver-minor
...
`

const majorBumpCommit = `Bumps [react](https://github.com/facebook/react) from 19.1.0 to 20.0.0.

---
updated-dependencies:
- dependency-name: react
  update-type: version-update:semver-major
...
`

const groupedMinorPatchCommit = `Bumps the aws group with 2 updates.

---
updated-dependencies:
- dependency-name: github.com/aws/aws-sdk-go-v2
  update-type: version-update:semver-minor
- dependency-name: github.com/aws/smithy-go
  update-type: version-update:semver-patch
...
`

const groupedWithMajorCommit = `Bumps the tooling group with 2 updates.

---
updated-dependencies:
- dependency-name: left-pad
  update-type: version-update:semver-patch
- dependency-name: webpack
  update-type: version-update:semver-major
...
`

func TestEvaluate(t *testing.T) {
	tests := map[string]struct {
		config string // depsmate.yml content; empty = defaults
		pr     PR
		want   bool
	}{
		"non-dependabot author denied": {
			pr: PR{
				AuthorLogin: "jianzeng",
				HeadRef:     "dependabot/go_modules/github.com/foo/bar-1.3.0",
				Title:       "chore: bump github.com/foo/bar from 1.2.3 to 1.3.0",
			},
			want: false,
		},
		"non-dependabot branch denied": {
			pr: PR{
				AuthorLogin: "dependabot[bot]",
				HeadRef:     "feature/manual-bump",
				Title:       "bump foo from 1.2.3 to 1.3.0",
			},
			want: false,
		},
		"github actions major bump allowed": {
			pr: PR{
				AuthorLogin: "dependabot[bot]",
				HeadRef:     "dependabot/github_actions/actions/checkout-5",
				Title:       "ci: bump actions/checkout from 4 to 5",
			},
			want: true,
		},
		"library minor bump allowed": {
			pr: PR{
				AuthorLogin:       "dependabot[bot]",
				HeadRef:           "dependabot/go_modules/github.com/foo/bar-1.3.0",
				Title:             "chore: bump github.com/foo/bar from 1.2.3 to 1.3.0",
				HeadCommitMessage: minorBumpCommit,
			},
			want: true,
		},
		"library major bump denied": {
			pr: PR{
				AuthorLogin:       "dependabot[bot]",
				HeadRef:           "dependabot/npm_and_yarn/react-20.0.0",
				Title:             "chore: bump react from 19.1.0 to 20.0.0",
				HeadCommitMessage: majorBumpCommit,
			},
			want: false,
		},
		"grouped minor and patch allowed": {
			pr: PR{
				AuthorLogin:       "dependabot[bot]",
				HeadRef:           "dependabot/go_modules/aws-1b2c3d",
				Title:             "chore: bump the aws group with 2 updates",
				HeadCommitMessage: groupedMinorPatchCommit,
			},
			want: true,
		},
		"grouped containing major denied": {
			pr: PR{
				AuthorLogin:       "dependabot[bot]",
				HeadRef:           "dependabot/npm_and_yarn/tooling-4e5f6a",
				Title:             "chore: bump the tooling group with 2 updates",
				HeadCommitMessage: groupedWithMajorCommit,
			},
			want: false,
		},
		"no metadata, title within same major allowed": {
			pr: PR{
				AuthorLogin: "dependabot[bot]",
				HeadRef:     "dependabot/pip/requests-2.32.5",
				Title:       "build: bump requests from 2.32.4 to 2.32.5",
			},
			want: true,
		},
		"no metadata, title crossing major denied": {
			pr: PR{
				AuthorLogin: "dependabot[bot]",
				HeadRef:     "dependabot/pip/numpy-3.0.0",
				Title:       "build: bump numpy from v2.3.1 to v3.0.0",
			},
			want: false,
		},
		"no metadata, grouped title denied": {
			pr: PR{
				AuthorLogin: "dependabot[bot]",
				HeadRef:     "dependabot/npm_and_yarn/tooling-4e5f6a",
				Title:       "chore: bump the tooling group with 3 updates",
			},
			want: false,
		},
		"config disabled denies everything": {
			config: "enabled: false\n",
			pr: PR{
				AuthorLogin:       "dependabot[bot]",
				HeadRef:           "dependabot/go_modules/github.com/foo/bar-1.3.0",
				Title:             "chore: bump github.com/foo/bar from 1.2.3 to 1.3.0",
				HeadCommitMessage: minorBumpCommit,
			},
			want: false,
		},
		"config wildcard patch denies minor": {
			config: "ecosystems:\n  \"*\": patch\n",
			pr: PR{
				AuthorLogin:       "dependabot[bot]",
				HeadRef:           "dependabot/go_modules/github.com/foo/bar-1.3.0",
				Title:             "chore: bump github.com/foo/bar from 1.2.3 to 1.3.0",
				HeadCommitMessage: minorBumpCommit,
			},
			want: false,
		},
		"config ecosystem major allows major": {
			config: "ecosystems:\n  npm_and_yarn: major\n",
			pr: PR{
				AuthorLogin:       "dependabot[bot]",
				HeadRef:           "dependabot/npm_and_yarn/react-20.0.0",
				Title:             "chore: bump react from 19.1.0 to 20.0.0",
				HeadCommitMessage: majorBumpCommit,
			},
			want: true,
		},
		"config wildcard merges over default, actions keep major": {
			config: "ecosystems:\n  \"*\": patch\n",
			pr: PR{
				AuthorLogin: "dependabot[bot]",
				HeadRef:     "dependabot/github_actions/actions/checkout-5",
				Title:       "ci: bump actions/checkout from 4 to 5",
			},
			want: true,
		},
		"config ecosystem override caps actions at minor": {
			config: "ecosystems:\n  github_actions: minor\n",
			pr: PR{
				AuthorLogin: "dependabot[bot]",
				HeadRef:     "dependabot/github_actions/actions/checkout-5",
				Title:       "ci: bump actions/checkout from 4 to 5",
			},
			want: false,
		},
		"config ignore glob denies matching dependency": {
			config: "ignore:\n  - github.com/aws/*\n",
			pr: PR{
				AuthorLogin:       "dependabot[bot]",
				HeadRef:           "dependabot/go_modules/aws-1b2c3d",
				Title:             "chore: bump the aws group with 2 updates",
				HeadCommitMessage: groupedMinorPatchCommit,
			},
			want: false,
		},
		"config ignore exact name denies": {
			config: "ignore:\n  - github.com/foo/bar\n",
			pr: PR{
				AuthorLogin:       "dependabot[bot]",
				HeadRef:           "dependabot/go_modules/github.com/foo/bar-1.3.0",
				Title:             "chore: bump github.com/foo/bar from 1.2.3 to 1.3.0",
				HeadCommitMessage: minorBumpCommit,
			},
			want: false,
		},
	}

	for name, tc := range tests {
		t.Run(name, func(t *testing.T) {
			cfg := Default()
			if tc.config != "" {
				var err error
				cfg, err = ParseConfig([]byte(tc.config))
				if err != nil {
					t.Fatalf("ParseConfig() error: %v", err)
				}
			}
			got := Evaluate(cfg, tc.pr)
			if got.Merge != tc.want {
				t.Errorf("Evaluate() merge = %v, want %v (reason: %s)", got.Merge, tc.want, got.Reason)
			}
			if got.Reason == "" {
				t.Error("Evaluate() returned an empty reason")
			}
		})
	}
}

func TestParseConfigRejectsInvalid(t *testing.T) {
	tests := map[string]string{
		"unknown key":        "max_update_type: minor\n",
		"bad ecosystem type": "ecosystems:\n  pip: everything\n",
		"not yaml":           ":\n  - {",
	}
	for name, content := range tests {
		t.Run(name, func(t *testing.T) {
			if _, err := ParseConfig([]byte(content)); err == nil {
				t.Errorf("ParseConfig(%q) succeeded, want error", content)
			}
		})
	}
}
