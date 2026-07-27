import { useEffect } from "react";
import { cn } from "../lib/cn";

/**
 * The shared UI primitives: input styling, form fields, modals, table cells,
 * empty and error states.
 *
 * Extracted from VariablesPage, where they first grew, for a structural
 * reason rather than tidiness: the portal build must not import an admin
 * page. UI atoms living inside pages/VariablesPage.tsx would drag that whole
 * page — and api.ts behind it — into any bundle that wants a Modal.
 */

export const inputCls =
  "w-full rounded-xl border border-line-strong bg-surface px-3.5 py-2.5 text-sm outline-none transition-colors placeholder:text-faint focus:border-signal";

// label arrives already translated by the caller; translating again here would
// be a second lookup of an English string.
export function Field({ label, children }: { label: string; children: React.ReactNode }) {
  return (
    <div className="mt-4">
      <div className="mb-1.5 text-[11px] font-medium uppercase tracking-[0.07em] text-faint">
        {label}
      </div>
      {children}
    </div>
  );
}

export function Modal({
  onClose,
  children,
  wide,
}: {
  onClose: () => void;
  children: React.ReactNode;
  wide?: boolean;
}) {
  useEffect(() => {
    const onKey = (e: KeyboardEvent) => e.key === "Escape" && onClose();
    window.addEventListener("keydown", onKey);
    return () => window.removeEventListener("keydown", onKey);
  }, [onClose]);
  return (
    <div
      className="fixed inset-0 z-50 grid place-items-center overflow-y-auto bg-black/40 px-4 py-8 backdrop-blur-sm"
      onClick={onClose}
    >
      <div
        role="dialog"
        aria-modal="true"
        className={cn(
          "w-full animate-rise rounded-2xl border border-line bg-raised p-6 shadow-[var(--shadow-pop)]",
          wide ? "max-w-3xl" : "max-w-lg",
        )}
        onClick={(e) => e.stopPropagation()}
      >
        {children}
      </div>
    </div>
  );
}

export function Th({ children }: { children: React.ReactNode }) {
  return <th className="px-4 py-2.5 font-medium whitespace-nowrap">{children}</th>;
}

export function Td({
  children,
  className = "",
}: {
  children: React.ReactNode;
  className?: string;
}) {
  return <td className={`px-4 py-3 ${className}`}>{children}</td>;
}

export function Empty({ children }: { children: React.ReactNode }) {
  return (
    <div className="rounded-2xl border border-dashed border-line-strong bg-surface px-8 py-14 text-center text-sm text-muted">
      {children}
    </div>
  );
}

export function ErrorBar({ text }: { text: string }) {
  return (
    <div
      className="mb-5 rounded-xl px-4 py-3 text-sm text-danger"
      style={{
        border: "1px solid color-mix(in srgb, var(--danger) 35%, transparent)",
        background: "color-mix(in srgb, var(--danger) 10%, transparent)",
      }}
    >
      {text}
    </div>
  );
}
