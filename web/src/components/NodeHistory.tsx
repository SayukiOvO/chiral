import { useEffect, useState } from "react";
import { api, type NodeSample } from "../api";
import { bitrate, bytes, percent } from "../format";
import { cn } from "../lib/cn";
import { Chart, type Series } from "./Chart";
import { useT } from "../lib/i18n";

const RANGES = [
  { label: "1 小时", seconds: 3600 },
  { label: "6 小时", seconds: 6 * 3600 },
  { label: "24 小时", seconds: 24 * 3600 },
  { label: "7 天", seconds: 7 * 24 * 3600 },
];

/**
 * A node's resource history. The live sparkline on the roster only holds what
 * this browser tab has seen; this is the panel's own record, so it survives a
 * reload and covers the retention window.
 */
export function NodeHistory({ nodeId }: { nodeId: string }) {
  const { t } = useT();
  const [range, setRange] = useState(RANGES[1].seconds);
  const [samples, setSamples] = useState<NodeSample[] | null>(null);
  const [error, setError] = useState("");

  useEffect(() => {
    let cancelled = false;
    setSamples(null);
    api
      .nodeSamples(nodeId, range)
      .then((r) => {
        if (!cancelled) {
          setSamples(r.samples);
          setError("");
        }
      })
      .catch((e) => {
        if (!cancelled) setError((e as Error).message);
      });
    return () => {
      cancelled = true;
    };
  }, [nodeId, range]);

  const cpu: Series[] = [
    { label: "CPU", points: (samples ?? []).map((s) => ({ at: s.at, value: s.cpu_percent })) },
  ];
  const mem: Series[] = [
    { label: t("内存"), points: (samples ?? []).map((s) => ({ at: s.at, value: s.mem_used_bytes })) },
  ];
  const net: Series[] = [
    { label: "↑", points: (samples ?? []).map((s) => ({ at: s.at, value: s.net_tx_bps })) },
    { label: "↓", points: (samples ?? []).map((s) => ({ at: s.at, value: s.net_rx_bps })), muted: true },
  ];

  return (
    <div>
      <div className="mb-3 flex items-center justify-between gap-3">
        <span className="text-[11px] font-medium uppercase tracking-[0.07em] text-faint">
          {t("资源历史")}
        </span>
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

      {error && <p className="mb-3 text-sm text-danger">{error}</p>}

      <div className="grid gap-4 sm:grid-cols-3">
        <Panel title="CPU">
          <Chart series={cpu} height={120} format={percent} emptyLabel={loadingLabel(t, samples)} />
        </Panel>
        <Panel title={t("内存")}>
          <Chart series={mem} height={120} format={bytes} emptyLabel={loadingLabel(t, samples)} />
        </Panel>
        <Panel title={t("网速 ↑ / ↓")}>
          <Chart series={net} height={120} format={bitrate} emptyLabel={loadingLabel(t, samples)} />
        </Panel>
      </div>
      <p className="mt-2 text-xs text-faint">
        {t("每分钟一个采样点，保留 7 天。")}
      </p>
    </div>
  );
}

function Panel({ title, children }: { title: string; children: React.ReactNode }) {
  return (
    <div>
      <div className="mb-1 text-xs text-muted">{title}</div>
      {children}
    </div>
  );
}

// Distinguishing "still loading" from "nothing recorded" matters: a new node
// legitimately has no history, and that should not read as a failure.
function loadingLabel(t: (k: string) => string, samples: NodeSample[] | null): string {
  return samples === null ? t("加载中…") : t("暂无采样");
}
