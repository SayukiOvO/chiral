import { useCallback, useEffect, useState } from "react";
import { api, type Profile, type Ruleset, type SubscriberGroup, type User } from "../api";
import { expiryLabel, periodLabel } from "../format";
import { cn } from "../lib/cn";
import { QuotaBar } from "../components/QuotaBar";
import { ExitUsage } from "../components/ExitUsage";
import { UserTraffic } from "../components/UserTraffic";
import { UserDevices } from "../components/UserDevices";
import { PortalLinkDialog } from "../components/PortalLinkDialog";
import { KeyIcon } from "../components/icons";
import { UserDialog } from "../components/UserDialog";
import { SubscriptionDialog } from "../components/SubscriptionDialog";
import { Button, IconButton } from "../components/ui";
import { NodeAccess } from "../components/NodeAccess";
import { CheckIcon, LinkIcon, PencilIcon, PlusIcon, TrashIcon } from "../components/icons";
import { useT } from "../lib/i18n";

export function UsersPage() {
  const { t } = useT();
  const [users, setUsers] = useState<User[]>([]);
  const [profiles, setProfiles] = useState<Profile[]>([]);
  const [rulesets, setRulesets] = useState<Ruleset[]>([]);
  const [groups, setGroups] = useState<SubscriberGroup[]>([]);
  // Bumped on every reload, so a card's node list re-reads entitlement after a
  // grant rather than keeping what it was mounted with.
  const [version, setVersion] = useState(0);
  const [loaded, setLoaded] = useState(false);
  const [error, setError] = useState("");
  const [editing, setEditing] = useState<User | null>(null);
  const [creating, setCreating] = useState(false);
  const [subscription, setSubscription] = useState<{
    url: string;
    name: string;
    id: string;
    fresh: boolean;
  } | null>(null);

  const refresh = useCallback(async () => {
    try {
      const [u, p, rs, g] = await Promise.all([
        api.listUsers(),
        api.listProfiles(),
        api.listRulesets(),
        api.listGroups(),
      ]);
      setUsers(u.users);
      setProfiles(p.profiles);
      setRulesets(rs.rulesets);
      setGroups(g.groups ?? []);
      setVersion((v) => v + 1);
      setError("");
    } catch (e) {
      setError((e as Error).message);
    } finally {
      setLoaded(true);
    }
  }, []);

  useEffect(() => {
    refresh();
    // Sync settles asynchronously (a push has to reach the node), so poll to
    // let "syncing" resolve on its own rather than leaving a stale badge.
    const t = window.setInterval(refresh, 5000);
    return () => window.clearInterval(t);
  }, [refresh]);

  return (
    <div>
      <div className="mb-6 flex items-end justify-between gap-4">
        <div className="animate-rise">
          <h1 className="font-display text-[26px] font-semibold tracking-tight">{t("用户")}</h1>
          <p className="mt-1 text-sm text-muted">
            {t("订阅者、配额，以及每个接入点上的独立凭证。")}
          </p>
        </div>
        <Button variant="primary" onClick={() => setCreating(true)}>
          <PlusIcon size={16} />
          {t("新增用户")}
        </Button>
      </div>

      {error && (
        <div className="mb-6 rounded-xl border border-[color-mix(in_srgb,var(--danger)_35%,transparent)] bg-[color-mix(in_srgb,var(--danger)_10%,transparent)] px-4 py-3 text-sm text-danger">
          {error}
        </div>
      )}

      {loaded &&
        (users.length === 0 ? (
          <EmptyState />
        ) : (
          <div className="flex flex-col gap-2.5">
            {users.map((u) => (
              <UserCard
                key={u.id}
                user={u}
                profiles={profiles}
                rulesets={rulesets}
                groups={groups}
                version={version}
                onChanged={refresh}
                onEdit={() => setEditing(u)}
                onSubscription={(url, fresh) =>
                  setSubscription({ url, name: u.name, id: u.id, fresh })
                }
              />
            ))}
          </div>
        ))}

      {(creating || editing) && (
        <UserDialog
          user={editing ?? undefined}
          onClose={() => {
            setCreating(false);
            setEditing(null);
          }}
          onSaved={(created) => {
            setCreating(false);
            setEditing(null);
            refresh();
            if (created) {
              setSubscription({
                url: created.subscription_url,
                name: created.user.name,
                id: created.user.id,
                fresh: true,
              });
            }
          }}
        />
      )}
      {subscription && (
        <SubscriptionDialog
          url={subscription.url}
          userName={subscription.name}
          fresh={subscription.fresh}
          onReset={async () => {
            const { subscription_url } = await api.resetSubToken(subscription.id);
            setSubscription({ ...subscription, url: subscription_url, fresh: true });
            refresh();
          }}
          onClose={() => setSubscription(null)}
        />
      )}
    </div>
  );
}

