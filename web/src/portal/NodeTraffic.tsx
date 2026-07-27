import type { TrafficPoint } from "./api";
import { bytes } from "../format";
import { useT } from "../lib/i18n";

/**
 * Twenty-four hourly bars of this user's own traffic on one line.
 *
 * Bars rather than the console's sparkline, and no live glow: the console's
 * line is a ten-second poll of instantaneous throughput, and this is an hourly
 * accumulation that moves once an hour. Animating it the same way would be
 * saying something untrue about how fresh it is.
 *
 * Deliberately not shown next to the quota. There is no record anywhere of
 * when the current billing period began — RenewUser only writes expires_at and
 * zeroes used_bytes, and an operator can edit expires_at directly — so any
 * breakdown placed beside the quota number would eventually fail to add up.
 * "The last 24 hours" is a different question, and visually separate.
 */
export function NodeTraffic({ points }: { points: TrafficPoint[] }) {
  const { t } = useT();
  if (points.length === 0) {
    return <div className="text-xs text-faint">{t("最近 24 小时没有流量")}</div>;
  }

  // Bucket into 24 hourly slots ending now, so gaps read as gaps rather than
  // being closed up by a dense-array chart.
  const hour = 3600;
  const now = Math.floor(Date.now() / 1000 / hour) * hour;
  const slots = new Array(24).fill(0);
  let total = 0;
  for (const p of points) {
    const idx = 23 - Math.floor((now - p.at) / hour);
    const value = p.up_bytes + p.down_bytes;
    total += value;
    if (idx >= 0 && idx < 24) slots[idx] += value;
  }
  const peak = Math.max(...slots, 1);

  return (
    <div>
      <div className="flex h-8 items-end gap-[2px]">
        {slots.map((v, i) => (
          <div
            key={i}
            className="flex-1 rounded-[1px]"
            style={{
              // A floor of 2px so an hour with a little traffic is visible;
              // zero stays zero, which is the distinction that matters.
              height: v === 0 ? "1px" : `${Math.max(2, (v / peak) * 32)}px`,
              background: v === 0 ? "var(--line-strong)" : "var(--online)",
              opacity: v === 0 ? 1 : 0.85,
            }}
          />
        ))}
      </div>
      <div className="mt-1.5 text-xs text-faint">
        {t("最近 24 小时")} · {bytes(total)}
      </div>
    </div>
  );
}
