import { useEffect, useRef, useState } from "react";
import { api, type OrderEntry } from "../api";
import { Button } from "./ui";
import { ArrowUpIcon, ArrowDownIcon, GripIcon } from "./icons";
import { cn } from "../lib/cn";
import { useT } from "../lib/i18n";

/**
 * The order subscribers see their nodes in.
 *
 * One list for both kinds. An external exit belongs next to the fleet node it
 * is chained through, and two lists could not express that.
 *
 * It is also the only ordering there is: the proxy-group generator walks the
 * rendered proxy list and takes whatever each group's pattern matches, in the
 * order it finds them. So this decides the order inside 节点选择 and every other
 * group as well — and, since a select group opens on its first member, which
 * node a client picks by default. There is deliberately no second setting for
 * that; two settings for one arrangement is two settings that can disagree.
 *
 * Drag to move, or the arrows. The arrows are not a fallback for the squeamish:
 * HTML5 drag-and-drop does not fire for touch at all, so on a phone they are
 * the whole feature.
 */
export function ProxyOrder({ reloadKey }: { reloadKey?: number }) {
  const { t } = useT();
  const [entries, setEntries] = useState<OrderEntry[]>([]);
  const [dirty, setDirty] = useState(false);
  const [busy, setBusy] = useState(false);
  const [error, setError] = useState("");
  const [saved, setSaved] = useState(false);
  const dragFrom = useRef<number | null>(null);
  const [dragOver, setDragOver] = useState<number | null>(null);

  async function load() {
    try {
      setEntries((await api.proxyOrder()).entries);
      setDirty(false);
      setError("");
    } catch (e) {
      setError((e as Error).message);
    }
  }
  useEffect(() => {
    load();
    // Adding or removing a relay line adds or removes a row here, and the two
    // sitting on the same page made the staleness plain: a line the operator
    // had just created was missing from the list that decides where it goes.
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [reloadKey]);

  function move(from: number, to: number) {
    if (to < 0 || to >= entries.length || from === to) return;
    const next = entries.slice();
    const [row] = next.splice(from, 1);
    next.splice(to, 0, row);
    setEntries(next);
    setDirty(true);
    setSaved(false);
  }

  async function save() {
    setBusy(true);
    setError("");
    try {
      await api.setProxyOrder(entries.map((e) => ({ kind: e.kind, id: e.id })));
      setDirty(false);
      setSaved(true);
    } catch (e) {
      setError((e as Error).message);
    } finally {
      setBusy(false);
    }
  }

  if (entries.length === 0) {
    return null;
  }

  return (
    <section className="mt-8">
      <div className="mb-2 flex flex-wrap items-center justify-between gap-x-4 gap-y-2">
        <div>
          <h2 className="font-display text-[15px] font-semibold tracking-tight">{t("订阅顺序")}</h2>
          <p className="mt-0.5 text-xs text-muted">
            {t("订阅者看到的节点顺序，也决定每个策略组内的顺序；组的第一个节点是客户端的默认选中项。")}
          </p>
        </div>
        <div className="flex items-center gap-2">
          {saved && !dirty && <span className="text-xs text-online">{t("已保存")}</span>}
          {dirty && (
            <button onClick={load} disabled={busy} className="text-xs text-muted hover:text-ink">
              {t("撤销")}
            </button>
          )}
          <Button variant="primary" onClick={save} disabled={!dirty || busy}>
            {busy ? t("保存中…") : t("保存顺序")}
          </Button>
        </div>
      </div>

      {error && <p className="mb-2 text-sm text-danger">{error}</p>}

      <ol className="overflow-hidden rounded-2xl border border-line bg-surface">
        {entries.map((e, i) => (
          <li
            key={e.kind + e.id}
            draggable
            onDragStart={() => (dragFrom.current = i)}
            onDragOver={(ev) => {
              ev.preventDefault();
              setDragOver(i);
            }}
            onDragLeave={() => setDragOver((v) => (v === i ? null : v))}
            onDrop={(ev) => {
              ev.preventDefault();
              if (dragFrom.current !== null) move(dragFrom.current, i);
              dragFrom.current = null;
              setDragOver(null);
            }}
            onDragEnd={() => {
              dragFrom.current = null;
              setDragOver(null);
            }}
            className={cn(
              "flex items-center gap-3 border-b border-line px-3 py-2 last:border-b-0 transition-colors",
              dragOver === i && "bg-signal-soft",
            )}
          >
            <span className="cursor-grab text-faint active:cursor-grabbing">
              <GripIcon size={14} />
            </span>
            <span className="w-6 shrink-0 text-right font-mono text-[11px] text-faint">{i + 1}</span>
            {/* Green only for a fleet node that is up. An external node is never
                probed, so it gets no dot rather than a grey one that would read
                as "offline". */}
            {e.online !== undefined && (
              <span
                className={cn("h-1.5 w-1.5 shrink-0 rounded-full", e.online ? "bg-online" : "bg-faint")}
              />
            )}
            <span className="min-w-0 flex-1 truncate text-sm">{e.name}</span>
            <span className="shrink-0 truncate text-[11px] text-faint">
              {e.kind === "node" ? t("自有") : e.source}
            </span>
            <span className="flex shrink-0 items-center gap-0.5">
              <button
                onClick={() => move(i, i - 1)}
                disabled={i === 0}
                aria-label={t("上移")}
                className="grid h-7 w-7 place-items-center rounded-lg text-muted transition-colors hover:bg-[color-mix(in_srgb,var(--muted)_10%,transparent)] hover:text-ink disabled:opacity-25"
              >
                <ArrowUpIcon size={13} />
              </button>
              <button
                onClick={() => move(i, i + 1)}
                disabled={i === entries.length - 1}
                aria-label={t("下移")}
                className="grid h-7 w-7 place-items-center rounded-lg text-muted transition-colors hover:bg-[color-mix(in_srgb,var(--muted)_10%,transparent)] hover:text-ink disabled:opacity-25"
              >
                <ArrowDownIcon size={13} />
              </button>
            </span>
          </li>
        ))}
      </ol>
      <p className="mt-2 text-xs text-faint">
        {t("订阅者下次刷新订阅时生效。")}
      </p>
    </section>
  );
}
