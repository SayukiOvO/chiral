import { useEffect, useState } from "react";
import {
  api,
  type GeneratorInfo,
  type Node,
  type Profile,
  type Scope,
  type Variable,
} from "../api";
import { Button, IconButton } from "../components/ui";
import { PlusIcon, TrashIcon } from "../components/icons";
import { cn } from "../lib/cn";
import { useT } from "../lib/i18n";

const SCOPE_LABEL: Record<Scope, string> = {
  global: "全局",
  profile: "接入配置",
  node: "节点",
};

export function VariablesPage() {
  const { t, tf } = useT();
  const [vars, setVars] = useState<Variable[]>([]);
  const [profiles, setProfiles] = useState<Profile[]>([]);
  const [nodes, setNodes] = useState<Node[]>([]);
  const [generators, setGenerators] = useState<GeneratorInfo[]>([]);
  const [error, setError] = useState("");
  const [adding, setAdding] = useState(false);

  async function refresh() {
    try {
      const [v, p, n, g] = await Promise.all([
        api.listVariables(),
        api.listProfiles(),
        api.listNodes(),
        api.listGenerators(),
      ]);
      setVars(v.variables);
      setProfiles(p.profiles);
      setNodes(n.nodes);
      setGenerators(g.generators);
      setError("");
    } catch (e) {
      setError((e as Error).message);
    }
  }

  useEffect(() => {
    refresh();
  }, []);

  const ownerName = (v: Variable) => {
    if (v.scope === "profile") {
      return profiles.find((p) => p.id === v.profile_id)?.name ?? v.profile_id ?? "";
    }
    if (v.scope === "node") {
      return nodes.find((n) => n.id === v.node_id)?.name ?? v.node_id ?? "";
    }
    return "";
  };

  return (
    <div>
      <div className="mb-6 flex items-end justify-between gap-4">
        <div>
          <h1 className="font-display text-[26px] font-semibold tracking-tight">{t("变量")}</h1>
          <p className="mt-1 text-sm text-muted">
            {t("模板里")} <code className="font-mono">{`{{${t("名字")}}}`}</code> {t("引用的值。私钥类分量只存不取。")}
          </p>
        </div>
        <Button variant="primary" onClick={() => setAdding(true)}>
          <PlusIcon size={16} />
          {t("新增变量")}
        </Button>
      </div>

      {error && <ErrorBar text={error} />}

      {vars.length === 0 ? (
        <Empty>
          {t("还没有变量。生成一组 REALITY 密钥或填一个静态值，模板就能引用它。")}
        </Empty>
      ) : (
        <div className="overflow-hidden rounded-2xl border border-line bg-surface">
          <table className="w-full text-sm">
            <thead>
              <tr className="border-b border-line text-left text-[11px] uppercase tracking-[0.07em] text-faint">
                <Th>{t("名字")}</Th>
                <Th>{t("作用域")}</Th>
                <Th>{t("取值")}</Th>
                <Th> </Th>
              </tr>
            </thead>
            <tbody>
              {vars.map((v) => (
                <tr key={v.id} className="border-b border-line last:border-0 align-top">
                  <Td>
                    <span className="font-mono text-[13px]">{v.name}</span>
                    {v.generator && (
                      <span className="ml-2 rounded px-1.5 py-0.5 font-mono text-[10px] text-muted"
                        style={{ background: "color-mix(in srgb, var(--muted) 12%, transparent)" }}>
                        {v.generator}
                      </span>
                    )}
                  </Td>
                  <Td>
                    <span className="text-muted">{t(SCOPE_LABEL[v.scope])}</span>
                    {ownerName(v) && (
                      <span className="ml-1.5 text-faint">· {ownerName(v)}</span>
                    )}
                  </Td>
                  <Td>
                    <div className="flex flex-col gap-1">
                      {v.components.map((c) => (
                        <div key={c.name} className="flex items-baseline gap-2">
                          <span className="font-mono text-[11px] text-faint w-16 shrink-0">
                            {c.name || "—"}
                          </span>
                          <span
                            className={cn(
                              "font-mono text-[12px] break-all",
                              c.secret ? "text-faint" : "text-ink",
                            )}
                          >
                            {c.value}
                          </span>
                        </div>
                      ))}
                    </div>
                  </Td>
                  <Td className="text-right">
                    <IconButton
                      label={t("删除变量")}
                      className="hover:text-danger"
                      onClick={async () => {
                        if (!confirm(tf("删除变量「{name}」？引用它的模板会渲染失败。", { name: v.name }))) return;
                        await api.deleteVariable(v.id);
                        refresh();
                      }}
                    >
                      <TrashIcon size={16} />
                    </IconButton>
                  </Td>
                </tr>
              ))}
            </tbody>
          </table>
        </div>
      )}

      {adding && (
        <AddVariableDialog
          generators={generators}
          profiles={profiles}
          nodes={nodes}
          onClose={() => setAdding(false)}
          onCreated={refresh}
        />
      )}
    </div>
  );
}

