import { useState } from "react";
import { api, type User, type UserInput } from "../api";
import { dateToUnix, unixToDate } from "../format";
import { Button } from "./ui";

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
  const [name, setName] = useState(user?.name ?? "");
  const [quotaGB, setQuotaGB] = useState(
    user ? String(user.quota_bytes ? user.quota_bytes / GB : 0) : "0",
  );
  const [expires, setExpires] = useState(unixToDate(user?.expires_at ?? 0));
  const [period, setPeriod] = useState(user?.renew_period ?? 0);
  const [enabled, setEnabled] = useState(user?.enabled ?? true);
  const [busy, setBusy] = useState(false);
  const [error, setError] = useState("");

  async function save(e: React.FormEvent) {
    e.preventDefault();
    const quota = Math.round(Number(quotaGB) * GB);
    if (!Number.isFinite(quota) || quota < 0) {
      setError("流量配额必须是 0 或正数（0 表示不限）");
      return;
    }
    const input: UserInput = {
      name: name.trim(),
      quota_bytes: quota,
      expires_at: dateToUnix(expires),
      renew_period: period,
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
          {user ? "编辑用户" : "新增用户"}
        </h3>
        <p className="mt-1 text-sm text-muted">
          {user ? "改动会立即下发到节点。" : "创建后会给出订阅链接，只显示这一次。"}
        </p>

        <label className="mt-4 block">
          <span className="text-xs font-medium uppercase tracking-[0.07em] text-faint">
            用户名
          </span>
          <input
            autoFocus
            value={name}
            onChange={(e) => setName(e.target.value)}
            placeholder="例如 alice"
            className="mt-1.5 w-full rounded-xl border border-line-strong bg-surface px-3.5 py-2.5 text-sm outline-none transition-colors placeholder:text-faint focus:border-signal"
          />
        </label>

        <div className="mt-4 grid grid-cols-2 gap-3">
          <label className="block">
            <span className="text-xs font-medium uppercase tracking-[0.07em] text-faint">
              流量配额 (GB)
            </span>
            <input
              type="number"
              min="0"
              step="0.1"
              value={quotaGB}
              onChange={(e) => setQuotaGB(e.target.value)}
              className="mt-1.5 w-full rounded-xl border border-line-strong bg-surface px-3.5 py-2.5 font-mono text-sm outline-none transition-colors focus:border-signal"
            />
            <span className="mt-1 block text-xs text-faint">0 表示不限</span>
          </label>
          <label className="block">
            <span className="text-xs font-medium uppercase tracking-[0.07em] text-faint">
              到期日
            </span>
            <input
              type="date"
              value={expires}
              onChange={(e) => setExpires(e.target.value)}
              className="mt-1.5 w-full rounded-xl border border-line-strong bg-surface px-3.5 py-2.5 font-mono text-sm outline-none transition-colors focus:border-signal"
            />
            <span className="mt-1 block text-xs text-faint">留空表示永不过期</span>
          </label>
        </div>

        <label className="mt-4 block">
          <span className="text-xs font-medium uppercase tracking-[0.07em] text-faint">
            自动续期
          </span>
          <select
            value={period}
            onChange={(e) => setPeriod(Number(e.target.value))}
            className="mt-1.5 w-full rounded-xl border border-line-strong bg-surface px-3.5 py-2.5 text-sm outline-none transition-colors focus:border-signal"
          >
            {PERIODS.map((p) => (
              <option key={p.seconds} value={p.seconds}>
                {p.label}
              </option>
            ))}
          </select>
          <span className="mt-1 block text-xs text-faint">
            到期时顺延一个周期，并把已用流量清零
          </span>
        </label>

        <label className="mt-4 flex items-center gap-2.5">
          <input
            type="checkbox"
            checked={enabled}
            onChange={(e) => setEnabled(e.target.checked)}
            className="h-4 w-4 accent-[var(--online)]"
          />
          <span className="text-sm">启用</span>
        </label>

        {error && <p className="mt-3 text-sm text-danger">{error}</p>}

        <div className="mt-5 flex justify-end gap-2">
          <Button type="button" variant="ghost" onClick={onClose}>
            取消
          </Button>
          <Button type="submit" variant="primary" disabled={busy || !name.trim()}>
            {busy ? "保存中…" : "保存"}
          </Button>
        </div>
      </form>
    </div>
  );
}
