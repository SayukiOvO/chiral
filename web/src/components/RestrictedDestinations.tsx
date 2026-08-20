import { useEffect, useState } from "react";
import { api, type Node, type RestrictedDestination, type User } from "../api";
import { Button, IconButton } from "./ui";
import { PlusIcon, TrashIcon, CheckIcon, PencilIcon } from "./icons";
import { Field, Modal, inputCls } from "./primitives";
import { cn } from "../lib/cn";
import { useT } from "../lib/i18n";

/**
 * Networks some nodes reach that most subscribers must not — DN42 being the
 * case this was built for.
 *
 * Unlike every other permission on this panel, the resting state is
 * default-nobody: a private network forgotten in a deny table is explored
 * before anyone notices, so this one stores who IS allowed. Enforcement is a
 * routing rule on the scoped nodes (and, automatically, on the entry of any
 * relay line landing on one — the exit only sees the line's machine
 * credential, so the entry is the last place users can be told apart).
 */
export function RestrictedDestinations({ nodes }: { nodes: Node[] }) {
  const { t } = useT();
  const [dests, setDests] = useState<RestrictedDestination[]>([]);
  const [users, setUsers] = useState<User[]>([]);
  const [editing, setEditing] = useState<RestrictedDestination | null>(null);
  const [adding, setAdding] = useState(false);
  const [error, setError] = useState("");
  const [busy, setBusy] = useState(false);

  async function refresh() {
    try {
      const [d, u] = await Promise.all([api.listRestricted(), api.listUsers()]);
      setDests(d.destinations);
      setUsers(u.users);
      setError("");
    } catch (e) {
      setError((e as Error).message);
    }
  }
  useEffect(() => {
    refresh();
  }, []);

  async function toggleNode(d: RestrictedDestination, nodeID: string) {
    const next = d.node_ids.includes(nodeID)
      ? d.node_ids.filter((id) => id !== nodeID)
      : [...d.node_ids, nodeID];
    await mutate(() => api.setRestrictedNodes(d.id, next));
  }

  async function toggleUser(d: RestrictedDestination, userID: string) {
    const next = d.allowed_user_ids.includes(userID)
      ? d.allowed_user_ids.filter((id) => id !== userID)
      : [...d.allowed_user_ids, userID];
    await mutate(() => api.setRestrictedUsers(d.id, next));
  }

  async function remove(d: RestrictedDestination) {
    if (!window.confirm(t("确认删除此受限目的地？相关节点将移除对应拦截规则并重新下发配置。"))) return;
    await mutate(() => api.deleteRestricted(d.id));
  }

  async function mutate(fn: () => Promise<unknown>) {
    setBusy(true);
    try {
      await fn();
      await refresh();
    } catch (e) {
      setError((e as Error).message);
    } finally {
      setBusy(false);
    }
  }

  if (nodes.length === 0 && dests.length === 0) return null;

  return (
    <section className="mt-8">
      <div className="mb-2 flex flex-wrap items-center justify-between gap-x-4 gap-y-2">
        <div>
          <h2 className="font-display text-[15px] font-semibold tracking-tight">
            {t("受限目的地")}
          </h2>
          <p className="mt-0.5 text-xs text-muted">
            {t("节点可达、但默认不对任何订阅者开放的网段。规则在所选节点上生效；经中转线路借道的入口节点将自动继承该规则。")}
          </p>
        </div>
        <Button onClick={() => setAdding(true)}>
          <PlusIcon size={15} />
          {t("新增目的地")}
        </Button>
      </div>

      {error && <p className="mb-2 text-sm text-danger">{error}</p>}

      {dests.length === 0 ? (
        <div className="rounded-2xl border border-dashed border-line-strong bg-surface px-6 py-8 text-center text-sm text-muted">
          {t("暂无受限目的地。")}
        </div>
      ) : (
        <ul className="flex flex-col gap-2.5">
          {dests.map((d) => (
            <li
              key={d.id}
              className="rounded-2xl border border-line bg-surface px-4 py-3"
            >
              <div className="flex flex-wrap items-center gap-x-3 gap-y-1.5">
                <span className="min-w-0 flex-1 truncate text-sm font-medium">{d.name}</span>
                <span className="min-w-0 truncate font-mono text-[11px] text-faint">
                  {[...(d.cidrs ?? []), ...(d.domains ?? []).map((x) => "*." + x)].join("  ")}
                </span>
                <IconButton label={t("编辑")} onClick={() => setEditing(d)}>
                  <PencilIcon size={14} />
                </IconButton>
                <IconButton label={t("删除")} onClick={() => remove(d)}>
                  <TrashIcon size={14} />
                </IconButton>
              </div>

              <div className="mt-2.5 grid gap-2.5 sm:grid-cols-2">
                <div>
                  <div className="mb-1.5 text-[11px] text-faint">{t("在哪些节点上生效")}</div>
                  <div className="flex flex-wrap gap-1.5">
                    {nodes.map((n) => {
                      const on = d.node_ids.includes(n.id);
                      return (
                        <button
                          key={n.id}
                          disabled={busy}
                          onClick={() => toggleNode(d, n.id)}
                          className={cn(
                            "inline-flex items-center gap-1.5 rounded-lg border px-2.5 py-1 text-xs transition-colors disabled:opacity-50",
                            on
                              ? "border-signal text-ink"
                              : "border-line-strong text-muted hover:border-signal hover:text-ink",
                          )}
                        >
                          {on && <CheckIcon size={12} />}
                          {n.display_name || n.name}
                        </button>
                      );
                    })}
                  </div>
                </div>
                <div>
                  <div className="mb-1.5 text-[11px] text-faint">
                    {t("允许访问的用户（其余用户将被拦截）")}
                  </div>
                  <div className="flex flex-wrap gap-1.5">
                    {users.length === 0 ? (
                      <span className="text-xs text-muted">{t("暂无订阅者。")}</span>
                    ) : (
                      users.map((u) => {
                        const on = d.allowed_user_ids.includes(u.id);
                        return (
                          <button
                            key={u.id}
                            disabled={busy}
                            onClick={() => toggleUser(d, u.id)}
                            className={cn(
                              "inline-flex items-center gap-1.5 rounded-lg border px-2.5 py-1 text-xs transition-colors disabled:opacity-50",
                              on
                                ? "border-[color-mix(in_srgb,var(--online)_45%,transparent)] text-online"
                                : "border-line-strong text-muted hover:border-signal hover:text-ink",
                            )}
                          >
                            {on && <CheckIcon size={12} />}
                            {u.name}
                          </button>
                        );
                      })
                    )}
                  </div>
                </div>
              </div>
            </li>
          ))}
        </ul>
      )}

      {(adding || editing) && (
        <DestinationDialog
          dest={editing}
          onClose={() => {
            setAdding(false);
            setEditing(null);
          }}
          onSaved={refresh}
        />
      )}
    </section>
  );
}