function AddVariableDialog({
  generators,
  profiles,
  nodes,
  onClose,
  onCreated,
}: {
  generators: GeneratorInfo[];
  profiles: Profile[];
  nodes: Node[];
  onClose: () => void;
  onCreated: () => void;
}) {
  const { t } = useT();
  const [name, setName] = useState("");
  const [scope, setScope] = useState<Scope>("profile");
  const [owner, setOwner] = useState("");
  const [mode, setMode] = useState<"generator" | "static">("generator");
  const [generator, setGenerator] = useState("x25519");
  const [value, setValue] = useState("");
  const [busy, setBusy] = useState(false);
  const [error, setError] = useState("");

  const chosen = generators.find((g) => g.name === generator);
  const owners = scope === "profile" ? profiles : scope === "node" ? nodes : [];
  const needsOwner = scope !== "global";

  async function submit(e: React.FormEvent) {
    e.preventDefault();
    setBusy(true);
    setError("");
    try {
      await api.createVariable({
        name: name.trim(),
        scope,
        profile_id: scope === "profile" ? owner : undefined,
        node_id: scope === "node" ? owner : undefined,
        generator: mode === "generator" ? generator : undefined,
        value: mode === "static" ? value : undefined,
      });
      onCreated();
      onClose();
    } catch (e) {
      setError((e as Error).message);
    } finally {
      setBusy(false);
    }
  }

  return (
    <Modal onClose={onClose}>
      <form onSubmit={submit}>
        <h3 className="font-display text-lg font-semibold tracking-tight">{t("新增变量")}</h3>
        <p className="mt-1 text-sm text-muted">
          {t("生成器会产出成组的分量（如")} <code className="font-mono">reality.private</code> /{" "}
          <code className="font-mono">reality.public</code>{t("），服务端与客户端各引用一半，天然配对。")}
        </p>

        <Field label={t("名字")}>
          <input
            autoFocus
            value={name}
            onChange={(e) => setName(e.target.value)}
            placeholder="reality"
            className={inputCls}
          />
        </Field>

        <Field label={t("作用域")}>
          <div className="flex gap-1.5">
            {(["global", "profile", "node"] as Scope[]).map((s) => (
              <button
                key={s}
                type="button"
                onClick={() => {
                  setScope(s);
                  setOwner("");
                }}
                className={cn(
                  "rounded-lg border px-2.5 py-1.5 text-[13px] transition-colors",
                  scope === s
                    ? "border-signal bg-signal-soft text-ink"
                    : "border-line-strong text-muted hover:border-signal",
                )}
              >
                {t(SCOPE_LABEL[s])}
              </button>
            ))}
          </div>
        </Field>

        {needsOwner && (
          <Field label={t(scope === "profile" ? "属于哪个接入配置" : "属于哪个节点")}>
            <select
              value={owner}
              onChange={(e) => setOwner(e.target.value)}
              className={inputCls}
            >
              <option value="">{t("选择…")}</option>
              {owners.map((o) => (
                <option key={o.id} value={o.id}>
                  {o.name}
                </option>
              ))}
            </select>
          </Field>
        )}

        <Field label={t("取值方式")}>
          <div className="flex gap-1.5">
            {(
              [
                ["generator", "生成器"],
                ["static", "静态值"],
              ] as const
            ).map(([m, label]) => (
              <button
                key={m}
                type="button"
                onClick={() => setMode(m)}
                className={cn(
                  "rounded-lg border px-2.5 py-1.5 text-[13px] transition-colors",
                  mode === m
                    ? "border-signal bg-signal-soft text-ink"
                    : "border-line-strong text-muted hover:border-signal",
                )}
              >
                {t(label)}
              </button>
            ))}
          </div>
        </Field>

        {mode === "generator" ? (
          <Field label={t("生成器")}>
            <select
              value={generator}
              onChange={(e) => setGenerator(e.target.value)}
              className={inputCls}
            >
              {generators.map((g) => (
                <option key={g.name} value={g.name} disabled={!g.available}>
                  {g.name}
                  {!g.available ? t("（需要 xray 二进制，当前不可用）") : ""}
                </option>
              ))}
            </select>
            {chosen?.needs_xray && (
              <p className="mt-1.5 text-xs text-muted">
                {t("该生成器由面板调用 xray 二进制产出，保证格式与内核一致。")}
              </p>
            )}
          </Field>
        ) : (
          <Field label={t("值")}>
            <input
              value={value}
              onChange={(e) => setValue(e.target.value)}
              placeholder="www.microsoft.com"
              className={inputCls}
            />
          </Field>
        )}

        {error && <p className="mt-3 text-sm text-danger">{error}</p>}

        <div className="mt-5 flex justify-end gap-2">
          <Button type="button" variant="ghost" onClick={onClose}>
            {t("取消")}
          </Button>
          <Button
            type="submit"
            variant="primary"
            disabled={busy || !name.trim() || (needsOwner && !owner)}
          >
            {busy ? t("创建中…") : t("创建")}
          </Button>
        </div>
      </form>
    </Modal>
  );
}

