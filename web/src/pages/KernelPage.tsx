import { useEffect, useState } from "react";
import {
  api,
  type Node,
  type XrayAvailable,
  type XrayInstall,
  type XrayUpgrade,
} from "../api";
import { Button } from "../components/ui";
import { Empty, ErrorBar, inputCls } from "../components/primitives";
import { relativeTime } from "../format";
import { cn } from "../lib/cn";
import { useT } from "../lib/i18n";

/**
 * Runtime Xray-core upgrades: one node first, then a person decides.
 *
 * The page is built around the decision it exists to support — should the rest
 * of the fleet follow the canary — so the two things that decision rests on are
 * the ones given room: whether the canary was CONFIRMED serving or merely did
 * not fall over, and, when something rolled back, the kernel's own words about
 * why. Everything else is a table.
 */
export function KernelPage() {
  const { t } = useT();
  const [avail, setAvail] = useState<XrayAvailable | null>(null);
  const [availError, setAvailError] = useState("");
  const [nodes, setNodes] = useState<Node[]>([]);
  const [installs, setInstalls] = useState<XrayInstall[]>([]);
  const [upgrade, setUpgrade] = useState<XrayUpgrade | null>(null);
  const [canary, setCanary] = useState("");
  const [error, setError] = useState("");
  const [busy, setBusy] = useState(false);
  const [loaded, setLoaded] = useState(false);

  async function refresh() {
    try {
      const [ns, ins, up] = await Promise.all([
        api.listNodes(),
        api.xrayInstalls(),
        api.xrayUpgrade(),
      ]);
      setNodes(ns.nodes);
      setInstalls(ins);
      setUpgrade(up.active);
      setError("");
    } catch (e) {
      setError((e as Error).message);
    } finally {
      setLoaded(true);
    }
    // Upstream is fetched separately: GitHub being unreachable is an ordinary
    // condition for a panel behind a restricted egress, and it must not take
    // the rest of the page with it.
    try {
      setAvail(await api.xrayAvailable());
      setAvailError("");
    } catch (e) {
      setAvailError((e as Error).message);
    }
  }

  useEffect(() => {
    refresh();
    // An upgrade moves on the agents' reports, so the page has to keep asking.
    const id = setInterval(refresh, 5000);
    return () => clearInterval(id);
  }, []);

  async function act(fn: () => Promise<unknown>) {
    setBusy(true);
    try {
      await fn();
      setError("");
      await refresh();
    } catch (e) {
      setError((e as Error).message);
    } finally {
      setBusy(false);
    }
  }

  const installByNode = new Map(installs.map((i) => [i.node_id, i]));
  const canaryNode = nodes.find((n) => n.id === upgrade?.canary_node_id);

  return (
    <div>
      <div className="mb-6 animate-rise">
        <h1 className="font-display text-[26px] font-semibold tracking-tight">{t("内核")}</h1>
        <p className="mt-1 text-sm text-muted">
          {t("先升一台，确认没问题再放行到全部节点。起不来会自动回滚，节点继续用旧版本服务。")}
        </p>
      </div>

      {error && <ErrorBar text={error} />}

      <section className="mb-6 rounded-2xl border border-line bg-paper p-5">
        <div className="flex flex-wrap items-center justify-between gap-4">
          <div>
            <div className="text-[11px] font-medium uppercase tracking-[0.07em] text-faint">
              {t("上游最新版本")}
            </div>
            {avail ? (
              <div className="mt-1 flex items-center gap-2">
                <span className="font-mono text-lg tnum">{avail.version}</span>
                {avail.prerelease && (
                  <span className="rounded-md bg-signal-soft px-1.5 py-0.5 text-[11px] text-muted">
                    {t("预发布")}
                  </span>
                )}
                {avail.panel_has && (
                  <span className="text-[11px] text-online">{t("面板已具备校验能力")}</span>
                )}
              </div>
            ) : (
              <div className="mt-1 text-sm text-muted">
                {availError ? t("取不到上游版本") : t("查询中…")}
              </div>
            )}
            {availError && <p className="mt-1 max-w-lg text-xs text-muted">{availError}</p>}
          </div>

          {!upgrade && avail && (
            <div className="flex items-center gap-2">
              <select
                className={cn(inputCls, "w-48")}
                value={canary}
                onChange={(e) => setCanary(e.target.value)}
              >
                <option value="">{t("选一台做金丝雀")}</option>
                {/* A node whose architecture is unknown has no release asset
                    to be handed, so it cannot be a canary. Disabled with the
                    reason attached beats a button that fails on click. */}
                {nodes.map((n) => (
                  <option key={n.id} value={n.id} disabled={!n.platform}>
                    {n.name}
                    {n.xray_version ? ` · ${n.xray_version}` : ""}
                    {!n.platform ? ` · ${t("架构未知")}` : ""}
                  </option>
                ))}
              </select>
              <Button
                variant="primary"
                disabled={busy || !canary}
                onClick={() => act(() => api.startXrayCanary(avail.version, canary))}
              >
                {t("升级这一台")}
              </Button>
            </div>
          )}
        </div>
      </section>

      {upgrade && (
        <UpgradeCard
          upgrade={upgrade}
          canaryName={canaryNode?.name ?? upgrade.canary_node_id}
          busy={busy}
          onPromote={() => act(() => api.promoteXray())}
          onRetry={() => act(() => api.retryXray())}
          onAbandon={() => act(() => api.abandonXray())}
        />
      )}

      <h2 className="mb-2 mt-6 text-[11px] font-medium uppercase tracking-[0.07em] text-faint">
        {t("各节点内核")}
      </h2>
      {loaded && nodes.length === 0 ? (
        <Empty>{t("还没有节点。")}</Empty>
      ) : (
        <div className="overflow-x-auto rounded-2xl border border-line bg-paper">
          <table className="w-full min-w-[640px] text-sm">
            <thead>
              <tr className="border-b border-line text-left text-[11px] uppercase tracking-[0.07em] text-faint">
                <th className="px-4 py-2.5 font-medium">{t("节点")}</th>
                <th className="px-4 py-2.5 font-medium">{t("运行中")}</th>
                <th className="px-4 py-2.5 font-medium">{t("磁盘上")}</th>
                <th className="px-4 py-2.5 font-medium">{t("最近一次升级")}</th>
              </tr>
            </thead>
            <tbody>
              {nodes.map((n) => {
                const inst = installByNode.get(n.id);
                return (
                  <tr key={n.id} className="border-b border-line last:border-0">
                    <td className="px-4 py-2.5">
                      {n.name}
                      {n.id === upgrade?.canary_node_id && (
                        <span className="ml-2 text-[11px] text-muted">{t("金丝雀")}</span>
                      )}
                    </td>
                    <td className="px-4 py-2.5 font-mono tnum text-[13px]">
                      {n.xray_version || <span className="text-faint">—</span>}
                    </td>
                    <td className="px-4 py-2.5 font-mono tnum text-[13px]">
                      {/* Shown only when it differs: in the steady state a second
                          version number is noise, and when it differs it is the
                          whole story. */}
                      {n.xray_installed_version && n.xray_installed_version !== n.xray_version ? (
                        <span className="text-muted">{n.xray_installed_version}</span>
                      ) : (
                        <span className="text-faint">—</span>
                      )}
                    </td>
                    <td className="px-4 py-2.5">
                      {inst ? <PhaseBadge install={inst} /> : <span className="text-faint">—</span>}
                    </td>
                  </tr>
                );
              })}
            </tbody>
          </table>
        </div>
      )}
    </div>
  );
}

