import { describe, expect, it } from "vitest";
import { checkRefs, refs } from "./template";

/**
 * These rules must stay in step with the Go engine
 * (core/internal/template/engine.go). If they drift, the editor tells the
 * operator a template is fine and the server then refuses it — or worse,
 * stays quiet about a secret leaking into a client template.
 */
describe("refs", () => {
  it("finds each variable once, in order", () => {
    expect(refs(`{{a}} {{ b }} {{a}} literal {{c.d}}`)).toEqual(["a", "b", "c.d"]);
  });

  it("accepts the whitespace the engine accepts", () => {
    expect(refs("{{ sni }}")).toEqual(["sni"]);
    expect(refs("{{sni}}")).toEqual(["sni"]);
  });

  it("ignores things that are not references", () => {
    expect(refs(`{ "a": 1 }`)).toEqual([]);
    expect(refs("{{ not-a-name }}")).toEqual([]); // '-' is not a legal char
    expect(refs("{{}}")).toEqual([]);
  });

  it("finds references inside strings", () => {
    expect(refs(`"target": "{{sni}}:443"`)).toEqual(["sni"]);
  });
});

describe("checkRefs", () => {
  const known = new Set(["sni", "reality.private", "reality.public"]);
  const secrets = new Set(["reality.private"]);

  it("passes a template that only uses known variables", () => {
    expect(checkRefs(`"{{sni}}"`, known, secrets, false)).toEqual([]);
  });

  it("flags an undefined variable", () => {
    const [p] = checkRefs(`"{{nope}}"`, known, secrets, false);
    expect(p.name).toBe("nope");
  });

  it("allows a secret in a server template", () => {
    expect(checkRefs(`"{{reality.private}}"`, known, secrets, false)).toEqual([]);
  });

  it("flags a secret in a client template", () => {
    const [p] = checkRefs(`"{{reality.private}}"`, known, secrets, true);
    expect(p.name).toBe("reality.private");
    expect(p.reason).toContain("私钥");
  });

  it("allows the public half in a client template", () => {
    expect(checkRefs(`"{{reality.public}}"`, known, secrets, true)).toEqual([]);
  });

  // A node-scoped variable is defined per node on purpose. Reporting it as
  // undefined made the one mechanism for per-node values look like a mistake.
  it("accepts a node-scoped variable every bound node defines", () => {
    expect(checkRefs(`"{{addr}}"`, new Set([...known, "addr"]), secrets, true)).toEqual([]);
  });

  it("names the nodes when only some of them define it", () => {
    const partial = new Map([["addr", ["tokyo-02", "osaka-01"]]]);
    const [p] = checkRefs(`"{{addr}}"`, known, secrets, true, partial);
    expect(p.name).toBe("addr");
    expect(p.missingOn).toEqual(["tokyo-02", "osaka-01"]);
    expect(p.reason).not.toContain("未定义");
  });

  // The secret rule outranks it: a private key in a client template is wrong
  // on every node, and saying "missing on tokyo-02" would read as fixable.
  it("still refuses a secret in a client template when it is also partial", () => {
    const partial = new Map([["reality.private", ["tokyo-02"]]]);
    const [p] = checkRefs(`"{{reality.private}}"`, known, secrets, true, partial);
    expect(p.reason).toContain("私钥");
  });

  it("reports every problem, not just the first", () => {
    const problems = checkRefs(
      `{{nope}} {{reality.private}} {{alsoNope}}`,
      known,
      secrets,
      true,
    );
    expect(problems.map((p) => p.name)).toEqual([
      "nope",
      "reality.private",
      "alsoNope",
    ]);
  });

  it("reports a secret once even when referenced twice", () => {
    const problems = checkRefs(
      `{{reality.private}} {{reality.private}}`,
      known,
      secrets,
      true,
    );
    expect(problems).toHaveLength(1);
  });
});
