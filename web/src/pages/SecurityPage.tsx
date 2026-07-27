import { useEffect, useState } from "react";
import { api, type MfaFactor, type MfaStatus, type Whoami } from "../api";
import { Button, IconButton } from "../components/ui";
import { CheckIcon, CopyIcon, PlusIcon, TrashIcon } from "../components/icons";
import { QRCode } from "../components/QRCode";
import { Empty, ErrorBar, Field, Modal, inputCls } from "../components/primitives";
import { relativeTime } from "../format";
import { cn } from "../lib/cn";
import { useT } from "../lib/i18n";
import * as webauthn from "../lib/webauthn";

const KIND_LABEL: Record<MfaFactor["kind"], string> = {
  totp: "验证器应用",
  passkey: "通行密钥",
  email: "邮箱",
};

/**
 * Your own account: password, second factors, recovery codes.
 *
 * Everything here acts on the signed-in account only — the API has no route
 * for editing someone else's factors, because that would be a way to take
 * over their account.
 */
export function SecurityPage() {
  const { t, tf } = useT();
  const [me, setMe] = useState<Whoami | null>(null);
  const [status, setStatus] = useState<MfaStatus | null>(null);
  const [error, setError] = useState("");
  // Shown once, never fetchable again — the panel only keeps hashes.
  const [codes, setCodes] = useState<string[] | null>(null);
  const [adding, setAdding] = useState<"totp" | "email" | null>(null);
  const [changingPassword, setChangingPassword] = useState(false);

  async function refresh() {
    try {
      const [w, s] = await Promise.all([api.whoami(), api.mfaStatus()]);
      setMe(w);
      setStatus(s);
      setError("");
    } catch (e) {
      setError((e as Error).message);
    }
  }

  useEffect(() => {
    refresh();
  }, []);

  if (me?.via_token) return <TokenNotice />;
  if (!status) return error ? <ErrorBar text={error} /> : null;

  const confirmed = status.factors.filter((f) => f.confirmed);

  async function addPasskey() {
    try {
      // No name: the server's default is language-neutral, and a translated
      // one would be frozen into the database at whatever language was
      // active when it was enrolled.
      const r = await webauthn.enrol("");
      if (r.recovery_codes) setCodes(r.recovery_codes);
      refresh();
    } catch (e) {
      const msg = (e as Error).message;
      if (!/NotAllowed|abort/i.test(msg)) setError(msg);
    }
  }

  return (
    <div>
      <div className="mb-6">
        <h1 className="font-display text-[26px] font-semibold tracking-tight">{t("安全")}</h1>
        <p className="mt-1 text-sm text-muted">
          {t("登录到")} <span className="font-mono">{me?.name}</span>
          {me && (
            <span className="ml-1.5 text-faint">
              · {t(ROLE_LABEL[me.role] ?? me.role)}
            </span>
          )}
        </p>
      </div>

      {error && <ErrorBar text={error} />}

      <Section
        title={t("二次验证")}
        note={
          confirmed.length === 0
            ? t("只有密码。加一个第二因素，密码泄露就不足以登录。")
            : t("登录时，密码之外还需要下面任意一项。")
        }
      >
        {confirmed.length === 0 ? (
          <Empty>{t("还没有第二因素。")}</Empty>
        ) : (
          <div className="overflow-hidden rounded-2xl border border-line bg-surface">
            {confirmed.map((f) => (
              <div
                key={f.id}
                className="flex items-center justify-between gap-4 border-b border-line px-4 py-3 last:border-0"
              >
                <div className="min-w-0">
                  {/* Factors carry a server-side name, but only the email one
                      says anything a person needs — the rest are named after
                      their kind, which we can say in the reader's language. */}
                  <div className="truncate text-sm">
                    {f.kind === "email" ? f.name : t(KIND_LABEL[f.kind])}
                  </div>
                  <div className="mt-0.5 text-xs text-faint">
                    {f.last_used_at
                      ? tf("最近使用 {when}", { when: relativeTime(f.last_used_at) })
                      : t("从未使用")}
                  </div>
                </div>
                <IconButton
                  label={t("移除")}
                  className="hover:text-danger"
                  onClick={async () => {
                    if (
                      !confirm(
                        confirmed.length === 1
                          ? t("这是最后一个第二因素，移除后只剩密码。确定？")
                          : t("移除这个第二因素？"),
                      )
                    )
                      return;
                    try {
                      await api.deleteMfaFactor(f.id);
                      refresh();
                    } catch (e) {
                      setError((e as Error).message);
                    }
                  }}
                >
                  <TrashIcon size={16} />
                </IconButton>
              </div>
            ))}
          </div>
        )}

        <div className="mt-3 flex flex-wrap gap-2">
          <Button onClick={() => setAdding("totp")}>
            <PlusIcon size={16} />
            {t("验证器应用")}
          </Button>
          <Button
            onClick={addPasskey}
            disabled={!status.passkey_ready || !webauthn.supported()}
            title={
              status.passkey_ready
                ? undefined
                : t("通行密钥需要面板经 https（或 localhost）访问")
            }
          >
            <PlusIcon size={16} />
            {t("通行密钥")}
          </Button>
          <Button
            onClick={() => setAdding("email")}
            disabled={!status.email_ready}
            title={status.email_ready ? undefined : t("面板未配置 SMTP")}
          >
            <PlusIcon size={16} />
            {status.email_verified ? t("更换邮箱") : t("邮箱验证码")}
          </Button>
        </div>
        {!status.passkey_ready && (
          <p className="mt-2 text-xs text-faint">
            {t("通行密钥需要面板经 https（或 localhost）访问，且 CHIRAL_PUBLIC_URL 指向它。")}
          </p>
        )}
        {!status.email_ready && (
          <p className="mt-2 text-xs text-faint">
            {t("邮箱验证码需要在 .env 里配置 CHIRAL_SMTP_HOST 与 CHIRAL_SMTP_FROM。")}
          </p>
        )}
      </Section>

      <Section
        title={t("恢复码")}
        note={t("设备丢了的时候用它登录。每个只能用一次，重新生成会作废旧的。")}
      >
        <div className="flex items-center justify-between gap-4 rounded-2xl border border-line bg-surface px-4 py-3">
          <span className="text-sm text-muted">
            {status.recovery_left > 0
              ? tf("剩余 {n} 个未使用", { n: status.recovery_left })
              : t("还没有恢复码。")}
          </span>
          <Button
            size="sm"
            onClick={async () => {
              if (
                status.recovery_left > 0 &&
                !confirm(t("重新生成会作废现有的恢复码。确定？"))
              )
                return;
              try {
                const r = await api.regenerateRecoveryCodes();
                setCodes(r.codes);
                refresh();
              } catch (e) {
                setError((e as Error).message);
              }
            }}
          >
            {status.recovery_left > 0 ? t("重新生成") : t("生成")}
          </Button>
        </div>
      </Section>

      <Section title={t("密码")} note={t("改密码会让所有已登录的会话失效，包括当前这个。")}>
        <Button onClick={() => setChangingPassword(true)}>{t("修改密码")}</Button>
      </Section>

      {adding === "totp" && (
        <TotpDialog
          onClose={() => setAdding(null)}
          onDone={(c) => {
            if (c) setCodes(c);
            refresh();
          }}
        />
      )}
      {adding === "email" && (
        <EmailDialog
          current={status.email}
          onClose={() => setAdding(null)}
          onDone={(c) => {
            if (c) setCodes(c);
            refresh();
          }}
        />
      )}
      {codes && <RecoveryCodes codes={codes} onClose={() => setCodes(null)} />}
      {changingPassword && me && (
        <PasswordDialog id={me.id} onClose={() => setChangingPassword(false)} />
      )}
    </div>
  );
}

