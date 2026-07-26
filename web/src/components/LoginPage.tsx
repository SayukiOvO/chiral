import { useEffect, useState } from "react";
import { api, ApiError, setToken, type LoginResult } from "../api";
import { Mark } from "./Mark";
import { Button } from "./ui";
import { ThemeToggle } from "./ThemeToggle";
import { LangToggle } from "./LangToggle";
import { useT } from "../lib/i18n";
import { cn } from "../lib/cn";
import * as webauthn from "../lib/webauthn";

/**
 * Sign-in. One screen with two steps: the password, and — only when the
 * account has a second factor — the factor.
 *
 * The second step is not a modal or a separate route. Losing the challenge by
 * navigating away would mean starting over, and the challenge is short-lived.
 */
export function LoginPage({ onAuthed }: { onAuthed: () => void }) {
  const [pending, setPending] = useState<Extract<LoginResult, { kind: "mfa" }> | null>(null);

  return (
    <div className="relative min-h-screen">
      <div className="absolute right-5 top-5 flex items-center gap-2 sm:right-8">
        <LangToggle />
        <ThemeToggle />
      </div>
      <div className="grid min-h-screen place-items-center px-4">
        <div className="w-full max-w-[380px] animate-rise">
          <div className="mb-7 flex items-center gap-2.5">
            <span className="text-signal">
              <Mark size={26} />
            </span>
            <span className="font-display text-xl font-semibold tracking-tight">Chiral</span>
          </div>

          {pending ? (
            <SecondFactor
              pending={pending}
              onAuthed={onAuthed}
              onRestart={() => setPending(null)}
            />
          ) : (
            <PasswordStep onAuthed={onAuthed} onChallenge={setPending} />
          )}
        </div>
      </div>
    </div>
  );
}

function PasswordStep({
  onAuthed,
  onChallenge,
}: {
  onAuthed: () => void;
  onChallenge: (c: Extract<LoginResult, { kind: "mfa" }>) => void;
}) {
  const { t } = useT();
  const [username, setUsername] = useState("");
  const [password, setPassword] = useState("");
  const [busy, setBusy] = useState(false);
  const [error, setError] = useState("");

  async function submit(e: React.FormEvent) {
    e.preventDefault();
    setBusy(true);
    setError("");
    try {
      const result = await api.login(username.trim(), password);
      if (result.kind === "mfa") {
        onChallenge(result);
        return;
      }
      setToken(result.token);
      onAuthed();
    } catch (err) {
      // The one error a person actually hits is worth saying in their own
      // language; the server deliberately does not say which half was wrong.
      setError(
        err instanceof ApiError && err.status === 401
          ? t("用户名或密码不正确。")
          : (err as Error).message,
      );
    } finally {
      setBusy(false);
    }
  }

  return (
    <form onSubmit={submit}>
      <h1 className="font-display text-2xl font-semibold tracking-tight">{t("节点控制台")}</h1>
      <p className="mt-1.5 text-sm text-muted">{t("请登录以继续。")}</p>

      <input
        autoFocus
        autoComplete="username"
        value={username}
        onChange={(e) => setUsername(e.target.value)}
        placeholder={t("登录名")}
        className={inputCls + " mt-6"}
      />
      <input
        type="password"
        autoComplete="current-password"
        value={password}
        onChange={(e) => setPassword(e.target.value)}
        placeholder={t("密码")}
        className={inputCls + " mt-2.5"}
      />
      {error && <p className="mt-2 text-sm text-danger">{error}</p>}
      <Button
        type="submit"
        variant="primary"
        disabled={busy || !username.trim() || !password}
        className="mt-4 w-full"
      >
        {busy ? t("验证中…") : t("登录")}
      </Button>
    </form>
  );
}

