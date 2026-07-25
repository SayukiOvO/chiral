import { Suspense, lazy, useMemo } from "react";
import { cn } from "../lib/cn";

// Monaco is ~4 MB; the node dashboard never opens an editor, so it loads only
// when one is actually rendered. Nothing here may import ./MonacoEditor
// statically — a single static import pulls all of Monaco into the entry
// chunk and silently undoes this.
const MonacoEditor = lazy(() => import("./MonacoEditor"));

function EditorSkeleton() {
  return (
    <div className="grid h-full w-full place-items-center bg-surface text-xs text-faint">
      载入编辑器…
    </div>
  );
}

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
}

/** Mirrors the engine's rules so the editor flags what the server would reject. */
export function checkRefs(
  template: string,
  known: Set<string>,
  secrets: Set<string>,
  clientSide: boolean,
): VarProblem[] {
  const out: VarProblem[] = [];
  for (const name of refs(template)) {
    if (clientSide && secrets.has(name)) {
      out.push({ name, reason: "私钥变量不能用在客户端模板里" });
    } else if (!known.has(name)) {
      out.push({ name, reason: "未定义的变量" });
    }
  }
  return out;
}

export function TemplateEditor({
  value,
  onChange,
  known,
  secrets,
  clientSide = false,
  height = 320,
  dark,
}: {
  value: string;
  onChange: (v: string) => void;
  known: Set<string>;
  secrets?: Set<string>;
  clientSide?: boolean;
  height?: number;
  dark: boolean;
}) {
  const secretSet = useMemo(() => secrets ?? new Set<string>(), [secrets]);
  const problems = useMemo(
    () => checkRefs(value, known, secretSet, clientSide),
    [value, known, secretSet, clientSide],
  );

  return (
    <div>
      <div className="overflow-hidden rounded-xl border border-line" style={{ height }}>
        <Suspense fallback={<EditorSkeleton />}>
          <MonacoEditor
            value={value}
            onChange={onChange}
            problems={problems}
            height={height}
            dark={dark}
          />
        </Suspense>
      </div>
      <VarChips template={value} known={known} secrets={secretSet} problems={problems} />
    </div>
  );
}

/** The variables this template uses, and whether each one resolves. */
function VarChips({
  template,
  known,
  secrets,
  problems,
}: {
  template: string;
  known: Set<string>;
  secrets: Set<string>;
  problems: VarProblem[];
}) {
  const used = refs(template);
  if (used.length === 0) return null;
  const bad = new Map(problems.map((p) => [p.name, p.reason]));

  return (
    <div className="mt-2.5 flex flex-wrap items-center gap-1.5">
      {used.map((name) => {
        const reason = bad.get(name);
        return (
          <span
            key={name}
            title={reason ?? (secrets.has(name) ? "私钥变量（仅服务端）" : "已定义")}
            className={cn(
              "inline-flex items-center gap-1 rounded-md px-1.5 py-0.5 font-mono text-[11px]",
              reason ? "text-danger" : known.has(name) ? "text-muted" : "text-faint",
            )}
            style={{
              background: reason
                ? "color-mix(in srgb, var(--danger) 12%, transparent)"
                : "color-mix(in srgb, var(--muted) 10%, transparent)",
            }}
          >
            {secrets.has(name) && <LockGlyph />}
            {name}
          </span>
        );
      })}
    </div>
  );
}

function LockGlyph() {
  return (
    <svg width="9" height="9" viewBox="0 0 12 12" fill="none" aria-hidden="true">
      <rect x="2.5" y="5.5" width="7" height="5" rx="1" fill="currentColor" />
      <path d="M4 5.5V4a2 2 0 1 1 4 0v1.5" stroke="currentColor" strokeWidth="1.2" />
    </svg>
  );
}
