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
  "修改值": "Edit value",
  "修改「{name}」": "Edit “{name}”",
  "修改后需重新下发受影响的节点。": "Re-apply the affected nodes afterwards.",
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
  "需要第二因素验证。": "A second factor is required.",
  通行密钥: "Passkey",
  验证器应用: "Authenticator app",
  邮箱验证码: "Emailed code",
  恢复码: "Recovery code",
  "使用通行密钥、指纹或安全密钥完成验证。":
    "Verify with your passkey, fingerprint or security key.",
  "等待验证…": "Waiting…",
  使用通行密钥: "Use passkey",
  "通行密钥验证已取消。": "Passkey verification cancelled.",
  发送验证码: "Send code",
  重新发送: "Send again",
  "验证码已发送至 {addr}": "Code sent to {addr}",
  "6 位验证码": "6-digit code",
  验证: "Verify",
  请重新登录: "Start again",
  "本次登录已超时。": "This sign-in timed out.",
  重新开始: "Start over",
  "← 换个账号": "← Different account",

  // nodes page
  "代理节点集群与实时状态。": "The proxy fleet and its live state.",
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
  "新增节点后，此处显示其实时状态。":
    "Add a node and its live state appears here.",
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
  "订阅者、配额，以及每个接入点上的独立凭证。":
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
  "新增用户并授权接入配置后，凭证自动下发至该配置绑定的所有节点。":
    "Add a user and grant a profile; credentials reach every node bound to it.",
  "删除此用户？": "Delete this user?",
  可访问的接入配置: "Profiles they may use",
  "授权后，在该配置绑定的每个节点上生成独立凭证。":
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
  "0 表示不限。仅作提示，不会自动断开连接。":
    "0 means unlimited. A hint only — nothing is disconnected.",
  来源地址: "Source addresses",
  "暂无地址记录。": "No addresses recorded.",
  "仅超级管理员可查看具体地址。": "Only a superadmin can view the addresses.",
  "每次查看均记入审计日志。": "Every view is written to the audit log.",
  "最近 {when}": "last seen {when}",
  节点已删除: "node deleted",
  用户名: "Name",
  "流量配额 (GB)": "Quota (GB)",
  到期日: "Expires",
  自动续期: "Auto-renew",
  启用: "Enabled",
  "0 表示不限": "0 means unlimited",
  留空表示永不过期: "Leave blank for no expiry",
  "到期顺延一个周期，并清零已用流量":
    "On expiry, roll forward one period and reset usage",
  "改动立即下发至节点。": "Changes reach the nodes immediately.",
  "订阅链接在创建后仅显示一次。":
    "The subscription link is shown once, at creation.",
  "流量配额须为 0 或正数（0 表示不限）":
    "Quota must be 0 or more (0 means unlimited)",
  "每 30 天": "Every 30 days",
  "每 7 天": "Every 7 days",
  "每 90 天": "Every 90 days",

  // profiles / variables / node config
  "渲染后作为一项写入节点 config.json 的 inbounds。clients 留空，由 Core 按绑定用户注入。":
    "Rendered as one entry of the node's config.json inbounds. Leave clients empty; Core fills it from the bound users.",
  "clients 数组中单个用户对象的模板。在线增删用户即修改此项。":
    "The template for one entry of the clients array. Online add/remove edits exactly this.",
  "每种客户端各写一份，不经订阅转换。此处不可引用私钥变量。":
    "One hand-written template per client, with no converter in between. Secret variables are unavailable here.",
  未填: "empty",
  "创建中…": "Creating…",
  "（需要 xray 二进制，当前不可用）": " (needs the xray binary; unavailable)",
  "{ok} 个成功，{bad} 个失败：": "{ok} succeeded, {bad} failed: ",
  "删除接入配置「{name}」？其绑定关系与变量将一并删除。":
    "Delete profile “{name}”? Its bindings and variables go with it.",
  "删除变量「{name}」？引用它的模板将渲染失败。":
    "Delete variable “{name}”? Templates referencing it will fail to render.",
  "骨架不是合法 JSON：{msg}": "The skeleton is not valid JSON: {msg}",
  属于哪个接入配置: "Which profile",
  属于哪个节点: "Which node",
  生成器: "Generator",
  静态值: "Static value",
  全局: "Global",
  "私钥变量不能用在客户端模板里": "Secret variables cannot be used in a client template",
  节点设置: "Node settings",
  "内部名用于运维，对客名称展示给订阅者，连接地址写进订阅。": "The internal name is for operations, the customer-facing one is shown to subscribers, and the address goes into subscriptions.",
  客户端连接地址: "Address clients dial",
  "订阅与模板中的 {{node.address}} 用这个值。": "Subscriptions and templates resolve {{node.address}} to this.",
  "留空则用探测到的 {ip}，它取自 Agent 连接的对端地址；中间有 NAT 时并不可靠。": "Left empty, the detected {ip} is used — the peer address of the agent's connection, which is unreliable when a NAT sits in between.",
  "（尚未探测到）": "(not detected yet)",
  全部走代理: "Everything via proxy",
  "决定哪些网站走代理、哪些直连。仅对 Clash 类客户端生效，改动后请在客户端更新订阅。":
    "Decides which sites go through the proxy and which go direct. Clash-family clients only; update your subscription in the client after changing it.",
  // navigation
  菜单: "Menu",
  收起侧栏: "Collapse sidebar",
  展开侧栏: "Expand sidebar",
  机队: "Fleet",
  订阅者: "Subscribers",
  运维: "Operations",
  // per-user node access
  可用节点: "Nodes they may use",
  自有节点: "Own nodes",
  该用户的接入配置未覆盖此节点: "No profile this user holds reaches this node",
  "链经「{node}」，而此用户拿不到那个节点":
    "Chained through “{node}”, which this user does not get",
  "取消后该节点不再出现在此用户的订阅里。凭证仍在节点上，订阅者下次刷新订阅时生效。":
    "Unticked, the node stops appearing in this subscriber's subscription. The credential stays on the node; it applies on their next refresh.",
  // panel settings
  设置: "Settings",
  "面板产出物的一些选项。": "Choices about what the panel produces.",
  订阅: "Subscription",
  订阅名称: "Subscription name",
  客户端里显示为: "Shown in the client as",
  "订阅者在客户端里看到的配置名。留空则为 chiral。":
    "The profile name subscribers see in their client. Defaults to chiral.",
  已保存: "Saved",
  "改动对之后每次拉取订阅生效；已导入的客户端需要重新导入才会改名。":
    "Applies to every subscription fetched from now on; a client that already imported one keeps the old name until it re-imports.",
  // external nodes
  外部节点: "External nodes",
  "别人提供的订阅或单条链接。可为每个节点指定一台本机队节点作为前置，链式出站。":
    "Subscriptions and links from other providers. Each node can be dialled through one of your own, as a chain.",
  "还没有外部节点。添加后它们会出现在所有订阅者的 Clash 订阅里。":
    "No external nodes yet. Once added they appear in every subscriber's Clash subscription.",
  新增外部节点: "Add external nodes",
  "外部节点由对方运营：所有订阅者共用同一份凭证，没有按用户隔离、没有流量统计，停用某个用户也不会让他连不上。":
    "External nodes are run by someone else: every subscriber shares one credential, there is no per-user isolation, no traffic accounting, and disabling a user does not stop them using it.",
  直接粘贴: "Paste directly",
  "例如 机场 A": "e.g. Provider A",
  订阅地址: "Subscription address",
  "Clash YAML 或分享链接列表均可，自动识别。每天自动更新一次。":
    "Clash YAML or a list of share links, detected automatically. Refreshed once a day.",
  内容: "Content",
  "粘贴 Clash 配置片段或若干条分享链接。不会自动更新。":
    "Paste a Clash fragment or some share links. Not refreshed automatically.",
  "解析中…": "Parsing…",
  添加: "Add",
  展开节点: "Show nodes",
  收起: "Collapse",
  直连: "Direct",
  经由: "via",
  "（手动粘贴，不自动更新）": "(pasted; not refreshed)",
  "这个来源里没有解析出节点。": "No nodes were parsed from this source.",
  "删除「{name}」？其节点将从所有订阅中移除。":
    "Delete “{name}”? Its nodes leave every subscription.",
  // routing rules
  分流规则: "Routing rules",
  "决定 Clash 类客户端把哪些流量走代理、哪些直连或拦截。在用户页指派给订阅者。":
    "Decides what clash-family clients send through the proxy, direct, or reject. Assigned to subscribers on the users page.",
  "还没有规则集。新增后在用户页指派，订阅即带上分流规则。":
    "No rule sets yet. Add one and assign it on the users page; subscriptions then carry routing rules.",
  新增分流规则: "Add routing rules",
  "内置的是 ACL4SSR 各档预设；自定义可指向任意 subconverter 格式的 .ini。":
    "The built-ins are ACL4SSR's presets; a custom one can point at any subconverter-format .ini.",
  内置预设: "Built-in preset",
  "自定义 ini": "Custom .ini",
  内置: "Built-in",
  自定义: "Custom",
  搜索: "Search",
  按名称或键筛选: "Filter by name or key",
  已添加: "already added",
  "名字（可留空）": "Name (optional)",
  留空则用预设名: "Defaults to the preset's name",
  "ini 地址": "ini URL",
  "例如 自用规则": "e.g. My rules",
  "subconverter 远程配置格式，需含 custom_proxy_group 与 ruleset 指令。":
    "subconverter remote-config format, with custom_proxy_group and ruleset directives.",
  "获取中…": "Fetching…",
  添加并获取: "Add and fetch",
  "更新中…": "Updating…",
  更新: "Update",
  "{n} 个策略组": "{n} policy groups",
  "{n} 条规则": "{n} rules",
  "{n} 个规则列表": "{n} rule lists",
  "{g} 组 · {l} 列表": "{g} groups · {l} lists",
  "更新于 {when}": "updated {when}",
  尚未获取: "never fetched",
  "尚未获取到内容，订阅暂不会带上分流规则":
    "Nothing fetched yet; subscriptions will not carry routing rules",
  "删除「{name}」？使用它的订阅者将回到无分流规则。":
    "Delete “{name}”? Subscribers using it fall back to no routing rules.",
  不分流: "No rules",
  "还没有规则集，订阅不带分流规则。可在「分流规则」页添加。":
    "No rule sets yet, so subscriptions carry no routing rules. Add one on the routing rules page.",
  "仅影响 Clash 类客户端；订阅者下次刷新订阅时生效。":
    "Affects clash-family clients only; applies on the subscriber's next refresh.",
  "修改分流规则失败：": "Could not change the routing rules: ",
  未定义的变量: "Undefined variable",
  这些节点上没有定义: "Not defined on these nodes",
  "私钥变量（仅服务端）": "Secret variable (server side only)",
  已定义: "Defined",
  下发: "Apply",
  "xray -test 通过": "xray -test passed",
  "xray -test 未通过": "xray -test failed",
  "磁盘上为 {v}，运行中的进程仍是旧版本，重启内核后生效":
    "{v} is on disk; the running process is still the older build. Takes effect after a kernel restart.",

  // kernel upgrades
  "先升级一台并确认，再放行至全部节点。新内核无法启动时自动回滚，节点继续以旧版本服务。": "Upgrade one node and confirm it, then release to the rest. A kernel that will not start is rolled back automatically and the node keeps serving on the old one.",
  上游最新版本: "Latest upstream release",
  预发布: "prerelease",
  面板已具备校验能力: "the panel can validate for it",
  "无法获取上游版本": "could not reach upstream",
  "查询中…": "checking…",
  "选择金丝雀节点": "Pick a canary node",
  "升级此节点": "Upgrade this node",
  各节点内核: "Kernel per node",
  磁盘上: "On disk",
  架构未知: "architecture unknown",
  // 失败 rather than reusing 异常: that key is the kernel's ERROR state on the
  // nodes page, and the English differs ("Error" vs "Failed").
  失败: "Failed",
  最近一次升级: "Last upgrade",
  金丝雀: "canary",
  放行到全部节点: "Release to all nodes",
  "重试": "Retry",
  "结束升级": "End upgrade",
  金丝雀升级中: "Canary upgrading",
  等待放行: "Waiting for release",
  正在放行: "Releasing",
  已完成: "Complete",
  已阻塞: "Blocked",
  已下发指令: "Instruction sent",
  下载中: "Downloading",
  校验中: "Verifying",
  "已就位（未启用）": "Staged (not activated)",
  切换中: "Switching over",
  "已验证可上网": "Verified: traffic flows",
  "运行中，未验证": "Running, unverified",
  已回滚: "Rolled back",
  // subscription dialog
  订阅链接: "subscription link",
  重置订阅链接: "Reset subscription link",
  "面板仅保存令牌哈希，此链接": "The panel stores only a hash of the token, so this link is ",
  只显示这一次: "shown only once",
  "。客户端将自动获取对应格式。":
    ". Clients receive the format they ask for.",
  "需要指定格式时可加": "To force a format, append",
  "的订阅链接": "'s subscription link",

  // add-node dialog
  "填写节点名称，生成一次性加入命令。":
    "Name the node to generate its one-time join command.",
  "例如 tokyo-1": "e.g. tokyo-1",
  生成加入命令: "Generate join command",
  节点已创建: "Node created",
  在目标主机保存为: "On the target host, save this as",
  "，然后运行": ", then run",
  "在目标主机上运行这条命令。加入令牌仅可使用一次。": "Run this on the node. The join token can be used once.",
  "改用 Docker": "Use Docker instead",
  "保存为 docker-compose.yml 后运行 docker compose up -d。": "Save as docker-compose.yml, then run docker compose up -d.",
  "。加入令牌仅可使用一次。": ". The join token can be used once.",

  // node config dialog
  "骨架为 inbounds 之外的部分。inbounds 由绑定的接入配置渲染装配。":
    "The skeleton is everything except inbounds. Those are assembled from the profiles bound to this node.",
  关闭: "Close",
  "config 骨架": "Config skeleton",
  保存骨架: "Save skeleton",
  装配预览: "Assembly preview",
  "无法装配。请先绑定接入配置，并确认模板引用的变量均已定义。":
    "Cannot assemble. Bind a profile, and check every variable the template references is defined.",
  未校验: "Not validated",
  "面板未配置 xray 二进制，下发前不做校验":
    "No xray binary on the panel; configs are pushed without validation",
  "骨架不是合法 JSON：": "Skeleton is not valid JSON: ",
  "已下发，版本 v{n}": "Applied as v{n}",

  // profiles page
  "一套接入方式：服务端 inbound 骨架、每用户凭证与各客户端模板。":
    "One way in: a server inbound skeleton, a per-user credential, and a template per client.",
  新增: "New",
  "暂无接入配置。新建后绑定至节点。":
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
  "模板通过 {{名字}} 引用的值。私钥类分量只存不取。":
    "The values templates reference. Secret components are stored but never returned.",
  新增变量: "New variable",
  "暂无变量。生成一组 REALITY 密钥或填入静态值后，模板即可引用。":
    "No variables yet. Generate a REALITY keypair or set a static value, and templates can reference it.",
  作用域: "Scope",
  取值: "Value",
  删除变量: "Delete variable",
  "生成器产出成组分量（如": "A generator produces a group of components (for example",

  "绑定后，下发时将此 inbound 装配进该节点配置。":
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
  "服务端 inbound 骨架": "Server inbound skeleton",
  "每用户 client-entry": "Per-user client entry",
  绑定节点: "Bound nodes",
  "还没有节点。": "No nodes yet.",
  "服务端与客户端引用同一变量组的不同分量，因此不会配错。":
    "Server and client reference different components of the same variable, so they cannot drift apart.",

  // variables page detail
  值: "Value",
  取值方式: "Source",
  "选择…": "Choose…",
  模板里: "Templates reference",
  "引用的值。私钥类分量只存不取。":
    " — these values. Secret components are stored but never returned.",
  "由面板调用 xray 二进制产出，格式与内核一致。":
    "Produced by running the xray binary, so the format matches the kernel.",
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
  "当前仅使用密码。添加第二因素后，密码泄露不足以登录。":
    "Password only. Add a second factor and a leaked password is no longer enough.",
  "登录时需密码，加以下任意一项。":
    "Signing in needs the password plus any one of these.",
  "暂无第二因素。": "No second factor.",
  "最近使用 {when}": "Last used {when}",
  从未使用: "Never used",
  移除: "Remove",
  "这是最后一个第二因素，移除后仅剩密码。是否继续？":
    "This is the last second factor; removing it leaves only the password. Continue?",
  "移除此第二因素？": "Remove this second factor?",
  更换邮箱: "Change email",
  "面板未配置 SMTP": "SMTP is not configured on this panel",
  "通行密钥需面板经 https（或 localhost）访问": "Passkeys need https (or localhost)",
  "通行密钥需面板经 https（或 localhost）访问，且 CHIRAL_PUBLIC_URL 指向该地址。":
    "Passkeys need the panel served over https (or localhost), with CHIRAL_PUBLIC_URL pointing at it.",
  "邮箱验证码需在 .env 中配置 CHIRAL_SMTP_HOST 与 CHIRAL_SMTP_FROM。":
    "Emailed codes need CHIRAL_SMTP_HOST and CHIRAL_SMTP_FROM set in .env.",
  "设备丢失时用于登录。每个仅可使用一次，重新生成将作废现有恢复码。":
    "Your way back in if you lose a device. Each works once; regenerating voids the existing set.",
  "剩余 {n} 个未使用": "{n} unused",
  "暂无恢复码。": "No recovery codes.",
  "重新生成将作废现有恢复码。是否继续？":
    "Regenerating voids the existing recovery codes. Continue?",
  重新生成: "Regenerate",
  生成: "Generate",
  "修改密码将使所有会话失效，包括当前会话。":
    "Changing it signs out every session, including this one.",
  修改密码: "Change password",
  "当前使用环境变量中的管理令牌登录。该令牌没有对应账号，无法配置第二因素。请改用管理员账号登录。":
    "You are signed in with the admin token from the environment. It has no account behind it, so there is nothing here to configure. Sign in with an admin account instead.",
  添加验证器应用: "Add an authenticator app",
  "使用 Authy、1Password、Google Authenticator 等应用扫码，然后填入其显示的验证码。":
    "Scan this with Authy, 1Password, Google Authenticator or similar, then enter the code it shows.",
  或手动输入密钥: "Or enter the secret by hand",
  "应用显示的 6 位验证码": "The 6-digit code from the app",
  开启: "Turn on",
  验证邮箱: "Verify email",
  "验证后，此地址可作为第二因素接收登录验证码。":
    "Once verified, this address can receive sign-in codes as a second factor.",
  邮箱地址: "Email address",
  发送: "Send",
  "邮件中的 6 位验证码": "The 6-digit code from the email",
  "请立即保存。面板仅保存其哈希，关闭后无法再次查看。":
    "Save these now. The panel keeps only their hashes; they are not shown again.",
  复制全部: "Copy all",
  // The recovery-code button, distinct from the 已保存 status label:
  // the English has to differ, so the Chinese key does too.
  我已保存: "I have saved them",
  "修改后需重新登录。": "You will need to sign in again.",
  当前密码: "Current password",
  新密码: "New password",
  "确认新密码": "Confirm new password",
  "两次输入不一致。": "The two entries do not match.",

  // config rollback
  下发历史: "Push history",
  "暂无下发记录。": "No config has been pushed.",
  当前: "current",
  节点拒绝: "rejected by the node",
  等待确认: "awaiting ack",
  回滚到此版本: "Roll back to this",
  "回滚中…": "Rolling back…",
  "回滚到版本 {v}？该内容将作为新版本重新下发。":
    "Roll back to v{v}? The content is pushed again as a new version.",
  "仅保留最近 {n} 个版本。回滚前会重新执行 xray -test：内核升级后，旧配置未必仍合法。":
    "The last {n} versions are kept. A rollback re-runs xray -test: after a kernel upgrade an old config may no longer be valid.",

  // alerts page
  告警: "Alerts",
  "节点上线 / 掉线通知的接收方。状态需稳定两分钟才播报，避免抖动。":
    "Where node up/down notifications go. A state must hold for two minutes before it is announced, so a flapping link does not flood the channel.",
  新增目标: "Add target",
  新增通知目标: "Add a notification target",
  "暂无通知目标。未配置时，节点掉线不会通知任何人。":
    "No targets yet. Until one exists, a node going down tells nobody.",
  "创建后请发送测试消息：填错的 chat id 在真正告警前与正常配置无异。":
    "Send a test afterwards: a wrong chat id looks exactly like a working one until the night it matters.",
  "发送测试": "Send a test",
  已送达: "Delivered",
  发送失败: "Failed",
  最近发送: "last sent",
  停用: "Disable",
  类型: "Kind",
  // 配置 already means the node's "Configure" action; this is a noun.
  目标配置: "Target config",
  "例如 运维群": "e.g. Ops group",
  "格式 <bot-token>:<chat-id>。bot token 自身含冒号，按最右侧冒号切分。":
    "Format <bot-token>:<chat-id>. The bot token contains a colon itself, so it is split at the rightmost one.",
  "以 POST 发送 JSON body。": "POSTs a JSON body.",

  // admins page
  管理员: "Admins",
  新增管理员: "Add admin",
  "可登录控制台的账号及其角色。": "Who can sign in to the console, and as what.",
  "暂无管理员账号。": "No admin accounts.",
  "仅超级管理员可管理管理员账号。": "Only a superadmin can manage admin accounts.",
  "该账号首次登录后，可在「安全」页自行修改密码并添加第二因素。":
    "After their first sign-in they can change the password and add a second factor from Security.",
  "初始密码（至少 8 位）": "Initial password (8 characters or more)",
  角色: "Role",
  "全部权限，含管理其他管理员": "Everything, including managing other admins",
  "可修改：节点 / 接入配置 / 变量 / 用户": "Can change nodes, profiles, variables and users",
  "仅可查看": "Read-only",
  "（当前账号）": "(current account)",
  最近登录: "last signed in",
  "最近登录 {when}": "last signed in {when}",
  从未登录: "never signed in",
  "不可修改自己的角色": "You cannot change your own role",
  删除管理员: "Delete admin",

  // audit page
  审计: "Audit",
  "变更记录，保留 180 天。": "Who changed what, and when. Kept for 180 days.",
  "按动作过滤，例如 user.delete": "Filter by action, e.g. user.delete",
  "暂无审计记录。": "Nothing recorded.",
  "无匹配记录。": "No entries match.",
  加载更多: "Load more",

  // node naming / portal handover (console side)
  重命名节点: "Rename node",
  重命名: "Rename",
  "内部名用于运维，对客名称展示给订阅者。":
    "The internal name is for operators; the customer-facing one is what subscribers see.",
  内部名: "Internal name",
  对客名称: "Customer-facing name",
  "仅在控制台显示，不会发送给订阅者。": "Console only; never sent to subscribers.",
  "例如 日本 · 东京 01": "e.g. Japan · Tokyo 01",
  "留空时门户按序号显示为「线路 01」，不会回落到内部名。":
    "Leave blank and the portal numbers the line instead. It never falls back to the internal name.",
  未设对客名称: "no customer-facing name",
  门户认领链接: "Portal claim link",
  "将此链接发送给 {name}，用于设置门户密码。":
    "Send this to {name} to set their portal password.",
  "7 天内有效，仅可使用一次，关闭后无法再次查看。若账号已存在，仅可重设密码，不可修改邮箱。":
    "Valid for 7 days, usable once, and not shown again after this window closes. If the account already exists it can set a new password but cannot change the email address.",

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
  "查看订阅、线路与用量。": "Your subscription, lines and usage.",
  "注册后请联系管理员开通线路。": "After signing up, ask the operator to enable your lines.",
  "忘记密码请联系管理员重置。": "Forgotten your password? Ask the operator to reset it.",
  "本面板暂不开放注册。": "This panel is not accepting new accounts.",
  可以登录了: "Ready to sign in",
  "若该邮箱尚未注册，账号已创建。":
    "If that address was not already registered, the account is ready.",
  去登录: "Go to sign in",
  账户: "Account",
  外观: "Appearance",
  退出登录: "Sign out",

  // claim / reset
  设置密码: "Set a password",
  "此链接对应账号 {name}。": "This link is for the account {name}.",
  "邮箱（首次设置时填写）": "Email (only when setting up)",
  设置密码并登录: "Set password and sign in",
  链接无效: "That link does not work",
  "此链接已失效或已被使用，请联系管理员重新获取。":
    "It has expired or already been used. Ask the operator for another.",

  // home
  已用流量: "Used",
  "续期时清零": "resets on renewal",
  线路: "Lines",
  "线路 {n}": "Line {n}",
  正常: "available",
  开通中: "being set up",
  暂不可用: "unavailable",
  "账户尚未开通任何线路。": "No lines have been enabled on this account.",
  "最近 24 小时": "Last 24 hours",
  "最近 24 小时没有流量": "No traffic in the last 24 hours",

  // subscription
  我的订阅链接: "Subscription link",
  "复制到客户端，将自动获取全部线路。":
    "Paste it into your client to pull every line.",
  通用: "Generic",
  分享链接: "Share link",
  显示二维码: "Show QR code",
  收起二维码: "Hide QR code",
  "二维码含订阅密钥，请勿分享截图。":
    "This code contains your subscription key. Do not share a screenshot.",
  "此订阅链接由旧版本创建，面板无法显示，请联系管理员重新生成。":
    "This link predates the panel being able to display it. Ask the operator to regenerate it.",

  // status banners
  "账户已停用，请联系管理员。": "This account has been suspended. Please contact the operator.",
  "订阅已到期，请联系管理员续期。": "Your subscription has expired. Please contact the operator.",
  "本期流量已用完。": "You have used all of this period's traffic.",
  "账户已创建，尚未开通线路，请联系管理员。":
    "The account exists but no lines are enabled yet. Please contact the operator.",

  // addresses
  "最近连接过的来源地址。": "Source addresses seen recently.",
  "最近连接的来源地址，上限 {n} 个。":
    "Recently seen source addresses, limit {n}.",
  "暂无连接记录。": "No connections recorded.",
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
