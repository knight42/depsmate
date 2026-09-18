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
it("rejects untyped non-indirect entries in a group", () => {
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
it("accepts indirect metadata without update-type", () => {
  expect(evaluate(defaultConfig(), securityPR).merge).toBe(true);
});
it("allows indirect updates regardless of title or version cap", () => {
  expect(evaluate(parseConfig("ecosystems:\n  '*': patch"), {
    ...securityPR, title: "security update with no version information",
  }).merge).toBe(true);
});
it.each(["ignore:\n  - golang.org/x/net", "enabled: false"])("still applies %s", config => {
  expect(evaluate(parseConfig(config), securityPR).merge).toBe(false);
});
it.each(["unknown", "", "version-update:semver-major"])("allows indirect updates with update-type %s", type => {
  expect(evaluate(defaultConfig(), { ...securityPR,
    headCommitMessage: securityPR.headCommitMessage + `\n  update-type: ${type}`,
  }).merge).toBe(true);
});
it.each(["before", "after"])("checks typed entries %s an untyped entry", order => {
  const typed = "- dependency-name: other\n  update-type: version-update:semver-major\n";
  const untyped = "- dependency-name: golang.org/x/net\n  dependency-type: indirect\n";
  expect(evaluate(defaultConfig(), { ...securityPR,
    headCommitMessage: order === "before" ? typed + untyped : untyped + typed,
  }).merge).toBe(false);
});
it("applies ignore rules to every untyped entry in a group", () => {
  expect(evaluate(parseConfig("ignore: [other]"), { ...securityPR,
    headCommitMessage: "- dependency-name: golang.org/x/net\n  dependency-type: indirect\n- dependency-name: other\n  dependency-type: indirect\n",
  }).merge).toBe(false);
});

it.each(["direct:production", "direct:development", "unknown", ""])("requires update-type for %s dependencies", dependencyType => {
  expect(evaluate(defaultConfig(), { ...securityPR,
    headCommitMessage: securityPR.headCommitMessage.replace("dependency-type: indirect", `dependency-type: ${dependencyType}`),
  }).merge).toBe(false);
});
it("allows a mixed group of indirect and direct minor updates", () => {
  expect(evaluate(defaultConfig(), { ...securityPR,
    headCommitMessage: "- dependency-name: indirect/package\n  dependency-type: indirect\n- dependency-name: direct/package\n  dependency-type: direct:production\n  update-type: version-update:semver-minor\n",
  }).merge).toBe(true);
});