function SecondFactor({
  pending,
  onAuthed,
  onRestart,
}: {
  pending: Extract<LoginResult, { kind: "mfa" }>;
  onAuthed: () => void;
  onRestart: () => void;
}) {
  const { t, tf } = useT();
  const kinds = new Set(pending.methods.map((m) => m.kind));
  // A passkey enrolled while the panel had a public URL is useless if that URL
  // is gone, so the server's own readiness has a vote.
  const canPasskey = kinds.has("passkey") && pending.passkey_ready && webauthn.supported();
  // Passkey first when available: it is both the strongest and the least
  // typing.
  const initial: Choice = canPasskey
    ? "passkey"
    : kinds.has("totp")
      ? "totp"
      : kinds.has("email")
        ? "email"
        : "recovery";

  const [choice, setChoice] = useState<Choice>(initial);
  const [code, setCode] = useState("");
  const [busy, setBusy] = useState(false);
  const [error, setError] = useState("");
  const [sentTo, setSentTo] = useState("");
  const [expired, setExpired] = useState(false);

  // The challenge has a deadline; say so rather than letting the next attempt
  // fail with a confusing message.
  useEffect(() => {
    const left = pending.expires_at * 1000 - Date.now();
    if (left <= 0) {
      setExpired(true);
      return;
    }
    const timer = window.setTimeout(() => setExpired(true), left);
    return () => window.clearTimeout(timer);
  }, [pending.expires_at]);

  const options: { key: Choice; label: string; show: boolean }[] = [
    { key: "passkey", label: t("通行密钥"), show: canPasskey },
    { key: "totp", label: t("验证器应用"), show: kinds.has("totp") },
    { key: "email", label: t("邮箱验证码"), show: kinds.has("email") },
    { key: "recovery", label: t("恢复码"), show: pending.has_recovery },
  ];
  const available = options.filter((o) => o.show);

  async function submitCode(e: React.FormEvent) {
    e.preventDefault();
    setBusy(true);
    setError("");
    try {
      const r = await api.verifyMfa(pending.challenge, choice, code);
      setToken(r.token);
      onAuthed();
    } catch (err) {
      setError((err as Error).message);
      setCode("");
    } finally {
      setBusy(false);
    }
  }

  async function usePasskey() {
    setBusy(true);
    setError("");
    try {
      const r = await webauthn.completeLogin(pending.challenge);
      setToken(r.token);
      onAuthed();
    } catch (err) {
      // A cancelled prompt is not a failure worth shouting about.
      const msg = (err as Error).message;
      setError(/NotAllowed|abort/i.test(msg) ? t("通行密钥已取消。") : msg);
    } finally {
      setBusy(false);
    }
  }

  async function sendEmail() {
    setBusy(true);
    setError("");
    try {
      const r = await api.sendLoginEmailCode(pending.challenge);
      setSentTo(r.sent_to);
    } catch (err) {
      setError((err as Error).message);
    } finally {
      setBusy(false);
    }
  }

  if (expired) {
    return (
      <div>
        <h1 className="font-display text-2xl font-semibold tracking-tight">{t("请重新登录")}</h1>
        <p className="mt-1.5 text-sm text-muted">{t("这次登录已超时。")}</p>
        <Button variant="primary" onClick={onRestart} className="mt-4 w-full">
          {t("重新开始")}
        </Button>
      </div>
    );
  }

  return (
    <div>
      <h1 className="font-display text-2xl font-semibold tracking-tight">{t("二次验证")}</h1>
      <p className="mt-1.5 text-sm text-muted">{t("密码正确，还需要一个验证方式。")}</p>

      {available.length > 1 && (
        <div className="mt-5 flex flex-wrap gap-1.5">
          {available.map((o) => (
            <button
              key={o.key}
              onClick={() => {
                setChoice(o.key);
                setError("");
                setCode("");
              }}
              className={cn(
                "rounded-lg border px-2.5 py-1.5 text-[13px] transition-colors",
                choice === o.key
                  ? "border-signal bg-signal-soft text-ink"
                  : "border-line-strong text-muted hover:border-signal",
              )}
            >
              {o.label}
            </button>
          ))}
        </div>
      )}

      {choice === "passkey" ? (
        <div className="mt-5">
          <p className="text-sm text-muted">{t("用你的通行密钥、指纹或安全密钥完成验证。")}</p>
          <Button variant="primary" onClick={usePasskey} disabled={busy} className="mt-4 w-full">
            {busy ? t("等待验证…") : t("使用通行密钥")}
          </Button>
        </div>
      ) : (
        <form onSubmit={submitCode} className="mt-5">
          {choice === "email" && (
            <div className="mb-3">
              <Button type="button" variant="outline" onClick={sendEmail} disabled={busy}>
                {sentTo ? t("重新发送") : t("发送验证码")}
              </Button>
              {sentTo && (
                <p className="mt-2 text-xs text-muted">
                  {tf("验证码已发送至 {addr}", { addr: sentTo })}
                </p>
              )}
            </div>
          )}
          <input
            autoFocus
            value={code}
            onChange={(e) => setCode(e.target.value)}
            inputMode={choice === "recovery" ? "text" : "numeric"}
            autoComplete="one-time-code"
            placeholder={choice === "recovery" ? t("恢复码") : t("6 位验证码")}
            className={inputCls + " font-mono"}
          />
          {error && <p className="mt-2 text-sm text-danger">{error}</p>}
          <Button
            type="submit"
            variant="primary"
            disabled={busy || !code.trim()}
            className="mt-4 w-full"
          >
            {busy ? t("验证中…") : t("验证")}
          </Button>
        </form>
      )}

      {choice === "passkey" && error && <p className="mt-2 text-sm text-danger">{error}</p>}

      <button onClick={onRestart} className="mt-4 text-sm text-muted hover:text-ink">
        {t("← 换个账号")}
      </button>
    </div>
  );
}

type Choice = "passkey" | "totp" | "email" | "recovery";

const inputCls =
  "w-full rounded-xl border border-line-strong bg-surface px-3.5 py-2.5 text-sm outline-none transition-colors placeholder:text-faint focus:border-signal";
