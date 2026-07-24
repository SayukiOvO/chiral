import type { ButtonHTMLAttributes, ReactNode } from "react";
import { cn } from "../lib/cn";

type Variant = "primary" | "ghost" | "outline" | "danger";
type Size = "sm" | "md";

const VARIANT: Record<Variant, string> = {
  primary:
    "bg-signal text-signal-ink hover:brightness-110 active:brightness-95 shadow-[0_1px_2px_rgba(16,19,28,0.12)]",
  ghost: "text-muted hover:text-ink hover:bg-[color-mix(in_srgb,var(--muted)_10%,transparent)]",
  outline: "border border-line-strong text-ink hover:border-signal hover:text-signal bg-surface",
  danger:
    "border border-transparent text-danger hover:bg-[color-mix(in_srgb,var(--danger)_12%,transparent)]",
};

const SIZE: Record<Size, string> = {
  sm: "h-8 px-3 text-[13px] gap-1.5 rounded-lg",
  md: "h-10 px-4 text-sm gap-2 rounded-xl",
};

export function Button({
  variant = "outline",
  size = "md",
  className,
  children,
  ...props
}: {
  variant?: Variant;
  size?: Size;
  children: ReactNode;
} & ButtonHTMLAttributes<HTMLButtonElement>) {
  return (
    <button
      className={cn(
        "inline-flex items-center justify-center font-medium transition-all duration-150 disabled:opacity-45 disabled:pointer-events-none whitespace-nowrap",
        VARIANT[variant],
        SIZE[size],
        className,
      )}
      {...props}
    >
      {children}
    </button>
  );
}

export function IconButton({
  className,
  children,
  label,
  ...props
}: { children: ReactNode; label: string } & ButtonHTMLAttributes<HTMLButtonElement>) {
  return (
    <button
      aria-label={label}
      title={label}
      className={cn(
        "inline-grid place-items-center h-8 w-8 rounded-lg text-muted transition-colors hover:text-ink hover:bg-[color-mix(in_srgb,var(--muted)_10%,transparent)] disabled:opacity-40 disabled:pointer-events-none",
        className,
      )}
      {...props}
    >
      {children}
    </button>
  );
}
