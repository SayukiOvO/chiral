import { useEffect, useState } from "react";

export type ThemeMode = "light" | "dark" | "system";
const KEY = "chiral_theme";

function resolve(mode: ThemeMode): boolean {
  return (
    mode === "dark" ||
    (mode === "system" && window.matchMedia("(prefers-color-scheme: dark)").matches)
  );
}

function paint(mode: ThemeMode) {
  document.documentElement.classList.toggle("dark", resolve(mode));
}

export function useTheme() {
  const [mode, setMode] = useState<ThemeMode>(
    () => (localStorage.getItem(KEY) as ThemeMode) ?? "system",
  );

  useEffect(() => {
    paint(mode);
    localStorage.setItem(KEY, mode);
    if (mode !== "system") return;
    const mq = window.matchMedia("(prefers-color-scheme: dark)");
    const onChange = () => paint("system");
    mq.addEventListener("change", onChange);
    return () => mq.removeEventListener("change", onChange);
  }, [mode]);

  return { mode, setMode };
}

// Paint before React mounts to avoid a flash of the wrong theme.
paint((localStorage.getItem(KEY) as ThemeMode) ?? "system");
