import { cn } from "../lib/cn";

const LABEL: Record<string, string> = {
  RUNNING: "运行中",
  STOPPED: "已停止",
  ERROR: "异常",
};

/** Xray-core kernel state + version. Running carries the signal accent. */
export function KernelState({ state, version }: { state?: string; version?: string }) {
  if (!state || state === "UNSPECIFIED") {
    return <span className="text-faint text-sm">—</span>;
  }
  const running = state === "RUNNING";
  const error = state === "ERROR";

  return (
    <span className="inline-flex items-center gap-2">
      <span
        className={cn(
          "inline-flex items-center gap-1.5 rounded-full px-2 py-0.5 text-xs font-medium",
          running && "text-signal",
          !running && !error && "text-muted",
          error && "text-danger",
        )}
        style={{
          background: running
            ? "var(--signal-soft)"
            : error
              ? "color-mix(in srgb, var(--danger) 12%, transparent)"
              : "color-mix(in srgb, var(--muted) 10%, transparent)",
        }}
      >
        <Glyph running={running} error={error} />
        {LABEL[state] ?? state}
      </span>
      {version && <span className="font-mono text-xs text-faint">{version}</span>}
    </span>
  );
}

function Glyph({ running, error }: { running: boolean; error: boolean }) {
  if (running) {
    return (
      <svg width="8" height="8" viewBox="0 0 8 8" aria-hidden="true">
        <path d="M1.5 1 L7 4 L1.5 7 Z" fill="currentColor" />
      </svg>
    );
  }
  if (error) {
    return (
      <svg width="9" height="9" viewBox="0 0 12 12" fill="none" aria-hidden="true">
        <path
          d="M6 3v3.5M6 8.6v.1"
          stroke="currentColor"
          strokeWidth="1.6"
          strokeLinecap="round"
        />
      </svg>
    );
  }
  return <span className="h-1.5 w-1.5 rounded-[1px]" style={{ background: "currentColor" }} />;
}
