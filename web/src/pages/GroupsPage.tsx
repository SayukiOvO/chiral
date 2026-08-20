import { useEffect, useState } from "react";
import { api, type Profile, type Ruleset, type SubscriberGroup } from "../api";
import { Button, IconButton } from "../components/ui";
import { PlusIcon, TrashIcon, CheckIcon, PencilIcon } from "../components/icons";
import { Field, Modal, inputCls } from "../components/primitives";
import { NodeAccess } from "../components/NodeAccess";
import { cn } from "../lib/cn";
import { useT } from "../lib/i18n";

/**
 * Subscriber groups: say once what a class of subscriber gets.
 *
 * Access has been decided per subscriber, which is correct and does not scale —
 * a new node is one decision repeated for everyone, and a decision repeated for
 * everyone is one that ends up being made by a loop. A group holds the same
 * three things a subscriber does (access configurations, the nodes and lines
 * they reach, a rule set), and a member's own settings remain available as
 * exceptions on top.
 */
export function GroupsPage() {
  const { t, tf } = useT();
  const [groups, setGroups] = useState<SubscriberGroup[]>([]);
  const [profiles, setProfiles] = useState<Profile[]>([]);
  const [rulesets, setRulesets] = useState<Ruleset[]>([]);
  const [expanded, setExpanded] = useState<string>("");
  const [editing, setEditing] = useState<SubscriberGroup | null>(null);
  const [adding, setAdding] = useState(false);
  const [busy, setBusy] = useState(false);
  const [version, setVersion] = useState(0);
  const [error, setError] = useState("");

  async function refresh() {
    try {
      const [g, p, r] = await Promise.all([
        api.listGroups(),
        api.listProfiles(),
        api.listRulesets(),
      ]);
      setGroups(g.groups ?? []);
      setProfiles(p.profiles ?? []);
      setRulesets(r.rulesets ?? []);
      setVersion((v) => v + 1);
      setError("");
    } catch (e) {
      setError((e as Error).message);
    }
  }
  useEffect(() => {
    refresh();
  }, []);

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

  async function toggleProfile(g: SubscriberGroup, profileID: string, bound: boolean) {
    await mutate(() =>
      bound ? api.unbindGroupProfile(g.id, profileID) : api.bindGroupProfile(g.id, profileID),
    );
  }

  async function remove(g: SubscriberGroup) {
    if (
      !window.confirm(
        tf("确认删除用户组「{name}」？其 {n} 名成员将保留该组当前授予的全部权限，订阅不受影响。", {
          name: g.name,
          n: String(g.members),
        }),
      )
    )
      return;
    await mutate(() => api.deleteGroup(g.id));
  }

  return (
    <section>
      <div className="mb-4 flex flex-wrap items-center justify-between gap-x-4 gap-y-2">
        <div>
          <h1 className="font-display text-xl font-semibold tracking-tight">{t("用户组")}</h1>
          <p className="mt-1 text-sm text-muted">
            {t("为一类订阅者统一设定接入配置、可用节点与分流规则。每位订阅者至多属于一个组，其个人设置优先于组。")}
          </p>
        </div>
        <Button variant="primary" onClick={() => setAdding(true)}>
          <PlusIcon size={15} />
          {t("新增用户组")}
        </Button>
      </div>

      {error && <p className="mb-3 text-sm text-danger">{error}</p>}

      {groups.length === 0 ? (
        <div className="rounded-2xl border border-dashed border-line-strong bg-surface px-6 py-10 text-center text-sm text-muted">
          {t("暂无用户组。新增后可在用户页将订阅者加入。")}
        </div>
      ) : (
        <ul className="flex flex-col gap-3">
          {groups.map((g) => {
            const open = expanded === g.id;
            return (
              <li key={g.id} className="rounded-2xl border border-line bg-surface px-4 py-3.5">
                <div className="flex flex-wrap items-center gap-x-3 gap-y-1.5">
                  <button
                    onClick={() => setExpanded(open ? "" : g.id)}
                    className="min-w-0 flex-1 truncate text-left text-sm font-medium hover:text-signal"
                  >
                    {g.name}
                  </button>
                  <span className="font-mono text-[11px] text-faint">
                    {tf("{n} 名成员", { n: String(g.members) })}
                  </span>
                  <IconButton label={t("编辑")} onClick={() => setEditing(g)}>
                    <PencilIcon size={14} />
                  </IconButton>
                  <IconButton label={t("删除")} onClick={() => remove(g)}>
                    <TrashIcon size={14} />
                  </IconButton>
                </div>
                {g.note && <p className="mt-1 text-xs text-muted">{g.note}</p>}

                {open && (
                  <div className="mt-4 border-t border-line pt-4">
                    <div className="mb-2 text-[11px] font-medium uppercase tracking-[0.07em] text-faint">
                      {t("可访问的接入配置")}
                    </div>
                    {profiles.length === 0 ? (
                      <p className="text-sm text-muted">{t("暂无接入配置。")}</p>
                    ) : (
                      <div className="flex flex-wrap gap-1.5">
                        {profiles.map((p) => {
                          const bound = (g.profile_ids ?? []).includes(p.id);
                          return (
                            <button
                              key={p.id}
                              disabled={busy}
                              onClick={() => toggleProfile(g, p.id, bound)}
                              className={cn(
                                "inline-flex items-center gap-1.5 rounded-lg border px-2.5 py-1 text-xs transition-colors disabled:opacity-50",
                                bound
                                  ? "border-[color-mix(in_srgb,var(--online)_45%,transparent)] text-online"
                                  : "border-line-strong text-muted hover:border-signal hover:text-ink",
                              )}
                            >
                              {bound && <CheckIcon size={12} />}
                              {p.name}
                            </button>
                          );
                        })}
                      </div>
                    )}
                    <p className="mt-2 text-xs text-faint">
                      {t("授权后，该组每位成员在此配置绑定的每个节点上生成独立凭证。")}
                    </p>

                    <div className="mt-5">
                      <div className="mb-2 text-[11px] font-medium uppercase tracking-[0.07em] text-faint">
                        {t("可用节点")}
                      </div>
                      <NodeAccess subject="group" id={g.id} version={version} />
                    </div>

                    <div className="mt-5">
                      <div className="mb-2 text-[11px] font-medium uppercase tracking-[0.07em] text-faint">
                        {t("分流规则")}
                      </div>
                      {rulesets.length === 0 ? (
                        <p className="text-sm text-muted">
                          {t("暂无规则集，订阅不包含分流规则。可在「分流规则」页新增。")}
                        </p>
                      ) : (
                        <div className="flex flex-wrap gap-1.5">
                          {[{ id: "", name: t("不分流") }, ...rulesets].map((rs) => {
                            const on = (g.ruleset_id ?? "") === rs.id;
                            return (
                              <button
                                key={rs.id || "none"}
                                disabled={busy}
                                onClick={() => mutate(() => api.setGroupRuleset(g.id, rs.id))}
                                className={cn(
                                  "inline-flex items-center gap-1.5 rounded-lg border px-2.5 py-1 text-xs transition-colors disabled:opacity-50",
                                  on
                                    ? "border-[color-mix(in_srgb,var(--online)_45%,transparent)] text-online"
                                    : "border-line-strong text-muted hover:border-signal hover:text-ink",
                                )}
                              >
                                {on && <CheckIcon size={12} />}
                                {rs.name}
                              </button>
                            );
                          })}
                        </div>
                      )}
                      <p className="mt-2 text-xs text-faint">
                        {t("适用于未单独指定分流规则的成员。")}
                      </p>
                    </div>
                  </div>
                )}
              </li>
            );
          })}
        </ul>
      )}

      {(adding || editing) && (
        <GroupDialog
          group={editing}
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

function GroupDialog({
  group,
  onClose,
  onSaved,
}: {
  group: SubscriberGroup | null;
  onClose: () => void;
  onSaved: () => void;
}) {
  const { t } = useT();
  const [name, setName] = useState(group?.name ?? "");
  const [note, setNote] = useState(group?.note ?? "");
  const [busy, setBusy] = useState(false);
  const [error, setError] = useState("");

  async function submit() {
    setBusy(true);
    setError("");
    try {
      const body = { name: name.trim(), note: note.trim() };
      if (group) await api.updateGroup(group.id, body);
      else await api.createGroup(body);
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
        {group ? t("编辑用户组") : t("新增用户组")}
      </h2>
      <p className="mt-1 text-sm text-muted">
        {t("新建的用户组不包含任何权限：请先创建，再为其选择接入配置与可用节点。")}
      </p>
      <Field label={t("名称")}>
        <input
          autoFocus
          className={inputCls}
          value={name}
          onChange={(e) => setName(e.target.value)}
          placeholder={t("例如 标准套餐")}
        />
      </Field>
      <Field label={t("备注（可留空）")}>
        <input
          className={inputCls}
          value={note}
          onChange={(e) => setNote(e.target.value)}
        />
      </Field>
      {error && <p className="mt-3 text-sm text-danger">{error}</p>}
      <div className="mt-5 flex justify-end gap-2">
        <Button onClick={onClose}>{t("取消")}</Button>
        <Button variant="primary" onClick={submit} disabled={busy || !name.trim()}>
          {busy ? t("保存中…") : t("保存")}
        </Button>
      </div>
    </Modal>
  );
}
