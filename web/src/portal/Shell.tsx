import type { ReactNode } from "react";
import { Mark } from "../components/Mark";
import { IconButton } from "../components/ui";
import { SignOutIcon } from "../components/icons";
import { href } from "./router";
import { useT } from "../lib/i18n";

/**
 * The portal's frame: one narrow column, mobile first.
 *
 * Narrower than the console's 1120px because there is one column of content
 * and no tables — the console's Th/Td grids would break at 375px, which is
 * where most of this will be read.
 *
 * The theme and language toggles are NOT here, unlike the console's header.
 * Six controls plus a wordmark overflow 375px, and a horizontally scrolling
 * page is worse than a preference that takes one more tap. They live on the
 * account page for someone signed in, and on the sign-in screen itself for
 * someone who is not — which is where a first-time visitor needs them.
 */
export function Shell({
  children,
  signedIn,
  onSignOut,
}: {
  children: ReactNode;
  signedIn?: boolean;
  onSignOut?: () => void;
}) {
  const { t } = useT();
  return (
    <div className="min-h-screen">
      <header className="sticky top-0 z-30 border-b border-line bg-paper/80 backdrop-blur-md">
        <div className="mx-auto flex h-14 max-w-[720px] items-center justify-between gap-4 px-5">
          <a href={href({ view: "home" })} className="flex shrink-0 items-center gap-2.5">
            <span className="text-signal">
              <Mark size={22} />
            </span>
            <span className="font-display text-[17px] font-semibold tracking-tight">Chiral</span>
          </a>
          {signedIn && (
            <div className="flex shrink-0 items-center gap-1">
              <a
                href={href({ view: "account" })}
                className="rounded-lg px-2.5 py-2 text-sm text-muted transition-colors hover:text-ink"
              >
                {t("账户")}
              </a>
              <IconButton label={t("退出")} onClick={onSignOut}>
                <SignOutIcon size={16} />
              </IconButton>
            </div>
          )}
        </div>
      </header>
      {/* The bottom padding clears the home indicator on a notched phone;
          index.html sets viewport-fit=cover so the inset is non-zero there. */}
      <main
        className="mx-auto max-w-[720px] px-5 py-7"
        style={{ paddingBottom: "calc(1.75rem + env(safe-area-inset-bottom))" }}
      >
        {children}
      </main>
    </div>
  );
}
