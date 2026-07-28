import { useEffect, useState } from "react";
import { portal, setToken } from "./api";
import { AuthShell, authInput } from "./AuthShell";
import { Button } from "../components/ui";
import { navigate } from "./router";
import { useT } from "../lib/i18n";

/**
 * Setting a password from a one-time link.
 *
 * Does double duty: claiming an operator-created account, and resetting a
 * password on one that already exists. The server decides which, and refuses
 * to move the email address in the second case — so this screen only asks for
 * an address when the account is new to it.
 */
export function Claim({ token, onAuthed }: { token: string; onAuthed: () => void }) {
  const { t, tf } = useT();
  const [name, setName] = useState<string | null>(null);
  const [lookupError, setLookupError] = useState("");
  const [email, setEmail] = useState("");
  const [password, setPassword] = useState("");
  const [again, setAgain] = useState("");
  const [busy, setBusy] = useState(false);
  const [error, setError] = useState("");

  useEffect(() => {
    let cancelled = false;
    portal
      .claimLookup(token)
      .then((r) => !cancelled && setName(r.user_name))
      .catch((e) => !cancelled && setLookupError((e as Error).message));
    return () => {
      cancelled = true;
    };
  }, [token]);

  async function submit(e: React.FormEvent) {
    e.preventDefault();
    setBusy(true);
    setError("");
    try {
      const r = await portal.claim(token, password, email.trim() || undefined);
      setToken(r.token);
      onAuthed();
      navigate({ view: "home" });
    } catch (err) {
      setError((err as Error).message);
    } finally {
      setBusy(false);
    }
  }

  if (lookupError) {
    return (
      <AuthShell title={t("链接无效")} subtitle={t("此链接已失效或已被使用，请联系管理员重新获取。")}>
        <Button variant="primary" className="w-full" onClick={() => navigate({ view: "signin" })}>
          {t("去登录")}
        </Button>
      </AuthShell>
    );
  }
  if (name === null) return null;

  const mismatch = again.length > 0 && password !== again;

  return (
    <AuthShell title={t("设置密码")} subtitle={tf("此链接对应账号 {name}。", { name })}>
      <form onSubmit={submit}>
        {/* Only offered when the account has no address yet. The server
            rejects an attempt to change an existing one, and asking for
            something that will be refused is worse than not asking. */}
        <input
          type="email"
          autoComplete="username"
          inputMode="email"
          value={email}
          onChange={(e) => setEmail(e.target.value)}
          placeholder={t("邮箱（首次设置时填写）")}
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
        <input
          type="password"
          autoComplete="new-password"
          value={again}
          onChange={(e) => setAgain(e.target.value)}
          placeholder={t("确认新密码")}
          className={authInput + " mt-2.5"}
        />
        {mismatch && <p className="mt-2 text-sm text-danger">{t("两次输入不一致。")}</p>}
        {error && <p className="mt-2 text-sm text-danger">{error}</p>}
        <Button
          type="submit"
          variant="primary"
          disabled={busy || password.length < 8 || password !== again}
          className="mt-4 w-full"
        >
          {busy ? t("设置中…") : t("设置密码并登录")}
        </Button>
      </form>
    </AuthShell>
  );
}
