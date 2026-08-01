import { useEffect, useState } from "react";
import { api, type ExternalProxy, type ExternalSub, type Node, type ProxyUser } from "../api";
import { Button, IconButton } from "../components/ui";
import { PlusIcon, TrashIcon } from "../components/icons";
import { Empty, ErrorBar, Field, Modal, inputCls } from "../components/primitives";
import { relativeTime } from "../format";
import { cn } from "../lib/cn";
import { useT } from "../lib/i18n";

/**
 * Nodes this panel does not run.
 *
 * A provider hands over a link and nothing else, so none of the fleet's
 * machinery applies: one credential shared by every subscriber, no per-user
 * isolation, no traffic counted, and disabling a user does not stop them using
 * it. The page says so where an operator adds one rather than leaving it to be
 * discovered.
 *
 * What the panel adds is the chain: an external node can be reached through
 * another node — one of this fleet's own, or another external one — so the
 * provider sees that node and not the subscriber. Chaining through a second
 * provider is the case with no fleet node in the middle at all: a relay bought
 * from one, an exit from another.
 *
 * Per-subscriber access lives here too, read from the node's end. The same
 * relation is on the user page the other way round; an operator who has just
 * added a source is thinking about the node, not about each subscriber in turn.
 */
export function ExternalsPage() {
  const { t, tf } = useT();
  const [subs, setSubs] = useState<ExternalSub[]>([]);
  const [nodes, setNodes] = useState<Node[]>([]);
  const [error, setError] = useState("");
  const [adding, setAdding] = useState(false);
  const [busy, setBusy] = useState("");

  async function refresh() {
    try {
      const [e, n] = await Promise.all([api.listExternals(), api.listNodes()]);
      setSubs(e.externals);
      setNodes(n.nodes);
      setError("");
    } catch (err) {
      setError((err as Error).message);
    }
  }
  useEffect(() => {
    refresh();
  }, []);

  async function refetch(id: string) {
    setBusy(id);
    try {
      await api.refreshExternal(id);
      await refresh();
    } catch (e) {
      setError((e as Error).message);
    } finally {
      setBusy("");
    }
  }

  async function remove(sub: ExternalSub) {
    if (!confirm(tf("删除「{name}」？其节点将从所有订阅中移除。", { name: sub.name }))) return;
    try {
      await api.deleteExternal(sub.id);
      await refresh();
    } catch (e) {
      setError((e as Error).message);
    }
  }

  return (
    <div>
      <div className="mb-6 flex items-end justify-between gap-4">
        <div>
          <h1 className="font-display text-[26px] font-semibold tracking-tight">{t("外部节点")}</h1>
          <p className="mt-1 text-sm text-muted">
            {t("别人提供的订阅或单条链接。每个节点可指定前置——本机队节点或另一个外部节点——链式出站。")}
          </p>
        </div>
        <Button variant="primary" onClick={() => setAdding(true)}>
          <PlusIcon size={16} />
          {t("新增")}
        </Button>
      </div>

      {error && <ErrorBar text={error} />}

      {subs.length === 0 ? (
        <Empty>{t("还没有外部节点。添加后它们会出现在所有订阅者的 Clash 订阅里。")}</Empty>
      ) : (
        <div className="flex flex-col gap-2.5">
          {subs.map((s) => (
            <SubCard
              key={s.id}
              sub={s}
              nodes={nodes}
              externals={subs}
              busy={busy === s.id}
              onRefresh={() => refetch(s.id)}
              onDelete={() => remove(s)}
              onChanged={refresh}
            />
          ))}
        </div>
      )}

      {adding && <AddDialog onClose={() => setAdding(false)} onAdded={refresh} />}
    </div>
  );
}

