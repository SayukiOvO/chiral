/**
 * The editor's copy of the engine's reference rules.
 *
 * Pure, and deliberately in lib/ rather than beside the editor component:
 * these are the rules the tests pin down, and a test for them should not have
 * to drag React, Monaco and the browser's storage in behind it.
 */

/** Variable references a template uses, in order of first appearance. */
export function refs(template: string): string[] {
  const seen = new Set<string>();
  const out: string[] = [];
  for (const m of template.matchAll(/\{\{\s*([a-zA-Z0-9_.]+)\s*\}\}/g)) {
    if (!seen.has(m[1])) {
      seen.add(m[1]);
      out.push(m[1]);
    }
  }
  return out;
}

export interface VarProblem {
  name: string;
  reason: string;
  /** Nodes that do not define this one, for a node-scoped variable. */
  missingOn?: string[];
}

/**
 * Mirrors the engine's rules so the editor flags what the server would reject.
 *
 * `partial` carries node-scoped variables that some bound nodes define and
 * others do not. Those are neither undefined nor fine: the template renders
 * once per node, so it works for the nodes that have the variable and fails
 * for the rest. Reported as their own kind of problem, naming the nodes.
 */
export function checkRefs(
  template: string,
  known: Set<string>,
  secrets: Set<string>,
  clientSide: boolean,
  partial?: Map<string, string[]>,
): VarProblem[] {
  const out: VarProblem[] = [];
  for (const name of refs(template)) {
    const missingOn = partial?.get(name);
    if (clientSide && secrets.has(name)) {
      out.push({ name, reason: "私钥变量不能用在客户端模板里" });
    } else if (missingOn && missingOn.length > 0) {
      out.push({ name, reason: "这些节点上没有定义", missingOn });
    } else if (!known.has(name)) {
      out.push({ name, reason: "未定义的变量" });
    }
  }
  return out;
}
