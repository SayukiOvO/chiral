import { useSyncExternalStore } from "react";

/**
 * A small i18n layer, hand-rolled rather than pulled from a library: the panel
 * has two languages and a few hundred strings, and i18next would be larger
 * than everything it translates.
 *
 * Keys ARE the Chinese source text. That keeps call sites readable, means a
 * missing translation degrades to the original rather than to a bare key, and
 * makes an untranslated string obvious in the English build instead of
 * invisible.
 */
export type Lang = "zh" | "en";

const KEY = "chiral_lang";

// English translations, keyed by the Chinese source. Anything absent falls
// through to the key itself.
const EN: Record<string, string> = {
  // nav / chrome
  节点: "Nodes",
  接入配置: "Profiles",
  用户: "Users",
  变量: "Variables",
  退出: "Sign out",
  日间: "Light",
  夜间: "Dark",
  跟随系统: "System",
  主题: "Theme",
  语言: "Language",

  // common actions
  取消: "Cancel",
  删除: "Delete",
  保存: "Save",
  完成: "Done",
  复制: "Copy",
  已复制: "Copied",
  编辑: "Edit",
  重启: "Restart",
  配置: "Configure",
  应用: "Apply",
  预览: "Preview",
  "加载中…": "Loading…",
  "保存中…": "Saving…",
  "生成中…": "Generating…",
  暂无数据: "No data",

  // login
  节点控制台: "Node console",
  "请登录以继续。": "Sign in to continue.",
  // 登录名 rather than 用户名: that key already belongs to the proxy user's
  // name on the users page, and the English differs ("Name" vs "Username").
  登录名: "Username",
  密码: "Password",
  登录: "Sign in",
  "用户名或密码不正确。": "Incorrect username or password.",
  "验证中…": "Checking…",
  二次验证: "Two-factor check",
  "密码正确，还需要一个验证方式。": "Password accepted. One more factor to go.",
  通行密钥: "Passkey",
  验证器应用: "Authenticator app",
  邮箱验证码: "Emailed code",
  恢复码: "Recovery code",
  "用你的通行密钥、指纹或安全密钥完成验证。":
    "Finish with your passkey, fingerprint or security key.",
  "等待验证…": "Waiting…",
  使用通行密钥: "Use passkey",
  "通行密钥已取消。": "Passkey prompt cancelled.",
  发送验证码: "Send code",
  重新发送: "Send again",
  "验证码已发送至 {addr}": "Code sent to {addr}",
  "6 位验证码": "6-digit code",
  验证: "Verify",
  请重新登录: "Start again",
  "这次登录已超时。": "This sign-in took too long.",
  重新开始: "Start over",
  "← 换个账号": "← Different account",

  // nodes page
  "你的代理节点集群与实时状态。": "Your proxy fleet and its live state.",
  "+ 新增节点": "+ Add node",
  新增节点: "Add node",
  在线节点: "Online",
  运行内核: "Running kernels",
  总上行: "Total up",
  总下行: "Total down",
  内核: "Kernel",
  "流量 ↑↓": "Traffic ↑↓",
  "CPU / 内存": "CPU / memory",
  最近心跳: "Last heartbeat",
  在线: "Online",
  离线: "Offline",
  运行中: "Running",
  已停止: "Stopped",
  异常: "Error",
  还没有节点: "No nodes yet",
  "新增第一个节点后，它的实时状态会显示在这里。":
    "Add your first node and its live state will appear here.",
  "删除此节点？": "Delete this node?",
  重启内核: "Restart kernel",
  已发送重启: "Restart sent",
  删除节点: "Delete node",

  // node history
  资源历史: "Resource history",
  "1 小时": "1 hour",
  "6 小时": "6 hours",
  "24 小时": "24 hours",
  "7 天": "7 days",
  "30 天": "30 days",
  内存: "Memory",
  "网速 ↑ / ↓": "Throughput ↑ / ↓",
  "每分钟一个采样点，保留 7 天。": "One sample per minute, kept for 7 days.",
  暂无采样: "No samples yet",

  // users page
  "订阅者、他们的配额，以及每个接入点上的独立凭证。":
    "Subscribers, their quotas, and a separate credential per access point.",
  "+ 新增用户": "+ Add user",
  新增用户: "Add user",
  编辑用户: "Edit user",
  流量: "Traffic",
  状态: "Status",
  可用: "Active",
  已停用: "Disabled",
  已过期: "Expired",
  超出配额: "Over quota",
  "下发中…": "Syncing…",
  未授权任何接入配置: "No profiles granted",
  永不过期: "No expiry",
  不续期: "No renewal",
  不限: "Unlimited",
  还没有用户: "No users yet",
  "新增用户后，授权他们使用某个接入配置，凭证会自动下发到该配置绑定的所有节点。":
    "Add a user, grant them a profile, and their credentials reach every node bound to it.",
  "删除此用户？": "Delete this user?",
  可访问的接入配置: "Profiles they may use",
  "授权后，该用户会自动获得这个接入配置绑定的每个节点上的独立凭证。":
    "Granting a profile mints a separate credential on each of its nodes.",
  还没有接入配置: "No profiles yet",
  流量趋势: "Traffic over time",
  "按小时累计。": "Accumulated hourly.",
  "这段时间没有流量": "No traffic in this period",

  // concurrent source addresses
  //
  // "Addresses", never "devices", in both languages. Xray counts distinct
  // source addresses: a household behind one NAT is 1, and a phone moving
  // between wifi and cellular is 2. Calling them devices would promise
  // something the kernel does not measure.
  并发地址: "Addresses",
  并发地址上限: "Address limit",
  "0 表示不限。仅作展示提醒，不会自动断开连接。":
    "0 means unlimited. Shown as a hint only — nothing is disconnected.",
  来源地址: "Source addresses",
  "还没有记录到任何地址。": "No addresses recorded yet.",
  "只有超级管理员能查看具体地址。": "Only a superadmin can view the addresses themselves.",
  "每次查看都会记入审计日志。": "Every view is written to the audit log.",
  "最近 {when}": "last seen {when}",
  节点已删除: "node deleted",
  用户名: "Name",
  "流量配额 (GB)": "Quota (GB)",
  到期日: "Expires",
  自动续期: "Auto-renew",
  启用: "Enabled",
  "0 表示不限": "0 means unlimited",
  留空表示永不过期: "Leave blank for no expiry",
  "到期时顺延一个周期，并把已用流量清零":
    "On expiry, roll forward one period and reset usage",
  "改动会立即下发到节点。": "Changes reach the nodes immediately.",
  "创建后会给出订阅链接，只显示这一次。":
    "You will get the subscription link once, at creation.",
  "流量配额必须是 0 或正数（0 表示不限）":
    "Quota must be 0 or more (0 means unlimited)",
  "每 30 天": "Every 30 days",
  "每 7 天": "Every 7 days",
  "每 90 天": "Every 90 days",

  // profiles / variables / node config
  "渲染后作为一项进节点 config.json 的 inbounds。clients 留空，由 Core 按绑定用户注入。":
    "Rendered as one entry in the node's config.json inbounds. Leave clients empty — Core fills it from the bound users.",
  "clients 数组里单个用户对象的模板。M3 的在线增删用户改的就是这一条。":
    "The template for one entry of the clients array. This is exactly what online add/remove edits.",
  "每种客户端手写一份，避开订阅转换的表达力上限。私钥变量在这里不可用。":
    "One hand-written template per client, so nothing is limited by what a converter can express. Secret variables are unavailable here.",
  未填: "empty",
  "创建中…": "Creating…",
  "（需要 xray 二进制，当前不可用）": " (needs the xray binary; unavailable)",
  "{ok} 个成功，{bad} 个失败：": "{ok} succeeded, {bad} failed: ",
  "删除接入配置「{name}」？绑定关系与其变量会一并删除。":
    "Delete profile \u201c{name}\u201d? Its bindings and variables go with it.",
  "删除变量「{name}」？引用它的模板会渲染失败。":
    "Delete variable \u201c{name}\u201d? Templates referencing it will fail to render.",
  "骨架不是合法 JSON：{msg}": "The skeleton is not valid JSON: {msg}",
  属于哪个接入配置: "Which profile",
  属于哪个节点: "Which node",
  生成器: "Generator",
  静态值: "Static value",
  全局: "Global",
  "私钥变量不能用在客户端模板里": "Secret variables cannot be used in a client template",
  未定义的变量: "Undefined variable",
  "私钥变量（仅服务端）": "Secret variable (server side only)",
  已定义: "Defined",
  下发: "Apply",
  "xray -test 通过": "xray -test passed",
  "xray -test 未通过": "xray -test failed",

  // subscription dialog
  订阅链接: "subscription link",
  重置订阅链接: "Reset subscription link",
  "面板只保存令牌的哈希，所以这个链接": "The panel only stores a hash of the token, so this link is ",
  只显示这一次: "shown only once",
  "。 客户端会按自己的类型自动取到对应格式。":
    ". Clients receive the format they ask for automatically.",
  "需要指定格式时可加": "To force a format, append",
  "的订阅链接": "'s subscription link",

  // add-node dialog
  "为节点起个名字，生成一次性加入命令。":
    "Name the node to generate its one-time join command.",
  "例如 tokyo-1": "e.g. tokyo-1",
  生成加入命令: "Generate join command",
  节点已创建: "Node created",
  在目标主机保存为: "On the target host, save this as",
  "，然后运行": ", then run",
  "。加入令牌仅可使用一次。": ". The join token can only be used once.",

  // node config dialog
  "骨架是 inbounds 之外的部分；inbounds 由绑定的接入配置渲染装配。":
    "The skeleton is everything but inbounds; those are assembled from the profiles bound to this node.",
  关闭: "Close",
  "config 骨架": "Config skeleton",
  保存骨架: "Save skeleton",
  装配预览: "Assembly preview",
  "无法装配。先绑定接入配置，并确认模板里的变量都已定义。":
    "Cannot assemble. Bind a profile first, and check every variable the template uses is defined.",
  未校验: "Not validated",
  "面板未配置 xray 二进制，下发前不做预校验":
    "No xray binary on the panel, so configs are pushed without validation",
  "骨架不是合法 JSON：": "Skeleton is not valid JSON: ",
  "已下发，版本 v{n}": "Applied as v{n}",

  // profiles page
  "一套接入方式：服务端 inbound 骨架 + 每用户凭证 + 各客户端模板。":
    "One way in: a server inbound skeleton, a per-user credential, and a template per client.",
  新增: "New",
  "还没有接入配置。新建一个，再把它绑定到节点上。":
    "No profiles yet. Create one, then bind it to a node.",
  "{n} 个节点": "{n} nodes",
  "{n} 份客户端模板": "{n} client templates",
  "未填 inbound 骨架": "No inbound skeleton",
  新增接入配置: "New profile",
  名字: "Name",
  创建: "Create",
  "已下发到 {n} 个节点": "Applied to {n} nodes",
  "{ok} 个成功，{failed} 个失败：": "{ok} succeeded, {failed} failed: ",
  "载入中…": "Loading…",

  // variables page
  "模板里 {{名字}} 引用的值。私钥类分量只存不取。":
    "The values templates reference. Secret components are stored but never returned.",
  新增变量: "New variable",
  "还没有变量。生成一组 REALITY 密钥或填一个静态值，模板就能引用它。":
    "No variables yet. Generate a REALITY keypair or set a static value, and templates can use it.",
  作用域: "Scope",
  取值: "Value",
  删除变量: "Delete variable",
  "生成器会产出成组的分量（如": "A generator produces a group of components (for example",

  "绑上以后，下发即把这套 inbound 装配进该节点的 config。":
    "Once bound, applying assembles this inbound into that node's config.",

  // template editor
  "载入编辑器…": "Loading the editor…",

  // misc
  "重启失败：": "Restart failed: ",
  "重置订阅链接失败：": "Could not reset the subscription link: ",
  "修改权限失败：": "Could not change access: ",
  "{n} 个接入配置": "{n} profiles",
  删除用户: "Delete user",
  流量用量: "Quota usage",
  "还没有接入配置。": "No profiles yet.",

  // profile editor
  "← 接入配置": "← Profiles",
  删除此接入配置: "Delete this profile",
  客户端模板: "Client templates",
  已保存: "Saved",
  "服务端 inbound 骨架": "Server inbound skeleton",
  "每用户 client-entry": "Per-user client entry",
  绑定节点: "Bound nodes",
  "还没有节点。": "No nodes yet.",
  "服务端与客户端引用同一组变量的不同分量，因此不可能配错。":
    "Server and client reference different components of the same variable, so they cannot drift apart.",

  // variables page detail
  值: "Value",
  取值方式: "Source",
  "选择…": "Choose…",
  模板里: "Templates reference",
  "引用的值。私钥类分量只存不取。":
    " — these values. Secret components are stored but never returned.",
  "该生成器由面板调用 xray 二进制产出，保证格式与内核一致。":
    "The panel produces this by running the xray binary, so the format always matches the kernel.",
  "），服务端与客户端各引用一半，天然配对。":
    "); the server and client each reference one half, so they always match.",
  "例如 alice": "e.g. alice",
  "下发到 {n} 个节点": "Apply to {n} nodes",

  // security page
  安全: "Security",
  登录到: "Signed in as",
  超级管理员: "Superadmin",
  操作员: "Operator",
  只读: "Read-only",
  邮箱: "Email",
  "只有密码。加一个第二因素，密码泄露就不足以登录。":
    "Password only. Add a second factor and a leaked password is no longer enough.",
  "登录时，密码之外还需要下面任意一项。":
    "Signing in needs the password plus any one of these.",
  "还没有第二因素。": "No second factor yet.",
  "最近使用 {when}": "Last used {when}",
  从未使用: "Never used",
  移除: "Remove",
  "这是最后一个第二因素，移除后只剩密码。确定？":
    "This is the last second factor — removing it leaves only the password. Continue?",
  "移除这个第二因素？": "Remove this second factor?",
  更换邮箱: "Change email",
  "面板未配置 SMTP": "SMTP is not configured on this panel",
  "通行密钥需要面板经 https（或 localhost）访问": "Passkeys need https (or localhost)",
  "通行密钥需要面板经 https（或 localhost）访问，且 CHIRAL_PUBLIC_URL 指向它。":
    "Passkeys need the panel served over https (or localhost), with CHIRAL_PUBLIC_URL pointing at it.",
  "邮箱验证码需要在 .env 里配置 CHIRAL_SMTP_HOST 与 CHIRAL_SMTP_FROM。":
    "Emailed codes need CHIRAL_SMTP_HOST and CHIRAL_SMTP_FROM set in .env.",
  "设备丢了的时候用它登录。每个只能用一次，重新生成会作废旧的。":
    "Your way back in if you lose a device. Each works once; regenerating voids the old set.",
  "剩余 {n} 个未使用": "{n} unused",
  "还没有恢复码。": "No recovery codes yet.",
  "重新生成会作废现有的恢复码。确定？":
    "Regenerating voids your existing recovery codes. Continue?",
  重新生成: "Regenerate",
  生成: "Generate",
  "改密码会让所有已登录的会话失效，包括当前这个。":
    "Changing it signs out every session, including this one.",
  修改密码: "Change password",
  "当前是用环境变量里的管理令牌进入的，它没有对应的账号，也就没有第二因素可设。用一个管理员账号登录后再来这里。":
    "You are in with the admin token from the environment. It has no account behind it, so there is nothing here to configure. Sign in with an admin account instead.",
  添加验证器应用: "Add an authenticator app",
  "用 Authy、1Password、Google Authenticator 之类的应用扫码，再填一次它给出的验证码。":
    "Scan this with Authy, 1Password, Google Authenticator or similar, then type back the code it shows.",
  或手动输入密钥: "Or enter the secret by hand",
  "应用给出的 6 位验证码": "The app's 6-digit code",
  开启: "Turn on",
  验证邮箱: "Verify email",
  "验证后，这个地址可以作为登录时的第二因素接收验证码。":
    "Once verified, this address can receive sign-in codes as a second factor.",
  邮箱地址: "Email address",
  发送: "Send",
  "邮件里的 6 位验证码": "The 6-digit code from the email",
  "现在就存好。面板只保存它们的哈希，关掉这个窗口后再也看不到。":
    "Save these now. The panel keeps only their hashes — close this and they are gone.",
  复制全部: "Copy all",
  我存好了: "I've saved them",
  "改完需要重新登录。": "You will need to sign in again afterwards.",
  当前密码: "Current password",
  新密码: "New password",
  再输一次: "Repeat it",
  "两次输入不一致。": "Those do not match.",

  // --- portal (the subscriber's side) ---
  //
  // Written for someone who bought a subscription, not for an operator: no
  // "node", no "profile", no "sync". A line is a 线路 / line, and what gets
  // counted are addresses, never devices.
  //
  // Where a portal string needs different English from an identical console
  // string, the CHINESE key differs too — the dictionary is keyed by the
  // source text, so one key cannot carry two meanings. 可用 is the console's
  // "Active" (a user's admission state); a line that is reachable is 正常.
  "密码（至少 8 位）": "Password (8 characters or more)",
  "新密码（至少 8 位）": "New password (8 characters or more)",
  注册码: "Registration code",
  "登录中…": "Signing in…",
  "提交中…": "Submitting…",
  "设置中…": "Saving…",
  "刷新中…": "Refreshing…",
  刷新: "Refresh",
  注册: "Sign up",
  "还没有账号？": "No account yet?",
  "已经有账号？": "Already have an account?",
  "查看你的订阅、线路与用量。": "Your subscription, lines and usage.",
  "注册后请联系管理员开通线路。": "After signing up, ask the operator to enable your lines.",
  "忘记密码请联系管理员重置。": "Forgotten your password? Ask the operator to reset it.",
  "这个面板暂不开放注册。": "This panel is not accepting new accounts.",
  可以登录了: "Ready to sign in",
  "如果这个邮箱还没被注册，账号已经建好。":
    "If that address was not already registered, the account is ready.",
  去登录: "Go to sign in",
  账户: "Account",
  外观: "Appearance",
  退出登录: "Sign out",

  // claim / reset
  设置密码: "Set a password",
  "这个链接属于账号 {name}。": "This link is for the account {name}.",
  "邮箱（首次设置时填写）": "Email (only when setting up)",
  设置密码并登录: "Set password and sign in",
  链接无效: "That link does not work",
  "这个链接已失效或已被使用。请向管理员再要一个。":
    "It has expired or already been used. Ask the operator for another.",

  // home
  已用流量: "Used",
  "续期时清零": "resets on renewal",
  线路: "Lines",
  "线路 {n}": "Line {n}",
  正常: "available",
  开通中: "being set up",
  暂不可用: "unavailable",
  "你的账户还没有开通任何线路。": "No lines have been enabled on your account yet.",
  "最近 24 小时": "Last 24 hours",
  "最近 24 小时没有流量": "No traffic in the last 24 hours",

  // subscription
  我的订阅链接: "Subscription link",
  "复制到你的客户端，它会自动获取全部线路。":
    "Paste it into your client; it will pull every line automatically.",
  通用: "Generic",
  分享链接: "Share link",
  显示二维码: "Show QR code",
  收起二维码: "Hide QR code",
  "二维码包含你的订阅密钥，不要分享截图。":
    "This code contains your subscription key — do not share a screenshot.",
  "这个账户的订阅链接是旧版本创建的，面板无法显示。请联系管理员重新生成。":
    "This account's link predates the panel being able to show it. Ask the operator to regenerate it.",

  // status banners
  "账户已停用，请联系管理员。": "This account has been suspended. Please contact the operator.",
  "订阅已到期，请联系管理员续期。": "Your subscription has expired. Please contact the operator.",
  "本期流量已用完。": "You have used all of this period's traffic.",
  "账户已创建，但还没有开通线路。请联系管理员。":
    "Your account exists but no lines have been enabled yet. Please contact the operator.",

  // addresses
  "最近连接过的来源地址。": "Source addresses seen recently.",
  "最近连接过的来源地址，上限 {n} 个。":
    "Source addresses seen recently. The limit is {n}.",
  "最近没有记录到连接。": "No connections recorded recently.",
};

