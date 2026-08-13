import { useState, type ReactNode } from "react";
import { api, type Node } from "../api";
import { bytes, percent, relativeTime, splitBitrate } from "../format";
import { cn } from "../lib/cn";
import { KernelState } from "./KernelState";
import { NodeHistory } from "./NodeHistory";
import { Sparkline } from "./Sparkline";
import { StatusDot } from "./StatusDot";
import { IconButton } from "./ui";
import { CheckIcon, ChevronIcon, PencilIcon, RestartIcon, SlidersIcon, TrashIcon } from "./icons";
import { NodeNameDialog } from "./NodeNameDialog";
import { useT } from "../lib/i18n";

export function NodeRoster({
  nodes,
  history,
  onChanged,
  onConfigure,
}: {
  nodes: Node[];
  history: Map<string, number[]>;
  onChanged: () => void;
  onConfigure: (n: Node) => void;
}) {
  if (nodes.length === 0) return <EmptyState />;
  return (
    <div className="flex flex-col gap-2.5">
      {nodes.map((n) => (
        <NodeCard
          key={n.id}
          node={n}
          history={history.get(n.id) ?? []}
          onChanged={onChanged}
          onConfigure={() => onConfigure(n)}
        />
      ))}
    </div>
  );
}

function NodeCard({
  node,
  history,
  onChanged,
  onConfigure,
}: {
  node: Node;
  history: number[];
  onChanged: () => void;
  onConfigure: () => void;
}) {
  const { t } = useT();
  const [confirming, setConfirming] = useState(false);
  const [busy, setBusy] = useState(false);
  const [restarted, setRestarted] = useState(false);
  const [expanded, setExpanded] = useState(false);
  const [renaming, setRenaming] = useState(false);
  const m = node.metrics;
  const running = node.xray_state === "RUNNING";
  const total = (m?.net_tx_bps ?? 0) + (m?.net_rx_bps ?? 0);
  const [rate, rateUnit] = splitBitrate(total);

  async function remove() {
    setBusy(true);
    try {
      await api.deleteNode(node.id);
      onChanged();
    } finally {
      setBusy(false);
      setConfirming(false);
    }
  }

  async function restart() {
    setBusy(true);
    try {
      await api.restartXray(node.id);
      setRestarted(true);
      setTimeout(() => setRestarted(false), 1600);
    } catch (e) {
      alert(t("重启失败：") + (e as Error).message);
    } finally {
      setBusy(false);
    }
  }

  return (
    <article
      className={cn(
        "group rounded-2xl border border-line bg-surface px-5 py-4 shadow-[var(--shadow-card)] transition-all duration-150",
        "hover:border-line-strong hover:shadow-[var(--shadow-lift)]",
      )}
    >
      <div className="flex items-start justify-between gap-4">
        <div className="flex items-center gap-3 min-w-0">
          <StatusDot online={node.online} />
          <div className="min-w-0">
            {/* The name reads as a heading, and headings do not look
                pressable — the chevron is what says "this opens". Always
                visible rather than hover-only, because on a phone hover
                never happens. */}
            <button
              onClick={() => setExpanded((v) => !v)}
              className="flex min-w-0 items-center gap-1.5 font-display text-[15px] font-semibold tracking-tight hover:text-signal"
              aria-expanded={expanded}
              title={t("展开配置历史")}
            >
              <span className="truncate">{node.name}</span>
              <ChevronIcon
                size={12}
                className={cn(
                  "shrink-0 text-faint transition-transform duration-150",
                  expanded ? "-rotate-90" : "rotate-90",
                )}
              />
            </button>
            <div className="font-mono text-xs text-faint truncate">
              {node.hostname || "—"}
              {node.dialable ? ` · ${node.dialable}` : ""}
            </div>
            {/* The portal shows this instead of the internal name. Flagged when
                unset, because an unnamed line reads as "线路 03" to every
                subscriber until someone fills it in. */}
            <div className="truncate text-xs">
              {node.display_name ? (
                <span className="text-muted">{node.display_name}</span>
              ) : (
                <span className="text-warn">{t("未设对客名称")}</span>
              )}
            </div>
          </div>
        </div>

        {confirming ? (
          <div className="flex items-center gap-2 text-sm">
            <span className="text-muted hidden sm:inline">{t("删除此节点？")}</span>
            <button
              onClick={() => setConfirming(false)}
              className="rounded-lg px-2.5 py-1 text-muted hover:text-ink"
            >
              {t("取消")}
            </button>
            <button
              onClick={remove}
              disabled={busy}
              className="rounded-lg px-2.5 py-1 font-medium text-danger hover:bg-[color-mix(in_srgb,var(--danger)_12%,transparent)] disabled:opacity-50"
            >
              {t("删除")}
            </button>
          </div>
        ) : (
          <div className="flex items-center gap-1 opacity-70 transition-opacity group-hover:opacity-100">
            <IconButton label={t("重命名")} onClick={() => setRenaming(true)}>
              <PencilIcon size={16} />
            </IconButton>
            <IconButton label={t("配置")} onClick={onConfigure}>
              <SlidersIcon size={16} />
            </IconButton>
            <IconButton
              label={restarted ? t("已发送重启") : t("重启内核")}
              onClick={restart}
              disabled={busy || !node.online}
            >
              {restarted ? (
                <CheckIcon size={16} className="text-online" />
              ) : (
                <RestartIcon size={16} />
              )}
            </IconButton>
            <IconButton
              label={t("删除节点")}
              onClick={() => setConfirming(true)}
              className="hover:text-danger"
            >
              <TrashIcon size={16} />
            </IconButton>
          </div>
        )}
      </div>

      <div className="mt-4 flex flex-wrap items-end gap-x-8 gap-y-3 pl-[22px]">
        <Field label={t("内核")}>
          <KernelState
            state={node.xray_state}
            version={node.xray_version}
            installed={node.xray_installed_version}
          />
        </Field>
        <Field label={t("流量 ↑↓")}>
          <div className="flex items-center gap-3">
            <Sparkline data={history} live={running} />
            {node.online ? (
              <span className="font-mono text-sm tnum">
                {rate}
                <span className="ml-1 text-xs text-faint">{rateUnit}</span>
              </span>
            ) : (
              <Dash />
            )}
          </div>
        </Field>
        <Field label={t("CPU / 内存")}>
          {m ? (
            <span className="font-mono text-sm tnum">
              {percent(m.cpu_percent)}
              <span className="mx-1 text-faint">·</span>
              {bytes(m.mem_used_bytes)}
            </span>
          ) : (
            <Dash />
          )}
        </Field>
        <Field label={t("最近心跳")}>
          <span className="text-sm text-muted">{relativeTime(node.last_seen_at)}</span>
        </Field>
      </div>

      {/* Mounted only when opened: the history is a per-node request, and
          fetching it for every card on the page would be wasteful. */}
      {expanded && (
        <div className="mt-4 border-t border-line pt-4 pl-[22px]">
          <NodeHistory nodeId={node.id} />
        </div>
      )}

      {renaming && (
        <NodeNameDialog node={node} onClose={() => setRenaming(false)} onSaved={onChanged} />
      )}
    </article>
  );
}

function Field({ label, children }: { label: string; children: ReactNode }) {
  return (
    <div className="min-w-0">
      <div className="mb-1 text-[11px] font-medium uppercase tracking-[0.07em] text-faint">
        {label}
      </div>
      <div className="h-[34px] flex items-center">{children}</div>
    </div>
  );
}

function Dash() {
  return <span className="text-faint text-sm">—</span>;
}

function EmptyState() {
  const { t } = useT();
  return (
    <div className="rounded-2xl border border-dashed border-line-strong bg-surface px-8 py-16 text-center">
      <p className="text-ink font-medium">{t("还没有节点")}</p>
      <p className="mt-1.5 text-sm text-muted">
        {t("新增节点后，此处显示其实时状态。")}
      </p>
    </div>
  );
}
