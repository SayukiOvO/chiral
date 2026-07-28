import { useEffect, useState } from "react";
import { api, type AlertTarget } from "../api";
import { Button, IconButton } from "../components/ui";
import { CheckIcon, PlusIcon, TrashIcon } from "../components/icons";
import { Empty, ErrorBar, Field, Modal, inputCls } from "../components/primitives";
import { relativeTime } from "../format";
import { cn } from "../lib/cn";
import { useT } from "../lib/i18n";

/**
 * Where node availability alerts go.
 *
 * A target that can only be configured with curl is a target nobody
 * configures, which is why this page exists. Its most important control is the
 * test button: a wrong chat id looks exactly like a working setup until the
 * night something actually goes down.
 */
export function AlertsPage() {
  const { t } = useT();
  const [targets, setTargets] = useState<AlertTarget[]>([]);
  const [loaded, setLoaded] = useState(false);
  const [error, setError] = useState("");
  const [adding, setAdding] = useState(false);

  async function refresh() {
    try {
      const r = await api.listAlertTargets();
      setTargets(r.targets);
      setError("");
    } catch (e) {
      setError((e as Error).message);
    } finally {
      setLoaded(true);
    }
  }

  useEffect(() => {
    refresh();
  }, []);

  return (
    <div>
      <div className="mb-6 flex items-end justify-between gap-4">
        <div className="animate-rise">
          <h1 className="font-display text-[26px] font-semibold tracking-tight">{t("告警")}</h1>
          <p className="mt-1 text-sm text-muted">
            {t("节点上线 / 掉线通知的接收方。状态需稳定两分钟才播报，避免抖动。")}
          </p>
        </div>
        <Button variant="primary" onClick={() => setAdding(true)}>
          <PlusIcon size={16} />
          {t("新增目标")}
        </Button>
      </div>

      {error && <ErrorBar text={error} />}

      {loaded &&
        (targets.length === 0 ? (
          <Empty>{t("暂无通知目标。未配置时，节点掉线不会通知任何人。")}</Empty>
        ) : (
          <div className="flex flex-col gap-2.5">
            {targets.map((tg) => (
              <TargetCard key={tg.id} target={tg} onChanged={refresh} />
            ))}
          </div>
        ))}

      {adding && <AddTargetDialog onClose={() => setAdding(false)} onCreated={refresh} />}
    </div>
  );
}

function TargetCard({ target, onChanged }: { target: AlertTarget; onChanged: () => void }) {
  const { t } = useT();
  const [busy, setBusy] = useState(false);
  const [tested, setTested] = useState<"ok" | "failed" | null>(null);
  const [confirming, setConfirming] = useState(false);

  async function test() {
    setBusy(true);
    setTested(null);
    try {
      await api.testAlertTarget(target.id);
      setTested("ok");
      onChanged();
    } catch {
      setTested("failed");
      onChanged();
    } finally {
      setBusy(false);
      setTimeout(() => setTested(null), 3000);
    }
  }

  return (
    <article className="group rounded-2xl border border-line bg-surface px-5 py-4 shadow-[var(--shadow-card)] transition-all duration-150 hover:border-line-strong">
      <div className="flex items-start justify-between gap-4">
        <div className="min-w-0">
          <div className="flex items-center gap-2">
            <span className="font-display text-[15px] font-semibold tracking-tight">
              {target.name}
            </span>
            <span className="rounded px-1.5 py-0.5 font-mono text-[10px] text-muted"
              style={{ background: "color-mix(in srgb, var(--muted) 12%, transparent)" }}>
              {target.kind}
            </span>
            {!target.enabled && <span className="text-xs text-faint">{t("已停用")}</span>}
          </div>
          <div className="mt-0.5 font-mono text-xs text-faint">{target.config_hint}</div>
          {/* The last failure is the only thing that tells an operator their
              alerting is broken, so it is not tucked away. */}
          {target.last_error ? (
            <div className="mt-1 text-xs text-danger">{target.last_error}</div>
          ) : target.last_sent_at ? (
            <div className="mt-1 text-xs text-faint">
              {t("最近发送")} {relativeTime(target.last_sent_at)}
            </div>
          ) : null}
        </div>

        {confirming ? (
          <div className="flex shrink-0 items-center gap-2 text-sm">
            <button
              onClick={() => setConfirming(false)}
              className="rounded-lg px-2.5 py-1 text-muted hover:text-ink"
            >
              {t("取消")}
            </button>
            <button
              onClick={async () => {
                await api.deleteAlertTarget(target.id);
                onChanged();
              }}
              className="rounded-lg px-2.5 py-1 font-medium text-danger hover:bg-[color-mix(in_srgb,var(--danger)_12%,transparent)]"
            >
              {t("删除")}
            </button>
          </div>
        ) : (
          <div className="flex shrink-0 items-center gap-1.5">
            <Button size="sm" onClick={test} disabled={busy}>
              {tested === "ok" ? (
                <CheckIcon size={14} className="text-online" />
              ) : null}
              {tested === "ok" ? t("已送达") : tested === "failed" ? t("发送失败") : t("发送测试")}
            </Button>
            <Button
              size="sm"
              variant="ghost"
              onClick={async () => {
                await api.updateAlertTarget(target.id, { enabled: !target.enabled });
                onChanged();
              }}
            >
              {target.enabled ? t("停用") : t("启用")}
            </Button>
            <IconButton
              label={t("删除")}
              className="hover:text-danger"
              onClick={() => setConfirming(true)}
            >
              <TrashIcon size={16} />
            </IconButton>
          </div>
        )}
      </div>
    </article>
  );
}

