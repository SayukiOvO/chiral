import { useEffect, useState } from "react";
import { api, type Settings } from "../api";
import { Button } from "../components/ui";
import { ErrorBar, Field, inputCls } from "../components/primitives";
import { useT } from "../lib/i18n";

/**
 * Panel settings: choices about what the panel produces.
 *
 * Deliberately not environment variables. Those are deployment facts — where
 * the database is, which port to bind — and changing one means editing a file
 * on the host and restarting. These are things an operator decides and changes
 * their mind about.
 */
export function SettingsPage() {
  const { t } = useT();
  const [settings, setSettings] = useState<Settings | null>(null);
  const [name, setName] = useState("");
  const [busy, setBusy] = useState(false);
  const [saved, setSaved] = useState(false);
  const [error, setError] = useState("");

  async function load() {
    try {
      const s = await api.getSettings();
      setSettings(s);
      setName(s.subscription_name);
      setError("");
    } catch (e) {
      setError((e as Error).message);
    }
  }
  useEffect(() => {
    load();
  }, []);

  async function save(e: React.FormEvent) {
    e.preventDefault();
    setBusy(true);
    setError("");
    try {
      const s = await api.updateSettings({ subscription_name: name.trim() });
      setSettings(s);
      setName(s.subscription_name);
      setSaved(true);
      setTimeout(() => setSaved(false), 1800);
    } catch (err) {
      setError((err as Error).message);
    } finally {
      setBusy(false);
    }
  }

  if (!settings) return error ? <ErrorBar text={error} /> : null;

  return (
    <div>
      <div className="mb-6">
        <h1 className="font-display text-[26px] font-semibold tracking-tight">{t("设置")}</h1>
        <p className="mt-1 text-sm text-muted">{t("面板产出内容的相关选项。")}</p>
      </div>

      {error && <ErrorBar text={error} />}

      <form onSubmit={save} className="max-w-xl rounded-2xl border border-line bg-surface px-5 py-4">
        <h2 className="font-display text-[15px] font-semibold tracking-tight">{t("订阅")}</h2>

        <Field label={t("订阅名称")}>
          <input
            value={name}
            onChange={(e) => setName(e.target.value)}
            placeholder="chiral"
            className={inputCls}
          />
          {/* Said plainly because it is not obvious that a download filename
              becomes the profile's name in the client's list. */}
          <span className="mt-1 block text-xs text-faint">
            {t("订阅者在客户端里看到的配置名。留空则为 chiral。")}
          </span>
        </Field>

        {/* What the subscriber will actually see, after the characters that
            would break a header or escape a directory are removed. */}
        <div className="mt-2 text-xs text-faint">
          {t("客户端里显示为")}{" "}
          <span className="font-mono text-muted">
            {(name.trim() || "chiral").replace(/["\\/\n\r]/g, "") || "chiral"}
          </span>
        </div>

        <div className="mt-5 flex items-center justify-end gap-3">
          {saved && <span className="text-xs text-online">{t("已保存")}</span>}
          <Button type="submit" variant="primary" disabled={busy}>
            {busy ? t("保存中…") : t("保存")}
          </Button>
        </div>
      </form>

      <p className="mt-3 max-w-xl text-xs text-faint">
        {t("变更对此后每次订阅拉取生效；已导入的客户端需重新导入方可更新名称。")}
      </p>
    </div>
  );
}
