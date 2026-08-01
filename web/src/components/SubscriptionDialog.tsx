import { useEffect, useState } from "react";
import { Button } from "./ui";
import { CheckIcon, CopyIcon } from "./icons";
import { useT } from "../lib/i18n";

/**
 * A subscriber's link, and — separately, deliberately — the way to replace it.
 *
 * These were one action. The console had no read path, so the only way to show
 * an operator a link was to mint a new one, which meant "let me check what
 * their link is" and "break every client they have configured" were the same
 * button. The token has been recoverable since M5; only the missing handler
 * made a reset the price of looking.
 *
 * Replacing it is now behind its own confirmation, and says what it costs. A
 * new link is not an inconvenience to a subscriber — every client they own
 * stops updating until they import the new one by hand.
 */
export function SubscriptionDialog({
  url,
  userName,
  fresh,
  onReset,
  onClose,
}: {
  url: string;
  userName: string;
  /** True right after creation or a reset: nobody has this link yet. */
  fresh?: boolean;
  /** Absent when there is nothing to reset from here (a brand-new user). */
  onReset?: () => Promise<void>;
  onClose: () => void;
}) {
  const { t } = useT();
  const [copied, setCopied] = useState(false);
  const [confirming, setConfirming] = useState(false);
  const [busy, setBusy] = useState(false);
  useEffect(() => {
    const onKey = (e: KeyboardEvent) => e.key === "Escape" && onClose();
    window.addEventListener("keydown", onKey);
    return () => window.removeEventListener("keydown", onKey);
  }, [onClose]);
  return (
    <div
      className="fixed inset-0 z-50 flex items-center justify-center bg-black/40 px-4 backdrop-blur-sm"
      onClick={onClose}
    >
      <div
        role="dialog"
        aria-modal="true"
        onClick={(e) => e.stopPropagation()}
        className="w-full max-w-lg animate-rise rounded-2xl border border-line bg-raised p-6 shadow-[var(--shadow-pop)]"
      >
        <h3 className="font-display text-lg font-semibold tracking-tight">
          {userName}
          {t("的订阅链接")}
        </h3>
        <p className="mt-1 text-sm text-muted">
          {fresh
            ? t("这是一条新链接，之前的已失效。客户端将自动获取对应格式。")
            : t("随时可以回来看，链接不会因为查看而改变。客户端将自动获取对应格式。")}
        </p>

        <div className="relative mt-4">
          {/* A subscription URL is one long unbreakable token; wrapping it
              keeps the whole link visible instead of hiding most of it behind
              a horizontal scrollbar. Extra right padding clears the button. */}
          <pre className="whitespace-pre-wrap break-all rounded-xl border border-line bg-paper p-4 pr-20 font-mono text-xs leading-relaxed text-ink">
            {url}
          </pre>
          <button
            onClick={() => {
              navigator.clipboard.writeText(url);
              setCopied(true);
              setTimeout(() => setCopied(false), 1500);
            }}
            className="absolute right-2.5 top-2.5 inline-flex items-center gap-1.5 rounded-lg border border-line-strong bg-surface px-2.5 py-1.5 text-xs font-medium text-muted transition-colors hover:border-signal hover:text-signal"
          >
            {copied ? <CheckIcon size={13} className="text-online" /> : <CopyIcon size={13} />}
            {copied ? t("已复制") : t("复制")}
          </button>
        </div>

        <p className="mt-3 text-xs text-faint">
          {t("需要指定格式时可加")} <code className="font-mono">?client=clash</code>
          （clash / stash / xray-json / vless-uri）。
        </p>

        <div className="mt-5 flex items-center justify-between gap-3">
          {onReset ? (
            confirming ? (
              <div className="flex items-center gap-2 text-xs">
                <span className="text-warn">{t("换成新链接？该用户已配置的客户端全部需要重新导入。")}</span>
                <button
                  onClick={() => setConfirming(false)}
                  className="shrink-0 rounded-lg px-2 py-1 text-muted hover:text-ink"
                >
                  {t("取消")}
                </button>
                <button
                  disabled={busy}
                  onClick={async () => {
                    setBusy(true);
                    try {
                      await onReset();
                    } finally {
                      setBusy(false);
                      setConfirming(false);
                    }
                  }}
                  className="shrink-0 rounded-lg px-2 py-1 font-medium text-danger hover:bg-[color-mix(in_srgb,var(--danger)_12%,transparent)] disabled:opacity-50"
                >
                  {busy ? t("重置中…") : t("确认重置")}
                </button>
              </div>
            ) : (
              <button
                onClick={() => setConfirming(true)}
                className="text-xs text-muted underline-offset-2 hover:text-danger hover:underline"
              >
                {t("重置链接")}
              </button>
            )
          ) : (
            <span />
          )}
          <Button variant="primary" onClick={onClose}>
            {t("完成")}
          </Button>
        </div>
      </div>
    </div>
  );
}
