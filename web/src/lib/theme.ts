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

/**
 * Whether the resolved theme is currently dark. Components that cannot use
 * CSS variables — Monaco picks a theme by name — need the resolved value, not
 * the mode, and must re-render when the OS preference flips under "system".
 */
export function useIsDark(): boolean {
  const [dark, setDark] = useState(() =>
    document.documentElement.classList.contains("dark"),
  );
  useEffect(() => {
    const sync = () => setDark(document.documentElement.classList.contains("dark"));
    const observer = new MutationObserver(sync);
    observer.observe(document.documentElement, {
      attributes: true,
      attributeFilter: ["class"],
    });
    const mq = window.matchMedia("(prefers-color-scheme: dark)");
    mq.addEventListener("change", sync);
    return () => {
      observer.disconnect();
      mq.removeEventListener("change", sync);
    };
  }, []);
  return dark;
}

// Paint before React mounts to avoid a flash of the wrong theme.
paint((localStorage.getItem(KEY) as ThemeMode) ?? "system");
