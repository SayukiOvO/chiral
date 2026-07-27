import { useState } from "react";
import { portal } from "./api";
import { Button } from "../components/ui";
import { Field, inputCls } from "../components/primitives";
import { ThemeToggle } from "../components/ThemeToggle";
import { LangToggle } from "../components/LangToggle";
import { cn } from "../lib/cn";
import { useT } from "../lib/i18n";

/** The two things a subscriber can change about themselves. */
export function AccountPage({ onSignOut }: { onSignOut: () => void }) {
  const { t } = useT();
  const [current, setCurrent] = useState("");
  const [next, setNext] = useState("");
  const [again, setAgain] = useState("");
  const [busy, setBusy] = useState(false);
  const [error, setError] = useState("");

  const mismatch = again.length > 0 && next !== again;

  async function submit(e: React.FormEvent) {
    e.preventDefault();
    setBusy(true);
    setError("");
    try {
      await portal.changePassword(current, next);
      // Every session is gone, this one included — that is the point of
      // changing a password. Land on the sign-in screen rather than letting
      // the next request fail mysteriously.
      onSignOut();
    } catch (err) {
      setError((err as Error).message);
      setBusy(false);
    }
  }

  return (
    <div className="animate-rise">
      <h1 className="font-display text-[22px] font-semibold tracking-tight">{t("账户")}</h1>

      <section className="mt-5 rounded-2xl border border-line bg-surface px-5 py-5">
        <h2 className="font-display text-[15px] font-semibold tracking-tight">{t("修改密码")}</h2>
        <p className="mt-1 text-sm text-muted">{t("改完需要重新登录。")}</p>
        <form onSubmit={submit}>
          <Field label={t("当前密码")}>
            <input
              type="password"
              autoComplete="current-password"
              value={current}
              onChange={(e) => setCurrent(e.target.value)}
              className={inputCls}
            />
          </Field>
          <Field label={t("新密码（至少 8 位）")}>
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
          <Button
            type="submit"
            variant="primary"
            disabled={busy || !current || next.length < 8 || next !== again}
            className="mt-4"
          >
            {busy ? t("保存中…") : t("保存")}
          </Button>
        </form>
      </section>

      {/* Theme and language live here rather than in the header: six controls
          plus a wordmark do not fit 375px, and a subscriber sets these once. */}
      <section className="mt-5 rounded-2xl border border-line bg-surface px-5 py-5">
        <h2 className="font-display text-[15px] font-semibold tracking-tight">{t("外观")}</h2>
        <div className="mt-3 flex flex-wrap items-center gap-3">
          <LangToggle />
          <ThemeToggle />
        </div>
      </section>

      <section className="mt-5">
        <Button variant="ghost" onClick={onSignOut}>
          {t("退出登录")}
        </Button>
      </section>
    </div>
  );
}
