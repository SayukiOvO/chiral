import { useState } from "react";
import { api, setToken } from "../api";
import { Mark } from "./Mark";
import { Button } from "./ui";
import { ThemeToggle } from "./ThemeToggle";
import { useT } from "../lib/i18n";

/** Admin-token gate. A calm, centered brand moment. */
export function TokenGate({ onAuthed }: { onAuthed: () => void }) {
  const { t } = useT();
  const [value, setValue] = useState("");
  const [error, setError] = useState("");
  const [busy, setBusy] = useState(false);

  async function submit(e: React.FormEvent) {
    e.preventDefault();
    setToken(value.trim());
    setBusy(true);
    setError("");
    try {
      await api.listNodes();
      onAuthed();
    } catch {
      setError(t("令牌无效，请重试。"));
    } finally {
      setBusy(false);
    }
  }

  return (
    <div className="relative min-h-screen">
      <div className="absolute right-5 top-5 sm:right-8">
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
          <h1 className="font-display text-2xl font-semibold tracking-tight">{t("节点控制台")}</h1>
          <p className="mt-1.5 text-sm text-muted">{t("输入管理令牌以进入。")}</p>

          <form onSubmit={submit} className="mt-6">
            <input
              type="password"
              autoFocus
              value={value}
              onChange={(e) => setValue(e.target.value)}
              placeholder={t("管理令牌")}
              className="w-full rounded-xl border border-line-strong bg-surface px-3.5 py-2.5 text-sm outline-none transition-colors placeholder:text-faint focus:border-signal"
            />
            {error && <p className="mt-2 text-sm text-danger">{error}</p>}
            <Button
              type="submit"
              variant="primary"
              disabled={busy || !value.trim()}
              className="mt-4 w-full"
            >
              {busy ? t("验证中…") : t("进入")}
            </Button>
          </form>
        </div>
      </div>
    </div>
  );
}
