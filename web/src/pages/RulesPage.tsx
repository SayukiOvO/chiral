import { useEffect, useMemo, useState } from "react";
import { api, type Preset, type Ruleset } from "../api";
import { Button, IconButton } from "../components/ui";
import { PlusIcon, TrashIcon } from "../components/icons";
import { Empty, ErrorBar, Field, Modal, inputCls } from "../components/primitives";
import { relativeTime } from "../format";
import { cn } from "../lib/cn";
import { useT } from "../lib/i18n";

/**
 * Routing rules for clash-family clients.
 *
 * A ruleset is an ACL4SSR preset or an .ini the operator points at; both are
 * fetched, cached and rendered into each subscriber's subscription. Which one
 * a subscriber gets is set on the user, so this page is about the sources
 * themselves.
 */
export function RulesPage() {
  const { t, tf } = useT();
  const [rulesets, setRulesets] = useState<Ruleset[]>([]);
  const [presets, setPresets] = useState<Preset[]>([]);
  const [error, setError] = useState("");
  const [adding, setAdding] = useState(false);
  const [busy, setBusy] = useState("");

  async function refresh() {
    try {
      const [r, p] = await Promise.all([api.listRulesets(), api.listPresets()]);
      setRulesets(r.rulesets);
      setPresets(p.presets);
      setError("");
    } catch (e) {
      setError((e as Error).message);
    }
  }
  useEffect(() => {
    refresh();
  }, []);

  async function refetch(id: string) {
    setBusy(id);
    try {
      await api.refreshRuleset(id);
      await refresh();
    } catch (e) {
      setError((e as Error).message);
    } finally {
      setBusy("");
    }
  }

  async function remove(r: Ruleset) {
    if (!confirm(tf("删除「{name}」？使用它的订阅者将回到无分流规则。", { name: r.name }))) return;
    try {
      await api.deleteRuleset(r.id);
      await refresh();
    } catch (e) {
      setError((e as Error).message);
    }
  }

  return (
    <div>
      <div className="mb-6 flex items-end justify-between gap-4">
        <div>
          <h1 className="font-display text-[26px] font-semibold tracking-tight">{t("分流规则")}</h1>
          <p className="mt-1 text-sm text-muted">
            {t("决定 Clash 类客户端把哪些流量走代理、哪些直连或拦截。在用户页指派给订阅者。")}
          </p>
        </div>
        <Button variant="primary" onClick={() => setAdding(true)}>
          <PlusIcon size={16} />
          {t("新增")}
        </Button>
      </div>

      {error && <ErrorBar text={error} />}

      {rulesets.length === 0 ? (
        <Empty>{t("还没有规则集。新增后在用户页指派，订阅即带上分流规则。")}</Empty>
      ) : (
        <div className="flex flex-col gap-2.5">
          {rulesets.map((r) => (
            <RulesetRow
              key={r.id}
              ruleset={r}
              busy={busy === r.id}
              onRefresh={() => refetch(r.id)}
              onDelete={() => remove(r)}
            />
          ))}
        </div>
      )}

      {adding && (
        <AddDialog
          presets={presets}
          taken={new Set(rulesets.map((r) => r.preset).filter(Boolean))}
          onClose={() => setAdding(false)}
          onAdded={refresh}
        />
      )}
    </div>
  );
}

function RulesetRow({
  ruleset: r,
  busy,
  onRefresh,
  onDelete,
}: {
  ruleset: Ruleset;
  busy: boolean;
  onRefresh: () => void;
  onDelete: () => void;
}) {
  const { t, tf } = useT();
  const loaded = r.groups > 0;
  return (
    <article className="rounded-2xl border border-line bg-surface px-5 py-4 shadow-[var(--shadow-card)]">
      <div className="flex items-start justify-between gap-4">
        <div className="min-w-0">
          <div className="flex items-center gap-2">
            <span className="font-display text-[15px] font-semibold tracking-tight">{r.name}</span>
            <span
              className={cn(
                "rounded-md px-1.5 py-0.5 text-[11px]",
                r.preset ? "text-muted" : "text-faint",
              )}
              style={{ background: "color-mix(in srgb, var(--muted) 10%, transparent)" }}
            >
              {r.preset ? t("内置") : t("自定义")}
            </span>
          </div>
          <div className="mt-0.5 truncate font-mono text-xs text-faint">{r.url}</div>
        </div>
        <div className="flex shrink-0 items-center gap-1">
          <Button variant="ghost" onClick={onRefresh} disabled={busy}>
            {busy ? t("更新中…") : t("更新")}
          </Button>
          <IconButton label={t("删除")} onClick={onDelete}>
            <TrashIcon size={15} />
          </IconButton>
        </div>
      </div>

      <div className="mt-3 flex flex-wrap items-center gap-x-5 gap-y-1 text-xs">
        {loaded ? (
          <>
            <span className="text-muted">{tf("{n} 个策略组", { n: r.groups })}</span>
            <span className="text-muted">{tf("{n} 条规则", { n: r.rules })}</span>
            <span className="text-muted">{tf("{n} 个规则列表", { n: r.lists })}</span>
            <span className="text-faint">
              {r.fetched_at ? tf("更新于 {when}", { when: relativeTime(r.fetched_at) }) : t("尚未获取")}
            </span>
          </>
        ) : (
          <span className="text-warn">{t("尚未获取到内容，订阅暂不会带上分流规则")}</span>
        )}
      </div>

      {/* Shown rather than logged: a ruleset that quietly stopped updating
          serves last year's rules and looks exactly like a healthy one. */}
      {r.last_error && (
        <div className="mt-2 rounded-lg px-2.5 py-1.5 text-xs text-warn"
          style={{ background: "color-mix(in srgb, var(--warn) 10%, transparent)" }}>
          {r.last_error}
        </div>
      )}
    </article>
  );
}

