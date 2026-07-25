import { Mark } from "./Mark";
import { ThemeToggle } from "./ThemeToggle";
import { IconButton } from "./ui";
import { SignOutIcon } from "./icons";
import { cn } from "../lib/cn";
import { href, type Route } from "../lib/router";

const NAV: { view: Route["view"]; label: string }[] = [
  { view: "nodes", label: "节点" },
  { view: "profiles", label: "接入配置" },
  { view: "variables", label: "变量" },
];

export function TopBar({ route, onSignOut }: { route: Route; onSignOut: () => void }) {
  return (
    <header className="sticky top-0 z-30 border-b border-line bg-paper/80 backdrop-blur-md">
      <div className="mx-auto flex h-14 max-w-[1120px] items-center justify-between gap-4 px-5 sm:px-8">
        <div className="flex min-w-0 items-center gap-5">
          <a href={href({ view: "nodes" })} className="flex shrink-0 items-center gap-2.5">
            <span className="text-signal">
              <Mark size={22} />
            </span>
            <span className="font-display text-[17px] font-semibold tracking-tight">
              Chiral
            </span>
          </a>
          <nav className="flex items-center gap-0.5 overflow-x-auto">
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
                {item.label}
              </a>
            ))}
          </nav>
        </div>
        <div className="flex shrink-0 items-center gap-2">
          <ThemeToggle />
          <IconButton label="退出" onClick={onSignOut}>
            <SignOutIcon size={16} />
          </IconButton>
        </div>
      </div>
    </header>
  );
}
