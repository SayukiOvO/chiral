import { useEffect, useMemo, useState } from "react";
import { api, type Node, type Profile, type Variable } from "../api";
import { Button, IconButton } from "../components/ui";
import { CheckIcon, PlusIcon, TrashIcon } from "../components/icons";
import { TemplateEditor } from "../components/TemplateEditor";
import { cn } from "../lib/cn";
import { href, navigate } from "../lib/router";
import { useIsDark } from "../lib/theme";
import { Empty, ErrorBar, Field, Modal, inputCls } from "../components/primitives";
import { useT } from "../lib/i18n";

/** Injected by the Core for every render; see profile.contextFor. */
const BUILTIN_NODE_VARS = ["node.name", "node.display_name", "node.address", "node.hostname"];

const CLIENT_KINDS = ["xray-json", "clash", "vless-uri", "stash"] as const;

export function ProfilesPage({ id }: { id?: string }) {
  const [profiles, setProfiles] = useState<Profile[]>([]);
  const [error, setError] = useState("");

  async function refresh() {
    try {
      const { profiles } = await api.listProfiles();
      setProfiles(profiles);
      setError("");
    } catch (e) {
      setError((e as Error).message);
    }
  }
  useEffect(() => {
    refresh();
  }, []);

  if (id) return <ProfileEditor id={id} onChanged={refresh} />;

  return (
    <ProfileList profiles={profiles} error={error} onChanged={refresh} />
  );
}

function ProfileList({
  profiles,
  error,
  onChanged,
}: {
  profiles: Profile[];
  error: string;
  onChanged: () => void;
}) {
  const { t, tf } = useT();
  const [adding, setAdding] = useState(false);
  const [name, setName] = useState("");
  const [busy, setBusy] = useState(false);
  const [addError, setAddError] = useState("");

  async function create(e: React.FormEvent) {
    e.preventDefault();
    setBusy(true);
    setAddError("");
    try {
      const p = await api.createProfile(name.trim());
      onChanged();
      setAdding(false);
      setName("");
      navigate({ view: "profiles", id: p.id });
    } catch (e) {
      setAddError((e as Error).message);
    } finally {
      setBusy(false);
    }
  }

  return (
    <div>
      <div className="mb-6 flex items-end justify-between gap-4">
        <div>
          <h1 className="font-display text-[26px] font-semibold tracking-tight">{t("接入配置")}</h1>
          <p className="mt-1 text-sm text-muted">
            {t("一套接入方式：服务端 inbound 骨架、每用户凭证与各客户端模板。")}
          </p>
        </div>
        <Button variant="primary" onClick={() => setAdding(true)}>
          <PlusIcon size={16} />
          {t("新增")}
        </Button>
      </div>

      {error && <ErrorBar text={error} />}

      {profiles.length === 0 ? (
        <Empty>{t("暂无接入配置。新建后绑定至节点。")}</Empty>
      ) : (
        <div className="flex flex-col gap-2.5">
          {profiles.map((p) => (
            <a
              key={p.id}
              href={href({ view: "profiles", id: p.id })}
              className="group flex items-center justify-between rounded-2xl border border-line bg-surface px-5 py-4 shadow-[var(--shadow-card)] transition-all hover:border-line-strong hover:shadow-[var(--shadow-lift)]"
            >
              <div>
                <div className="font-display text-[15px] font-semibold tracking-tight">
                  {p.name}
                </div>
                <div className="mt-0.5 text-xs text-muted">
                  {tf("{n} 个节点", { n: p.node_ids.length })}
                  <span className="mx-1.5 text-faint">·</span>
                  {tf("{n} 份客户端模板", { n: (p.client_kinds ?? []).length })}
                  {!p.inbound_template && (
                    <>
                      <span className="mx-1.5 text-faint">·</span>
                      <span className="text-warn">{t("未填 inbound 骨架")}</span>
                    </>
                  )}
                </div>
              </div>
              <span className="text-faint transition-colors group-hover:text-ink">→</span>
            </a>
          ))}
        </div>
      )}

      {adding && (
        <Modal onClose={() => setAdding(false)}>
          <form onSubmit={create}>
            <h3 className="font-display text-lg font-semibold tracking-tight">{t("新增接入配置")}</h3>
            <Field label={t("名字")}>
              <input
                autoFocus
                value={name}
                onChange={(e) => setName(e.target.value)}
                placeholder="tokyo-reality"
                className={inputCls}
              />
            </Field>
            {addError && <p className="mt-3 text-sm text-danger">{addError}</p>}
            <div className="mt-5 flex justify-end gap-2">
              <Button type="button" variant="ghost" onClick={() => setAdding(false)}>
                {t("取消")}
              </Button>
              <Button type="submit" variant="primary" disabled={busy || !name.trim()}>
                {t("创建")}
              </Button>
            </div>
          </form>
        </Modal>
      )}
    </div>
  );
}

