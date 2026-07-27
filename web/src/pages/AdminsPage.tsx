import { useEffect, useState } from "react";
import { api, type Admin, type Whoami } from "../api";
import { Button, IconButton } from "../components/ui";
import { PlusIcon, TrashIcon } from "../components/icons";
import { Empty, ErrorBar, Field, Modal, inputCls } from "../components/primitives";
import { relativeTime } from "../format";
import { cn } from "../lib/cn";
import { useT } from "../lib/i18n";

const ROLES: { role: Admin["role"]; label: string; note: string }[] = [
  { role: "superadmin", label: "超级管理员", note: "全部权限，含管理其他管理员" },
  { role: "operator", label: "操作员", note: "改状态：节点 / 接入配置 / 变量 / 用户" },
  { role: "viewer", label: "只读", note: "只能看" },
];

/**
 * Who can sign in to the console, and as what.
 *
 * Superadmin only, because handing out roles is how someone gives themselves
 * more of them. Note that "viewer" is not nothing: it reads the whole fleet
 * and every user, so it is the right role for someone on call, not for
 * someone you merely want to show a dashboard to.
 */
export function AdminsPage() {
  const { t } = useT();
  const [admins, setAdmins] = useState<Admin[]>([]);
  const [me, setMe] = useState<Whoami | null>(null);
  const [loaded, setLoaded] = useState(false);
  const [error, setError] = useState("");
  const [adding, setAdding] = useState(false);

  async function refresh() {
    try {
      const [list, who] = await Promise.all([api.listAdmins(), api.whoami()]);
      setAdmins(list.admins);
      setMe(who);
      setError("");
    } catch (e) {
      setError((e as Error).message);
    } finally {
      setLoaded(true);
    }
  }

  useEffect(() => {
    refresh();
  }, []);

  if (loaded && error && admins.length === 0) {
    return (
      <div>
        <h1 className="font-display text-[26px] font-semibold tracking-tight">{t("管理员")}</h1>
        <p className="mt-4 max-w-prose text-sm text-muted">
          {/unauthorized|forbidden|403/i.test(error)
            ? t("只有超级管理员能管理管理员账号。")
            : error}
        </p>
      </div>
    );
  }

  return (
    <div>
      <div className="mb-6 flex items-end justify-between gap-4">
        <div className="animate-rise">
          <h1 className="font-display text-[26px] font-semibold tracking-tight">{t("管理员")}</h1>
          <p className="mt-1 text-sm text-muted">
            {t("能登录控制台的人，以及他们的角色。")}
          </p>
        </div>
        <Button variant="primary" onClick={() => setAdding(true)}>
          <PlusIcon size={16} />
          {t("新增管理员")}
        </Button>
      </div>

      {error && <ErrorBar text={error} />}

      {loaded &&
        (admins.length === 0 ? (
          <Empty>{t("还没有管理员账号。")}</Empty>
        ) : (
          <div className="overflow-hidden rounded-2xl border border-line bg-surface">
            {admins.map((a) => (
              <AdminRow
                key={a.id}
                admin={a}
                isMe={me?.id === a.id}
                onChanged={refresh}
                onError={setError}
              />
            ))}
          </div>
        ))}

      {adding && <AddAdminDialog onClose={() => setAdding(false)} onCreated={refresh} />}
    </div>
  );
}

