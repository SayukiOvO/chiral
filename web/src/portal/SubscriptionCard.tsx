import { useEffect, useState } from "react";
import type { Subscription } from "./api";
import { Button } from "../components/ui";
import { QRCode } from "../components/QRCode";
import { CheckIcon, CopyIcon } from "../components/icons";
import { copyText } from "../lib/clipboard";
import { cn } from "../lib/cn";
import { useT } from "../lib/i18n";

const KIND_LABEL: Record<string, string> = {
  "xray-json": "通用",
  clash: "Clash",
  stash: "Stash",
  "vless-uri": "分享链接",
};

/**
 * The subscription link.
 *
 * Copying is the primary action, so the full URL is never drawn in one piece:
 * it is a bearer credential, and a screenshot of this page should not be a
 * working subscription. The QR is behind a deliberate tap for the same reason,
 * and folds itself away again.
 */
export function SubscriptionCard({ sub }: { sub: Subscription }) {
  const { t } = useT();
  const [kind, setKind] = useState<string>(sub.kinds[0] ?? "xray-json");
  const [copied, setCopied] = useState(false);
  const [showQR, setShowQR] = useState(false);

  const url = sub.url ? `${sub.url}?client=${kind}` : "";

  // Fold the code away on its own. A subscription QR left on screen in a café
  // is the same problem as the URL, with a longer exposure.
  useEffect(() => {
    if (!showQR) return;
    const timer = window.setTimeout(() => setShowQR(false), 60_000);
    return () => window.clearTimeout(timer);
  }, [showQR]);

  if (!sub.available) {
    return (
      <section className="mt-4 rounded-2xl border border-line bg-surface px-5 py-5">
        <h2 className="font-display text-[15px] font-semibold tracking-tight">{t("我的订阅链接")}</h2>
        <p className="mt-1.5 text-sm text-muted">
          {t("这个账户的订阅链接是旧版本创建的，面板无法显示。请联系管理员重新生成。")}
        </p>
      </section>
    );
  }

  return (
    <section className="mt-4 rounded-2xl border border-line bg-surface px-5 py-5">
      <h2 className="font-display text-[15px] font-semibold tracking-tight">{t("我的订阅链接")}</h2>
      <p className="mt-1.5 text-sm text-muted">{t("复制到你的客户端，它会自动获取全部线路。")}</p>

      {sub.kinds.length > 1 && (
        <div className="mt-4 flex flex-wrap gap-1.5">
          {sub.kinds.map((k) => (
            <button
              key={k}
              onClick={() => {
                setKind(k);
                setCopied(false);
              }}
              className={cn(
                "rounded-lg border px-2.5 py-1.5 text-[13px] transition-colors",
                kind === k
                  ? "border-signal bg-signal-soft text-ink"
                  : "border-line-strong text-muted hover:border-signal",
              )}
            >
              {KIND_LABEL[k] ? t(KIND_LABEL[k]) : k}
            </button>
          ))}
        </div>
      )}

      <div className="mt-4 flex items-center gap-2">
        {/* Masked: enough to recognise, not enough to transcribe. */}
        <code className="min-w-0 flex-1 truncate rounded-lg bg-[color-mix(in_srgb,var(--muted)_10%,transparent)] px-3 py-2.5 font-mono text-[13px] text-muted">
          {mask(url)}
        </code>
        <Button
          onClick={async () => {
            if (await copyText(url)) {
              setCopied(true);
              setTimeout(() => setCopied(false), 1600);
            }
          }}
        >
          {copied ? <CheckIcon size={16} className="text-online" /> : <CopyIcon size={16} />}
          {copied ? t("已复制") : t("复制")}
        </Button>
      </div>

      <button
        onClick={() => setShowQR((v) => !v)}
        className="mt-3 text-sm text-muted underline underline-offset-2 hover:text-ink"
      >
        {showQR ? t("收起二维码") : t("显示二维码")}
      </button>
      {showQR && (
        <div className="mt-3">
          {/* A white card behind it: the code is forced black-on-white for
              scanning reliability, and on a dark page a bare one looks like a
              rendering fault. */}
          <div className="inline-block rounded-xl bg-white p-3">
            <QRCode value={url} size={176} />
          </div>
          <p className="mt-2 text-xs text-faint">
            {t("二维码包含你的订阅密钥，不要分享截图。")}
          </p>
        </div>
      )}
    </section>
  );
}

/**
 * A recognisable fragment, not the URL.
 *
 * The origin is dropped: it is the site the reader is already looking at, and
 * keeping it pushed the only informative part — the token — past the end of
 * the box, so the whole thing rendered as "http://localhost:517…". What is
 * useful here is enough of the token to tell "this is mine" and "it changed
 * after a reset", which is the first and last few characters.
 */
function mask(url: string): string {
  const at = url.indexOf("/sub/");
  if (at < 0) return url;
  const token = url.slice(at + 5).split("?")[0];
  if (token.length <= 10) return `/sub/${token}`;
  return `/sub/${token.slice(0, 5)}……${token.slice(-5)}`;
}