function DestinationDialog({
  dest,
  onClose,
  onSaved,
}: {
  dest: RestrictedDestination | null;
  onClose: () => void;
  onSaved: () => void;
}) {
  const { t } = useT();
  const [name, setName] = useState(dest?.name ?? "");
  const [cidrs, setCidrs] = useState((dest?.cidrs ?? []).join("\n"));
  const [domains, setDomains] = useState((dest?.domains ?? []).join("\n"));
  const [busy, setBusy] = useState(false);
  const [error, setError] = useState("");

  async function submit() {
    setBusy(true);
    setError("");
    try {
      const body = { name: name.trim(), cidrs, domains };
      if (dest) await api.updateRestricted(dest.id, body);
      else await api.createRestricted(body);
      onSaved();
      onClose();
    } catch (e) {
      setError((e as Error).message);
    } finally {
      setBusy(false);
    }
  }

  return (
    <Modal onClose={onClose}>
      <h2 className="font-display text-lg font-semibold tracking-tight">
        {dest ? t("编辑受限目的地") : t("新增受限目的地")}
      </h2>
      <p className="mt-1 text-sm text-muted">
        {t("新建的目的地默认不对任何人开放：请先定义网段，再在列表中选择生效节点与获准用户。")}
      </p>
      <Field label={t("名字")}>
        <input
          autoFocus
          className={inputCls}
          value={name}
          onChange={(e) => setName(e.target.value)}
          placeholder="DN42"
        />
      </Field>
      <Field label={t("IP 段（每行一个 CIDR；单个 IP 视为 /32 或 /128）")}>
        <textarea
          className={cn(inputCls, "h-20 resize-y font-mono text-xs")}
          value={cidrs}
          onChange={(e) => setCidrs(e.target.value)}
          placeholder={"172.20.0.0/14\nfd00::/8"}
        />
      </Field>
      <Field label={t("域名后缀（每行一个）")}>
        <textarea
          className={cn(inputCls, "h-14 resize-y font-mono text-xs")}
          value={domains}
          onChange={(e) => setDomains(e.target.value)}
          placeholder="dn42"
        />
      </Field>
      {error && <p className="mt-3 text-sm text-danger">{error}</p>}
      <div className="mt-5 flex justify-end gap-2">
        <Button onClick={onClose}>{t("取消")}</Button>
        <Button
          variant="primary"
          onClick={submit}
          disabled={busy || !name.trim() || !(cidrs.trim() || domains.trim())}
        >
          {busy ? t("保存中…") : t("保存")}
        </Button>
      </div>
    </Modal>
  );
}
