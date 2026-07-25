import { bytes, quotaFraction } from "../format";
import { useT } from "../lib/i18n";

/**
 * Usage against quota. Green while there is headroom, amber as it runs out,
 * red once it is spent — the one place colour carries meaning beyond "alive",
 * because an operator scanning the list needs to spot the trouble.
 */
export function QuotaBar({ used, quota }: { used: number; quota: number }) {
  const { t } = useT();
  const fraction = quotaFraction(used, quota);
  if (fraction === null) {
    return (
      <div className="font-mono text-sm tnum">
        {bytes(used)}
        <span className="ml-1.5 text-xs text-faint">{t("不限")}</span>
      </div>
    );
  }
  const pct = Math.round(fraction * 100);
  const colour =
    fraction >= 1 ? "var(--danger)" : fraction >= 0.85 ? "var(--warn)" : "var(--online)";
  return (
    <div className="min-w-[132px]">
      <div className="flex items-baseline justify-between gap-2 font-mono text-xs tnum">
        <span>{bytes(used)}</span>
        <span className="text-faint">{bytes(quota)}</span>
      </div>
      <div
        className="mt-1 h-1 overflow-hidden rounded-full"
        style={{ background: "color-mix(in srgb, var(--muted) 22%, transparent)" }}
        role="progressbar"
        aria-valuenow={pct}
        aria-valuemin={0}
        aria-valuemax={100}
        aria-label={t("流量用量")}
      >
        <div
          className="h-full rounded-full transition-[width] duration-500"
          style={{ width: `${pct}%`, background: colour }}
        />
      </div>
    </div>
  );
}
