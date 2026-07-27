import { useEffect, useState } from "react";
import { api } from "../api";
import { Button } from "./ui";
import { CheckIcon, CopyIcon } from "./icons";
import { Modal } from "./primitives";
import { copyText } from "../lib/clipboard";
import { useT } from "../lib/i18n";

/**
 * A one-time link the subscriber uses to set their own password.
 *
 * This is how an existing proxy user gets a portal account, and — with no SMTP
 * configured — the only password reset there is. It exists so nobody has to
 * send a password by hand.
 *
 * Shown once, like the join token and the subscription link: only a hash is
 * stored. Closing this dialog without copying means minting another.
 */
export function PortalLinkDialog({
  userId,
  userName,
  onClose,
}: {
  userId: string;
  userName: string;
  onClose: () => void;
}) {
  const { t, tf } = useT();
  const [url, setUrl] = useState("");
  const [error, setError] = useState("");
  const [copied, setCopied] = useState(false);

  useEffect(() => {
    let cancelled = false;
    api
      .portalLink(userId)
      .then((r) => !cancelled && setUrl(r.claim_url))
      .catch((e) => !cancelled && setError((e as Error).message));
    // Once: a second call would mint a second link and invalidate nothing,
    // leaving two live ways into the same account.
    return () => {
      cancelled = true;
    };
  }, [userId]);

  // Relative when the panel has no CHIRAL_PUBLIC_URL; make it openable anyway.
  const absolute = url.startsWith("http") ? url : window.location.origin + url;

  return (
    <Modal onClose={onClose}>
      <h3 className="font-display text-lg font-semibold tracking-tight">{t("门户认领链接")}</h3>
      <p className="mt-1 text-sm text-muted">
        {tf("把这条链接发给 {name}，他用它设置自己的门户密码。", { name: userName })}
      </p>

      {error && <p className="mt-3 text-sm text-danger">{error}</p>}

      {url && (
        <>
          <div className="mt-4 flex items-start gap-2">
            <code className="min-w-0 flex-1 break-all rounded-lg bg-[color-mix(in_srgb,var(--muted)_10%,transparent)] px-3 py-2.5 font-mono text-[12px]">
              {absolute}
            </code>
            <Button
              onClick={async () => {
                if (await copyText(absolute)) {
                  setCopied(true);
                  setTimeout(() => setCopied(false), 1600);
                }
              }}
            >
              {copied ? <CheckIcon size={16} className="text-online" /> : <CopyIcon size={16} />}
              {copied ? t("已复制") : t("复制")}
            </Button>
          </div>
          <p className="mt-2 text-xs text-faint">
            {t("7 天内有效，只能用一次，关掉这个窗口就再也看不到。账号已存在时它只能改密码，不能改邮箱。")}
          </p>
        </>
      )}

      <div className="mt-5 flex justify-end">
        <Button variant="primary" onClick={onClose}>
          {t("完成")}
        </Button>
      </div>
    </Modal>
  );
}
