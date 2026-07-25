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

  // subscription dialog
  订阅链接: "subscription link",
  重置订阅链接: "Reset subscription link",
  "面板只保存令牌的哈希，所以这个链接": "The panel only stores a hash of the token, so this link is ",
  只显示这一次: "shown only once",
  "。 客户端会按自己的类型自动取到对应格式。":
    ". Clients receive the format they ask for automatically.",
  "需要指定格式时可加": "To force a format, append",
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
  return { t, lang, setLang };
}

// Set the document language before React mounts, for screen readers and for
// the browser's own text handling.
document.documentElement.lang = current === "zh" ? "zh-CN" : "en";