function AdminRow({
  admin,
  isMe,
  onChanged,
  onError,
}: {
  admin: Admin;
  isMe: boolean;
  onChanged: () => void;
  onError: (msg: string) => void;
}) {
  const { t, tf } = useT();
  const [busy, setBusy] = useState(false);
  const [confirming, setConfirming] = useState(false);

  async function patch(p: { role?: Admin["role"]; disabled?: boolean }) {
    setBusy(true);
    try {
      await api.updateAdmin(admin.id, p);
      onChanged();
    } catch (e) {
      onError((e as Error).message);
    } finally {
      setBusy(false);
    }
  }

  return (
    <div className="flex flex-wrap items-center justify-between gap-3 border-b border-line px-4 py-3 last:border-0">
      <div className="min-w-0">
        <div className="flex items-center gap-2">
          <span className="truncate text-sm font-medium">{admin.username}</span>
          {isMe && <span className="text-xs text-faint">{t("（你自己）")}</span>}
          {admin.disabled && <span className="text-xs text-danger">{t("已停用")}</span>}
        </div>
        <div className="mt-0.5 text-xs text-faint">
          {admin.last_login
            ? tf("最近登录 {when}", { when: relativeTime(admin.last_login) })
            : t("从未登录")}
        </div>
      </div>

      <div className="flex shrink-0 items-center gap-1.5">
        <select
          value={admin.role}
          disabled={busy || isMe}
          onChange={(e) => patch({ role: e.target.value as Admin["role"] })}
          // Changing your own role is refused by the server too; disabling it
          // here just means the refusal is not a surprise.
          title={isMe ? t("不能改自己的角色") : undefined}
          className="rounded-lg border border-line-strong bg-surface px-2 py-1.5 text-[13px] outline-none focus:border-signal disabled:opacity-50"
        >
          {ROLES.map((r) => (
            <option key={r.role} value={r.role}>
              {t(r.label)}
            </option>
          ))}
        </select>
        <Button
          size="sm"
          variant="ghost"
          disabled={busy || isMe}
          onClick={() => patch({ disabled: !admin.disabled })}
        >
          {admin.disabled ? t("启用") : t("停用")}
        </Button>
        {confirming ? (
          <>
            <button
              onClick={() => setConfirming(false)}
              className="rounded-lg px-2 py-1 text-sm text-muted hover:text-ink"
            >
              {t("取消")}
            </button>
            <button
              onClick={async () => {
                try {
                  await api.deleteAdmin(admin.id);
                  onChanged();
                } catch (e) {
                  onError((e as Error).message);
                }
              }}
              className="rounded-lg px-2 py-1 text-sm font-medium text-danger hover:bg-[color-mix(in_srgb,var(--danger)_12%,transparent)]"
            >
              {t("删除")}
            </button>
          </>
        ) : (
          <IconButton
            label={t("删除管理员")}
            className="hover:text-danger"
            disabled={isMe}
            onClick={() => setConfirming(true)}
          >
            <TrashIcon size={16} />
          </IconButton>
        )}
      </div>
    </div>
  );
}

function AddAdminDialog({
  onClose,
  onCreated,
}: {
  onClose: () => void;
  onCreated: () => void;
}) {
  const { t } = useT();
  const [username, setUsername] = useState("");
  const [password, setPassword] = useState("");
  const [role, setRole] = useState<Admin["role"]>("operator");
  const [busy, setBusy] = useState(false);
  const [error, setError] = useState("");

  async function submit(e: React.FormEvent) {
    e.preventDefault();
    setBusy(true);
    setError("");
    try {
      await api.createAdmin(username.trim(), password, role);
      onCreated();
      onClose();
    } catch (err) {
      setError((err as Error).message);
    } finally {
      setBusy(false);
    }
  }

  return (
    <Modal onClose={onClose}>
      <form onSubmit={submit}>
        <h3 className="font-display text-lg font-semibold tracking-tight">{t("新增管理员")}</h3>
        <p className="mt-1 text-sm text-muted">
          {t("对方首次登录后可以在「安全」页自行改密码并加第二因素。")}
        </p>

        <Field label={t("登录名")}>
          <input
            autoFocus
            autoComplete="off"
            value={username}
            onChange={(e) => setUsername(e.target.value)}
            className={inputCls}
          />
        </Field>
        <Field label={t("初始密码（至少 8 位）")}>
          <input
            type="password"
            autoComplete="new-password"
            value={password}
            onChange={(e) => setPassword(e.target.value)}
            className={inputCls}
          />
        </Field>
        <Field label={t("角色")}>
          <div className="flex flex-col gap-1.5">
            {ROLES.map((r) => (
              <button
                key={r.role}
                type="button"
                onClick={() => setRole(r.role)}
                className={cn(
                  "rounded-lg border px-3 py-2 text-left transition-colors",
                  role === r.role
                    ? "border-signal bg-signal-soft"
                    : "border-line-strong hover:border-signal",
                )}
              >
                <div className="text-[13px]">{t(r.label)}</div>
                <div className="text-xs text-faint">{t(r.note)}</div>
              </button>
            ))}
          </div>
        </Field>

        {error && <p className="mt-3 text-sm text-danger">{error}</p>}

        <div className="mt-5 flex justify-end gap-2">
          <Button type="button" variant="ghost" onClick={onClose}>
            {t("取消")}
          </Button>
          <Button
            type="submit"
            variant="primary"
            disabled={busy || !username.trim() || password.length < 8}
          >
            {busy ? t("创建中…") : t("创建")}
          </Button>
        </div>
      </form>
    </Modal>
  );
}
