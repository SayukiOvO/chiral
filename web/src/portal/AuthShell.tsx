import type { ReactNode } from "react";
import { Mark } from "../components/Mark";
import { ThemeToggle } from "../components/ThemeToggle";
import { LangToggle } from "../components/LangToggle";
import { useT } from "../lib/i18n";

/** Shared frame for the signed-out screens: centred, calm, one column. */
export function AuthShell({
  title,
  subtitle,
  children,
}: {
  title: string;
  subtitle: string;
  children: ReactNode;
}) {
  return (
    <div className="relative min-h-screen">
      <div className="absolute right-5 top-5 flex items-center gap-2">
        <LangToggle />
        <ThemeToggle />
      </div>
      <div className="grid min-h-screen place-items-center px-5">
        <div className="w-full max-w-[380px] animate-rise">
          <div className="mb-7 flex items-center gap-2.5">
            <span className="text-signal">
              <Mark size={26} />
            </span>
            <span className="font-display text-xl font-semibold tracking-tight">Chiral</span>
          </div>
          <h1 className="font-display text-2xl font-semibold tracking-tight">{title}</h1>
          <p className="mb-6 mt-1.5 text-sm text-muted">{subtitle}</p>
          {children}
        </div>
      </div>
    </div>
  );
}

/**
 * Taller than the console's inputs: 44px is the minimum comfortable touch
 * target, and this is read on phones.
 */
export const authInput =
  "w-full rounded-xl border border-line-strong bg-surface px-3.5 py-3 text-base outline-none " +
  "transition-colors placeholder:text-faint focus:border-signal";

/** A hint that the panel is not accepting new accounts, said plainly. */
export function ClosedNotice() {
  const { t } = useT();
  return <p className="text-sm text-muted">{t("这个面板暂不开放注册。")}</p>;
}