const ROLE_LABEL: Record<string, string> = {
  superadmin: "超级管理员",
  operator: "操作员",
  viewer: "只读",
};

function Section({
  title,
  note,
  children,
}: {
  title: string;
  note: string;
  children: React.ReactNode;
}) {
  return (
    <section className="mb-9">
      <h2 className="font-display text-[15px] font-semibold tracking-tight">{title}</h2>
      <p className="mb-3 mt-1 text-sm text-muted">{note}</p>
      {children}
    </section>
  );
}

function TokenNotice() {
  const { t } = useT();
  return (
    <div>
      <h1 className="font-display text-[26px] font-semibold tracking-tight">{t("安全")}</h1>
      <p className="mt-4 max-w-prose text-sm text-muted">
        {t("当前是用环境变量里的管理令牌进入的，它没有对应的账号，也就没有第二因素可设。用一个管理员账号登录后再来这里。")}
      </p>
    </div>
  );
}

/** TOTP: show the secret, take a code back to prove the app has it. */
function TotpDialog({
  onClose,
  onDone,
}: {
  onClose: () => void;
  onDone: (codes?: string[]) => void;
}) {
  const { t } = useT();
  const [begun, setBegun] = useState<{ id: string; secret: string; uri: string } | null>(null);
  const [code, setCode] = useState("");
  const [busy, setBusy] = useState(false);
  const [error, setError] = useState("");

  useEffect(() => {
    // Unnamed on purpose — see addPasskey.
    api
      .beginTotp("")
      .then(setBegun)
      .catch((e) => setError((e as Error).message));
    // Once: a second call would leave an orphan unconfirmed factor behind.
  }, []);

  async function confirm(e: React.FormEvent) {
    e.preventDefault();
    if (!begun) return;
    setBusy(true);
    setError("");
    try {
      const r = await api.confirmTotp(begun.id, code.trim());
      onDone(r.recovery_codes);
      onClose();
    } catch (e) {
      setError((e as Error).message);
      setCode("");
    } finally {
      setBusy(false);
    }
  }

  return (
    <Modal onClose={onClose}>
      <h3 className="font-display text-lg font-semibold tracking-tight">{t("添加验证器应用")}</h3>
      <p className="mt-1 text-sm text-muted">
        {t("用 Authy、1Password、Google Authenticator 之类的应用扫码，再填一次它给出的验证码。")}
      </p>

      {error && !begun && <p className="mt-3 text-sm text-danger">{error}</p>}

      {begun && (
        <form onSubmit={confirm}>
          <div className="mt-5 flex flex-col items-center gap-3 sm:flex-row sm:items-start sm:gap-5">
            <QRCode value={begun.uri} />
            <div className="min-w-0 flex-1">
              <div className="text-[11px] font-medium uppercase tracking-[0.07em] text-faint">
                {t("或手动输入密钥")}
              </div>
              <div className="mt-1.5 flex items-start gap-1.5">
                <code className="min-w-0 flex-1 break-all font-mono text-[13px]">
                  {begun.secret}
                </code>
                <CopyButton value={begun.secret} />
              </div>
            </div>
          </div>

          <Field label={t("应用给出的 6 位验证码")}>
            <input
              autoFocus
              value={code}
              onChange={(e) => setCode(e.target.value)}
              inputMode="numeric"
              autoComplete="one-time-code"
              placeholder="000000"
              className={inputCls + " font-mono"}
            />
          </Field>
          {error && <p className="mt-3 text-sm text-danger">{error}</p>}

          <div className="mt-5 flex justify-end gap-2">
            <Button type="button" variant="ghost" onClick={onClose}>
              {t("取消")}
            </Button>
            <Button type="submit" variant="primary" disabled={busy || code.trim().length < 6}>
              {busy ? t("验证中…") : t("开启")}
            </Button>
          </div>
        </form>
      )}
    </Modal>
  );
}

