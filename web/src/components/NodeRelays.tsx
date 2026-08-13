import { useEffect, useState } from "react";
import { api, type Node, type Profile, type ProxyUser, type Relay } from "../api";
import { Button, IconButton } from "./ui";
import { PlusIcon, TrashIcon, CheckIcon, UsersIcon } from "./icons";
import { Field, Modal, inputCls } from "./primitives";
import { cn } from "../lib/cn";
import { useT } from "../lib/i18n";

/**
 * Lines out through another of our own nodes.
 *
 * The external-node page already has "经由", which puts somebody else's
 * provider behind one of our machines. This is the same arrangement with both
 * ends ours, and it exists for the opposite reason: not to hide a landing, but
 * to reach one. A subscriber whose route to a distant exit is bad can often
 * reach a nearby entry, and that entry can reach the exit.
 *
 * It is a third line, not a replacement for either node — both keep their own
 * — so it gets a name of its own, its own permissions and its own rate.
 */
export function NodeRelays({ nodes, onChanged }: { nodes: Node[]; onChanged?: () => void }) {
  const { t } = useT();
  const [relays, setRelays] = useState<Relay[]>([]);
  const [profiles, setProfiles] = useState<Profile[]>([]);
  const [adding, setAdding] = useState(false);
  const [error, setError] = useState("");
  const [open, setOpen] = useState<string | null>(null);

  async function refresh(changed?: boolean) {
    try {
      const [r, p] = await Promise.all([api.listRelays(), api.listProfiles()]);
      setRelays(r.relays);
      setProfiles(p.profiles);
      setError("");
      // A line is a row in the subscription order too, and that list is a
      // sibling on this page rather than a child of this one.
      if (changed) onChanged?.();
    } catch (e) {
      setError((e as Error).message);
    }
  }
  useEffect(() => {
    refresh();
  }, []);

  async function patch(r: Relay, p: { enabled?: boolean; label?: string; traffic_rate?: number }) {
    try {
      await api.updateRelay(r.id, p);
      refresh(true);
    } catch (e) {
      setError((e as Error).message);
    }
  }

  async function remove(r: Relay) {
    if (!window.confirm(t("删除这条中转线路？订阅者会失去这条线，两端节点会重新下发配置。"))) return;
    try {
      await api.deleteRelay(r.id);
      refresh(true);
    } catch (e) {
      setError((e as Error).message);
    }
  }

  // Nothing to relay between until there are two nodes.
  if (nodes.length < 2 && relays.length === 0) return null;

  return (
    <section className="mt-8">
      <div className="mb-2 flex flex-wrap items-center justify-between gap-x-4 gap-y-2">
        <div>
          <h2 className="font-display text-[15px] font-semibold tracking-tight">{t("中转线路")}</h2>
          <p className="mt-0.5 text-xs text-muted">
            {t("订阅者连入口节点，流量从出口节点出去。两端都是自有节点，落地对订阅者不可见。")}
          </p>
        </div>
        <Button onClick={() => setAdding(true)}>
          <PlusIcon size={15} />
          {t("新增线路")}
        </Button>
      </div>

      {error && <p className="mb-2 text-sm text-danger">{error}</p>}

      {relays.length === 0 ? (
        <div className="rounded-2xl border border-dashed border-line-strong bg-surface px-6 py-8 text-center text-sm text-muted">
          {t("还没有中转线路。")}
        </div>
      ) : (
        <ul className="overflow-hidden rounded-2xl border border-line bg-surface">
          {relays.map((r) => (
            <li key={r.id} className="border-b border-line px-3 py-2.5 last:border-b-0">
              <div className="flex flex-wrap items-center gap-x-3 gap-y-1.5">
                <span className="min-w-0 flex-1 truncate text-sm">
                  {r.label}
                  <span className="ml-2 text-[11px] text-faint">
                    {r.entry_name} → {r.exit_name} · {r.profile_name}
                  </span>
                </span>
                {r.traffic_rate !== 1 && (
                  <span className="shrink-0 rounded-md border border-warn px-1.5 text-[10px] text-warn">
                    ×{r.traffic_rate}
                  </span>
                )}
                <button
                  onClick={() => patch(r, { enabled: !r.enabled })}
                  className={cn(
                    "shrink-0 rounded-lg border px-2 py-0.5 text-[11px] transition-colors",
                    r.enabled
                      ? "border-[color-mix(in_srgb,var(--online)_45%,transparent)] text-online"
                      : "border-line-strong text-muted hover:border-signal hover:text-ink",
                  )}
                >
                  {r.enabled ? t("启用") : t("停用")}
                </button>
                <button
                  onClick={() => setOpen(open === r.id ? null : r.id)}
                  aria-expanded={open === r.id}
                  className={cn(
                    "inline-flex shrink-0 items-center gap-1 rounded-md border px-2 py-0.5 text-[11px] transition-colors",
                    open === r.id
                      ? "border-signal text-ink"
                      : "border-line-strong text-muted hover:border-signal hover:text-ink",
                  )}
                >
                  <UsersIcon size={11} />
                  {t("可用用户")}
                </button>
                <IconButton label={t("删除")} onClick={() => remove(r)}>
                  <TrashIcon size={14} />
                </IconButton>
              </div>
              {/* A line that cannot be assembled still renders into every
                  subscription that carries it, and the only symptom out there
                  is a proxy that never connects. Both causes are things an
                  operator does on another page. */}
              {r.problem && <p className="mt-1 text-xs text-danger">{r.problem}</p>}
              {open === r.id && <RelayAccess relayId={r.id} />}
            </li>
          ))}
        </ul>
      )}

      {adding && (
        <AddRelayDialog
          nodes={nodes}
          profiles={profiles}
          onClose={() => setAdding(false)}
          onCreated={() => refresh(true)}
        />
      )}
    </section>
  );
}