function UserCard({
  user,
  profiles,
  rulesets,
  groups,
  version,
  onChanged,
  onEdit,
  onSubscription,
}: {
  user: User;
  profiles: Profile[];
  rulesets: Ruleset[];
  groups: SubscriberGroup[];
  version: number;
  onChanged: () => void;
  onEdit: () => void;
  onSubscription: (url: string, fresh: boolean) => void;
}) {
  const { t, tf } = useT();
  const [confirming, setConfirming] = useState(false);
  const [busy, setBusy] = useState(false);
  const [expanded, setExpanded] = useState(false);
  const [portalLink, setPortalLink] = useState(false);

  async function remove() {
    setBusy(true);
    try {
      await api.deleteUser(user.id);
      onChanged();
    } finally {
      setBusy(false);
      setConfirming(false);
    }
  }

  // Shows the link the subscriber already has. Replacing it lives inside the
  // dialog, behind its own confirmation — looking must not cost anything.
  async function showLink() {
    setBusy(true);
    try {
      const r = await api.subToken(user.id);
      if (!r.recoverable || !r.subscription_url) {
        // Created before the token was stored recoverably, or sealed under a
        // key this panel no longer holds. A reset is the only way forward, and
        // it is the operator's call rather than a silent consequence.
        alert(t("此用户的订阅链接无法恢复（其创建时间早于可恢复存储，或密钥已更换），只能重置为新链接。"));
        return;
      }
      onSubscription(r.subscription_url, false);
    } catch (e) {
      alert(t("获取订阅链接失败：") + (e as Error).message);
    } finally {
      setBusy(false);
    }
  }

  // Three choices for a member of a group — inherit, none, or a rule set —
  // because an empty id now means "inherit" and could not also mean "none".
  async function setRuleset(id: string, none = false) {
    setBusy(true);
    try {
      await api.setUserRuleset(user.id, id, none);
      onChanged();
    } catch (e) {
      alert(t("修改分流规则失败：") + (e as Error).message);
    } finally {
      setBusy(false);
    }
  }

  // Joining discards this subscriber's own grants and denials — the group
  // decides from then on — so the operator is told before it happens rather
  // than after.
  async function setGroup(groupID: string) {
    if (groupID && groupID !== (user.group_id ?? "")) {
      const g = groups.find((x) => x.id === groupID);
      if (
        !window.confirm(
          tf("将「{user}」加入用户组「{group}」后，其现有的接入配置授权与节点设置将被清除，改由该组决定。是否继续？", {
            user: user.name,
            group: g?.name ?? groupID,
          }),
        )
      )
        return;
    }
    setBusy(true);
    try {
      await api.setUserGroup(user.id, groupID);
      onChanged();
    } catch (e) {
      alert(t("修改用户组失败：") + (e as Error).message);
    } finally {
      setBusy(false);
    }
  }

  // Three states rather than a toggle, because a member of a group can hold a
  // configuration without a row of their own: clicking such a chip has to
  // withhold it explicitly, and clicking again hands the decision back.
  async function cycleProfile(profileId: string) {
    const own = user.own_profile_ids?.includes(profileId);
    const denied = user.denied_profile_ids?.includes(profileId);
    const held = user.profile_ids.includes(profileId);
    const next: "grant" | "deny" | "inherit" = denied
      ? "inherit"
      : own
        ? user.group_id
          ? "inherit"
          : "deny"
        : held
          ? "deny"
          : "grant";
    setBusy(true);
    try {
      await api.setUserProfileAccess(user.id, profileId, next);
      onChanged();
    } catch (e) {
      alert(t("修改权限失败：") + (e as Error).message);
    } finally {
      setBusy(false);
    }
  }

  return (
    <article className="group rounded-2xl border border-line bg-surface px-5 py-4 shadow-[var(--shadow-card)] transition-all duration-150 hover:border-line-strong hover:shadow-[var(--shadow-lift)]">
      <div className="flex items-start justify-between gap-4">
        <div className="flex min-w-0 items-center gap-3">
          <AccessDot user={user} />
          <div className="min-w-0">
            <button
              onClick={() => setExpanded((v) => !v)}
              className="font-display text-[15px] font-semibold tracking-tight hover:text-signal"
            >
              {user.name}
            </button>
            <div className="text-xs text-muted">
              {user.profile_ids.length === 0
                ? t("未授权任何接入配置")
                : tf("{n} 个接入配置", { n: user.profile_ids.length })}
              <span className="mx-1.5 text-faint">·</span>
              {expiryLabel(user.expires_at)}
              {user.renew_period > 0 && (
                <>
                  <span className="mx-1.5 text-faint">·</span>
                  {periodLabel(user.renew_period)}
                </>
              )}
            </div>
          </div>
        </div>

        {confirming ? (
          <div className="flex items-center gap-2 text-sm">
            <span className="hidden text-muted sm:inline">{t("删除此用户？")}</span>
            <button
              onClick={() => setConfirming(false)}
              className="rounded-lg px-2.5 py-1 text-muted hover:text-ink"
            >
              {t("取消")}
            </button>
            <button
              onClick={remove}
              disabled={busy}
              className="rounded-lg px-2.5 py-1 font-medium text-danger hover:bg-[color-mix(in_srgb,var(--danger)_12%,transparent)] disabled:opacity-50"
            >
              {t("删除")}
            </button>
          </div>
        ) : (
          <div className="flex items-center gap-1 opacity-70 transition-opacity group-hover:opacity-100">
            <IconButton label={t("订阅链接")} onClick={showLink} disabled={busy}>
              <LinkIcon size={16} />
            </IconButton>
            {/* The only way to hand an existing subscriber a portal account,
                and — without SMTP — the only password reset there is. */}
            <IconButton label={t("门户认领链接")} onClick={() => setPortalLink(true)}>
              <KeyIcon size={16} />
            </IconButton>
            <IconButton label={t("编辑")} onClick={onEdit}>
              <PencilIcon size={16} />
            </IconButton>
            <IconButton
              label={t("删除用户")}
              onClick={() => setConfirming(true)}
              className="hover:text-danger"
            >
              <TrashIcon size={16} />
            </IconButton>
          </div>
        )}
      </div>

      <div className="mt-4 flex flex-wrap items-end gap-x-8 gap-y-3 pl-[22px]">
        <Field label={t("流量")}>
          <QuotaBar used={user.used_bytes} quota={user.quota_bytes} />
        </Field>
        <Field label={t("状态")}>
          <AccessLabel user={user} />
        </Field>
        {/* Only when recording is on. An absent count and a count of zero mean
            different things, and the API distinguishes them with an omitted
            field rather than a 0. */}
        {user.online_devices !== undefined && (
          <Field label={t("并发地址")}>
            <ConcurrentAddresses count={user.online_devices} limit={user.device_limit} />
          </Field>
        )}
      </div>

      {portalLink && (
        <PortalLinkDialog
          userId={user.id}
          userName={user.name}
          onClose={() => setPortalLink(false)}
        />
      )}

      {expanded && (
        <div className="mt-4 border-t border-line pt-4 pl-[22px]">
          {groups.length > 0 && (
            <div className="mb-5">
              <div className="mb-2 text-[11px] font-medium uppercase tracking-[0.07em] text-faint">
                {t("用户组")}
              </div>
              <div className="flex flex-wrap gap-1.5">
                {[{ id: "", name: t("不属于任何组") }, ...groups].map((g) => {
                  const on = (user.group_id ?? "") === g.id;
                  return (
                    <button
                      key={g.id || "none"}
                      onClick={() => setGroup(g.id)}
                      disabled={busy}
                      className={cn(
                        "inline-flex items-center gap-1.5 rounded-lg border px-2.5 py-1 text-xs transition-colors disabled:opacity-50",
                        on
                          ? "border-[color-mix(in_srgb,var(--online)_45%,transparent)] text-online"
                          : "border-line-strong text-muted hover:border-signal hover:text-ink",
                      )}
                    >
                      {on && <CheckIcon size={12} />}
                      {g.name}
                    </button>
                  );
                })}
              </div>
              <p className="mt-2 text-xs text-faint">
                {t("组决定成员的接入配置、可用节点与分流规则；以下各项的调整将作为该成员的个人设置，优先于组。移出组时保留组当前授予的权限。")}
              </p>
            </div>
          )}

          <div className="mb-2 text-[11px] font-medium uppercase tracking-[0.07em] text-faint">
            {t("可访问的接入配置")}
          </div>
          {profiles.length === 0 ? (
            <p className="text-sm text-muted">{t("还没有接入配置。")}</p>
          ) : (
            <div className="flex flex-wrap gap-1.5">
              {profiles.map((p) => {
                const held = user.profile_ids.includes(p.id);
                const inherited = held && !user.own_profile_ids?.includes(p.id);
                const excluded = user.denied_profile_ids?.includes(p.id);
                return (
                  <button
                    key={p.id}
                    onClick={() => cycleProfile(p.id)}
                    disabled={busy}
                    title={
                      excluded
                        ? t("已对此用户单独排除；再次点击恢复由用户组决定")
                        : inherited
                          ? t("由用户组授予；点击可对此用户单独排除")
                          : undefined
                    }
                    className={cn(
                      "inline-flex items-center gap-1.5 rounded-lg border px-2.5 py-1 text-xs transition-colors disabled:opacity-50",
                      held
                        ? "border-[color-mix(in_srgb,var(--online)_45%,transparent)] text-online"
                        : excluded
                          ? "border-dashed border-line-strong text-muted line-through"
                          : "border-line-strong text-muted hover:border-signal hover:text-ink",
                    )}
                  >
                    {held && <CheckIcon size={12} />}
                    {p.name}
                    {inherited && (
                      <span className="text-[10px] text-faint" title={t("该状态继承自用户组")}>
                        {t("组")}
                      </span>
                    )}
                  </button>
                );
              })}
            </div>
          )}
          <p className="mt-2 text-xs text-faint">
            {t("授权后，在该配置绑定的每个节点上生成独立凭证。")}
          </p>

          {/* Per node, under the profiles that grant them: the profile says
              which way in, this says which boxes. */}
          <div className="mt-5">
            <div className="mb-2 text-[11px] font-medium uppercase tracking-[0.07em] text-faint">
              {t("可用节点")}
            </div>
            {expanded && <NodeAccess subject="user" id={user.id} version={version} />}
          </div>

          {/* Per subscriber, not per profile: which traffic goes through the
              proxy is a property of the person, and one subscriber may want
              everything proxied while another wants China direct. */}
          <div className="mt-5">
            <div className="mb-2 text-[11px] font-medium uppercase tracking-[0.07em] text-faint">
              {t("分流规则")}
            </div>
            {rulesets.length === 0 ? (
              <p className="text-sm text-muted">
                {t("暂无规则集，订阅不包含分流规则。可在「分流规则」页新增。")}
              </p>
            ) : (
              <div className="flex flex-wrap gap-1.5">
                {[
                  ...(user.group_id ? [{ id: "inherit", name: t("继承自用户组") }] : []),
                  { id: "", name: t("不分流") },
                  ...rulesets,
                ].map((rs) => {
                  const chosen = user.ruleset_id
                    ? user.ruleset_id
                    : user.ruleset_none || !user.group_id
                      ? ""
                      : "inherit";
                  const on = chosen === rs.id;
                  return (
                    <button
                      key={rs.id || "none"}
                      onClick={() =>
                        rs.id === "inherit" ? setRuleset("") : setRuleset(rs.id, rs.id === "")
                      }
                      disabled={busy}
                      className={cn(
                        "inline-flex items-center gap-1.5 rounded-lg border px-2.5 py-1 text-xs transition-colors disabled:opacity-50",
                        on
                          ? "border-[color-mix(in_srgb,var(--online)_45%,transparent)] text-online"
                          : "border-line-strong text-muted hover:border-signal hover:text-ink",
                      )}
                    >
                      {on && <CheckIcon size={12} />}
                      {rs.name}
                    </button>
                  );
                })}
              </div>
            )}
            <p className="mt-2 text-xs text-faint">
              {t("仅影响 Clash 类客户端；订阅者下次刷新订阅时生效。")}
            </p>
          </div>

          {/* Above the timeline, because "which line did it go out of" is the
              question an operator has when the quota looks wrong, and the
              timeline answers "when" instead. */}
          <div className="mt-5">
            <div className="mb-2 text-[11px] font-medium uppercase tracking-[0.07em] text-faint">
              {t("按出口用量")}
            </div>
            <ExitUsage userId={user.id} />
          </div>

          <div className="mt-5">
            <div className="mb-2 text-[11px] font-medium uppercase tracking-[0.07em] text-faint">
              {t("流量趋势")}
            </div>
            <UserTraffic userId={user.id} />
          </div>

          {user.online_devices !== undefined && (
            <div className="mt-5">
              <div className="mb-2 text-[11px] font-medium uppercase tracking-[0.07em] text-faint">
                {t("来源地址")}
              </div>
              <UserDevices userId={user.id} />
            </div>
          )}
        </div>
      )}
    </article>
  );
}