/** Email: send a code to the address, take it back to prove it arrives. */
function EmailDialog({
  current,
  onClose,
  onDone,
}: {
  current: string;
  onClose: () => void;
  onDone: (codes?: string[]) => void;
}) {
  const { t, tf } = useT();
  const [email, setEmail] = useState(current);
  const [sentTo, setSentTo] = useState("");
  const [code, setCode] = useState("");
  const [busy, setBusy] = useState(false);
  const [error, setError] = useState("");

  async function send() {
    setBusy(true);
    setError("");
    try {
      const r = await api.sendEmailVerification(email.trim());
      setSentTo(r.sent_to);
    } catch (e) {
      setError((e as Error).message);
    } finally {
      setBusy(false);
    }
  }

  async function confirm(e: React.FormEvent) {
    e.preventDefault();
    setBusy(true);
    setError("");
    try {
      const r = await api.confirmEmail(code.trim());
      onDone(r.recovery_codes);
      onClose();
    } catch (e) {
      setError((e as Error).message);
      setCode("");
    } finally {
      setBusy(false);
    }
  }

  return (
    <Modal onClose={onClose}>
      <form onSubmit={confirm}>
        <h3 className="font-display text-lg font-semibold tracking-tight">{t("验证邮箱")}</h3>
        <p className="mt-1 text-sm text-muted">
          {t("验证后，这个地址可以作为登录时的第二因素接收验证码。")}
        </p>

        <Field label={t("邮箱地址")}>
          <div className="flex gap-2">
            <input
              autoFocus
              type="email"
              value={email}
              onChange={(e) => setEmail(e.target.value)}
              placeholder="you@example.com"
              className={inputCls}
            />
            <Button type="button" onClick={send} disabled={busy || !email.trim()}>
              {sentTo ? t("重新发送") : t("发送")}
            </Button>
          </div>
        </Field>

        {sentTo && (
          <>
            <p className="mt-2 text-xs text-muted">
              {tf("验证码已发送至 {addr}", { addr: sentTo })}
            </p>
            <Field label={t("邮件里的 6 位验证码")}>
              <input
                value={code}
                onChange={(e) => setCode(e.target.value)}
                inputMode="numeric"
                autoComplete="one-time-code"
                placeholder="000000"
                className={inputCls + " font-mono"}
              />
            </Field>
          </>
        )}
        {error && <p className="mt-3 text-sm text-danger">{error}</p>}

        <div className="mt-5 flex justify-end gap-2">
          <Button type="button" variant="ghost" onClick={onClose}>
            {t("取消")}
          </Button>
          <Button type="submit" variant="primary" disabled={busy || !sentTo || !code.trim()}>
            {busy ? t("验证中…") : t("验证")}
          </Button>
        </div>
      </form>
    </Modal>
  );
}

