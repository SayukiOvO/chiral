import { useEffect, useState } from "react";
import { api, type UserDevice } from "../api";
import { relativeTime } from "../format";
import { useT } from "../lib/i18n";

/**
 * The addresses a user has connected from.
 *
 * Superadmin only, and the panel logs every read. That is not caution for its
 * own sake: the count next door answers every operational question ("is this
 * account shared", "why can't they connect"), while this list answers a
 * different one — where a particular person is — and only the second needs a
 * record of who looked.
 *
 * Mounted lazily, so opening a user does not fetch it (or write the audit
 * entry) unless it is actually asked for.
 */
export function UserDevices({ userId }: { userId: string }) {
  const { t, tf } = useT();
  const [devices, setDevices] = useState<UserDevice[] | null>(null);
  const [error, setError] = useState("");

  useEffect(() => {
    let cancelled = false;
    api
      .userDevices(userId)
      .then((r) => !cancelled && setDevices(r.devices))
      .catch((e) => !cancelled && setError((e as Error).message));
    return () => {
      cancelled = true;
    };
  }, [userId]);

  if (error) {
    return (
      <p className="text-sm text-muted">
        {/unauthorized|forbidden|403/i.test(error)
          ? t("仅超级管理员可查看具体地址。")
          : error}
      </p>
    );
  }
  if (!devices) return <p className="text-sm text-faint">{t("加载中…")}</p>;
  if (devices.length === 0) {
    return <p className="text-sm text-muted">{t("暂无地址记录。")}</p>;
  }

  return (
    <div className="flex flex-col gap-1.5">
      {devices.map((d) => (
        <div key={d.ip} className="flex flex-wrap items-baseline gap-x-3 text-sm">
          <span className="font-mono text-[13px]">{d.ip}</span>
          <span className="text-xs text-faint">
            {d.node_name || d.node_id || t("节点已删除")}
            <span className="mx-1.5">·</span>
            {tf("最近 {when}", { when: relativeTime(d.last_seen) })}
          </span>
        </div>
      ))}
      <p className="mt-1 text-xs text-faint">
        {t("每次查看均记入审计日志。")}
      </p>
    </div>
  );
}
