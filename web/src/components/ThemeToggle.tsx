import { MonitorIcon, MoonIcon, SunIcon } from "./icons";
import { cn } from "../lib/cn";
import type { ThemeMode } from "../lib/theme";
import { useTheme } from "../lib/theme";

const OPTIONS: { mode: ThemeMode; label: string; Icon: typeof SunIcon }[] = [
  { mode: "light", label: "日间", Icon: SunIcon },
  { mode: "system", label: "跟随系统", Icon: MonitorIcon },
  { mode: "dark", label: "夜间", Icon: MoonIcon },
];

/** Three-state theme control as a compact segmented pill. */
export function ThemeToggle() {
  const { mode, setMode } = useTheme();
  return (
    <div
      role="radiogroup"
      aria-label="主题"
      className="inline-flex items-center gap-0.5 rounded-lg border border-line p-0.5"
    >
      {OPTIONS.map(({ mode: m, label, Icon }) => {
        const active = mode === m;
        return (
          <button
            key={m}
            role="radio"
            aria-checked={active}
            aria-label={label}
            title={label}
            onClick={() => setMode(m)}
            className={cn(
              "grid h-7 w-7 place-items-center rounded-md transition-colors",
              active
                ? "bg-signal-soft text-signal"
                : "text-faint hover:text-ink",
            )}
          >
            <Icon size={15} />
          </button>
        );
      })}
    </div>
  );
}
