import { api, type Node } from "../api";
import { bitrate, bytes, percent, relativeTime } from "../format";

export function NodeTable({
  nodes,
  onChanged,
}: {
  nodes: Node[];
  onChanged: () => void;
}) {
  if (nodes.length === 0) {
    return (
      <div className="rounded-xl border border-dashed border-[var(--color-border)] p-10 text-center text-sm text-[var(--color-muted)]">
        还没有节点。点击「新增节点」生成加入命令。
      </div>
    );
  }

  async function remove(n: Node) {
    if (!confirm(`删除节点「${n.name}」？该节点的凭证会立即失效。`)) return;
    await api.deleteNode(n.id);
    onChanged();
  }

  async function restart(n: Node) {
    try {
      await api.restartXray(n.id);
      alert("已发送重启指令");
    } catch (e) {
      alert(`重启失败：${(e as Error).message}`);
    }
  }

  return (
    <div className="overflow-x-auto rounded-xl border border-[var(--color-border)] bg-[var(--color-surface)]">
      <table className="w-full text-sm">
        <thead>
          <tr className="border-b border-[var(--color-border)] text-left text-[var(--color-muted)]">
            <Th>节点</Th>
            <Th>状态</Th>
            <Th>Xray</Th>
            <Th>CPU / 内存</Th>
            <Th>网速 ↑ / ↓</Th>
            <Th>最近心跳</Th>
            <Th> </Th>
          </tr>
        </thead>
        <tbody>
          {nodes.map((n) => (
            <tr
              key={n.id}
              className="border-b border-[var(--color-border)] last:border-0 hover:bg-black/[0.02] dark:hover:bg-white/[0.02]"
            >
              <Td>
                <div className="font-medium">{n.name}</div>
                <div className="text-xs text-[var(--color-muted)]">
                  {n.hostname || "—"}
                  {n.public_ip ? ` · ${n.public_ip}` : ""}
                </div>
              </Td>
              <Td>
                <StatusDot online={n.online} />
              </Td>
              <Td>
                <XrayBadge state={n.xray_state} version={n.xray_version} />
              </Td>
              <Td>
                {n.metrics ? (
                  <span>
                    {percent(n.metrics.cpu_percent)} ·{" "}
                    {bytes(n.metrics.mem_used_bytes)}
                  </span>
                ) : (
                  <Muted />
                )}
              </Td>
              <Td>
                {n.metrics ? (
                  <span className="tabular-nums">
                    {bitrate(n.metrics.net_tx_bps)} /{" "}
                    {bitrate(n.metrics.net_rx_bps)}
                  </span>
                ) : (
                  <Muted />
                )}
              </Td>
              <Td className="text-[var(--color-muted)]">
                {relativeTime(n.last_seen_at)}
              </Td>
              <Td>
                <div className="flex gap-1 justify-end">
                  <ActionBtn onClick={() => restart(n)} disabled={!n.online}>
                    重启
                  </ActionBtn>
                  <ActionBtn danger onClick={() => remove(n)}>
                    删除
                  </ActionBtn>
                </div>
              </Td>
            </tr>
          ))}
        </tbody>
      </table>
    </div>
  );
}

function Th({ children }: { children: React.ReactNode }) {
  return <th className="px-4 py-2.5 font-medium whitespace-nowrap">{children}</th>;
}

function Td({
  children,
  className = "",
}: {
  children: React.ReactNode;
  className?: string;
}) {
  return <td className={`px-4 py-3 align-middle ${className}`}>{children}</td>;
}

function Muted() {
  return <span className="text-[var(--color-muted)]">—</span>;
}

function StatusDot({ online }: { online: boolean }) {
  return (
    <span className="inline-flex items-center gap-1.5">
      <span
        className={`h-2 w-2 rounded-full ${
          online ? "bg-emerald-500" : "bg-gray-400"
        }`}
      />
      <span className={online ? "" : "text-[var(--color-muted)]"}>
        {online ? "在线" : "离线"}
      </span>
    </span>
  );
}

function XrayBadge({ state, version }: { state?: string; version?: string }) {
  if (!state) return <Muted />;
  const color =
    state === "RUNNING"
      ? "text-emerald-600 dark:text-emerald-400"
      : state === "ERROR"
        ? "text-red-500"
        : "text-[var(--color-muted)]";
  return (
    <span className={`text-xs ${color}`}>
      {state}
      {version ? ` · ${version}` : ""}
    </span>
  );
}

function ActionBtn({
  children,
  onClick,
  danger,
  disabled,
}: {
  children: React.ReactNode;
  onClick: () => void;
  danger?: boolean;
  disabled?: boolean;
}) {
  return (
    <button
      onClick={onClick}
      disabled={disabled}
      className={`rounded-md border px-2 py-1 text-xs disabled:opacity-40 disabled:cursor-not-allowed ${
        danger
          ? "border-red-500/40 text-red-500 hover:bg-red-500/10"
          : "border-[var(--color-border)] hover:border-[var(--color-accent)]"
      }`}
    >
      {children}
    </button>
  );
}