function SubCard({
  sub,
  nodes,
  externals,
  busy,
  onRefresh,
  onDelete,
  onChanged,
}: {
  sub: ExternalSub;
  nodes: Node[];
  externals: ExternalSub[];
  busy: boolean;
  onRefresh: () => void;
  onDelete: () => void;
  onChanged: () => void;
}) {
  const { t, tf } = useT();
  const [open, setOpen] = useState(false);

  async function toggleSource() {
    await api.updateExternal(sub.id, { enabled: !sub.enabled });
    onChanged();
  }

  return (
    <article className="rounded-2xl border border-line bg-surface px-5 py-4 shadow-[var(--shadow-card)]">
      <div className="flex items-start justify-between gap-4">
        <button onClick={() => setOpen((v) => !v)} className="min-w-0 text-left">
          <div className="flex items-center gap-2">
            <span className="font-display text-[15px] font-semibold tracking-tight">{sub.name}</span>
            {!sub.enabled && (
              <span className="rounded-md px-1.5 py-0.5 text-[11px] text-faint"
                style={{ background: "color-mix(in srgb, var(--muted) 10%, transparent)" }}>
                {t("已停用")}
              </span>
            )}
          </div>
          <div className="mt-0.5 truncate font-mono text-xs text-faint">
            {sub.url || t("（手动粘贴，不自动更新）")}
          </div>
        </button>
        <div className="flex shrink-0 items-center gap-1">
          <Button variant="ghost" onClick={toggleSource}>
            {sub.enabled ? t("停用") : t("启用")}
          </Button>
          {sub.url && (
            <Button variant="ghost" onClick={onRefresh} disabled={busy}>
              {busy ? t("更新中…") : t("更新")}
            </Button>
          )}
          <IconButton label={t("删除")} onClick={onDelete}>
            <TrashIcon size={15} />
          </IconButton>
        </div>
      </div>

      <div className="mt-3 flex flex-wrap items-center gap-x-5 gap-y-1 text-xs">
        <span className="text-muted">{tf("{n} 个节点", { n: sub.proxies.length })}</span>
        <span className="text-faint">
          {sub.fetched_at ? tf("更新于 {when}", { when: relativeTime(sub.fetched_at) }) : t("尚未获取")}
        </span>
        <button className="text-faint underline-offset-2 hover:underline" onClick={() => setOpen((v) => !v)}>
          {open ? t("收起") : t("展开节点")}
        </button>
      </div>

      {sub.last_error && (
        <div className="mt-2 rounded-lg px-2.5 py-1.5 text-xs text-warn"
          style={{ background: "color-mix(in srgb, var(--warn) 10%, transparent)" }}>
          {sub.last_error}
        </div>
      )}

      {open && (
        <div className="mt-4 border-t border-line pt-4">
          {sub.proxies.length === 0 ? (
            <p className="text-sm text-muted">{t("这个来源里没有解析出节点。")}</p>
          ) : (
            <div className="flex flex-col gap-2">
              {sub.proxies.map((p) => (
                <ProxyRow
                  key={p.id}
                  subId={sub.id}
                  proxy={p}
                  nodes={nodes}
                  externals={externals}
                  onChanged={onChanged}
                />
              ))}
            </div>
          )}
        </div>
      )}
    </article>
  );
}

