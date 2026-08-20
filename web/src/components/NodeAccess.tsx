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
  const [relay, setRelay] = useState<NodeAccessEntry[]>([]);
  const [busy, setBusy] = useState(false);
  const [error, setError] = useState("");

  async function load() {
    try {
      const a = await api.userNodeAccess(userId);
      setFleet(a.fleet);
      setExternal(a.external);
      setRelay(a.relay ?? []);
      setError("");
    } catch (e) {
      setError((e as Error).message);
    }
  }
  useEffect(() => {
    load();
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [userId]);

  type Kind = "fleet" | "external" | "relay";

  async function toggle(entry: NodeAccessEntry, kind: Kind) {
    const flip = (list: NodeAccessEntry[], mine: boolean) =>
      mine ? list.map((e) => (e.id === entry.id ? { ...e, allowed: !e.allowed } : e)) : list;
    const nextFleet = flip(fleet, kind === "fleet");
    const nextExternal = flip(external, kind === "external");
    const nextRelay = flip(relay, kind === "relay");
    setFleet(nextFleet);
    setExternal(nextExternal);
    setRelay(nextRelay);
    setBusy(true);
    try {
      await api.setUserNodeAccess(userId, {
        denied_nodes: nextFleet.filter((e) => !e.allowed).map((e) => e.id),
        denied_proxies: nextExternal.filter((e) => !e.allowed).map((e) => e.id),
        denied_relays: nextRelay.filter((e) => !e.allowed).map((e) => e.id),
      });
    } catch (e) {
      setError((e as Error).message);
      load();
    } finally {
      setBusy(false);
    }
  }

  const groups: { label: string; entries: NodeAccessEntry[]; kind: Kind }[] = [];
  if (fleet.length) groups.push({ label: "自有节点", entries: fleet, kind: "fleet" });
  if (relay.length) groups.push({ label: "中转线路", entries: relay, kind: "relay" });
  if (external.length) groups.push({ label: "外部节点", entries: external, kind: "external" });

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
            {g.entries.map((e) => {
              // Three states, and the third is not "the second, but fainter".
              //
              //   allowed        — the subscriber receives it
              //   not allowed    — withheld, and one click restores it
              //   not reachable  — no access configuration this subscriber
              //                    holds covers this node, so the toggle
              //                    would decide nothing
              //
              // The third used to be drawn as the second with opacity on top,
              // which read as "even more off than off" and still invited a
              // click that changed nothing. It is now inert and marked by a
              // dashed outline rather than by being harder to read: the point
              // is to say "not applicable here", not to hide it.
              const reachable = e.entitled && !e.chained_via;
              const reason = e.chained_via
                ? tf("经由「{node}」接入，而此用户无法使用该节点", { node: e.chained_via })
                : e.entitled
                  ? undefined
                  : g.kind === "relay"
                    ? t("此用户的接入配置未覆盖该线路的入口节点，因此无法选择")
                    : t("此用户的接入配置未覆盖该节点，因此无法选择");
              return (
                <button
                  key={e.id}
                  onClick={() => reachable && toggle(e, g.kind)}
                  disabled={busy || !reachable}
                  title={reason}
                  aria-disabled={!reachable}
                  className={cn(
                    "inline-flex items-center gap-1.5 rounded-lg border px-2.5 py-1 text-xs transition-colors",
                    !reachable
                      ? "cursor-not-allowed border-dashed border-line-strong text-muted"
                      : e.allowed
                        ? "border-[color-mix(in_srgb,var(--online)_45%,transparent)] text-online disabled:opacity-50"
                        : "border-line-strong text-muted hover:border-signal hover:text-ink disabled:opacity-50",
                  )}
                >
                  {reachable && e.allowed && <CheckIcon size={12} />}
                  {e.name}
                  {e.chained_via && <span className="text-faint">⛓</span>}
                </button>
              );
            })}
          </div>
        </div>
      ))}
      <p className="mt-2 text-xs text-faint">
        {t("取消勾选后，该节点将不再出现在此用户的订阅中。节点上的凭证予以保留，变更于订阅者下次刷新时生效。")}
      </p>
    </div>
  );
}
