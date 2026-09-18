import { describe, expect, it } from "vitest";
import { defaultConfig, evaluate, parseConfig } from "../src/policy";
import cases from "./policy-cases.json";

describe("policy parity with Go", () => {
  for (const c of cases) it(c.name, () => {
    const result = evaluate(parseConfig(c.config), c.pr);
    expect(result.merge).toBe(c.want);
    expect(result.reason).not.toBe("");
  });
});
it.each(["max_update_type: minor", "ecosystems:\n  pip: everything", ":\n  - {", "enabled: yes", "ignore: foo", "ecosystems: []", "enabled: true\nenabled: false", "ecosystems:\n  '*': constructor"])("rejects invalid config %s", text => {
  expect(() => parseConfig(text)).toThrow();
});
it("rejects incomplete grouped metadata without title fallback", () => {
  expect(evaluate(defaultConfig(), {
    authorLogin: "dependabot[bot]", headRef: "dependabot/npm_and_yarn/group", title: "Bump a from 1.0 to 1.1",
    headCommitMessage: "- dependency-name: a\n  update-type: version-update:semver-minor\n- dependency-name: b",
  }).merge).toBe(false);
});
it("denies an ecosystem without a fallback", () => {
  const config = defaultConfig(); config.ecosystems = {};
  expect(evaluate(config, { authorLogin: "dependabot[bot]", headRef: "dependabot/npm_and_yarn/foo", title: "Bump foo from 1.0 to 1.1" }).merge).toBe(false);
});

const securityPR = {
  authorLogin: "dependabot[bot]",
  headRef: "dependabot/go_modules/golang.org/x/net-0.55.0",
  title: "build(deps): bump golang.org/x/net from 0.49.0 to 0.55.0",
  headCommitMessage: "---\nupdated-dependencies:\n- dependency-name: golang.org/x/net\n  dependency-version: 0.55.0\n  dependency-type: indirect\n...",
};
it("accepts security update metadata without update-type", () => {
  expect(evaluate(defaultConfig(), securityPR).merge).toBe(true);
});
it.each([
  "bump another/package from 0.49.0 to 0.55.0",
  "bump golang.org/x/net from unknown to unknown",
  "bump golang.org/x/net from 0.49.0-rc.1 to 0.55.0",
  "bump golang.org/x/net from 0.49 to 0.55.0",
  "bump the security group with 2 updates",
  "bump golang.org/x/net from 0.49.0 to 0.55.0 and bump other from 1 to 2",
  "bump golang.org/x/net from 0.49.0 to 1.0.0",
])("denies ambiguous or disallowed security update: %s", title => {
  expect(evaluate(defaultConfig(), { ...securityPR, title }).merge).toBe(false);
});
it.each([
  "ecosystems:\n  '*': patch",
  "ignore:\n  - golang.org/x/net",
  "enabled: false",
])("applies policy to inferred security updates: %s", config => {
  expect(evaluate(parseConfig(config), securityPR).merge).toBe(false);
});
it.each([
  "\n- dependency-name: other/package",
  "\n  update-type: unknown",
])("does not infer grouped or unrecognized metadata: %s", suffix => {
  expect(evaluate(defaultConfig(), { ...securityPR, headCommitMessage: securityPR.headCommitMessage + suffix }).merge).toBe(false);
});
