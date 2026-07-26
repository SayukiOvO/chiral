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

  // token gate
  节点控制台: "Node console",
  "输入管理令牌以进入。": "Enter the admin token to continue.",
  管理令牌: "Admin token",
  进入: "Enter",
  "验证中…": "Checking…",
  "令牌无效，请重试。": "That token was not accepted.",

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