function AddDialog({
  presets,
  taken,
  onClose,
  onAdded,
}: {
  presets: Preset[];
  taken: Set<string>;
  onClose: () => void;
  onAdded: () => void;
}) {
  const { t, tf } = useT();
  const [mode, setMode] = useState<"preset" | "custom">("preset");
  const [preset, setPreset] = useState("");
  const [name, setName] = useState("");
  const [url, setUrl] = useState("");
  const [busy, setBusy] = useState(false);
  const [error, setError] = useState("");
  const [filter, setFilter] = useState("");

  const shown = useMemo(() => {
    const q = filter.trim().toLowerCase();
    if (!q) return presets;
    return presets.filter(
      (p) => p.Name.toLowerCase().includes(q) || p.Key.toLowerCase().includes(q),
    );
  }, [presets, filter]);

  async function save(e: React.FormEvent) {
    e.preventDefault();
    setBusy(true);
    setError("");
    try {
      await api.createRuleset(
        mode === "preset" ? { preset, name: name.trim() || undefined } : { name: name.trim(), url: url.trim() },
      );
      onAdded();
      onClose();
    } catch (err) {
      setError((err as Error).message);
    } finally {
      setBusy(false);
    }
  }

  const ready = mode === "preset" ? !!preset : !!name.trim() && !!url.trim();

  return (
    <Modal onClose={onClose} wide>
      <form onSubmit={save}>
        <h3 className="font-display text-lg font-semibold tracking-tight">{t("新增分流规则")}</h3>
        <p className="mt-1 text-sm text-muted">
          {t("内置的是 ACL4SSR 各档预设；自定义可指向任意 subconverter 格式的 .ini。")}
        </p>

        <div className="mt-4 flex gap-1.5">
          {(["preset", "custom"] as const).map((m) => (
            <button
              key={m}
              type="button"
              onClick={() => setMode(m)}
              className={cn(
                "rounded-lg px-3 py-1.5 text-sm transition-colors",
                mode === m ? "bg-signal text-signal-ink" : "text-muted hover:text-ink",
              )}
            >
              {m === "preset" ? t("内置预设") : t("自定义 ini")}
            </button>
          ))}
        </div>

        {mode === "preset" ? (
          <>
            <Field label={t("搜索")}>
              <input
                value={filter}
                onChange={(e) => setFilter(e.target.value)}
                placeholder={t("按名称或键筛选")}
                className={inputCls}
              />
            </Field>
            <div className="mt-3 max-h-[46vh] overflow-y-auto rounded-xl border border-line">
              {shown.map((p) => {
                const already = taken.has(p.Key);
                return (
                  <button
                    key={p.Key}
                    type="button"
                    disabled={already}
                    onClick={() => setPreset(p.Key)}
                    className={cn(
                      "block w-full border-b border-line px-4 py-3 text-left last:border-b-0 transition-colors",
                      already ? "cursor-not-allowed opacity-45" : "hover:bg-raised",
                      preset === p.Key && "bg-raised",
                    )}
                  >
                    <div className="flex items-center justify-between gap-3">
                      <span className="text-sm font-medium">{p.Name}</span>
                      <span className="shrink-0 font-mono text-[11px] text-faint">
                        {tf("{g} 组 · {l} 列表", { g: p.Groups, l: p.Lists })}
                      </span>
                    </div>
                    <div className="mt-1 flex flex-wrap gap-1">
                      {(p.Features ?? []).map((f) => (
                        <span
                          key={f}
                          className="rounded px-1.5 py-0.5 text-[11px] text-muted"
                          style={{ background: "color-mix(in srgb, var(--muted) 10%, transparent)" }}
                        >
                          {f}
                        </span>
                      ))}
                      {already && <span className="text-[11px] text-faint">{t("已添加")}</span>}
                    </div>
                  </button>
                );
              })}
            </div>
            <Field label={t("名字（可留空）")}>
              <input
                value={name}
                onChange={(e) => setName(e.target.value)}
                placeholder={t("留空则用预设名")}
                className={inputCls}
              />
            </Field>
          </>
        ) : (
          <>
            <Field label={t("名字")}>
              <input
                autoFocus
                value={name}
                onChange={(e) => setName(e.target.value)}
                placeholder={t("例如 自用规则")}
                className={inputCls}
              />
            </Field>
            <Field label={t("ini 地址")}>
              <input
                value={url}
                onChange={(e) => setUrl(e.target.value)}
                placeholder="https://example.com/my.ini"
                className={inputCls}
              />
              <span className="mt-1 block text-xs text-faint">
                {t("subconverter 远程配置格式，需含 custom_proxy_group 与 ruleset 指令。")}
              </span>
            </Field>
          </>
        )}

        {error && <p className="mt-3 text-sm text-danger">{error}</p>}

        <div className="mt-5 flex justify-end gap-2">
          <Button type="button" variant="ghost" onClick={onClose}>
            {t("取消")}
          </Button>
          <Button type="submit" variant="primary" disabled={busy || !ready}>
            {busy ? t("获取中…") : t("添加并获取")}
          </Button>
        </div>
      </form>
    </Modal>
  );
}
