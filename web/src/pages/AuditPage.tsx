import { useEffect, useState } from "react";
import { api, type AuditEntry } from "../api";
import { Button } from "../components/ui";
import { Empty, ErrorBar } from "../components/primitives";
import { relativeTime } from "../format";
import { useT } from "../lib/i18n";

/**
 * Who did what.
 *
 * Paged backwards by id rather than by offset: entries are only ever appended,
 * so an offset would shift under the reader as new ones arrive and quietly
 * show the same row twice.
 */
export function AuditPage() {
  const { t } = useT();
  const [entries, setEntries] = useState<AuditEntry[]>([]);
  const [action, setAction] = useState("");
  const [loaded, setLoaded] = useState(false);
  const [error, setError] = useState("");
  const [done, setDone] = useState(false);
  const [busy, setBusy] = useState(false);

  async function load(reset: boolean) {
    setBusy(true);
    try {
      const before = reset ? undefined : entries[entries.length - 1]?.id;
      const r = await api.auditLog({ before, action: action.trim() || undefined });
      setEntries(reset ? r.entries : [...entries, ...r.entries]);
      setDone(r.entries.length === 0);
      setError("");
    } catch (e) {
      setError((e as Error).message);
    } finally {
      setBusy(false);
      setLoaded(true);
    }
  }

  // Debounced so typing a filter does not fire a query per keystroke against
  // a database handle the whole panel shares.
  useEffect(() => {
    const timer = window.setTimeout(() => load(true), action ? 300 : 0);
    return () => window.clearTimeout(timer);
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [action]);

  return (
    <div>
      <div className="mb-6 animate-rise">
        <h1 className="font-display text-[26px] font-semibold tracking-tight">{t("审计")}</h1>
        <p className="mt-1 text-sm text-muted">
          {t("谁在什么时候改了什么。保留 180 天。")}
        </p>
      </div>

      <input
        value={action}
        onChange={(e) => setAction(e.target.value)}
        placeholder={t("按动作过滤，例如 user.delete")}
        className="mb-4 w-full max-w-sm rounded-xl border border-line-strong bg-surface px-3.5 py-2 font-mono text-[13px] outline-none transition-colors placeholder:font-sans placeholder:text-faint focus:border-signal"
      />

      {error && <ErrorBar text={error} />}

      {loaded &&
        (entries.length === 0 ? (
          <Empty>{action ? t("没有匹配的记录。") : t("还没有审计记录。")}</Empty>
        ) : (
          <>
            <div className="overflow-hidden rounded-2xl border border-line bg-surface">
              {entries.map((e) => (
                <div
                  key={e.id}
                  className="flex flex-wrap items-baseline gap-x-3 gap-y-1 border-b border-line px-4 py-2.5 text-sm last:border-0"
                >
                  <span className="w-20 shrink-0 text-xs text-faint">{relativeTime(e.at)}</span>
                  <span className="font-mono text-[13px]">{e.action}</span>
                  <span className="text-muted">{e.actor_name}</span>
                  {e.target_name && (
                    <span className="text-faint">
                      → {e.target_type ? `${e.target_type} ` : ""}
                      {e.target_name}
                    </span>
                  )}
                  {e.detail && <span className="text-xs text-faint">{e.detail}</span>}
                </div>
              ))}
            </div>
            {!done && (
              <div className="mt-4 flex justify-center">
                <Button size="sm" variant="ghost" onClick={() => load(false)} disabled={busy}>
                  {busy ? t("加载中…") : t("加载更多")}
                </Button>
              </div>
            )}
          </>
        ))}
    </div>
  );
}
