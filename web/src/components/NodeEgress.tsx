import { useEffect, useState } from "react";
import { api, type EgressRule, type ExternalSub, type Node, type Profile } from "../api";
import { Button, IconButton } from "./ui";
import { PlusIcon, TrashIcon, ArrowUpIcon, ArrowDownIcon } from "./icons";
import { Field, Modal, inputCls } from "./primitives";
import { cn } from "../lib/cn";
import { useT } from "../lib/i18n";

/**
 * Where a node sends particular traffic instead of straight out of itself —
 * "Netflix leaves through the Japanese line, the rest goes direct".
 *
 * The list is ordered because routing is first-match: position IS priority,
 * which is why the arrows are part of the feature rather than a convenience.
 * Restricted destinations still outrank every rule here, and every rule here
 * outranks the relay lines; that ordering is fixed in the assembler, not
 * something the operator can get wrong from this page.
 */
export function NodeEgress({
  node,
  nodes,
  profiles,
  externals,
}: {
  node: Node;
  nodes: Node[];
  profiles: Profile[];
  externals: ExternalSub[];
}) {
  const { t } = useT();
  const [rules, setRules] = useState<EgressRule[]>([]);
  const [adding, setAdding] = useState(false);
  const [busy, setBusy] = useState(false);
  const [error, setError] = useState("");

  async function refresh() {
    try {
      setRules((await api.listEgress(node.id)).rules);
      setError("");
    } catch (e) {
      setError((e as Error).message);
    }
  }
  useEffect(() => {
    refresh();
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [node.id]);

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

  async function move(i: number, to: number) {
    if (to < 0 || to >= rules.length) return;
    const next = rules.slice();
    const [row] = next.splice(i, 1);
    next.splice(to, 0, row);
    setRules(next);
    await mutate(() => api.reorderEgress(node.id, next.map((r) => r.id)));
  }

  return (
    <div className="mt-4 border-t border-line pt-4">
      <div className="mb-2 flex flex-wrap items-center justify-between gap-x-4 gap-y-1.5">
        <div>
          <div className="text-[13px] font-medium">{t("出站分流")}</div>
          <p className="mt-0.5 text-xs text-muted">
            {t("命中的流量从指定的落点出去，其余照常。自上而下匹配，第一条命中的生效。")}
          </p>
        </div>
        <Button size="sm" onClick={() => setAdding(true)}>
          <PlusIcon size={14} />
          {t("新增规则")}
        </Button>
      </div>

      {error && <p className="mb-2 text-sm text-danger">{error}</p>}

      {rules.length === 0 ? (
        <p className="text-xs text-muted">{t("还没有出站分流规则，全部流量直接从这个节点出去。")}</p>
      ) : (
        <ol className="overflow-hidden rounded-xl border border-line">
          {rules.map((r, i) => (
            <li key={r.id} className="border-b border-line px-3 py-2 last:border-b-0">
              <div className="flex flex-wrap items-center gap-x-3 gap-y-1">
                <span className="w-5 shrink-0 text-right font-mono text-[11px] text-faint">
                  {i + 1}
                </span>
                <span className="min-w-0 flex-1 truncate text-sm">{r.label}</span>
                <span className="min-w-0 truncate font-mono text-[11px] text-faint">
                  {[...(r.domains ?? []), ...(r.ips ?? [])].join("  ")}
                </span>
                <span className="shrink-0 text-[11px] text-muted">{"→ "}{r.target_name}</span>
                <button
                  disabled={busy}
                  onClick={() => mutate(() => api.updateEgress(r.id, { enabled: !r.enabled }))}
                  className={cn(
                    "shrink-0 rounded-md border px-2 py-0.5 text-[11px] transition-colors",
                    r.enabled
                      ? "border-[color-mix(in_srgb,var(--online)_45%,transparent)] text-online"
                      : "border-line-strong text-muted hover:border-signal hover:text-ink",
                  )}
                >
                  {r.enabled ? t("启用") : t("停用")}
                </button>
                <span className="flex shrink-0 items-center gap-0.5">
                  <IconButton label={t("上移")} onClick={() => move(i, i - 1)}>
                    <ArrowUpIcon size={13} />
                  </IconButton>
                  <IconButton label={t("下移")} onClick={() => move(i, i + 1)}>
                    <ArrowDownIcon size={13} />
                  </IconButton>
                  <IconButton
                    label={t("删除")}
                    onClick={() =>
                      mutate(async () => {
                        if (!window.confirm(t("删除这条出站分流规则？"))) return;
                        await api.deleteEgress(r.id);
                      })
                    }
                  >
                    <TrashIcon size={13} />
                  </IconButton>
                </span>
              </div>
              {r.problem && <p className="mt-1 pl-8 text-xs text-danger">{r.problem}</p>}
            </li>
          ))}
        </ol>
      )}

      {adding && (
        <EgressDialog
          node={node}
          nodes={nodes}
          profiles={profiles}
          externals={externals}
          onClose={() => setAdding(false)}
          onSaved={refresh}
        />
      )}
    </div>
  );
}

function EgressDialog({
  node,
  nodes,
  profiles,
  externals,
  onClose,
  onSaved,
}: {
  node: Node;
  nodes: Node[];
  profiles: Profile[];
  externals: ExternalSub[];
  onClose: () => void;
  onSaved: () => void;
}) {
  const { t } = useT();
  const [label, setLabel] = useState("");
  const [domains, setDomains] = useState("");
  const [ips, setIps] = useState("");
  const [kind, setKind] = useState<"direct" | "external" | "node">("node");
  const [proxyID, setProxyID] = useState("");
  const [nodeID, setNodeID] = useState("");
  const [profileID, setProfileID] = useState("");
  const [busy, setBusy] = useState(false);
  const [error, setError] = useState("");

  // Only profiles the chosen node actually serves can be dialled; offering
  // the others would produce a rule that assembles and never connects.
  const dialable = profiles.filter((p) => (p.node_ids ?? []).includes(nodeID));

  async function submit() {
    setBusy(true);
    setError("");
    try {
      await api.createEgress(node.id, {
        label: label.trim(),
        domains,
        ips,
        target_kind: kind,
        target_proxy_id: kind === "external" ? proxyID : undefined,
        target_node_id: kind === "node" ? nodeID : undefined,
        target_profile_id: kind === "node" ? profileID : undefined,
      });
      onSaved();
      onClose();
    } catch (e) {
      setError((e as Error).message);
    } finally {
      setBusy(false);
    }
  }

  const ready =
    label.trim() &&
    (domains.trim() || ips.trim()) &&
    (kind === "direct" ||
      (kind === "external" && proxyID) ||
      (kind === "node" && nodeID && profileID));

  return (
    <Modal onClose={onClose}>
      <h2 className="font-display text-lg font-semibold tracking-tight">{t("新增出站分流")}</h2>
      <p className="mt-1 text-sm text-muted">
        {t("在这个节点上，命中的流量改从别处出去。域名和 IP 两栏可以只填一栏。")}
      </p>

      <Field label={t("名字")}>
        <input
          autoFocus
          className={inputCls}
          value={label}
          onChange={(e) => setLabel(e.target.value)}
          placeholder="Netflix"
        />
      </Field>
      <Field label={t("域名 / geosite（每行一个）")}>
        <textarea
          className={cn(inputCls, "h-20 resize-y font-mono text-xs")}
          value={domains}
          onChange={(e) => setDomains(e.target.value)}
          placeholder={"geosite:netflix\nnflxvideo.net"}
        />
      </Field>
      <Field label={t("IP / geoip（每行一个）")}>
        <textarea
          className={cn(inputCls, "h-16 resize-y font-mono text-xs")}
          value={ips}
          onChange={(e) => setIps(e.target.value)}
          placeholder={"geoip:netflix\n1.2.3.0/24"}
        />
      </Field>

      <Field label={t("从哪出去")}>
        <div className="flex flex-wrap gap-1.5">
          {(
            [
              ["node", "本机队节点"],
              ["external", "外部节点"],
              ["direct", "直连"],
            ] as const
          ).map(([k, lbl]) => (
            <button
              key={k}
              type="button"
              onClick={() => setKind(k)}
              className={cn(
                "rounded-lg border px-2.5 py-1.5 text-[13px] transition-colors",
                kind === k
                  ? "border-signal bg-signal-soft text-ink"
                  : "border-line-strong text-muted hover:border-signal",
              )}
            >
              {t(lbl)}
            </button>
          ))}
        </div>
      </Field>

      {kind === "node" && (
        <>
          <Field label={t("目标节点")}>
            <select
              className={inputCls}
              value={nodeID}
              onChange={(e) => {
                setNodeID(e.target.value);
                setProfileID("");
              }}
            >
              <option value="">{t("请选择")}</option>
              {nodes
                .filter((n) => n.id !== node.id)
                .map((n) => (
                  <option key={n.id} value={n.id}>
                    {n.display_name || n.name}
                  </option>
                ))}
            </select>
          </Field>
          <Field label={t("用目标节点的哪个接入配置拨号")}>
            <select
              className={inputCls}
              value={profileID}
              onChange={(e) => setProfileID(e.target.value)}
              disabled={!nodeID}
            >
              <option value="">{t("请选择")}</option>
              {dialable.map((p) => (
                <option key={p.id} value={p.id}>
                  {p.name}
                </option>
              ))}
            </select>
          </Field>
        </>
      )}

      {kind === "external" && (
        <Field label={t("目标外部节点")}>
          <select className={inputCls} value={proxyID} onChange={(e) => setProxyID(e.target.value)}>
            <option value="">{t("请选择")}</option>
            {externals.flatMap((s) =>
              (s.proxies ?? []).map((p) => (
                <option key={p.id} value={p.id}>
                  {p.name} ({s.name})
                </option>
              )),
            )}
          </select>
        </Field>
      )}

      {error && <p className="mt-3 text-sm text-danger">{error}</p>}
      <div className="mt-5 flex justify-end gap-2">
        <Button type="button" variant="ghost" onClick={onClose}>
          {t("取消")}
        </Button>
        <Button variant="primary" onClick={submit} disabled={busy || !ready}>
          {busy ? t("创建中…") : t("创建")}
        </Button>
      </div>
    </Modal>
  );
}
