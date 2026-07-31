import { Mark } from "./Mark";
import { ThemeToggle } from "./ThemeToggle";
import { LangToggle } from "./LangToggle";
import { IconButton } from "./ui";
import { ShieldIcon, SignOutIcon } from "./icons";
import { cn } from "../lib/cn";
import { href, type Route } from "../lib/router";
import { useT } from "../lib/i18n";

const NAV: { view: Route["view"]; label: string }[] = [
  { view: "nodes", label: "节点" },
  { view: "profiles", label: "接入配置" },
  { view: "users", label: "用户" },
  { view: "variables", label: "变量" },
  { view: "rules", label: "分流规则" },
  { view: "externals", label: "外部节点" },
  { view: "settings", label: "设置" },
  { view: "kernel", label: "内核" },
  { view: "alerts", label: "告警" },
  { view: "admins", label: "管理员" },
  { view: "audit", label: "审计" },
];

export function TopBar({ route, onSignOut }: { route: Route; onSignOut: () => void }) {
  const { t } = useT();
  return (
    <header className="sticky top-0 z-30 border-b border-line bg-paper/80 backdrop-blur-md">
      {/* One row on a desktop; on a phone the brand and controls keep the top
          line and the nav drops to its own, where four labels actually fit. */}
      <div className="mx-auto flex max-w-[1120px] flex-wrap items-center gap-x-5 px-5 py-2.5 sm:h-14 sm:flex-nowrap sm:px-8 sm:py-0">
        <a
          href={href({ view: "nodes" })}
          className="order-1 flex shrink-0 items-center gap-2.5"
        >
          <span className="text-signal">
            <Mark size={22} />
          </span>
          {/* The mark alone on a phone: the wordmark is what pushes the
              controls onto a line of their own. */}
          <span className="hidden font-display text-[17px] font-semibold tracking-tight sm:inline">
            Chiral
          </span>
        </a>

        <nav className="order-3 -mx-1 flex w-full items-center gap-0.5 overflow-x-auto px-1 pt-1.5 sm:order-2 sm:mr-auto sm:w-auto sm:pt-0">
          {NAV.map((item) => (
            <a
              key={item.view}
              href={href({ view: item.view } as Route)}
              className={cn(
                "whitespace-nowrap rounded-lg px-2.5 py-1.5 text-sm transition-colors",
                route.view === item.view
                  ? "bg-signal-soft text-ink font-medium"
                  : "text-muted hover:text-ink",
              )}
            >
              {t(item.label)}
            </a>
          ))}
        </nav>

        <div className="order-2 ml-auto flex shrink-0 items-center gap-2 sm:order-3 sm:ml-0">
          <LangToggle />
          <ThemeToggle />
          {/* Account settings, not a resource — an icon rather than a nav tab. */}
          <a
            href={href({ view: "security" })}
            aria-label={t("安全")}
            title={t("安全")}
            className={cn(
              "inline-grid h-8 w-8 place-items-center rounded-lg transition-colors hover:bg-[color-mix(in_srgb,var(--muted)_10%,transparent)]",
              route.view === "security" ? "bg-signal-soft text-ink" : "text-muted hover:text-ink",
            )}
          >
            <ShieldIcon size={16} />
          </a>
          <IconButton label={t("退出")} onClick={onSignOut}>
            <SignOutIcon size={16} />
          </IconButton>
        </div>
      </div>
    </header>
  );
}