/**
 * Concurrent source addresses against the operator's expectation.
 *
 * Over the limit is amber, not red: nothing is broken and nothing was blocked.
 * The number is a hint that an account may be shared more widely than intended,
 * and an address is a poor proxy for a device — one household behind NAT counts
 * as one, one phone switching between wifi and cellular counts as two.
 */
function ConcurrentAddresses({ count, limit }: { count: number; limit: number }) {
  const over = limit > 0 && count > limit;
  return (
    <span className="font-mono text-sm tnum">
      <span className={over ? "text-warn" : count > 0 ? "text-online" : "text-faint"}>{count}</span>
      {limit > 0 && <span className="text-faint"> / {limit}</span>}
    </span>
  );
}

function Field({ label, children }: { label: string; children: React.ReactNode }) {
  return (
    <div className="min-w-0">
      <div className="mb-1 text-[11px] font-medium uppercase tracking-[0.07em] text-faint">
        {label}
      </div>
      <div className="flex h-[34px] items-center">{children}</div>
    </div>
  );
}

/** Green when the user is both entitled and installed on the nodes. */
function AccessDot({ user }: { user: User }) {
  const live = user.allowed && user.active;
  return (
    <span
      className={cn("inline-flex h-2.5 w-2.5 shrink-0 rounded-full", live && "animate-breathe")}
      style={{ background: live ? "var(--online)" : "var(--faint)" }}
    />
  );
}

