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
import { api, type Whoami } from "../api";
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
          {/* The wordmark is the first thing to go on a narrow phone: the mark
              still says where you are, and the two toggles do not shrink. */}
          <span className="hidden font-display text-[16px] font-semibold tracking-tight min-[360px]:inline">
            Chiral
          </span>
        </a>
        {/* The phone's top-right. The desktop plate would float over content a
            small screen cannot spare, and this bar is already pinned there. */}
        <div className="ml-auto flex items-center gap-1">
          <LangToggle />
          <ThemeToggle />
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

        {/* One row, one thing in it.

            This corner used to hold five controls side by side — two segmented
            toggles and three icon buttons — inside 220px, and at 64px it held
            whichever of them still fitted. Nothing there was wrong on its own;
            there were simply too many of them in the smallest space on the
            page. Theme and language are preferences rather than destinations,
            so they move into the account menu, which is the one place a person
            already goes looking for "settings about me". */}
        <div className="shrink-0 border-t border-line p-2">
          <AccountMenu route={route} collapsed={collapsed} onSignOut={onSignOut} />
        </div>
        {/* On the edge itself rather than in a row of its own. A whole
            bordered strip for one chevron was a lot of furniture for a very
            small thing, and it read as a gap someone forgot to fill. Here it
            costs no layout at all and sits in the same place at either width. */}
        <button
          onClick={() => setCollapsed((v) => !v)}
          aria-label={t(collapsed ? "展开侧栏" : "收起侧栏")}
          title={t(collapsed ? "展开侧栏" : "收起侧栏")}
          className="absolute -right-3 top-1/2 z-10 hidden h-6 w-6 -translate-y-1/2 place-items-center rounded-full border border-line bg-paper text-faint shadow-[var(--shadow-card)] transition-colors hover:border-signal hover:text-ink lg:grid"
        >
          <ChevronIcon size={13} flip={!collapsed} />
        </button>
      </aside>
    </>
  );
}

/**
 * The operator, and the things that are about them.
 *
 * A row rather than a cluster of icons: at 220px five controls left every one
 * of them 30 pixels wide with no label, and at 64px most of them had to be
 * hidden to fit at all. One row with a name on it says who is signed in — which
 * the console never did — and everything else moves one click away, where there
 * is room to write what it is.
 */
function AccountMenu({
  route,
  collapsed,
  onSignOut,
}: {
  route: Route;
  collapsed: boolean;
  onSignOut: () => void;
}) {
  const { t } = useT();
  const [open, setOpen] = useState(false);
  const [me, setMe] = useState<Whoami | null>(null);

  useEffect(() => {
    api.whoami().then(setMe).catch(() => {});
  }, []);

  // Anywhere else closes it. Without this the menu survives a click on the page
  // behind it and has to be dismissed by the button that opened it.
  useEffect(() => {
    if (!open) return;
    const away = () => setOpen(false);
    const esc = (e: KeyboardEvent) => e.key === "Escape" && setOpen(false);
    window.addEventListener("click", away);
    window.addEventListener("keydown", esc);
    return () => {
      window.removeEventListener("click", away);
      window.removeEventListener("keydown", esc);
    };
  }, [open]);

  useEffect(() => setOpen(false), [route.view]);

  const name = me?.name || "—";
  const roleLabel =
    me?.role === "superadmin" ? "超级管理员" : me?.role === "operator" ? "运维" : "只读";

  return (
    <div className="relative" onClick={(e) => e.stopPropagation()}>
      {open && (
        <div
          // Upwards: it hangs off the bottom of the page.
          className="absolute bottom-full left-0 z-50 mb-2 w-[180px] rounded-xl border border-line bg-paper p-1.5 shadow-[var(--shadow-lift)]"
        >
          <a
            href={href({ view: "security" })}
            className={cn(
              "flex items-center gap-2.5 rounded-lg px-2 py-1.5 text-sm transition-colors",
              route.view === "security"
                ? "bg-signal-soft text-ink"
                : "text-muted hover:bg-[color-mix(in_srgb,var(--muted)_8%,transparent)] hover:text-ink",
            )}
          >
            <ShieldIcon size={15} />
            {t("账号安全")}
          </a>
          <button
            onClick={onSignOut}
            className="flex w-full items-center gap-2.5 rounded-lg px-2 py-1.5 text-sm text-muted transition-colors hover:bg-[color-mix(in_srgb,var(--danger)_10%,transparent)] hover:text-danger"
          >
            <SignOutIcon size={15} />
            {t("退出登录")}
          </button>
        </div>
      )}

      <button
        onClick={() => setOpen((v) => !v)}
        title={collapsed ? name : undefined}
        className={cn(
          "flex w-full items-center gap-2.5 rounded-lg py-1.5 text-left transition-colors hover:bg-[color-mix(in_srgb,var(--muted)_8%,transparent)]",
          collapsed ? "lg:justify-center lg:px-0" : "px-2",
        )}
      >
        <span className="grid h-7 w-7 shrink-0 place-items-center rounded-full bg-signal-soft font-display text-[12px] font-semibold uppercase text-ink">
          {name.slice(0, 1)}
        </span>
        <span className={cn("min-w-0 flex-1", collapsed && "lg:hidden")}>
          <span className="block truncate text-[13px] font-medium text-ink">{name}</span>
          <span className="block truncate text-[11px] text-faint">{t(roleLabel)}</span>
        </span>
        <span className={cn("shrink-0 text-faint", collapsed && "lg:hidden")}>
          <ChevronIcon size={14} className={open ? "rotate-90" : "-rotate-90"} />
        </span>
      </button>
    </div>
  );
}