function ProxyRow({
  subId,
  proxy,
  nodes,
  externals,
  onChanged,
}: {
  subId: string;
  proxy: ExternalProxy;
  nodes: Node[];
  externals: ExternalSub[];
  onChanged: () => void;
}) {
  const { t } = useT();
  const [busy, setBusy] = useState(false);
  const [error, setError] = useState("");
  const [whoOpen, setWhoOpen] = useState(false);

  async function patch(p: { chain_node_id?: string; chain_proxy_id?: string; enabled?: boolean }) {
    setBusy(true);
    setError("");
    try {
      await api.setExternalProxy(subId, proxy.id, p);
      onChanged();
    } catch (e) {
      const msg = (e as Error).message;
      // The one server error an operator reaches by trying rather than by
      // misusing the API, so it is worth saying in their language. The rest of
      // the console shows server text as it comes.
      setError(msg.includes("loop back on itself") ? t("这样会让链路绕回自己。") : msg);
    } finally {
      setBusy(false);
    }
  }

  // One <select> for a setting with two kinds of value. Prefixing the value
  // with its kind keeps them apart in one control, because to the operator this
  // is one question — what does this node dial through — and splitting it into
  // two dropdowns would ask it twice and allow both to be answered.
  const value = proxy.chain_proxy_id
    ? `p:${proxy.chain_proxy_id}`
    : proxy.chain_node_id
      ? `n:${proxy.chain_node_id}`
      : "";
  function onPick(v: string) {
    if (v.startsWith("p:")) patch({ chain_proxy_id: v.slice(2) });
    else if (v.startsWith("n:")) patch({ chain_node_id: v.slice(2) });
    else patch({ chain_node_id: "", chain_proxy_id: "" });
  }

  return (
    <div className={cn(!proxy.enabled && "opacity-50")}>
      <div className="flex flex-wrap items-center gap-x-3 gap-y-2">
        <button
          onClick={() => patch({ enabled: !proxy.enabled })}
          disabled={busy}
          className={cn(
            "rounded-md border px-2 py-0.5 text-[11px] transition-colors",
            proxy.enabled
              ? "border-[color-mix(in_srgb,var(--online)_45%,transparent)] text-online"
              : "border-line-strong text-faint",
          )}
        >
          {proxy.enabled ? t("启用") : t("停用")}
        </button>
        <span className="min-w-0 flex-1 truncate text-sm">{proxy.name}</span>
        <span className="font-mono text-[11px] text-faint">
          {proxy.type} · {proxy.server}:{proxy.port}
        </span>
        <button
          onClick={() => setWhoOpen((v) => !v)}
          className="text-[11px] text-faint underline-offset-2 hover:text-ink hover:underline"
        >
          {t("谁能用")}
        </button>
        {/* The chain is the reason this page exists: the provider sees another
            node rather than the subscriber. */}
        <select
          value={value}
          disabled={busy}
          onChange={(e) => onPick(e.target.value)}
          className="rounded-lg border border-line-strong bg-surface px-2 py-1 text-xs outline-none focus:border-signal"
        >
          <option value="">{t("直连")}</option>
          {nodes.map((n) => (
            <option key={n.id} value={`n:${n.id}`}>
              {t("经由")} {n.display_name || n.name}
            </option>
          ))}
          {externals.flatMap((s) =>
            s.proxies
              // Itself is not an option, and the panel refuses longer loops on
              // save — they cannot be spotted from one dropdown.
              .filter((o) => o.id !== proxy.id)
              .map((o) => (
                <option key={o.id} value={`p:${o.id}`}>
                  {t("经由")} {o.name} ({s.name})
                </option>
              )),
          )}
        </select>
      </div>
      {error && <p className="mt-1 text-xs text-danger">{error}</p>}
      {whoOpen && <ProxyAccess subId={subId} proxyId={proxy.id} />}
    </div>
  );
}

/**
 * Which subscribers get this external node.
 *
 * Denials, not grants — a node nobody has been asked about reaches everyone,
 * which is what an operator expects the moment they add a source, and it leaves
 * every existing subscriber untouched.
 */
function ProxyAccess({ subId, proxyId }: { subId: string; proxyId: string }) {
  const { t } = useT();
  const [users, setUsers] = useState<ProxyUser[]>([]);
  const [busy, setBusy] = useState(false);
  const [error, setError] = useState("");

  async function load() {
    try {
      setUsers((await api.externalProxyUsers(subId, proxyId)).users);
      setError("");
    } catch (e) {
      setError((e as Error).message);
    }
  }
  useEffect(() => {
    load();
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [subId, proxyId]);

  async function toggle(u: ProxyUser) {
    const next = users.map((x) => (x.id === u.id ? { ...x, allowed: !x.allowed } : x));
    setUsers(next);
    setBusy(true);
    try {
      await api.setExternalProxyUsers(subId, proxyId, {
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
              className={cn(
                "rounded-md border px-2 py-0.5 text-[11px] transition-colors disabled:opacity-50",
                u.allowed
                  ? "border-[color-mix(in_srgb,var(--online)_45%,transparent)] text-online"
                  : "border-line-strong text-faint hover:border-signal hover:text-ink",
              )}
            >
              {u.name}
            </button>
          ))}
        </div>
      )}
      <p className="mt-1.5 text-[11px] text-faint">
        {t("取消后此节点不再出现在该订阅者的订阅里，下次刷新订阅时生效。")}
      </p>
    </div>
  );
}

