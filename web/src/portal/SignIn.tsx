import { useState } from "react";
import { portal, setToken, type PortalConfig } from "./api";
import { AuthShell, authInput } from "./AuthShell";
import { Button } from "../components/ui";
import { href } from "./router";
import { useT } from "../lib/i18n";

export function SignIn({
  config,
  onAuthed,
}: {
  config: PortalConfig;
  onAuthed: () => void;
}) {
  const { t } = useT();
  const [email, setEmail] = useState("");
  const [password, setPassword] = useState("");
  const [busy, setBusy] = useState(false);
  const [error, setError] = useState("");

  async function submit(e: React.FormEvent) {
    e.preventDefault();
    setBusy(true);
    setError("");
    try {
      const r = await portal.login(email.trim(), password);
      setToken(r.token);
      onAuthed();
    } catch (err) {
      setError((err as Error).message);
    } finally {
      setBusy(false);
    }
  }

  return (
    <AuthShell title={t("登录")} subtitle={t("查看你的订阅、线路与用量。")}>
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
          autoComplete="current-password"
          value={password}
          onChange={(e) => setPassword(e.target.value)}
          placeholder={t("密码")}
          className={authInput + " mt-2.5"}
        />
        {error && <p className="mt-2 text-sm text-danger">{error}</p>}
        <Button
          type="submit"
          variant="primary"
          disabled={busy || !email.trim() || !password}
          className="mt-4 w-full"
        >
          {busy ? t("登录中…") : t("登录")}
        </Button>
      </form>

      {config.registration_open && (
        <p className="mt-5 text-sm text-muted">
          {t("还没有账号？")}{" "}
          <a href={href({ view: "register" })} className="text-ink underline underline-offset-2">
            {t("注册")}
          </a>
        </p>
      )}
      {/* There is no self-service reset yet. Recovery goes through the
          one-time link an operator issues, which works with or without SMTP —
          so this says what to do rather than offering a form that would need
          mail configured to mean anything. */}
      <p className="mt-2 text-xs text-faint">{t("忘记密码请联系管理员重置。")}</p>
    </AuthShell>
  );
}