function AddRelayDialog({
  nodes,
  profiles,
  onClose,
  onCreated,
}: {
  nodes: Node[];
  profiles: Profile[];
  onClose: () => void;
  onCreated: () => void;
}) {
  const { t } = useT();
  const [entry, setEntry] = useState("");
  const [exit, setExit] = useState("");
  const [profile, setProfile] = useState("");
  const [label, setLabel] = useState("");
  const [rate, setRate] = useState("1");
  const [busy, setBusy] = useState(false);
  const [error, setError] = useState("");

  async function submit() {
    setBusy(true);
    setError("");
    try {
      await api.createRelay({
        entry_node_id: entry,
        exit_node_id: exit,
        profile_id: profile,
        label: label.trim(),
        traffic_rate: Number(rate) || 1,
      });
      onCreated();
      onClose();
    } catch (e) {
      setError((e as Error).message);
    } finally {
      setBusy(false);
    }
  }

  const name = (n: Node) => n.display_name || n.name;
  const ready = entry && exit && profile && label.trim() && entry !== exit;

  return (
    <Modal onClose={onClose}>
      <h2 className="font-display text-lg font-semibold tracking-tight">{t("新增中转线路")}</h2>
      <p className="mt-1 text-sm text-muted">
        {t("入口节点会用一份线路自己的凭证拨号出口节点。这份凭证不属于任何订阅者，流量只在入口计一次费。")}
      </p>

      <Field label={t("入口节点（订阅者连这里）")}>
        <select className={inputCls} value={entry} onChange={(e) => setEntry(e.target.value)}>
          <option value="">{t("请选择")}</option>
          {nodes.map((n) => (
            <option key={n.id} value={n.id}>
              {name(n)}
            </option>
          ))}
        </select>
      </Field>
      <Field label={t("出口节点（流量从这里出去）")}>
        <select className={inputCls} value={exit} onChange={(e) => setExit(e.target.value)}>
          <option value="">{t("请选择")}</option>
          {nodes
            .filter((n) => n.id !== entry)
            .map((n) => (
              <option key={n.id} value={n.id}>
                {name(n)}
              </option>
            ))}
        </select>
      </Field>
      {/* Which of the exit's access points the entry dials. The entry needs a
          client config for the exit, and a profile is exactly that — so this
          list is the exit's profiles, not the entry's. */}
      <Field label={t("出口节点的接入配置")}>
        <select className={inputCls} value={profile} onChange={(e) => setProfile(e.target.value)}>
          <option value="">{t("请选择")}</option>
          {profiles.map((p) => (
            <option key={p.id} value={p.id}>
              {p.name}
            </option>
          ))}
        </select>
      </Field>
      <Field label={t("线路名称（订阅者看到的）")}>
        <input
          className={inputCls}
          value={label}
          onChange={(e) => setLabel(e.target.value)}
          placeholder={t("例如：香港中转 · 东京落地")}
        />
      </Field>
      <Field label={t("流量倍率")}>
        <input
          className={inputCls}
          type="number"
          step="0.1"
          min="0.1"
          value={rate}
          onChange={(e) => setRate(e.target.value)}
        />
      </Field>

      {error && <p className="mt-3 text-sm text-danger">{error}</p>}
      <div className="mt-5 flex justify-end gap-2">
        <Button onClick={onClose}>{t("取消")}</Button>
        <Button variant="primary" onClick={submit} disabled={!ready || busy}>
          {busy ? t("创建中…") : t("创建")}
        </Button>
      </div>
    </Modal>
  );
}

