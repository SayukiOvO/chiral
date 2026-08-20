import { useState } from "react";
import { api, type User, type UserInput } from "../api";
import { dateToUnix, unixToDate } from "../format";
import { Button } from "./ui";
import { useT } from "../lib/i18n";

const PERIODS = [
  { label: "不续期", seconds: 0 },
  { label: "每 30 天", seconds: 30 * 86400 },
  { label: "每 7 天", seconds: 7 * 86400 },
  { label: "每 90 天", seconds: 90 * 86400 },
];

// Quota is entered in GB because that is how operators think about it; 0 is
// the documented "unlimited".
const GB = 1024 * 1024 * 1024;

export function UserDialog({
  user,
  onClose,
  onSaved,
}: {
  /** Absent for a new user. */
  user?: User;
  onClose: () => void;
  onSaved: (created?: { user: User; subscription_url: string }) => void;
}) {
  const { t } = useT();
  const [name, setName] = useState(user?.name ?? "");
  const [quotaGB, setQuotaGB] = useState(
    user ? String(user.quota_bytes ? user.quota_bytes / GB : 0) : "0",
  );
  const [expires, setExpires] = useState(unixToDate(user?.expires_at ?? 0));
  const [period, setPeriod] = useState(user?.renew_period ?? 0);
  const [deviceLimit, setDeviceLimit] = useState(String(user?.device_limit ?? 0));
  const [enabled, setEnabled] = useState(user?.enabled ?? true);
  const [busy, setBusy] = useState(false);
  const [error, setError] = useState("");

  async function save(e: React.FormEvent) {
    e.preventDefault();
    const quota = Math.round(Number(quotaGB) * GB);
    if (!Number.isFinite(quota) || quota < 0) {
      setError(t("流量配额须为 0 或正数（0 表示不限）"));
      return;
    }
    const input: UserInput = {
      name: name.trim(),
      quota_bytes: quota,
      expires_at: dateToUnix(expires),
      renew_period: period,
      device_limit: Math.max(0, Math.round(Number(deviceLimit) || 0)),
      enabled,
    };
    setBusy(true);
    setError("");
    try {
      if (user) {
        await api.updateUser(user.id, input);
        onSaved();
      } else {
        onSaved(await api.createUser(input));
      }
    } catch (err) {
      setError((err as Error).message);
    } finally {
      setBusy(false);
    }
  }

  return (
    <div
      className="fixed inset-0 z-50 flex items-center justify-center bg-black/40 px-4 backdrop-blur-sm"
      onClick={onClose}
    >
      <form
        role="dialog"
        aria-modal="true"
        onSubmit={save}
        onClick={(e) => e.stopPropagation()}
        className="w-full max-w-md animate-rise rounded-2xl border border-line bg-raised p-6 shadow-[var(--shadow-pop)]"
      >
        <h3 className="font-display text-lg font-semibold tracking-tight">
          {user ? t("编辑用户") : t("新增用户")}
        </h3>
        <p className="mt-1 text-sm text-muted">
          {user ? t("改动立即下发至节点。") : t("创建完成后将生成订阅链接，此后可随时查看。")}
        </p>

        <label className="mt-4 block">
          <span className="text-xs font-medium uppercase tracking-[0.07em] text-faint">
            {t("用户名")}
          </span>
          <input
            autoFocus
            value={name}
            onChange={(e) => setName(e.target.value)}
            placeholder={t("例如 alice")}
            className="mt-1.5 w-full rounded-xl border border-line-strong bg-surface px-3.5 py-2.5 text-sm outline-none transition-colors placeholder:text-faint focus:border-signal"
          />
        </label>

        <div className="mt-4 grid grid-cols-2 gap-3">
          <label className="block">
            <span className="text-xs font-medium uppercase tracking-[0.07em] text-faint">
              {t("流量配额 (GB)")}
            </span>
            <input
              type="number"
              min="0"
              step="0.1"
              value={quotaGB}
              onChange={(e) => setQuotaGB(e.target.value)}
              className="mt-1.5 w-full rounded-xl border border-line-strong bg-surface px-3.5 py-2.5 font-mono text-sm outline-none transition-colors focus:border-signal"
            />
            <span className="mt-1 block text-xs text-faint">{t("0 表示不限")}</span>
          </label>
          <label className="block">
            <span className="text-xs font-medium uppercase tracking-[0.07em] text-faint">
              {t("到期日")}
            </span>
            <input
              type="date"
              value={expires}
              onChange={(e) => setExpires(e.target.value)}
              className="mt-1.5 w-full rounded-xl border border-line-strong bg-surface px-3.5 py-2.5 font-mono text-sm outline-none transition-colors focus:border-signal"
            />
            <span className="mt-1 block text-xs text-faint">{t("留空表示永不过期")}</span>
          </label>
        </div>

        <label className="mt-4 block">
          <span className="text-xs font-medium uppercase tracking-[0.07em] text-faint">
            {t("自动续期")}
          </span>
          <select
            value={period}
            onChange={(e) => setPeriod(Number(e.target.value))}
            className="mt-1.5 w-full rounded-xl border border-line-strong bg-surface px-3.5 py-2.5 text-sm outline-none transition-colors focus:border-signal"
          >
            {PERIODS.map((p) => (
              <option key={p.seconds} value={p.seconds}>
                {t(p.label)}
              </option>
            ))}
          </select>
          <span className="mt-1 block text-xs text-faint">
            {t("到期顺延一个周期，并清零已用流量")}
          </span>
        </label>

        <label className="mt-4 block">
          <span className="text-xs font-medium uppercase tracking-[0.07em] text-faint">
            {t("并发地址上限")}
          </span>
          <input
            type="number"
            min="0"
            step="1"
            value={deviceLimit}
            onChange={(e) => setDeviceLimit(e.target.value)}
            className="mt-1.5 w-full rounded-xl border border-line-strong bg-surface px-3.5 py-2.5 font-mono text-sm outline-none transition-colors focus:border-signal"
          />
          {/* Deliberately not "devices": Xray counts distinct source
              addresses, so one household behind NAT is 1 and one phone moving
              between wifi and cellular is 2. Saying "devices" would promise
              something the kernel cannot deliver. */}
          <span className="mt-1 block text-xs text-faint">
            {t("0 表示不限。仅作提示，不会自动断开连接。")}
          </span>
        </label>

        <label className="mt-4 flex items-center gap-2.5">
          <input
            type="checkbox"
            checked={enabled}
            onChange={(e) => setEnabled(e.target.checked)}
            className="h-4 w-4 accent-[var(--online)]"
          />
          <span className="text-sm">{t("启用")}</span>
        </label>

        {error && <p className="mt-3 text-sm text-danger">{error}</p>}

        <div className="mt-5 flex justify-end gap-2">
          <Button type="button" variant="ghost" onClick={onClose}>
            {t("取消")}
          </Button>
          <Button type="submit" variant="primary" disabled={busy || !name.trim()}>
            {busy ? t("保存中…") : t("保存")}
          </Button>
        </div>
      </form>
    </div>
  );
}
