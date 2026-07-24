import { cn } from "../lib/cn";

/**
 * Node liveness. Online breathes with an expanding ring — the page's felt
 * "heartbeat". Offline is quiet and neutral: absence isn't an error.
 */
export function StatusDot({ online }: { online: boolean }) {
  return (
    <span className="relative inline-flex h-2.5 w-2.5 shrink-0">
      {online && (
        <span
          className="absolute inset-0 rounded-full animate-ping-soft"
          style={{ background: "var(--online)" }}
        />
      )}
      <span
        className={cn(
          "relative inline-flex h-2.5 w-2.5 rounded-full",
          online && "animate-breathe",
        )}
        style={{ background: online ? "var(--online)" : "var(--faint)" }}
      />
    </span>
  );
}
