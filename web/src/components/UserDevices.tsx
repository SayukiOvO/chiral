import { useEffect, useState } from "react";
import {
  api,
  type OnlineEstimate,
  type RecentUserAddress,
  type UserDevice,
  type UserOnlineStatus,
} from "../api";
import { relativeTime } from "../format";
import { useT } from "../lib/i18n";

export type ExactObservationKind = "complete" | "partial" | "unknown";
export type RecentObservationKind = "observed" | "not-observed" | "unknown";

/** A zero without a current reporting node is unknown, never "offline". */
export function exactObservationKind(
  status: Pick<UserOnlineStatus, "recording" | "partial" | "nodes_reporting">,
): ExactObservationKind {
  if (!status.recording || status.nodes_reporting <= 0) return "unknown";
  return status.partial ? "partial" : "complete";
}

/** Failed/stale reads never reuse a previous positive value as live evidence. */
export function recentObservationKind(estimate: OnlineEstimate): RecentObservationKind {
  if (!estimate.read_ok || estimate.nodes_reporting <= 0 || estimate.retrieved_at <= 0) {
    return "unknown";
  }
  return estimate.observed_online ? "observed" : "not-observed";
}

interface AddressResult {
  devices: UserDevice[];
  recent: RecentUserAddress[];
}

/**
 * Current exact observations, incomplete recent-history observations, and the
 * source addresses behind them. The first two are read-level aggregate data;
 * the addresses remain superadmin-only and every successful read is audited.
 */
export function UserDevices({ userId, online }: { userId: string; online: UserOnlineStatus }) {
  const { t, tf } = useT();
  const [addresses, setAddresses] = useState<AddressResult | null>(null);
  const [error, setError] = useState("");

  useEffect(() => {
    let cancelled = false;
    api
      .userDevices(userId)
      .then(
        (r) =>
          !cancelled &&
          setAddresses({ devices: r.devices, recent: r.recent_addresses ?? [] }),
      )
      .catch((e) => !cancelled && setError((e as Error).message));
    return () => {
      cancelled = true;
    };
  }, [userId]);

  const exactKind = exactObservationKind(online);
  const estimate = online.estimate;
  const recentKind = estimate ? recentObservationKind(estimate) : null;

  return (
    <div className="flex flex-col gap-5">
      <section>
        <div className="mb-1.5 text-xs font-medium text-muted">{t("实时观测地址")}</div>
        <div className="flex flex-wrap items-baseline gap-x-3 gap-y-1">
          <span
            className={
              exactKind === "unknown"
                ? "font-mono text-sm text-faint"
                : exactKind === "partial"
                  ? "font-mono text-sm text-warn"
                  : online.count > 0
                    ? "font-mono text-sm text-online"
                    : "font-mono text-sm text-faint"
            }
          >
            {exactKind === "unknown"
              ? t("未知")
              : exactKind === "partial"
                ? `≥${online.count}`
                : online.count}
            {exactKind !== "unknown" && online.device_limit > 0 && (
              <span className="text-faint"> / {online.device_limit}</span>
            )}
          </span>
          {online.nodes_reporting > 0 && (
            <span className="text-xs text-faint">
              {tf("{n} 个节点报告", { n: online.nodes_reporting })}
            </span>
          )}
        </div>
        <p className="mt-1 text-xs text-faint">
          {exactKind === "unknown"
            ? t("当前没有可用的实时观测；零值不能解释为离线。")
            : exactKind === "partial"
              ? t("实时观测不完整；该数量仅为已观测到的下限。")
              : t("仅统计提供完整实时快照的节点。")}
        </p>
      </section>

      {estimate && recentKind && (
        <section>
          <div className="mb-1.5 text-xs font-medium text-muted">{t("近期在线（近似）")}</div>
          <div className="flex flex-wrap items-baseline gap-x-3 gap-y-1">
            <span className={recentKind === "unknown" ? "text-sm text-faint" : "text-sm text-muted"}>
              {recentKind === "unknown"
                ? t("状态未知")
                : recentKind === "observed"
                  ? tf("近期在线记录：已观察到；{n} 个近期来源地址", {
                      n: estimate.recent_address_count,
                    })
                  : tf("近期在线记录：未观察到；{n} 个近期来源地址", {
                      n: estimate.recent_address_count,
                    })}
            </span>
            {recentKind !== "unknown" && (
              <span className="text-xs text-faint">
                {tf("{n} 个节点提供近期记录", { n: estimate.nodes_reporting })}
                <span className="mx-1.5">·</span>
                {tf("记录获取于 {when}", { when: relativeTime(estimate.retrieved_at) })}
              </span>
            )}
          </div>
          <p className="mt-1 text-xs text-faint">
            {recentKind === "unknown"
              ? t("近期记录读取失败或已过期；旧值不作为在线依据。")
              : recentKind === "observed"
                ? t("这是不完整的近期记录，不表示当前仍在线，也不参与地址上限比较。")
                : t("未观察到不表示当前离线；近期地址也不参与地址上限比较。")}
          </p>
        </section>
      )}

      <section>
        <div className="mb-1.5 text-xs font-medium text-muted">{t("具体来源地址")}</div>
        {error ? (
          <p className="text-sm text-muted">
            {/unauthorized|forbidden|403/i.test(error)
              ? t("仅超级管理员可查看具体地址。")
              : error}
          </p>
        ) : !addresses ? (
          <p className="text-sm text-faint">{t("加载中…")}</p>
        ) : (
          <AddressLists devices={addresses.devices} recent={addresses.recent} />
        )}
      </section>
    </div>
  );
}

export function AddressLists({ devices, recent }: AddressResult) {
  const { t, tf } = useT();
  return (
    <div className="flex flex-col gap-4">
      <div>
        <div className="mb-1 text-[11px] font-medium uppercase tracking-[0.07em] text-faint">
          {t("实际观测地址历史")}
        </div>
        {devices.length === 0 ? (
          <p className="text-sm text-muted">{t("暂无实际观测地址记录。")}</p>
        ) : (
          <div className="flex flex-col gap-1.5">
            {devices.map((d) => (
              <div key={`${d.node_id}:${d.ip}`} className="flex flex-wrap items-baseline gap-x-3 text-sm">
                <span className="font-mono text-[13px]">{d.ip}</span>
                <span className="text-xs text-faint">
                  {d.node_name || d.node_id || t("节点已删除")}
                  <span className="mx-1.5">·</span>
                  {tf("最近 {when}", { when: relativeTime(d.last_seen) })}
                </span>
              </div>
            ))}
          </div>
        )}
      </div>

      <div>
        <div className="mb-1 text-[11px] font-medium uppercase tracking-[0.07em] text-faint">
          {t("近期来源地址（近似）")}
        </div>
        {recent.length === 0 ? (
          <p className="text-sm text-muted">{t("近期记录未返回可展示的地址。")}</p>
        ) : (
          <div className="flex flex-col gap-1.5">
            {recent.map((a) => (
              <div key={`${a.node_id}:${a.ip}`} className="flex flex-wrap items-baseline gap-x-3 text-sm">
                <span className="font-mono text-[13px]">{a.ip}</span>
                <span className="text-xs text-faint">
                  {a.node_name || a.node_id || t("节点已删除")}
                  <span className="mx-1.5">·</span>
                  {tf("记录获取于 {when}", { when: relativeTime(a.retrieved_at) })}
                </span>
              </div>
            ))}
          </div>
        )}
      </div>

      <p className="text-xs text-faint">{t("每次查看均记入审计日志。")}</p>
    </div>
  );
}