const DICT: Record<Lang, Record<string, string>> = { zh: {}, en: EN };

function detect(): Lang {
  const stored = localStorage.getItem(KEY);
  if (stored === "zh" || stored === "en") return stored;
  // The panel is written in Chinese; anyone else gets English.
  return navigator.language.toLowerCase().startsWith("zh") ? "zh" : "en";
}

let current: Lang = detect();
const listeners = new Set<() => void>();

export function setLang(lang: Lang) {
  current = lang;
  localStorage.setItem(KEY, lang);
  document.documentElement.lang = lang === "zh" ? "zh-CN" : "en";
  listeners.forEach((l) => l());
}

export function getLang(): Lang {
  return current;
}

function subscribe(l: () => void) {
  listeners.add(l);
  return () => listeners.delete(l);
}

/**
 * useT returns the translator and re-renders its component when the language
 * changes. t(key) falls back to the key, which is the Chinese source — so a
 * missing translation reads as untranslated rather than as a broken lookup.
 */
export function useT() {
  const lang = useSyncExternalStore(subscribe, getLang, getLang);
  const t = (key: string) => DICT[lang][key] ?? key;
  // tf interpolates {name} placeholders, so a sentence with a number in it
  // stays one translatable unit instead of being glued together from
  // fragments — word order differs between languages.
  const tf = (key: string, params: Record<string, string | number>) =>
    Object.entries(params).reduce(
      (out, [k, v]) => out.replaceAll(`{${k}}`, String(v)),
      t(key),
    );
  return { t, tf, lang, setLang };
}

// Set the document language before React mounts, for screen readers and for
// the browser's own text handling.
document.documentElement.lang = current === "zh" ? "zh-CN" : "en";
