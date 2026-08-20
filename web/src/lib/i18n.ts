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
  "创建完成后将生成订阅链接，此后可随时查看。":
    "The subscription link is generated on creation and can be viewed again at any time.",
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
  "内部名称用于运维标识，对客名称向订阅者展示，连接地址写入订阅。": "The internal name identifies the node in operations, the customer-facing name is shown to subscribers, and the address is written into subscriptions.",
  客户端连接地址: "Address clients dial",
  "订阅与模板中的 {{node.address}} 用这个值。": "Subscriptions and templates resolve {{node.address}} to this.",
  "留空时使用探测到的 {ip}。该地址取自 Agent 连接的对端地址，若链路中存在 NAT 则并不可靠。": "When left empty, the detected {ip} is used. It is taken from the peer address of the agent's connection, and is unreliable when a NAT sits in between.",
  "（尚未探测到）": "(not detected yet)",
  全部走代理: "Everything via proxy",
  "用于决定哪些网站经代理访问、哪些直接连接。仅对 Clash 类客户端生效；变更后请在客户端更新订阅。":
    "Determines which sites are reached through the proxy and which are connected to directly. Applies to clash-family clients only; update the subscription in the client after making changes.",
  // navigation
  菜单: "Menu",
  收起侧栏: "Collapse sidebar",
  展开侧栏: "Expand sidebar",
  机队: "Fleet",
  订阅者: "Subscribers",
  运维: "Operations",
  流量倍率: "Traffic multiplier",
  按出口用量: "Usage by exit",
  节点直出: "Node's own exit",
  "暂无流量记录。": "No traffic has been recorded.",
  "按 ×{rate} 计入配额": "Billed at ×{rate}",
  "1 表示按实际用量计入配额。成本较高的线路可调高此值，例如 2 表示每传输 1 GB 计入 2 GB。":
    "A value of 1 charges the quota by actual usage. Raise it for a costlier line: 2 charges two gigabytes for every gigabyte transferred.",
  倍率: "rate",
  经出口: "via",
  "中继·已隐藏": "Relayed",
  "中继·并直发": "Relayed + direct",
  "中继：订阅者连接本机队节点，出口地址对其不可见":
    "Relayed: subscribers connect to a node of this fleet and do not see this address",
  重新解析: "Re-read",
  账号安全: "Account security",
  重置链接: "Replace link",
  确认重置: "Replace it",
  "重置中…": "Replacing…",
  "确认更换订阅链接？此用户已配置的所有客户端均需重新导入。":
    "Replace the subscription link? Every client this subscriber has configured must be re-imported.",
  "已生成新的订阅链接，此前的链接立即失效。客户端将自动获取对应格式。":
    "A new subscription link has been issued, and the previous one is no longer valid. Clients select the appropriate format automatically.",
  "该链接可随时查看，查看行为不会使其变更。客户端将自动获取对应格式。":
    "The link can be viewed at any time, and viewing it does not change it. Clients select the appropriate format automatically.",
  "此用户的订阅链接无法恢复（其创建时间早于可恢复存储，或密钥已更换），只能重置为新链接。":
    "This subscriber's link cannot be recovered: it predates recoverable storage, or the key has been changed. Resetting it to a new link is the only option.",
  "获取订阅链接失败：": "Could not read the subscription link: ",
  订阅顺序: "Order subscribers see",
  "订阅者看到的节点顺序，同时决定各策略组内的排列顺序；每组的首个节点为客户端的默认选中项。":
    "The order in which subscribers see the nodes, which also determines the order within each policy group. The first node in a group is what a client selects by default.",
  保存顺序: "Save order",
  撤销: "Undo",
  上移: "Move up",
  下移: "Move down",
  自有: "Fleet",
  "订阅者下次刷新订阅时生效。": "Applies on each subscriber's next refresh.",
  点击改名: "Click to rename",
  "恢复来源提供的名称": "Restore the name from the source",
  "该配置将使链路绕回自身。": "This configuration would make the chain loop back on itself.",
  "暂无订阅者。": "No subscribers have been created.",
  "取消勾选后，该节点将不再出现在此订阅者的订阅中，变更于其下次刷新时生效。":
    "Once unselected, the node no longer appears in this subscriber's subscription; the change takes effect at their next refresh.",
  "由第三方提供的订阅或单条分享链接。可为每个节点指定前置节点（本机队节点或另一个外部节点），以实现链式出站。":
    "Subscriptions or individual share links provided by a third party. Each node may be given a preceding hop — a node of this fleet, or another external node — to form an outbound chain.",
  // per-user node access
  可用节点: "Nodes they may use",
  可用用户: "Users who may use it",
  下发给订阅者: "Serve to subscribers",
  "关闭后订阅中将不再包含该线路；中转拨号与升级探活仍会使用此模板。":
    "When disabled, subscriptions no longer carry this line; relay dialling and upgrade probes continue to use the template.",
  不下发: "held back",
  // variable scope moves
  移到别的作用域: "Move to another scope",
  "移动「{name}」": "Move \u201c{name}\u201d",
  "变量取值保持不变，已下发的订阅不受影响。移至更窄的作用域后，原先依赖该变量的其他节点将在下次预览或下发时报告变量未定义。":
    "The value is preserved, and subscriptions already issued are unaffected. After a move to a narrower scope, other nodes that relied on the variable will report it undefined at their next preview or apply.",
  移到: "Move to",
  "移动中…": "Moving…",
  移动: "Move",
  "内核已安装，但尚无可运行的配置；为该节点绑定接入配置后即会启动":
    "The kernel is installed but has no configuration to run; it will start once an access configuration is bound to this node",
  // per-node egress
  出站分流: "Egress routing",
  "匹配的流量将从指定出口发出，其余流量维持原有路径。规则自上而下匹配，以首条命中的规则为准。":
    "Matched traffic leaves through the selected exit; all other traffic keeps its existing path. Rules are matched from top to bottom, and the first match applies.",
  新增规则: "Add rule",
  "暂无出站分流规则，全部流量从本节点直接出站。":
    "No egress rules have been configured; all traffic leaves directly from this node.",
  "确认删除此出站分流规则？": "Delete this egress rule?",
  "新增出站分流规则": "New egress rule",
  "编辑出站分流规则": "Edit egress rule",
  "在本节点上，匹配的流量将改由指定出口发出。域名与 IP 两栏可任填其一。":
    "On this node, matched traffic is redirected to the selected exit. Either the domain field or the IP field may be left empty.",
  "域名 / geosite 类别（每行一个）": "Domains / geosite categories (one per line)",
  "IP 段 / geoip 类别（每行一个）": "IP ranges / geoip categories (one per line)",
  从哪出去: "Leaves through",
  本机队节点: "A node of this fleet",
  目标节点: "Target node",
  "用于连接目标节点的接入配置": "Access configuration used to reach the target node",
  目标外部节点: "Target external node",
  // restricted destinations
  受限目的地: "Restricted destinations",
  "节点可达、但默认不对任何订阅者开放的网段。规则在所选节点上生效；经中转线路借道的入口节点将自动继承该规则。":
    "Networks a node can reach that are closed to every subscriber by default. Rules take effect on the selected nodes, and the entry node of any relay line landing there inherits them automatically.",
  新增目的地: "Add destination",
  "暂无受限目的地。": "No restricted destinations have been configured.",
  "确认删除此受限目的地？相关节点将移除对应拦截规则并重新下发配置。":
    "Delete this restricted destination? The affected nodes will drop the corresponding rules and receive a new configuration.",
  在哪些节点上生效: "Enforced on which nodes",
  "允许访问的用户（其余用户将被拦截）": "Subscribers permitted access (all others are blocked)",
  编辑受限目的地: "Edit restricted destination",
  新增受限目的地: "New restricted destination",
  "新建的目的地默认不对任何人开放：请先定义网段，再在列表中选择生效节点与获准用户。":
    "A new destination is closed to everyone: define the network first, then select the enforcing nodes and the permitted subscribers in the list.",
  "IP 段（每行一个 CIDR；单个 IP 视为 /32 或 /128）": "IP ranges (one CIDR per line; a bare address is treated as /32 or /128)",
  "域名后缀（每行一个）": "Domain suffixes (one per line)",
  展开配置历史: "Show config history",
  自有节点: "Own nodes",
  "此用户的接入配置未覆盖该节点，因此无法选择": "No access configuration held by this subscriber covers this node, so it cannot be selected",
  "经由「{node}」接入，而此用户无法使用该节点":
    "Reached by way of “{node}”, which this subscriber cannot use",
  "取消勾选后，该节点将不再出现在此用户的订阅中。节点上的凭证予以保留，变更于订阅者下次刷新时生效。":
    "Once unselected, the node no longer appears in this subscriber's subscription. The credential on the node is retained, and the change takes effect at their next refresh.",
  // subscriber groups: one class of subscriber, decided once
  "已对此用户单独排除；再次点击恢复由用户组决定": "Withheld from this subscriber individually; click again to let the group decide",
  "由用户组授予；点击可对此用户单独排除": "Granted by the group; click to withhold it from this subscriber",
  "用户组": "Subscriber groups",
  "为一类订阅者统一设定接入配置、可用节点与分流规则。每位订阅者至多属于一个组，其个人设置优先于组。": "Set the access configurations, available nodes and routing rules for a class of subscriber in one place. A subscriber belongs to at most one group, and their own settings take precedence over it.",
  "新增用户组": "New group",
  "编辑用户组": "Edit group",
  "暂无用户组。新增后可在用户页将订阅者加入。": "No groups have been created. Once one exists, subscribers can be added to it on the users page.",
  "新建的用户组不包含任何权限：请先创建，再为其选择接入配置与可用节点。": "A new group holds no permissions: create it first, then choose its access configurations and the nodes it may use.",
  "{n} 名成员": "{n} members",
  "名称": "Name",
  "备注（可留空）": "Note (optional)",
  "例如 标准套餐": "e.g. Standard plan",
  "确认删除用户组「{name}」？其 {n} 名成员将保留该组当前授予的全部权限，订阅不受影响。": "Delete the group “{name}”? Its {n} members keep everything it currently grants them, and no subscription changes.",
  "授权后，该组每位成员在此配置绑定的每个节点上生成独立凭证。": "Once granted, every member of the group receives a separate credential on each node this configuration is bound to.",
  "适用于未单独指定分流规则的成员。": "Applies to members who have not chosen a rule set of their own.",
  "不属于任何组": "No group",
  "修改用户组失败：": "Could not change the group: ",
  "将「{user}」加入用户组「{group}」后，其现有的接入配置授权与节点设置将被清除，改由该组决定。是否继续？": "Adding “{user}” to the group “{group}” clears their existing configuration grants and node settings; the group decides from then on. Continue?",
  "组决定成员的接入配置、可用节点与分流规则；以下各项的调整将作为该成员的个人设置，优先于组。移出组时保留组当前授予的权限。": "The group decides a member's access configurations, available nodes and routing rules. Anything changed below is recorded as this subscriber's own setting and takes precedence over the group. Leaving a group preserves what it currently grants.",
  "继承自用户组": "Inherited from the group",
  "该状态继承自用户组": "This state is inherited from the subscriber's group",
  "组": "group",
  "此处的选择适用于该组的全部成员；为个别成员单独调整后，其个人设置优先。": "These choices apply to every member of the group; where a member has been adjusted individually, their own setting takes precedence.",
  "该组的接入配置未覆盖此线路的入口节点，因此无法选择": "No access configuration granted to this group covers the line's entry node, so it cannot be selected",
  "该组的接入配置未覆盖此节点，因此无法选择": "No access configuration granted to this group covers this node, so it cannot be selected",
  "暂无接入配置。": "No access configurations have been created.",
  // relay lines: one of our nodes leaving through another
  中转线路: "Relay lines",
  "订阅者连接入口节点，流量由出口节点发出。两端均为自有节点，出口地址对订阅者不可见。":
    "Subscribers connect to the entry node, and their traffic leaves from the exit node. Both ends belong to this fleet, and the exit address is not disclosed to subscribers.",
  新增线路: "Add line",
  "暂无中转线路。": "No relay lines have been configured.",
  "确认删除此中转线路？订阅者将失去该线路，两端节点将重新下发配置。":
    "Delete this relay line? Subscribers will lose it, and both nodes will receive a new configuration.",
  新增中转线路: "New relay line",
  "入口节点使用该线路独立的凭证连接出口节点。该凭证不归属于任何订阅者，流量仅在入口节点计费一次。":
    "The entry node reaches the exit node with a credential belonging to the line itself. That credential belongs to no subscriber, and traffic is charged once, at the entry node.",
  "入口节点（订阅者的接入点）": "Entry node (where subscribers connect)",
  "出口节点（流量的发出位置）": "Exit node (where traffic leaves)",
  出口节点的接入配置: "The exit's access configuration",
  "线路名称（订阅者可见）": "Line name (visible to subscribers)",
  "例如：香港中转 · 东京出口": "e.g. HK relay · Tokyo exit",
  请选择: "Choose one",
  "此用户的接入配置未覆盖该线路的入口节点，因此无法选择":
    "No access configuration held by this subscriber covers the line's entry node, so it cannot be selected",
  "变更将立即重新下发入口节点的配置。": "Changes are applied to the entry node immediately.",
  // panel settings
  设置: "Settings",
  "面板产出内容的相关选项。": "Options governing what the panel produces.",
  订阅: "Subscription",
  订阅名称: "Subscription name",
  客户端里显示为: "Shown in the client as",
  "订阅者在客户端里看到的配置名。留空则为 chiral。":
    "The profile name subscribers see in their client. Defaults to chiral.",
  已保存: "Saved",
  "变更对此后每次订阅拉取生效；已导入的客户端需重新导入方可更新名称。":
    "Applies to every subscription fetched from now on. A client that has already imported one keeps the previous name until it re-imports.",
  // external nodes
  外部节点: "External nodes",
  "由第三方提供的订阅或单条分享链接。可为每个节点指定一台本机队节点作为前置，以实现链式出站。":
    "Subscriptions or individual share links provided by a third party. Each node may be given a node of this fleet as its preceding hop, to form an outbound chain.",
  "暂无外部节点。添加后将出现在所有订阅者的 Clash 订阅中。":
    "No external nodes have been added. Once added, they appear in every subscriber's Clash subscription.",
  新增外部节点: "Add external nodes",
  "外部节点由第三方运营：所有订阅者共用同一份凭证，不具备按用户隔离与流量统计的能力，停用某个订阅者亦无法阻止其继续使用。":
    "External nodes are operated by a third party: all subscribers share a single credential, there is no per-subscriber isolation or traffic accounting, and disabling a subscriber does not prevent them from continuing to use it.",
  直接粘贴: "Paste directly",
  "例如 机场 A": "e.g. Provider A",
  订阅地址: "Subscription address",
  "Clash YAML 或分享链接列表均可，自动识别。每天自动更新一次。":
    "Clash YAML or a list of share links, detected automatically. Refreshed once a day.",
  内容: "Content",
  "粘贴 Clash 配置片段或若干条分享链接。此类来源不会自动更新。":
    "Paste a Clash configuration fragment or a number of share links. Sources added this way are not refreshed automatically.",
  "解析中…": "Parsing…",
  添加: "Add",
  展开节点: "Show nodes",
  收起: "Collapse",
  直连: "Direct",
  经由: "via",
  "（手动粘贴，不自动更新）": "(pasted; not refreshed)",
  "未能从该来源解析出任何节点。": "No nodes could be parsed from this source.",
  "确认删除「{name}」？其提供的节点将从所有订阅中移除。":
    "Delete “{name}”? The nodes it provides will be removed from every subscription.",
  // routing rules
  分流规则: "Routing rules",
  "用于决定 Clash 类客户端将哪些流量经代理发送、哪些直接连接或拦截。可在用户页指派给订阅者。":
    "Determines which traffic clash-family clients send through the proxy, connect to directly, or reject. Assigned to subscribers on the users page.",
  "暂无规则集。新增后可在用户页指派，订阅将随之包含分流规则。":
    "No rule sets have been created. Once created, assign one on the users page and subscriptions will carry its routing rules.",
  新增分流规则: "Add routing rules",
  "内置项为 ACL4SSR 的各档预设；自定义项可指向任意 subconverter 格式的 .ini 文件。":
    "The built-in entries are ACL4SSR's presets; a custom entry may point at any subconverter-format .ini file.",
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
  "尚未获取到内容，订阅暂不包含分流规则":
    "No content has been fetched yet, so subscriptions do not carry routing rules",
  "确认删除「{name}」？使用该规则集的订阅者将不再获得分流规则。":
    "Delete “{name}”? Subscribers using it will no longer receive routing rules.",
  不分流: "No rules",
  "暂无规则集，订阅不包含分流规则。可在「分流规则」页新增。":
    "No rule sets exist, so subscriptions carry no routing rules. They can be added on the routing rules page.",
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
  订阅链接: "Subscription link",
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