const KINDS = [
  {
    kind: "telegram",
    label: "Telegram",
    placeholder: "123456:ABC-DEF… : -1001234567890",
    // The bot token contains a colon itself, which is why the server splits
    // from the right. Worth saying here so nobody quotes it defensively.
    hint: "格式 <bot-token>:<chat-id>。bot token 自身含冒号，按最右侧冒号切分。",
  },
  {
    kind: "webhook",
    label: "Webhook",
    placeholder: "https://example.com/hook",
    hint: "以 POST 发送 JSON body。",
  },
];

function AddTargetDialog({
  onClose,
  onCreated,
}: {
  onClose: () => void;
  onCreated: () => void;
}) {
  const { t } = useT();
  const [kind, setKind] = useState("telegram");
  const [name, setName] = useState("");
  const [config, setConfig] = useState("");
  const [busy, setBusy] = useState(false);
  const [error, setError] = useState("");

  const chosen = KINDS.find((k) => k.kind === kind)!;

  async function submit(e: React.FormEvent) {
    e.preventDefault();
    setBusy(true);
    setError("");
    try {
      await api.createAlertTarget({ kind, name: name.trim(), config: config.trim() });
      onCreated();
      onClose();
    } catch (err) {
      setError((err as Error).message);
    } finally {
      setBusy(false);
    }
  }

  return (
    <Modal onClose={onClose}>
      <form onSubmit={submit}>
        <h3 className="font-display text-lg font-semibold tracking-tight">{t("新增通知目标")}</h3>
        <p className="mt-1 text-sm text-muted">
          {t("创建后请发送测试消息：填错的 chat id 在真正告警前与正常配置无异。")}
        </p>

        <Field label={t("类型")}>
          <div className="flex gap-1.5">
            {KINDS.map((k) => (
              <button
                key={k.kind}
                type="button"
                onClick={() => setKind(k.kind)}
                className={cn(
                  "rounded-lg border px-2.5 py-1.5 text-[13px] transition-colors",
                  kind === k.kind
                    ? "border-signal bg-signal-soft text-ink"
                    : "border-line-strong text-muted hover:border-signal",
                )}
              >
                {k.label}
              </button>
            ))}
          </div>
        </Field>

        <Field label={t("名字")}>
          <input
            autoFocus
            value={name}
            onChange={(e) => setName(e.target.value)}
            placeholder={t("例如 运维群")}
            className={inputCls}
          />
        </Field>

        <Field label={t("目标配置")}>
          <input
            value={config}
            onChange={(e) => setConfig(e.target.value)}
            placeholder={chosen.placeholder}
            className={inputCls + " font-mono text-[13px]"}
          />
          <span className="mt-1 block text-xs text-faint">{t(chosen.hint)}</span>
        </Field>

        {error && <p className="mt-3 text-sm text-danger">{error}</p>}

        <div className="mt-5 flex justify-end gap-2">
          <Button type="button" variant="ghost" onClick={onClose}>
            {t("取消")}
          </Button>
          <Button type="submit" variant="primary" disabled={busy || !name.trim() || !config.trim()}>
            {busy ? t("创建中…") : t("创建")}
          </Button>
        </div>
      </form>
    </Modal>
  );
}