function UpgradeCard({
  upgrade,
  canaryName,
  busy,
  onPromote,
  onRetry,
  onAbandon,
}: {
  upgrade: XrayUpgrade;
  canaryName: string;
  busy: boolean;
  onPromote: () => void;
  onRetry: () => void;
  onAbandon: () => void;
}) {
  const { t } = useT();
  const blocked = upgrade.state === "blocked";
  const waiting = upgrade.state === "awaiting_promote";
  return (
    <section
      className={cn(
        "rounded-2xl border p-5",
        blocked ? "border-danger/40" : "border-line",
        "bg-paper",
      )}
    >
      <div className="flex flex-wrap items-start justify-between gap-4">
        <div>
          <div className="flex items-center gap-2">
            <StateBadge state={upgrade.state} />
            <span className="font-mono text-sm tnum">{upgrade.version}</span>
            <span className="text-xs text-muted">
              {t("金丝雀")}: {canaryName}
            </span>
          </div>
          {upgrade.message && (
            <pre className="scroll-slim mt-3 max-h-40 max-w-2xl overflow-auto whitespace-pre-wrap rounded-xl border border-line bg-canvas p-3 font-mono text-[11.5px] leading-relaxed text-muted">
              {upgrade.message}
            </pre>
          )}
          <div className="mt-2 text-xs text-faint">{relativeTime(upgrade.updated_at)}</div>
        </div>
        <div className="flex items-center gap-2">
          {waiting && (
            <Button variant="primary" disabled={busy} onClick={onPromote}>
              {t("放行到全部节点")}
            </Button>
          )}
          {blocked && (
            <Button variant="primary" disabled={busy} onClick={onRetry}>
              {t("修好了，再试一次")}
            </Button>
          )}
          <Button disabled={busy} onClick={onAbandon}>
            {t("结束这次升级")}
          </Button>
        </div>
      </div>
    </section>
  );
}

const STATE_LABEL: Record<string, string> = {
  canary: "金丝雀升级中",
  awaiting_promote: "等待放行",
  promoting: "正在放行",
  done: "已完成",
  blocked: "已阻塞",
};

function StateBadge({ state }: { state: string }) {
  const { t } = useT();
  const tone =
    state === "blocked"
      ? "text-danger"
      : state === "awaiting_promote" || state === "done"
        ? "text-online"
        : "text-muted";
  return (
    <span className={cn("text-xs font-medium", tone)}>{t(STATE_LABEL[state] ?? state)}</span>
  );
}

const PHASE_LABEL: Record<string, string> = {
  UNSPECIFIED: "已下发指令",
  DOWNLOADING: "下载中",
  VERIFYING: "校验中",
  INSTALLED: "已就位（未启用）",
  ACTIVATING: "切换中",
  ACTIVE: "运行中，API 有应答",
  INCONCLUSIVE: "起来了，但无法确认",
  FAILED: "失败",
  ROLLED_BACK: "已回滚",
};

/**
 * ACTIVE is green; INCONCLUSIVE is not.
 *
 * They are both "the kernel is up", and colouring them the same would make
 * every canary on a node without an API inbound look like a confirmed success —
 * which is exactly the rubber stamp the three-valued outcome exists to prevent.
 */
function PhaseBadge({ install }: { install: XrayInstall }) {
  const { t } = useT();
  const tone =
    install.phase === "ACTIVE"
      ? "text-online"
      : install.phase === "FAILED" || install.phase === "ROLLED_BACK"
        ? "text-danger"
        : "text-muted";
  return (
    <span className="inline-flex items-center gap-2" title={install.message || undefined}>
      <span className={cn("text-xs", tone)}>{t(PHASE_LABEL[install.phase] ?? install.phase)}</span>
      <span className="font-mono text-[11px] text-faint tnum">{install.version}</span>
    </span>
  );
}
