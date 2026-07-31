import { useEffect, useState } from "react";
import { Mark } from "./Mark";
import { ThemeToggle } from "./ThemeToggle";
import { LangToggle } from "./LangToggle";
import { IconButton } from "./ui";
import {
  AlertIcon,
  ChevronIcon,
  CloudIcon,
  KeyIcon,
  MenuIcon,
  NodeIcon,
  RouteIcon,
  ShieldIcon,
  SignOutIcon,
  SlidersIcon,
  StackIcon,
  UsersIcon,
} from "./icons";
import { cn } from "../lib/cn";
import { href, type Route } from "../lib/router";
import { useT } from "../lib/i18n";

type Item = { view: Route["view"]; label: string; icon: (p: { size?: number }) => React.ReactElement };

/**
 * The nav, grouped.
 *
 * Eleven destinations in one row had outgrown the top of the page — it read as
 * a wall of words with no shape to it. Down the side they get room for an icon
 * and a grouping, which is what makes a list of eleven scannable rather than
 * merely present.
 */
const GROUPS: { label: string; items: Item[] }[] = [
  {
    label: "机队",
    items: [
      { view: "nodes", label: "节点", icon: NodeIcon },
      { view: "externals", label: "外部节点", icon: CloudIcon },
      { view: "profiles", label: "接入配置", icon: StackIcon },
      { view: "variables", label: "变量", icon: KeyIcon },
      { view: "rules", label: "分流规则", icon: RouteIcon },
    ],
  },
  {
    label: "订阅者",
    items: [{ view: "users", label: "用户", icon: UsersIcon }],
  },
  {
    label: "运维",
    items: [
      { view: "kernel", label: "内核", icon: SlidersIcon },
      { view: "alerts", label: "告警", icon: AlertIcon },
      { view: "admins", label: "管理员", icon: ShieldIcon },
      { view: "audit", label: "审计", icon: StackIcon },
      { view: "settings", label: "设置", icon: SlidersIcon },
    ],
  },
];

const COLLAPSED_KEY = "chiral_nav_collapsed";