function ProfileEditor({ id, onChanged }: { id: string; onChanged: () => void }) {
  const { t, tf } = useT();
  const dark = useIsDark();
  const [profile, setProfile] = useState<Profile | null>(null);
  const [vars, setVars] = useState<Variable[]>([]);
  const [nodes, setNodes] = useState<Node[]>([]);
  const [error, setError] = useState("");
  const [saved, setSaved] = useState(false);
  const [busy, setBusy] = useState(false);
  const [applyResult, setApplyResult] = useState<string>("");

  const [inbound, setInbound] = useState("");
  const [clientEntry, setClientEntry] = useState("");
  const [clientTemplates, setClientTemplates] = useState<Record<string, string>>({});
  const [activeClient, setActiveClient] = useState<string>(CLIENT_KINDS[0]);

  async function load() {
    try {
      const [p, v, n] = await Promise.all([
        api.getProfile(id),
        api.listVariables(),
        api.listNodes(),
      ]);
      setProfile(p);
      setInbound(p.inbound_template);
      setClientEntry(p.client_entry);
      setClientTemplates(p.client_templates ?? {});
      setVars(v.variables);
      setNodes(n.nodes);
      setError("");
    } catch (e) {
      setError((e as Error).message);
    }
  }
  useEffect(() => {
    load();
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [id]);

  // What a template may reference: global variables, this profile's own,
  // node-scoped ones, the built-in node metadata, and (for client templates)
  // the per-user entries.
  //
  // Node scope is the whole point of node scope: one template, a different
  // value per node. A name is only safely referenceable when EVERY node this
  // profile is bound to defines it, though — the template renders once per
  // node, and a node that is missing the variable fails to render rather than
  // falling back. So the ones defined on only some bound nodes are reported
  // separately, naming the nodes that would break, instead of being called
  // undefined (they are not) or silently accepted (they do not all work).
  const { known, secrets, partial } = useMemo(() => {
    const known = new Set<string>(BUILTIN_NODE_VARS);
    const secrets = new Set<string>();
    const perNode = new Map<string, Set<string>>(); // variable name -> node ids
    for (const v of vars) {
      const add = (into: Set<string>) => {
        for (const c of v.components) {
          const name = c.name ? `${v.name}.${c.name}` : v.name;
          into.add(name);
          if (c.secret) secrets.add(name);
        }
      };
      if (v.scope === "global" || (v.scope === "profile" && v.profile_id === id)) {
        add(known);
      } else if (v.scope === "node" && v.node_id) {
        const names = new Set<string>();
        add(names);
        for (const n of names) {
          const holders = perNode.get(n) ?? new Set<string>();
          holders.add(v.node_id);
          perNode.set(n, holders);
        }
      }
    }

    const bound = profile?.node_ids ?? [];
    const partial = new Map<string, string[]>(); // variable name -> node names missing it
    for (const [name, holders] of perNode) {
      const missing = bound.filter((n) => !holders.has(n));
      // Not bound to anything yet: nothing renders, so nothing is broken.
      if (bound.length === 0 || missing.length === 0) {
        known.add(name);
      } else {
        partial.set(
          name,
          missing.map((nid) => nodes.find((n) => n.id === nid)?.name ?? nid),
        );
      }
    }
    return { known, secrets, partial };
  }, [vars, id, profile?.node_ids, nodes]);

  // The client-entry template is rendered per user, so it may also use the
  // user-scope names the Core fills in at subscription time (M3).
  const clientEntryKnown = useMemo(() => {
    const s = new Set(known);
    s.add("user.uuid");
    s.add("user.email");
    return s;
  }, [known]);

  async function save() {
    if (!profile) return;
    setBusy(true);
    setError("");
    try {
      await api.updateProfile(id, {
        inbound_template: inbound,
        client_entry: clientEntry,
      });
      for (const [kind, tmpl] of Object.entries(clientTemplates)) {
        if (tmpl.trim()) await api.putClientTemplate(id, kind, tmpl);
      }
      setSaved(true);
      setTimeout(() => setSaved(false), 1800);
      onChanged();
      load();
    } catch (e) {
      setError((e as Error).message);
    } finally {
      setBusy(false);
    }
  }

  async function apply() {
    setBusy(true);
    setError("");
    setApplyResult("");
    try {
      const r = await api.applyProfile(id);
      setApplyResult(
        r.failed === 0
          ? tf("已下发到 {n} 个节点", { n: r.applied })
          : tf("{ok} 个成功，{bad} 个失败：", { ok: r.applied, bad: r.failed }) +
              Object.entries(r.nodes)
                .filter(([, v]) => v !== "ok")
                .map(([k, v]) => `${nodes.find((n) => n.id === k)?.name ?? k}: ${v}`)
                .join("；"),
      );
    } catch (e) {
      setError((e as Error).message);
    } finally {
      setBusy(false);
    }
  }

  if (!profile) {
    return error ? <ErrorBar text={error} /> : <p className="text-sm text-muted">{t("载入中…")}</p>;
  }

  const bound = new Set(profile.node_ids);

  return (
    <div>
      <a
        href={href({ view: "profiles" })}
        className="text-sm text-muted transition-colors hover:text-ink"
      >
        {t("← 接入配置")}
      </a>
      <div className="mt-3 mb-6 flex flex-wrap items-end justify-between gap-4">
        <div>
          <h1 className="font-display text-[26px] font-semibold tracking-tight">
            {profile.name}
          </h1>
          <p className="mt-1 text-sm text-muted">
            {t("服务端与客户端引用同一变量组的不同分量，因此不会配错。")}
          </p>
        </div>
        <div className="flex items-center gap-2">
          <Button variant="outline" onClick={apply} disabled={busy || bound.size === 0}>
            {tf("下发到 {n} 个节点", { n: bound.size })}
          </Button>
          <Button variant="primary" onClick={save} disabled={busy}>
            {saved ? (
              <>
                <CheckIcon size={15} /> {t("已保存")}
              </>
            ) : busy ? (
              t("保存中…")
            ) : (
              t("保存")
            )}
          </Button>
        </div>
      </div>

      {error && <ErrorBar text={error} />}
      {applyResult && (
        <div className="mb-5 rounded-xl border border-line bg-surface px-4 py-3 text-sm">
          {applyResult}
        </div>
      )}

      <Section
        title={t("服务端 inbound 骨架")}
        hint={t("渲染后作为一项写入节点 config.json 的 inbounds。clients 留空，由 Core 按绑定用户注入。")}
      >
        <TemplateEditor
          value={inbound}
          onChange={setInbound}
          known={known}
          secrets={secrets}
          partial={partial}
          dark={dark}
          height={300}
        />
      </Section>

      <Section
        title={t("每用户 client-entry")}
        hint={t("clients 数组中单个用户对象的模板。在线增删用户即修改此项。")}
      >
        <TemplateEditor
          value={clientEntry}
          onChange={setClientEntry}
          known={clientEntryKnown}
          secrets={secrets}
          partial={partial}
          dark={dark}
          height={110}
        />
      </Section>

      <Section
        title={t("客户端模板")}
        hint={t("每种客户端各写一份，不经订阅转换。此处不可引用私钥变量。")}
      >
        <div className="mb-2.5 flex flex-wrap gap-1.5">
          {CLIENT_KINDS.map((k) => (
            <button
              key={k}
              onClick={() => setActiveClient(k)}
              className={cn(
                "rounded-lg border px-2.5 py-1.5 font-mono text-[12px] transition-colors",
                activeClient === k
                  ? "border-signal bg-signal-soft text-ink"
                  : "border-line-strong text-muted hover:border-signal",
                clientTemplates[k]?.trim() ? "" : "opacity-60",
              )}
            >
              {k}
              {clientTemplates[k]?.trim() ? "" : " ·" + t("未填")}
            </button>
          ))}
        </div>
        <TemplateEditor
          value={clientTemplates[activeClient] ?? ""}
          onChange={(v) => setClientTemplates((t) => ({ ...t, [activeClient]: v }))}
          known={clientEntryKnown}
          secrets={secrets}
          partial={partial}
          clientSide
          dark={dark}
          height={260}
        />
      </Section>

      <Section title={t("绑定节点")} hint={t("绑定后，下发时将此 inbound 装配进该节点配置。")}>
        {nodes.length === 0 ? (
          <p className="text-sm text-muted">{t("还没有节点。")}</p>
        ) : (
          <div className="flex flex-wrap gap-2">
            {nodes.map((n) => {
              const on = bound.has(n.id);
              return (
                <button
                  key={n.id}
                  onClick={async () => {
                    on ? await api.unbindNode(id, n.id) : await api.bindNode(id, n.id);
                    load();
                    onChanged();
                  }}
                  className={cn(
                    "inline-flex items-center gap-2 rounded-lg border px-3 py-1.5 text-sm transition-colors",
                    on
                      ? "border-online text-online"
                      : "border-line-strong text-muted hover:border-signal hover:text-ink",
                  )}
                  style={on ? { background: "color-mix(in srgb, var(--online) 10%, transparent)" } : undefined}
                >
                  {on && <CheckIcon size={14} />}
                  {n.name}
                </button>
              );
            })}
          </div>
        )}
      </Section>

      <div className="mt-8 flex justify-end">
        <Button
          variant="danger"
          onClick={async () => {
            if (!confirm(tf("删除接入配置「{name}」？其绑定关系与变量将一并删除。", { name: profile.name }))) return;
            await api.deleteProfile(id);
            onChanged();
            navigate({ view: "profiles" });
          }}
        >
          <TrashIcon size={15} />
          {t("删除此接入配置")}
        </Button>
      </div>
    </div>
  );
}

function Section({
  title,
  hint,
  children,
}: {
  title: string;
  hint?: string;
  children: React.ReactNode;
}) {
  return (
    <section className="mb-7">
      <h2 className="font-display text-[15px] font-semibold tracking-tight">{title}</h2>
      {hint && <p className="mb-2.5 mt-0.5 text-xs text-muted">{hint}</p>}
      {children}
    </section>
  );
}

export { IconButton };
