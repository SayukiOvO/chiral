import { useEffect, useState } from "react";
import { Button } from "./ui";
import { CheckIcon, CopyIcon } from "./icons";
import { useT } from "../lib/i18n";

/**
 * Shows a freshly issued subscription link. The panel only ever stores the
 * token's hash, so this is genuinely the one chance to copy it — the dialog
 * says so rather than letting an operator find out later.
 */
export function SubscriptionDialog({
  url,
  userName,
  onClose,
}: {
  url: string;
  userName: string;
  onClose: () => void;
}) {
  const { t } = useT();
  const [copied, setCopied] = useState(false);
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
          {t("面板仅保存令牌哈希，此链接")}
          <b className="text-ink">{t("只显示这一次")}</b>
          {t("。客户端将自动获取对应格式。")}
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

        <div className="mt-5 flex justify-end">
          <Button variant="primary" onClick={onClose}>
            {t("完成")}
          </Button>
        </div>
      </div>
    </div>
  );
}
