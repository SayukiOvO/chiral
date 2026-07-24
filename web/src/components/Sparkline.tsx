import { useId } from "react";

/**
 * The signature element: a node's recent throughput as a living line.
 * Fed by a client-side ring buffer of heartbeat samples (bytes/sec), so the
 * instantaneous telemetry the API returns accumulates into a real trace.
 * When the kernel is live the line carries the cobalt→violet signal glow.
 */
export function Sparkline({
  data,
  live,
  width = 108,
  height = 34,
}: {
  data: number[];
  live: boolean;
  width?: number;
  height?: number;
}) {
  const id = useId().replace(/:/g, "");
  const pad = 3;
  const w = width;
  const h = height;

  const usable = data.length >= 2 ? data : [0, 0];
  const max = Math.max(...usable, 1);
  const stepX = (w - pad * 2) / (usable.length - 1);
  const y = (v: number) => h - pad - (v / max) * (h - pad * 2);
  const points = usable.map((v, i) => [pad + i * stepX, y(v)] as const);

  const line = points
    .map(([px, py], i) => `${i === 0 ? "M" : "L"}${px.toFixed(1)},${py.toFixed(1)}`)
    .join(" ");
  const area = `${line} L${points[points.length - 1][0].toFixed(1)},${h - pad} L${pad},${h - pad} Z`;

  const flat = max <= 1; // no meaningful traffic yet
  // The signal color is reserved for a live kernel; an idle kernel's throughput
  // recedes to a quiet neutral line.
  const stroke = flat ? "var(--line-strong)" : live ? "var(--signal)" : "var(--muted)";

  return (
    <svg
      width={w}
      height={h}
      viewBox={`0 0 ${w} ${h}`}
      preserveAspectRatio="none"
      className={live && !flat ? "live-wire rounded-[3px]" : ""}
      aria-hidden="true"
    >
      <defs>
        <linearGradient id={`spark-${id}`} x1="0" y1="0" x2="0" y2="1">
          <stop offset="0%" stopColor="var(--signal)" stopOpacity="0.2" />
          <stop offset="100%" stopColor="var(--signal)" stopOpacity="0" />
        </linearGradient>
      </defs>
      {live && !flat && <path d={area} fill={`url(#spark-${id})`} />}
      <path
        d={line}
        fill="none"
        stroke={stroke}
        strokeWidth="1.5"
        strokeLinecap="round"
        strokeLinejoin="round"
        opacity={flat ? 0.6 : live ? 1 : 0.75}
      />
      {!flat && (
        <circle
          cx={points[points.length - 1][0]}
          cy={points[points.length - 1][1]}
          r="1.9"
          fill={live ? "var(--violet)" : "var(--muted)"}
        />
      )}
    </svg>
  );
}
