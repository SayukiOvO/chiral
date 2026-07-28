import { useEffect, useState } from "react";
import { api, type ConfigVersion } from "../api";
import { Button } from "./ui";
import { relativeTime } from "../format";
import { useT } from "../lib/i18n";

/**
 * A node's config history, with a way back.
 *
 * Rolling back is not a distinct instruction to the agent: the old bytes are
 * pushed as a NEW version, so the stored config stays the single source of
 * truth and a reconnecting agent reconciles to it like any other. That is why
 * this list only ever grows downward and there is no "current version"
 * pointer to move — the newest row is always what the node should be running.
 */
export function ConfigHistory({
  nodeId,
  onRolledBack,
}: {
  nodeId: string;
  onRolledBack: () => void;
}) {
  const { t, tf } = useT();
  const [versions, setVersions] = useState<ConfigVersion[] | null>(null);
  const [depth, setDepth] = useState(20);
  const [error, setError] = useState("");
  const [busy, setBusy] = useState(0);

  async function refresh() {
    try {
      const r = await api.configVersions(nodeId);
      setVersions(r.versions);
      setDepth(r.depth);
      setError("");
    } catch (e) {
      setError((e as Error).message);
    }
  }

  useEffect(() => {
    refresh();
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [nodeId]);

  async function rollback(version: number) {
    if (!confirm(tf("回滚到版本 {v}？该内容将作为新版本重新下发。", { v: version }))) return;
    setBusy(version);
    setError("");
    try {
      await api.rollbackConfig(nodeId, version);
      await refresh();
      onRolledBack();
    } catch (e) {
      setError((e as Error).message);
    } finally {
      setBusy(0);
    }
  }

  if (error && !versions) return <p className="text-sm text-danger">{error}</p>;
  if (!versions) return null;
  if (versions.length === 0) {
    return <p className="text-sm text-muted">{t("暂无下发记录。")}</p>;
  }

  return (
    <div>
      {error && <p className="mb-2 text-sm text-danger">{error}</p>}
      <div className="overflow-hidden rounded-xl border border-line">
        {versions.map((v, i) => (
          <div
            key={v.version}
            className="flex items-center justify-between gap-3 border-b border-line px-3 py-2 text-sm last:border-0"
          >
            <div className="flex min-w-0 items-baseline gap-2.5">
              <span className="font-mono text-[13px] tnum">v{v.version}</span>
              {i === 0 && <span className="text-xs text-online">{t("当前")}</span>}
              <span className="text-xs text-faint">{relativeTime(v.created_at)}</span>
              {v.applied === -1 && (
                <span className="truncate text-xs text-danger" title={v.error}>
                  {t("节点拒绝")}
                </span>
              )}
              {v.applied === 0 && i === 0 && (
                <span className="text-xs text-muted">{t("等待确认")}</span>
              )}
            </div>
            {/* A version the node itself rejected is not somewhere to go back
                to, and the newest one is where we already are. */}
            {i > 0 && v.applied !== -1 && (
              <Button
                size="sm"
                variant="ghost"
                disabled={busy !== 0}
                onClick={() => rollback(v.version)}
              >
                {busy === v.version ? t("回滚中…") : t("回滚到此版本")}
              </Button>
            )}
          </div>
        ))}
      </div>
      <p className="mt-2 text-xs text-faint">
        {tf("仅保留最近 {n} 个版本。回滚前会重新执行 xray -test：内核升级后，旧配置未必仍合法。", {
          n: depth,
        })}
      </p>
    </div>
  );
}
