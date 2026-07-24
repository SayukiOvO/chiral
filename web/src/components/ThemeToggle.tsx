import { useEffect, useState } from "react";

type Mode = "light" | "dark" | "system";
const KEY = "chiral_theme";

function apply(mode: Mode) {
  const dark =
    mode === "dark" ||
    (mode === "system" &&
      window.matchMedia("(prefers-color-scheme: dark)").matches);
  document.documentElement.classList.toggle("dark", dark);
}

export function ThemeToggle() {
  const [mode, setMode] = useState<Mode>(
    () => (localStorage.getItem(KEY) as Mode) ?? "system",
  );

  useEffect(() => {
    apply(mode);
    localStorage.setItem(KEY, mode);
    if (mode !== "system") return;
    const mq = window.matchMedia("(prefers-color-scheme: dark)");
    const onChange = () => apply("system");
    mq.addEventListener("change", onChange);
    return () => mq.removeEventListener("change", onChange);
  }, [mode]);

  const next: Record<Mode, Mode> = {
    light: "dark",
    dark: "system",
    system: "light",
  };
  const label: Record<Mode, string> = {
    light: "☀️ 日",
    dark: "🌙 夜",
    system: "🖥 跟随",
  };

  return (
    <button
      onClick={() => setMode(next[mode])}
      className="text-sm border border-[var(--color-border)] rounded-lg px-2 py-1 hover:border-[var(--color-accent)]"
      title="切换主题"
    >
      {label[mode]}
    </button>
  );
}
