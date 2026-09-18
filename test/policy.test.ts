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
