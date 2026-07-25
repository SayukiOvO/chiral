import { useT } from "../lib/i18n";
import { cn } from "../lib/cn";

/** Two languages, so a two-state pill rather than a dropdown. */
export function LangToggle() {
  const { lang, setLang, t } = useT();
  return (
    <div
      role="radiogroup"
      aria-label={t("语言")}
      className="inline-flex items-center gap-0.5 rounded-lg border border-line p-0.5"
    >
      {(["zh", "en"] as const).map((l) => (
        <button
          key={l}
          role="radio"
          aria-checked={lang === l}
          onClick={() => setLang(l)}
          className={cn(
            "rounded-md px-2 py-1 text-xs font-medium transition-colors",
            lang === l ? "bg-signal-soft text-ink" : "text-faint hover:text-ink",
          )}
        >
          {l === "zh" ? "中" : "EN"}
        </button>
      ))}
    </div>
  );
}