// --- small shared pieces ---

export const inputCls =
  "w-full rounded-xl border border-line-strong bg-surface px-3.5 py-2.5 text-sm outline-none transition-colors placeholder:text-faint focus:border-signal";

// label arrives already translated by the caller; translating again here would
// be a second lookup of an English string.
export function Field({ label, children }: { label: string; children: React.ReactNode }) {
  return (
    <div className="mt-4">
      <div className="mb-1.5 text-[11px] font-medium uppercase tracking-[0.07em] text-faint">
        {label}
      </div>
      {children}
    </div>
  );
}

export function Modal({
  onClose,
  children,
  wide,
}: {
  onClose: () => void;
  children: React.ReactNode;
  wide?: boolean;
}) {
  useEffect(() => {
    const onKey = (e: KeyboardEvent) => e.key === "Escape" && onClose();
    window.addEventListener("keydown", onKey);
    return () => window.removeEventListener("keydown", onKey);
  }, [onClose]);
  return (
    <div
      className="fixed inset-0 z-50 grid place-items-center overflow-y-auto bg-black/40 px-4 py-8 backdrop-blur-sm"
      onClick={onClose}
    >
      <div
        role="dialog"
        aria-modal="true"
        className={cn(
          "w-full animate-rise rounded-2xl border border-line bg-raised p-6 shadow-[var(--shadow-pop)]",
          wide ? "max-w-3xl" : "max-w-lg",
        )}
        onClick={(e) => e.stopPropagation()}
      >
        {children}
      </div>
    </div>
  );
}

export function Th({ children }: { children: React.ReactNode }) {
  return <th className="px-4 py-2.5 font-medium whitespace-nowrap">{children}</th>;
}

export function Td({
  children,
  className = "",
}: {
  children: React.ReactNode;
  className?: string;
}) {
  return <td className={`px-4 py-3 ${className}`}>{children}</td>;
}

export function Empty({ children }: { children: React.ReactNode }) {
  return (
    <div className="rounded-2xl border border-dashed border-line-strong bg-surface px-8 py-14 text-center text-sm text-muted">
      {children}
    </div>
  );
}

export function ErrorBar({ text }: { text: string }) {
  return (
    <div
      className="mb-5 rounded-xl px-4 py-3 text-sm text-danger"
      style={{
        border: "1px solid color-mix(in srgb, var(--danger) 35%, transparent)",
        background: "color-mix(in srgb, var(--danger) 10%, transparent)",
      }}
    >
      {text}
    </div>
  );
}
