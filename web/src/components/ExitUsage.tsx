import { useEffect, useState } from "react";
import { api, type Credential } from "../api";
import { bytes } from "../format";
import { useT } from "../lib/i18n";

/**
 * What this subscriber moved, split by where it went out.
 *
 * The numbers were already there — a credential is per access point and now
 * per exit as well, and traffic arrives under its email — but nothing showed
 * them, so "who is using the expensive provider" had no answer.
 *
 * Two columns on purpose. The bytes are what the agent measured; the billed
 * figure is those bytes times the rate this line carries, and it is the one
 * the quota compares against. Showing only the first makes a quota look wrong;
 * showing only the second makes the panel look like it is inventing traffic.
 */
export function ExitUsage({ userId }: { userId: string }) {
  const { t, tf } = useT();
  const [creds, setCreds] = useState<Credential[] | null>(null);

  useEffect(() => {
    api
      .getUser(userId)
      .then((u) => setCreds(u.credentials ?? []))
      .catch(() => setCreds([]));
  }, [userId]);

  if (creds === null) return <p className="text-xs text-muted">{t("加载中…")}</p>;
  const used = creds.filter((c) => c.up_bytes + c.down_bytes > 0);
  if (used.length === 0) {
    return <p className="text-xs text-muted">{t("暂无流量记录。")}</p>;
  }
  used.sort((a, b) => b.up_bytes + b.down_bytes - (a.up_bytes + a.down_bytes));

  return (
    <div className="overflow-hidden rounded-xl border border-line">
      {used.map((c) => {
        const raw = c.up_bytes + c.down_bytes;
        const rate = c.traffic_rate || 1;
        return (
          <div
            key={c.email}
            className="flex items-center gap-3 border-b border-line px-3 py-1.5 text-xs last:border-b-0"
          >
            <span className="min-w-0 flex-1 truncate">
              {c.exit_name ? (
                <>
                  <span className="text-faint">{t("经出口")} </span>
                  {c.exit_name}
                </>
              ) : (
                <span className="text-muted">{t("节点直出")}</span>
              )}
            </span>
            {rate !== 1 && (
              <span className="shrink-0 rounded-md border border-warn px-1.5 text-[10px] text-warn">
                ×{rate}
              </span>
            )}
            <span className="shrink-0 font-mono text-faint">{bytes(raw)}</span>
            {rate !== 1 && (
              <span
                className="shrink-0 font-mono text-ink"
                title={tf("按 ×{rate} 计入配额", { rate: String(rate) })}
              >
                → {bytes(Math.ceil(raw * rate))}
              </span>
            )}
          </div>
        );
      })}
    </div>
  );
}