/**
 * Who may take one line, read from the line's end.
 *
 * A new line belongs to nobody until somebody is ticked here — the same rule a
 * new node and a new external source follow, and for the same reason: adding a
 * route and deciding who gets it are two separate decisions.
 */
function RelayAccess({ relayId }: { relayId: string }) {
  const { t } = useT();
  const [users, setUsers] = useState<ProxyUser[]>([]);
  const [busy, setBusy] = useState(false);
  const [error, setError] = useState("");

  async function load() {
    try {
      setUsers((await api.relayUsers(relayId)).users);
      setError("");
    } catch (e) {
      setError((e as Error).message);
    }
  }
  useEffect(() => {
    load();
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [relayId]);

  async function toggle(u: ProxyUser) {
    const next = users.map((x) => (x.id === u.id ? { ...x, allowed: !x.allowed } : x));
    setUsers(next);
    setBusy(true);
    try {
      await api.setRelayUsers(relayId, {
        denied_users: next.filter((x) => !x.allowed).map((x) => x.id),
      });
    } catch (e) {
      setError((e as Error).message);
      load();
    } finally {
      setBusy(false);
    }
  }

  return (
    <div className="mt-2 rounded-lg border border-line px-3 py-2">
      {error && <p className="mb-1.5 text-xs text-danger">{error}</p>}
      {users.length === 0 ? (
        <p className="text-xs text-muted">{t("还没有订阅者。")}</p>
      ) : (
        <div className="flex flex-wrap gap-1.5">
          {users.map((u) => (
            <button
              key={u.id}
              onClick={() => toggle(u)}
              disabled={busy}
              // Entitled here means their access configuration reaches the
              // ENTRY node, because that is where they would connect. Ticking
              // someone who cannot get to the entry changes nothing.
              title={u.entitled ? undefined : t("此用户的接入配置没有覆盖这条线路的入口节点")}
              className={cn(
                "inline-flex items-center gap-1.5 rounded-lg border px-2.5 py-1 text-xs transition-colors disabled:opacity-50",
                u.allowed
                  ? "border-[color-mix(in_srgb,var(--online)_45%,transparent)] text-online"
                  : "border-line-strong text-muted hover:border-signal hover:text-ink",
                !u.entitled && "opacity-45",
              )}
            >
              {u.allowed && <CheckIcon size={12} />}
              {u.name}
            </button>
          ))}
        </div>
      )}
      <p className="mt-1.5 text-[11px] text-faint">
        {t("改动会立刻重新下发入口节点的配置。")}
      </p>
    </div>
  );
}