function AddDialog({ onClose, onAdded }: { onClose: () => void; onAdded: () => void }) {
  const { t } = useT();
  const [mode, setMode] = useState<"url" | "paste">("url");
  const [name, setName] = useState("");
  const [url, setUrl] = useState("");
  const [body, setBody] = useState("");
  const [busy, setBusy] = useState(false);
  const [error, setError] = useState("");

  async function save(e: React.FormEvent) {
    e.preventDefault();
    setBusy(true);
    setError("");
    try {
      await api.createExternal(
        mode === "url"
          ? { name: name.trim(), url: url.trim() }
          : { name: name.trim(), body: body.trim() },
      );
      onAdded();
      onClose();
    } catch (err) {
      setError((err as Error).message);
    } finally {
      setBusy(false);
    }
  }

  const ready = !!name.trim() && (mode === "url" ? !!url.trim() : !!body.trim());

  return (
    <Modal onClose={onClose} wide>
      <form onSubmit={save}>
        <h3 className="font-display text-lg font-semibold tracking-tight">{t("新增外部节点")}</h3>

        {/* Said here, where the decision is made. These are not fleet nodes and
            the differences are not things that get fixed later. */}
        <div className="mt-3 rounded-xl px-3 py-2 text-xs text-warn"
          style={{ background: "color-mix(in srgb, var(--warn) 10%, transparent)" }}>
          {t("外部节点由对方运营：所有订阅者共用同一份凭证，没有按用户隔离、没有流量统计，停用某个用户也不会让他连不上。")}
        </div>

        <div className="mt-4 flex gap-1.5">
          {(["url", "paste"] as const).map((m) => (
            <button
              key={m}
              type="button"
              onClick={() => setMode(m)}
              className={cn(
                "rounded-lg px-3 py-1.5 text-sm transition-colors",
                mode === m ? "bg-signal text-signal-ink" : "text-muted hover:text-ink",
              )}
            >
              {m === "url" ? t("订阅链接") : t("直接粘贴")}
            </button>
          ))}
        </div>

        <Field label={t("名字")}>
          <input
            autoFocus
            value={name}
            onChange={(e) => setName(e.target.value)}
            placeholder={t("例如 机场 A")}
            className={inputCls}
          />
        </Field>

        {mode === "url" ? (
          <Field label={t("订阅地址")}>
            <input
              value={url}
              onChange={(e) => setUrl(e.target.value)}
              placeholder="https://example.com/sub/xxxx"
              className={inputCls}
            />
            <span className="mt-1 block text-xs text-faint">
              {t("Clash YAML 或分享链接列表均可，自动识别。每天自动更新一次。")}
            </span>
          </Field>
        ) : (
          <Field label={t("内容")}>
            <textarea
              value={body}
              onChange={(e) => setBody(e.target.value)}
              rows={6}
              placeholder={"vless://…\ntrojan://…"}
              className={cn(inputCls, "font-mono text-xs")}
            />
            <span className="mt-1 block text-xs text-faint">
              {t("粘贴 Clash 配置片段或若干条分享链接。不会自动更新。")}
            </span>
          </Field>
        )}

        {error && <p className="mt-3 text-sm text-danger">{error}</p>}

        <div className="mt-5 flex justify-end gap-2">
          <Button type="button" variant="ghost" onClick={onClose}>
            {t("取消")}
          </Button>
          <Button type="submit" variant="primary" disabled={busy || !ready}>
            {busy ? t("解析中…") : t("添加")}
          </Button>
        </div>
      </form>
    </Modal>
  );
}
