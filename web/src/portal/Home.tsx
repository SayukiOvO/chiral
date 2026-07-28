import { useEffect, useState } from "react";
import { portal, type Me } from "./api";
import { QuotaBar } from "../components/QuotaBar";
import { StatusDot } from "../components/StatusDot";
import { Empty, ErrorBar } from "../components/primitives";
import { Button } from "../components/ui";
import { SubscriptionCard } from "./SubscriptionCard";
import { NodeTraffic } from "./NodeTraffic";
import { expiryLabel, periodLabel, relativeTime, splitBytes } from "../format";
import { useT } from "../lib/i18n";

/**
 * Everything a subscriber came for, on one screen and in one request.
 *
 * No polling. The console refreshes every ten seconds because an operator is
 * watching a fleet; a customer is checking their quota, and N customers
 * polling a page backed by a single-connection SQLite handle is a cheap way
 * to contend with config assembly for the write lock.
 */
export function Home() {
  const { t, tf } = useT();
  const [me, setMe] = useState<Me | null>(null);
  const [error, setError] = useState("");
  const [refreshing, setRefreshing] = useState(false);

  async function load() {
    setRefreshing(true);
    try {
      setMe(await portal.me());
      setError("");
    } catch (e) {
      setError((e as Error).message);
    } finally {
      setRefreshing(false);
    }
  }

  useEffect(() => {
    load();
  }, []);

  if (error && !me) return <ErrorBar text={error} />;
  if (!me) return null;

  const [used, usedUnit] = splitBytes(me.account.used_bytes);

  return (
    <div className="animate-rise">
      {error && <ErrorBar text={error} />}

      {me.account.status !== "active" && <StatusBanner status={me.account.status} />}

      <section className="rounded-2xl border border-line bg-surface px-5 py-5">
        <div className="flex items-baseline justify-between gap-4">
          <div>
            <div className="text-[11px] font-medium uppercase tracking-[0.07em] text-faint">
              {t("已用流量")}
            </div>
            <div className="mt-1 font-mono text-[32px] leading-none tnum">
              {used}
              <span className="ml-1.5 text-base text-faint">{usedUnit}</span>
            </div>
          </div>
          <div className="text-right text-sm text-muted">
            <div>{expiryLabel(me.account.expires_at)}</div>
            {me.account.renew_period > 0 && (
              <div className="mt-0.5 text-xs text-faint">
                {periodLabel(me.account.renew_period)}
                <span className="mx-1">·</span>
                {t("续期时清零")}
              </div>
            )}
          </div>
        </div>
        <div className="mt-4">
          <QuotaBar used={me.account.used_bytes} quota={me.account.quota_bytes} />
        </div>
      </section>

      <SubscriptionCard sub={me.subscription} />

      <section className="mt-6">
        <h2 className="mb-3 font-display text-[15px] font-semibold tracking-tight">
          {t("线路")}
        </h2>
        {me.nodes.length === 0 ? (
          <Empty>{t("账户尚未开通任何线路。")}</Empty>
        ) : (
          <div className="flex flex-col gap-2">
            {me.nodes.map((n) => (
              <article
                key={n.id}
                className="rounded-2xl border border-line bg-surface px-4 py-3.5"
              >
                <div className="flex items-center justify-between gap-4">
                  <div className="flex min-w-0 items-center gap-2.5">
                    <StatusDot online={n.availability === "available"} />
                    <span className="truncate text-sm">
                      {n.name || tf("线路 {n}", { n: String(n.index).padStart(2, "0") })}
                    </span>
                  </div>
                  <span className="shrink-0 text-xs text-faint">
                    {AVAILABILITY[n.availability] ? t(AVAILABILITY[n.availability]) : ""}
                  </span>
                </div>
                <div className="mt-3">
                  <NodeTraffic points={n.traffic_24h} />
                </div>
              </article>
            ))}
          </div>
        )}
      </section>

      {me.features.devices && me.devices && (
        <section className="mt-6">
          <h2 className="mb-1 font-display text-[15px] font-semibold tracking-tight">
            {t("并发地址")}
          </h2>
          {/* "Addresses", not "devices": the kernel counts distinct source
              addresses, and on a shared account these include whoever else is
              using the credentials. */}
          <p className="mb-3 text-xs text-faint">
            {me.account.device_limit > 0
              ? tf("最近连接的来源地址，上限 {n} 个。", { n: me.account.device_limit })
              : t("最近连接过的来源地址。")}
          </p>
          {me.devices.length === 0 ? (
            <Empty>{t("暂无连接记录。")}</Empty>
          ) : (
            <div className="rounded-2xl border border-line bg-surface">
              {me.devices.map((d) => (
                <div
                  key={d.ip}
                  className="flex items-baseline justify-between gap-4 border-b border-line px-4 py-3 last:border-0"
                >
                  <span className="font-mono text-[13px]">{d.ip}</span>
                  <span className="text-xs text-faint">{relativeTime(d.last_seen)}</span>
                </div>
              ))}
            </div>
          )}
        </section>
      )}

      <div className="mt-7 flex justify-center">
        <Button size="sm" variant="ghost" onClick={load} disabled={refreshing}>
          {refreshing ? t("刷新中…") : t("刷新")}
        </Button>
      </div>
    </div>
  );
}

const AVAILABILITY: Record<string, string> = {
  available: "正常",
  provisioning: "开通中",
  unavailable: "暂不可用",
};

/**
 * One sentence about why the account is not working, in the reader's terms.
 *
 * Amber rather than red for everything except a suspension: a spent quota or a
 * lapsed date is a normal state of affairs with an obvious remedy, not a fault.
 */
function StatusBanner({ status }: { status: string }) {
  const { t } = useT();
  const copy: Record<string, string> = {
    suspended: "账户已停用，请联系管理员。",
    expired: "订阅已到期，请联系管理员续期。",
    quota_exhausted: "本期流量已用完。",
    no_access: "账户已创建，尚未开通线路，请联系管理员。",
  };
  const message = copy[status];
  if (!message) return null;
  const severe = status === "suspended";
  return (
    <div
      className="mb-5 rounded-xl px-4 py-3 text-sm"
      style={{
        color: severe ? "var(--danger)" : "var(--warn)",
        border: `1px solid color-mix(in srgb, ${severe ? "var(--danger)" : "var(--warn)"} 35%, transparent)`,
        background: `color-mix(in srgb, ${severe ? "var(--danger)" : "var(--warn)"} 10%, transparent)`,
      }}
    >
      {t(message)}
    </div>
  );
}
