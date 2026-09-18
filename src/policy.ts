import { parseDocument } from "yaml";
import { minimatch } from "minimatch";

export const dependabotLogin = "dependabot[bot]";
const ranks = { patch: 1, minor: 2, major: 3 } as const;
type UpdateType = keyof typeof ranks;
export interface Config {
  enabled: boolean;
  ecosystems: Record<string, UpdateType>;
  ignore: string[];
}
export interface PolicyPR {
  authorLogin: string;
  headRef: string;
  title: string;
  headCommitMessage?: string;
}
export interface Decision { merge: boolean; reason: string }

export function defaultConfig(): Config {
  return { enabled: true, ecosystems: { github_actions: "major", "*": "minor" }, ignore: [] };
}
function object(value: unknown): value is Record<string, unknown> {
  return typeof value === "object" && value !== null && !Array.isArray(value);
}
export function parseConfig(text: string): Config {
  const doc = parseDocument(text, { uniqueKeys: true });
  if (doc.errors.length) throw new Error(`parsing depsmate.yml: ${doc.errors[0].message}`);
  const user: unknown = doc.toJS({ maxAliasCount: 100 });
  const cfg = defaultConfig();
  if (user === null) return cfg;
  if (!object(user)) throw new Error("config must be a mapping");
  for (const key of Object.keys(user)) {
    if (!["enabled", "ecosystems", "ignore"].includes(key)) throw new Error(`unknown config key: ${key}`);
  }
  if (user.enabled != null) {
    if (typeof user.enabled !== "boolean") throw new Error("enabled must be boolean");
    cfg.enabled = user.enabled;
  }
  if (user.ecosystems != null) {
    if (!object(user.ecosystems)) throw new Error("ecosystems must be a mapping");
    for (const [eco, type] of Object.entries(user.ecosystems)) {
      if (typeof type !== "string" || !Object.hasOwn(ranks, type)) throw new Error(`invalid update type for ${eco}`);
      Object.defineProperty(cfg.ecosystems, eco, { value: type, enumerable: true, configurable: true, writable: true });
    }
  }
  if (user.ignore != null) {
    if (!Array.isArray(user.ignore) || !user.ignore.every(v => typeof v === "string")) throw new Error("ignore must be a list of strings");
    cfg.ignore = user.ignore;
  }
  return cfg;
}

export function evaluate(cfg: Config, pr: PolicyPR): Decision {
  const deny = (reason: string): Decision => ({ merge: false, reason });
  if (!cfg.enabled) return deny("disabled by .github/depsmate.yml");
  if (pr.authorLogin !== dependabotLogin) return deny(`author ${pr.authorLogin} is not ${dependabotLogin}`);
  const eco = /^dependabot\/([^/]+)/.exec(pr.headRef)?.[1];
  if (!eco) return deny(`head ref ${pr.headRef} is not a dependabot branch`);
  const maxType = Object.hasOwn(cfg.ecosystems, eco) ? cfg.ecosystems[eco] : cfg.ecosystems["*"];
  const maxRank = ranks[maxType] ?? 0;
  const message = pr.headCommitMessage ?? "";
  const names = [...message.matchAll(/^\s*-\s*dependency-name:\s*"?([^"\s]+)"?\s*$/gm)].map(m => m[1]);
  const types = [...message.matchAll(/^\s*update-type:\s*version-update:semver-(major|minor|patch)\s*$/gm)].map(m => m[1] as UpdateType);
  if (names.length !== types.length) return deny(`commit metadata lists ${names.length} dependencies but ${types.length} update types`);
  if (!names.length) {
    const bumps = [...pr.title.matchAll(/\bbumps? (\S+) from v?(\S+) to v?(\S+)/gi)];
    if (bumps.length !== 1) return deny(`${eco} bump has no metadata or single-dependency title`);
    const [, name, from, to] = bumps[0];
    const f = from.split("."), t = to.split(".");
    names.push(name);
    types.push(f[0] !== t[0] ? "major" : f.length > 1 && t.length > 1 && f[1] !== t[1] ? "minor" : "patch");
  }
  for (let i = 0; i < names.length; i++) {
    // Match a whole dependency name; wildcards do not cross path separators.
    if (cfg.ignore.some(pattern => minimatch(names[i], pattern, {
      dot: true, noglobstar: true, noext: true, nobrace: true, nonegate: true, nocomment: true,
    }))) return deny(`dependency ${names[i]} is in the ignore list`);
    if (ranks[types[i]] > maxRank) return deny(`${eco} bump of ${names[i]} is semver-${types[i]}, above allowed semver-${maxType}`);
  }
  return { merge: true, reason: `${eco} bump, all ${names.length} update(s) within semver-${maxType}` };
}