/** Recovery codes are readable exactly once; the dialog says so plainly. */
function RecoveryCodes({ codes, onClose }: { codes: string[]; onClose: () => void }) {
  const { t } = useT();
  return (
    <Modal onClose={onClose}>
      <h3 className="font-display text-lg font-semibold tracking-tight">{t("恢复码")}</h3>
      <p className="mt-1 text-sm text-muted">
        {t("现在就存好。面板只保存它们的哈希，关掉这个窗口后再也看不到。")}
      </p>
      <div className="mt-4 grid grid-cols-2 gap-x-4 gap-y-1.5 rounded-xl border border-line bg-surface px-4 py-3.5 font-mono text-[13px]">
        {codes.map((c) => (
          <span key={c}>{c}</span>
        ))}
      </div>
      <div className="mt-5 flex justify-between gap-2">
        <CopyButton value={codes.join("\n")} label={t("复制全部")} />
        <Button variant="primary" onClick={onClose}>
          {t("我存好了")}
        </Button>
      </div>
    </Modal>
  );
}

function PasswordDialog({ id, onClose }: { id: string; onClose: () => void }) {
  const { t } = useT();
  const [current, setCurrent] = useState("");
  const [next, setNext] = useState("");
  const [again, setAgain] = useState("");
  const [busy, setBusy] = useState(false);
  const [error, setError] = useState("");

  async function submit(e: React.FormEvent) {
    e.preventDefault();
    setBusy(true);
    setError("");
    try {
      await api.changePassword(id, current, next);
      // Every session is gone, including this one; a reload lands on login.
      window.location.reload();
    } catch (e) {
      setError((e as Error).message);
      setBusy(false);
    }
  }

  const mismatch = again.length > 0 && next !== again;

  return (
    <Modal onClose={onClose}>
      <form onSubmit={submit}>
        <h3 className="font-display text-lg font-semibold tracking-tight">{t("修改密码")}</h3>
        <p className="mt-1 text-sm text-muted">{t("改完需要重新登录。")}</p>

        <Field label={t("当前密码")}>
          <input
            autoFocus
            type="password"
            autoComplete="current-password"
            value={current}
            onChange={(e) => setCurrent(e.target.value)}
            className={inputCls}
          />
        </Field>
        <Field label={t("新密码")}>
          <input
            type="password"
            autoComplete="new-password"
            value={next}
            onChange={(e) => setNext(e.target.value)}
            className={inputCls}
          />
        </Field>
        <Field label={t("再输一次")}>
          <input
            type="password"
            autoComplete="new-password"
            value={again}
            onChange={(e) => setAgain(e.target.value)}
            className={cn(inputCls, mismatch && "border-danger")}
          />
        </Field>
        {mismatch && <p className="mt-2 text-sm text-danger">{t("两次输入不一致。")}</p>}
        {error && <p className="mt-3 text-sm text-danger">{error}</p>}

        <div className="mt-5 flex justify-end gap-2">
          <Button type="button" variant="ghost" onClick={onClose}>
            {t("取消")}
          </Button>
          <Button
            type="submit"
            variant="primary"
            disabled={busy || !current || next.length < 8 || next !== again}
          >
            {busy ? t("保存中…") : t("保存")}
          </Button>
        </div>
      </form>
    </Modal>
  );
}

function CopyButton({ value, label }: { value: string; label?: string }) {
  const { t } = useT();
  const [done, setDone] = useState(false);
  const copy = async () => {
    await navigator.clipboard.writeText(value);
    setDone(true);
    setTimeout(() => setDone(false), 1400);
  };
  if (label) {
    return (
      <Button onClick={copy} type="button">
        {done ? <CheckIcon size={16} className="text-online" /> : <CopyIcon size={16} />}
        {done ? t("已复制") : label}
      </Button>
    );
  }
  return (
    <IconButton label={t("复制")} onClick={copy} type="button">
      {done ? <CheckIcon size={16} className="text-online" /> : <CopyIcon size={16} />}
    </IconButton>
  );
}