export function SideNav({ route, onSignOut }: { route: Route; onSignOut: () => void }) {
  const { t } = useT();
  // Remembered, because a nav that reopens wide on every page load is not
  // collapsible so much as briefly narrow.
  const [collapsed, setCollapsed] = useState(
    () => localStorage.getItem(COLLAPSED_KEY) === "1",
  );
  const [open, setOpen] = useState(false);

  useEffect(() => {
    localStorage.setItem(COLLAPSED_KEY, collapsed ? "1" : "0");
  }, [collapsed]);

  // The drawer closes on navigation. Without this a phone user taps a
  // destination and is left looking at the menu that took them there.
  useEffect(() => {
    setOpen(false);
  }, [route.view, "id" in route ? route.id : ""]);

  // Escape closes it too, for anyone who opened it by accident.
  useEffect(() => {
    if (!open) return;
    const onKey = (e: KeyboardEvent) => e.key === "Escape" && setOpen(false);
    window.addEventListener("keydown", onKey);
    return () => window.removeEventListener("keydown", onKey);
  }, [open]);

  return (
    <>
      {/* Phone: a bar with the menu button. The nav itself is a drawer, because
          eleven destinations do not fit across a phone and a horizontal
          scroller hides whatever is off the right edge. */}
      <header className="sticky top-0 z-30 flex h-14 items-center gap-3 border-b border-line bg-paper/85 px-4 backdrop-blur-md lg:hidden">
        <IconButton label={t("菜单")} onClick={() => setOpen(true)}>
          <MenuIcon size={18} />
        </IconButton>
        <a href={href({ view: "nodes" })} className="flex items-center gap-2">
          <span className="text-signal">
            <Mark size={20} />
          </span>
          <span className="font-display text-[16px] font-semibold tracking-tight">Chiral</span>
        </a>
        <div className="ml-auto flex items-center gap-1">
          <ThemeToggle />
          <IconButton label={t("退出")} onClick={onSignOut}>
            <SignOutIcon size={16} />
          </IconButton>
        </div>
      </header>

      {open && (
        <div
          className="fixed inset-0 z-40 bg-black/40 backdrop-blur-sm lg:hidden"
          onClick={() => setOpen(false)}
        />
      )}

      <aside
        className={cn(
          "z-50 flex flex-col border-line bg-paper",
          // Phone: a drawer that slides over the page.
          "fixed inset-y-0 left-0 w-[260px] border-r transition-transform duration-200",
          open ? "translate-x-0" : "-translate-x-full",
          // Desktop: always there, and its width is the collapse.
          "lg:sticky lg:top-0 lg:h-screen lg:translate-x-0 lg:transition-[width]",
          collapsed ? "lg:w-[64px]" : "lg:w-[220px]",
        )}
      >
        <div className={cn("flex h-14 shrink-0 items-center gap-2.5 px-4", collapsed && "lg:justify-center lg:px-0")}>
          <a href={href({ view: "nodes" })} className="flex items-center gap-2.5">
            <span className="text-signal">
              <Mark size={22} />
            </span>
            <span className={cn("font-display text-[17px] font-semibold tracking-tight", collapsed && "lg:hidden")}>
              Chiral
            </span>
          </a>
        </div>

        <nav className="flex-1 overflow-y-auto px-2 pb-2">
          {GROUPS.map((group) => (
            <div key={group.label} className="mt-3 first:mt-0">
              {/* The group heading is the first thing to go when collapsed: at
                  64px there is no room for a word, and the icons keep their
                  order so the grouping survives as spacing. */}
              <div
                className={cn(
                  "px-2.5 pb-1 text-[10px] font-medium uppercase tracking-[0.09em] text-faint",
                  collapsed && "lg:hidden",
                )}
              >
                {t(group.label)}
              </div>
              {group.items.map((item) => {
                const active = route.view === item.view;
                const Icon = item.icon;
                return (
                  <a
                    key={item.view}
                    href={href({ view: item.view } as Route)}
                    // The label becomes the tooltip when it is not on screen,
                    // so a collapsed nav is still readable by hovering.
                    title={collapsed ? t(item.label) : undefined}
                    className={cn(
                      "flex items-center gap-2.5 rounded-lg px-2.5 py-2 text-sm transition-colors",
                      active
                        ? "bg-signal-soft font-medium text-ink"
                        : "text-muted hover:bg-[color-mix(in_srgb,var(--muted)_8%,transparent)] hover:text-ink",
                      collapsed && "lg:justify-center lg:px-0",
                    )}
                  >
                    <span className="shrink-0">
                      <Icon size={16} />
                    </span>
                    <span className={cn("truncate", collapsed && "lg:hidden")}>{t(item.label)}</span>
                  </a>
                );
              })}
            </div>
          ))}
        </nav>

        <div
          className={cn(
            // The row is unconditional: `collapsed` is a desktop state, and
            // hanging the only `display: flex` off an `lg:` class left the
            // phone drawer with no flex at all — the controls stacked one per
            // line down the bottom of the drawer.
            "shrink-0 border-t border-line px-2 py-2 flex items-center gap-1",
            collapsed && "lg:flex-col lg:items-center lg:gap-1",
          )}
        >
          {/* Both are segmented controls wider than the 64px rail — kept for the
              expanded nav, and reached by expanding it. Leaving them in
              overflowed the border rather than shrinking. */}
          <div className={cn(collapsed && "lg:hidden")}>
            <LangToggle />
          </div>
          <div className={cn(collapsed && "lg:hidden")}>
            <ThemeToggle />
          </div>
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
          {/* Desktop only: on a phone the drawer closes rather than narrows. */}
          <button
            onClick={() => setCollapsed((v) => !v)}
            aria-label={t(collapsed ? "展开侧栏" : "收起侧栏")}
            title={t(collapsed ? "展开侧栏" : "收起侧栏")}
            className={cn(
              "ml-auto hidden h-8 w-8 place-items-center rounded-lg text-muted transition-colors hover:bg-[color-mix(in_srgb,var(--muted)_10%,transparent)] hover:text-ink lg:grid",
              collapsed && "lg:ml-0",
            )}
          >
            <ChevronIcon size={16} flip={!collapsed} />
          </button>
        </div>
      </aside>
    </>
  );
}