// `allowed` is what should be true and `active` is what the nodes were last
// told; showing the difference makes a stuck sync visible rather than
// mysterious.
function AccessLabel({ user }: { user: User }) {
  const { t } = useT();
  const reason = !user.enabled
    ? t("已停用")
    : user.expires_at && user.expires_at * 1000 < Date.now()
      ? t("已过期")
      : user.quota_bytes && user.used_bytes >= user.quota_bytes
        ? t("超出配额")
        : "";
  if (reason) {
    return (
      <span className="text-sm text-muted">
        {reason}
        {user.active && <span className="ml-1.5 text-xs text-warn">{t("下发中…")}</span>}
      </span>
    );
  }
  return (
    <span className="text-sm">
      <span className="text-online">{t("可用")}</span>
      {!user.active && <span className="ml-1.5 text-xs text-warn">{t("下发中…")}</span>}
    </span>
  );
}

function EmptyState() {
  const { t } = useT();
  return (
    <div className="rounded-2xl border border-dashed border-line-strong bg-surface px-8 py-16 text-center">
      <p className="font-medium text-ink">{t("还没有用户")}</p>
      <p className="mt-1.5 text-sm text-muted">
        {t("新增用户并授权接入配置后，凭证自动下发至该配置绑定的所有节点。")}
      </p>
    </div>
  );
}
