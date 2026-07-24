import type { Node } from "../api";
import { splitBitrate } from "../format";
import { ArrowDownIcon, ArrowUpIcon } from "./icons";
import type { ReactNode } from "react";

/**
 * Fleet vitals — an instrument rail, not a hero. Precise mono figures with
 * eyebrow labels and hairline dividers; the roster below is the star.
 */
export function LiveRail({ nodes }: { nodes: Node[] }) {
  const online = nodes.filter((n) => n.online);
  const tx = sum(online, (n) => n.metrics?.net_tx_bps);
  const rx = sum(online, (n) => n.metrics?.net_rx_bps);
  const running = nodes.filter((n) => n.xray_state === "RUNNING").length;

  const [txv, txu] = splitBitrate(tx);
  const [rxv, rxu] = splitBitrate(rx);

  return (
    <div className="grid grid-cols-2 divide-x divide-y divide-line rounded-2xl border border-line bg-surface shadow-[var(--shadow-card)] sm:grid-cols-4 sm:divide-y-0">
      <Cell label="在线节点">
        <span className="font-mono text-2xl font-medium tracking-tight tnum">
          {online.length}
          <span className="text-faint">/{nodes.length}</span>
        </span>
      </Cell>
      <Cell label="运行内核">
        <span className="font-mono text-2xl font-medium tracking-tight tnum">{running}</span>
      </Cell>
      <Cell label="总上行" icon={<ArrowUpIcon size={13} />}>
        <Rate value={txv} unit={txu} />
      </Cell>
      <Cell label="总下行" icon={<ArrowDownIcon size={13} />}>
        <Rate value={rxv} unit={rxu} />
      </Cell>
    </div>
  );
}

function Cell({ label, icon, children }: { label: string; icon?: ReactNode; children: ReactNode }) {
  return (
    <div className="px-5 py-4">
      <div className="mb-1.5 flex items-center gap-1.5 text-[11px] font-medium uppercase tracking-[0.08em] text-faint">
        {icon}
        {label}
      </div>
      {children}
    </div>
  );
}

function Rate({ value, unit }: { value: string; unit: string }) {
  return (
    <span className="font-mono text-2xl font-medium tracking-tight tnum">
      {value}
      <span className="ml-1 text-sm text-faint">{unit}</span>
    </span>
  );
}

function sum(nodes: Node[], pick: (n: Node) => number | undefined): number {
  return nodes.reduce((acc, n) => acc + (pick(n) ?? 0), 0);
}
