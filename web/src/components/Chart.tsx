import { useId, useMemo, useState } from "react";

/**
 * A small area/line chart, drawn by hand rather than pulled from a charting
 * library: the panel needs one shape of chart, and a library would outweigh
 * the whole frontend for it.
 *
 * Series share one y-scale so they stay comparable. Green stays reserved for
 * what is alive (the primary series); anything secondary is drawn in the
 * neutral ink colour so the palette keeps meaning what it means elsewhere.
 */
export interface Series {
  label: string;
  points: { at: number; value: number }[];
  /** Secondary series are drawn muted, without a fill. */
  muted?: boolean;
}

export function Chart({
  series,
  height = 160,
  format,
  emptyLabel = "—",
}: {
  series: Series[];
  height?: number;
  /** Renders a value for the axis and the hover readout. */
  format: (v: number) => string;
  emptyLabel?: string;
}) {
  const id = useId().replace(/:/g, "");
  const [hover, setHover] = useState<number | null>(null);

  const model = useMemo(() => build(series, height), [series, height]);
  if (!model) {
    return (
      <div
        className="grid place-items-center rounded-xl border border-dashed border-line text-sm text-faint"
        style={{ height }}
      >
        {emptyLabel}
      </div>
    );
  }

  const { width, max, xs, paths } = model;
  const pad = { top: 8, right: 8, bottom: 18, left: 8 };
  const plotH = height - pad.top - pad.bottom;
  const y = (v: number) => pad.top + plotH - (v / max) * plotH;
  const x = (i: number) => pad.left + (i / Math.max(xs.length - 1, 1)) * (width - pad.left - pad.right);

  const hoverIndex = hover === null ? null : clamp(Math.round(hover * (xs.length - 1)), 0, xs.length - 1);

  return (
    <div className="relative">
      <svg
        viewBox={`0 0 ${width} ${height}`}
        preserveAspectRatio="none"
        className="w-full"
        style={{ height }}
        onMouseMove={(e) => {
          const r = e.currentTarget.getBoundingClientRect();
          setHover((e.clientX - r.left) / r.width);
        }}
        onMouseLeave={() => setHover(null)}
        role="img"
        aria-label={series.map((s) => s.label).join("、")}
      >
        <defs>
          <linearGradient id={`fill-${id}`} x1="0" y1="0" x2="0" y2="1">
            <stop offset="0%" stopColor="var(--online)" stopOpacity="0.18" />
            <stop offset="100%" stopColor="var(--online)" stopOpacity="0" />
          </linearGradient>
        </defs>

        {/* Two reference lines: enough to read a magnitude, few enough to stay quiet. */}
        {[0.5, 1].map((f) => (
          <line
            key={f}
            x1={pad.left}
            x2={width - pad.right}
            y1={y(max * f)}
            y2={y(max * f)}
            stroke="var(--line)"
            strokeWidth="1"
          />
        ))}

        {paths.map((p, i) => {
          const s = series[i];
          const colour = s.muted ? "var(--muted)" : "var(--online)";
          return (
            <g key={s.label}>
              {!s.muted && (
                <path d={`${p.d} L${x(xs.length - 1)},${y(0)} L${x(0)},${y(0)} Z`} fill={`url(#fill-${id})`} />
              )}
              <path
                d={p.d}
                fill="none"
                stroke={colour}
                strokeWidth="1.5"
                strokeLinejoin="round"
                strokeLinecap="round"
                opacity={s.muted ? 0.7 : 1}
                vectorEffect="non-scaling-stroke"
              />
            </g>
          );
        })}

        {hoverIndex !== null && (
          <line
            x1={x(hoverIndex)}
            x2={x(hoverIndex)}
            y1={pad.top}
            y2={pad.top + plotH}
            stroke="var(--line-strong)"
            strokeWidth="1"
          />
        )}
      </svg>

      {/* Axis labels and the hover readout sit outside the SVG so they are not
          stretched by preserveAspectRatio="none". */}
      <div className="pointer-events-none absolute inset-x-0 top-0 flex justify-between px-1 font-mono text-[10px] text-faint">
        <span>{format(max)}</span>
        {hoverIndex !== null && (
          <span className="text-ink">
            {timeLabel(xs[hoverIndex])}
            {series.map((s) => (
              <span key={s.label} className="ml-2">
                {s.label} {format(s.points[hoverIndex]?.value ?? 0)}
              </span>
            ))}
          </span>
        )}
      </div>
      <div className="pointer-events-none absolute inset-x-0 bottom-0 flex justify-between px-1 font-mono text-[10px] text-faint">
        <span>{timeLabel(xs[0])}</span>
        <span>{timeLabel(xs[xs.length - 1])}</span>
      </div>
    </div>
  );
}

function build(series: Series[], height: number) {
  const usable = series.filter((s) => s.points.length > 0);
  if (usable.length === 0) return null;

  const xs = usable[0].points.map((p) => p.at);
  if (xs.length < 2) return null;

  // One scale across every series, or two lines in the same box would imply a
  // comparison that isn't true.
  let max = 0;
  for (const s of usable) {
    for (const p of s.points) max = Math.max(max, p.value);
  }
  if (max <= 0) max = 1;

  const width = 600;
  const pad = { top: 8, right: 8, bottom: 18, left: 8 };
  const plotH = height - pad.top - pad.bottom;
  const y = (v: number) => pad.top + plotH - (v / max) * plotH;
  const x = (i: number) => pad.left + (i / Math.max(xs.length - 1, 1)) * (width - pad.left - pad.right);

  const paths = usable.map((s) => ({
    d: s.points
      .map((p, i) => `${i === 0 ? "M" : "L"}${x(i).toFixed(1)},${y(p.value).toFixed(1)}`)
      .join(" "),
  }));

  return { width, max, xs, paths };
}

function timeLabel(unixSec: number): string {
  const d = new Date(unixSec * 1000);
  return `${pad2(d.getHours())}:${pad2(d.getMinutes())}`;
}

function pad2(n: number): string {
  return n < 10 ? `0${n}` : String(n);
}

function clamp(v: number, lo: number, hi: number): number {
  return Math.min(Math.max(v, lo), hi);
}
