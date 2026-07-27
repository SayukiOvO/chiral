import { useState } from "react";
import { portal, type PortalConfig } from "./api";
import { AuthShell, authInput } from "./AuthShell";
import { Button } from "../components/ui";
import { href } from "./router";
import { useT } from "../lib/i18n";

export function Register({ config }: { config: PortalConfig }) {
  const { t } = useT();
  const [email, setEmail] = useState("");
  const [password, setPassword] = useState("");
  const [invite, setInvite] = useState("");
  const [busy, setBusy] = useState(false);
  const [error, setError] = useState("");
  const [done, setDone] = useState(false);

  async function submit(e: React.FormEvent) {
    e.preventDefault();
    setBusy(true);
    setError("");
    try {
      await portal.register(email.trim(), password, invite.trim() || undefined);
      setDone(true);
    } catch (err) {
      setError((err as Error).message);
    } finally {
      setBusy(false);
    }
  }

  // The server answers identically whether or not the address was taken, so
  // this screen must not claim the account was created — only that it can be
  // signed in to if it exists. Saying more here would undo the server's care.
  if (done) {
    return (
      <AuthShell title={t("可以登录了")} subtitle={t("如果这个邮箱还没被注册，账号已经建好。")}>
        <Button
          variant="primary"
          className="w-full"
          onClick={() => (window.location.hash = href({ view: "signin" }))}
        >
          {t("去登录")}
        </Button>
      </AuthShell>
    );
  }

  return (
    <AuthShell title={t("注册")} subtitle={t("注册后请联系管理员开通线路。")}>
      <form onSubmit={submit}>
        <input
          autoFocus
          type="email"
          autoComplete="username"
          inputMode="email"
          value={email}
          onChange={(e) => setEmail(e.target.value)}
          placeholder={t("邮箱")}
          className={authInput}
        />
        <input
          type="password"
          autoComplete="new-password"
          value={password}
          onChange={(e) => setPassword(e.target.value)}
          placeholder={t("密码（至少 8 位）")}
          className={authInput + " mt-2.5"}
        />
        {config.invite_required && (
          <input
            value={invite}
            onChange={(e) => setInvite(e.target.value)}
            placeholder={t("注册码")}
            className={authInput + " mt-2.5"}
          />
        )}
        {error && <p className="mt-2 text-sm text-danger">{error}</p>}
        <Button
          type="submit"
          variant="primary"
          disabled={busy || !email.trim() || password.length < 8}
          className="mt-4 w-full"
        >
          {busy ? t("提交中…") : t("注册")}
        </Button>
      </form>
      <p className="mt-5 text-sm text-muted">
        {t("已经有账号？")}{" "}
        <a href={href({ view: "signin" })} className="text-ink underline underline-offset-2">
          {t("登录")}
        </a>
      </p>
    </AuthShell>
  );
}
