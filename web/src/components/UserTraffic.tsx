import { useEffect, useState } from "react";
import { api, type TrafficPoint } from "../api";
import { bytes } from "../format";
import { cn } from "../lib/cn";
import { Chart, type Series } from "./Chart";
import { useT } from "../lib/i18n";

const RANGES = [
  { label: "24 小时", seconds: 24 * 3600 },
  { label: "7 天", seconds: 7 * 24 * 3600 },
  { label: "30 天", seconds: 30 * 24 * 3600 },
];

/**
 * A user's traffic over time, from the panel's own accumulated buckets rather
 * than the live counters — so it survives a kernel restart and shows the
 * shape of usage against the quota, not just the running total.
 */
export function UserTraffic({ userId }: { userId: string }) {
  const { t } = useT();
  const [range, setRange] = useState(RANGES[0].seconds);
  const [points, setPoints] = useState<TrafficPoint[] | null>(null);
  const [error, setError] = useState("");

  useEffect(() => {
    let cancelled = false;
    setPoints(null);
    api
      .trafficSeries({ userId, windowSec: range })
      .then((r) => {
        if (!cancelled) {
          setPoints(r.points);
          setError("");
        }
      })
      .catch((e) => {
        if (!cancelled) setError((e as Error).message);
      });
    return () => {
      cancelled = true;
    };
  }, [userId, range]);

  const series: Series[] = [
    {
      label: "↑",
      points: (points ?? []).map((p) => ({ at: p.at, value: p.up_bytes })),
    },
    {
      label: "↓",
      points: (points ?? []).map((p) => ({ at: p.at, value: p.down_bytes })),
      muted: true,
    },
  ];

  return (
    <div>
      <div className="mb-2 flex justify-end">
        <div className="inline-flex items-center gap-0.5 rounded-lg border border-line p-0.5">
          {RANGES.map((r) => (
            <button
              key={r.seconds}
              onClick={() => setRange(r.seconds)}
              className={cn(
                "rounded-md px-2 py-1 text-xs transition-colors",
                range === r.seconds ? "bg-signal-soft text-ink" : "text-faint hover:text-ink",
              )}
            >
              {t(r.label)}
            </button>
          ))}
        </div>
      </div>
      {error && <p className="mb-2 text-sm text-danger">{error}</p>}
      <Chart
        series={series}
        height={120}
        format={bytes}
        emptyLabel={points === null ? t("加载中…") : t("这段时间没有流量")}
      />
      <p className="mt-1 text-xs text-faint">{t("按小时累计。")}</p>
    </div>
  );
}
