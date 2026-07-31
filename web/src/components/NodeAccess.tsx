import { useEffect, useState } from "react";
import { api, type NodeAccessEntry } from "../api";
import { CheckIcon } from "./icons";
import { cn } from "../lib/cn";
import { useT } from "../lib/i18n";

/**
 * Which nodes one subscriber's subscription carries.
 *
 * Entitlement is per access configuration — grant a profile and they get every
 * node bound to it — which is the right default and leaves no way to say "this
 * person, not that box". This is that exception, stored as denials so a node
 * nobody has been asked about is usable by everyone the moment it is bound.
 */
export function NodeAccess({ userId }: { userId: string }) {
  const { t, tf } = useT();
  const [fleet, setFleet] = useState<NodeAccessEntry[]>([]);
  const [external, setExternal] = useState<NodeAccessEntry[]>([]);
  const [busy, setBusy] = useState(false);
  const [error, setError] = useState("");

  async function load() {
    try {
      const a = await api.userNodeAccess(userId);
      setFleet(a.fleet);
      setExternal(a.external);
      setError("");
    } catch (e) {
      setError((e as Error).message);
    }
  }
  useEffect(() => {
    load();
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [userId]);

  async function toggle(entry: NodeAccessEntry, isExternal: boolean) {
    const next = { ...entry, allowed: !entry.allowed };
    const nextFleet = isExternal ? fleet : fleet.map((e) => (e.id === entry.id ? next : e));
    const nextExternal = isExternal ? external.map((e) => (e.id === entry.id ? next : e)) : external;
    setFleet(nextFleet);
    setExternal(nextExternal);
    setBusy(true);
    try {
      await api.setUserNodeAccess(userId, {
        denied_nodes: nextFleet.filter((e) => !e.allowed).map((e) => e.id),
        denied_proxies: nextExternal.filter((e) => !e.allowed).map((e) => e.id),
      });
    } catch (e) {
      setError((e as Error).message);
      load();
    } finally {
      setBusy(false);
    }
  }

  const groups: { label: string; entries: NodeAccessEntry[]; isExternal: boolean }[] = [];
  if (fleet.length) groups.push({ label: "自有节点", entries: fleet, isExternal: false });
  if (external.length) groups.push({ label: "外部节点", entries: external, isExternal: true });

  if (groups.length === 0) {
    return <p className="text-sm text-muted">{t("还没有节点。")}</p>;
  }

  return (
    <div>
      {error && <p className="mb-2 text-sm text-danger">{error}</p>}
      {groups.map((g) => (
        <div key={g.label} className="mb-3 last:mb-0">
          <div className="mb-1.5 text-[11px] text-faint">{t(g.label)}</div>
          <div className="flex flex-wrap gap-1.5">
            {g.entries.map((e) => (
              <button
                key={e.id}
                onClick={() => toggle(e, g.isExternal)}
                disabled={busy}
                // Not entitled is a different state from denied: no profile
                // this user holds reaches that node, and switching it on here
                // changes nothing. Shown rather than hidden, because "where did
                // my node go" is a worse question than a greyed row.
                title={
                  e.chained_via
                    ? tf("链经「{node}」，而此用户拿不到那个节点", { node: e.chained_via })
                    : e.entitled
                      ? undefined
                      : t("该用户的接入配置未覆盖此节点")
                }
                className={cn(
                  "inline-flex items-center gap-1.5 rounded-lg border px-2.5 py-1 text-xs transition-colors disabled:opacity-50",
                  // A chained node whose relay is gone is not carried, whatever
                  // the toggle says — so it does not get to look on.
                  e.allowed && !e.chained_via
                    ? "border-[color-mix(in_srgb,var(--online)_45%,transparent)] text-online"
                    : "border-line-strong text-muted hover:border-signal hover:text-ink",
                  (!e.entitled || e.chained_via) && "opacity-45",
                )}
              >
                {e.allowed && !e.chained_via && <CheckIcon size={12} />}
                {e.name}
                {e.chained_via && <span className="text-faint">⛓</span>}
              </button>
            ))}
          </div>
        </div>
      ))}
      <p className="mt-2 text-xs text-faint">
        {t("取消后该节点不再出现在此用户的订阅里。凭证仍在节点上，订阅者下次刷新订阅时生效。")}
      </p>
    </div>
  );
}
