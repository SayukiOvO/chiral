# 3x-ui 集成设计

## 目标状态与当前边界

完成全部功能等价验证后的**目标状态**是：Chiral 不再自行承担 Xray-core 的进程、版本与
运行时 API 适配，而把这些职责交给每台节点上的 3x-ui。**Panel、Core 与 Agent 仍然保留**：
Agent 继续主动连接 Core，并在本机调用 3x-ui API。这样不要求节点开放管理端口，也不改变
现有的 NAT / CGNAT 连接模型。

当前第一阶段尚未切换执行权：`direct-xray` 仍是唯一接收写入并服务订阅者的 ACTIVE provider，
`3x-ui-shadow` 只在旁路读取状态和契约。两者可以同时运行，但一次只能有一个 ACTIVE owner；
本阶段 Agent 不向 3x-ui 发送任何配置、用户、流量或生命周期写操作。

`SHADOW` 的只读边界不等于部署过程零中断。为现有 Agent 添加环境变量或 token 挂载会重建
容器；Agent 退出时会停止其管理的 Xray 子进程，已有连接随之中断。要求保留现有连接和数据时，
应使用 [`deploy/3x-ui-lab`](../deploy/3x-ui-lab/README.md) 的独立实验项目，不修改生产 Agent。
实验项目包含官方 3x-ui、全新 Core 和 Agent，采用无外部网络的共享回环命名空间与专属数据卷，
不接入生产控制面，不读取生产凭证。实验验证仅能证明实际覆盖的契约与上报行为，不能代替下述
生产接管门槛。

3x-ui 是节点侧的执行后端，不是第二个控制面。日常管理仍在 Chiral 完成；不把用户跳转到
3x-ui，也不把 3x-ui 页面嵌入 Chiral。破窗排障只能通过 SSH 端口转发访问只监听本机的
3x-ui，手工修改产生的漂移必须显式采纳或由下一轮调和覆盖。

迁移期保留 Agent 的原生 Xray 后端，新增 `3x-ui` 候选 provider。最终两种 provider 共用
Core ↔ Agent 协议所表达的配置版本、流量、在线快照、事件和确认语义；原生 provider 是
节点级回滚路径，不在首个集成版本中删除。

## 上游约束

3x-ui 采用 [GPL-3.0](https://github.com/MHSanaei/3x-ui/blob/main/LICENSE)。Chiral 只通过
HTTP API 调用未修改的独立进程或容器，不复制、链接或复刻 3x-ui 内部实现。
2026-09-29，用户已撤回独立增强分支授权：不得修改 3x-ui、维护增强 fork，或通过新增兼容
服务绕过此限制。3x-ui provider 也不得直接读取 Xray API 或管理本地内核缓存。官方 API 尚
无法满足的等价门槛必须如实保留为未完成项，不以降低验收标准或重新请求同一增强方案替代。

上游 [README](https://github.com/MHSanaei/3x-ui#disclaimer) 明示项目面向个人使用且不建议
生产部署，[安全策略](https://github.com/MHSanaei/3x-ui/security/policy) 只为最新版本提供安全
更新。采用它可以降低 Chiral 对 Xray 适配的维护量，但不能把上游声明转化成生产保证。因此
Chiral 必须保留契约测试、金丝雀、可逆 provider 和自己的真实数据路径判据，并在安全更新与
兼容性之间逐次做受控升级。

## 职责与数据主权

| 数据或能力 | 权威来源 | 3x-ui 中的角色 |
|---|---|---|
| 管理员、订阅者登录与 RBAC | Chiral Core | 不同步 |
| 用户、组、节点授权与受限目的地 | Chiral Core | 只接收已裁决的运行时结果 |
| Profile、变量、凭证与中转线路 | Chiral Core | 作为受 Chiral 管理的 inbound、client、outbound/routing 投影 |
| 订阅 token 与客户端模板 | Chiral Core | 禁用 3x-ui 订阅服务，不生成第二套订阅 |
| 配置版本、审计与回滚历史 | Chiral Core | 保存当前运行投影及迁移前快照 |
| Xray 安装、启动、停止与热应用 | 节点 3x-ui | 权威执行者 |
| 节点运行指标 | Chiral Agent | 3x-ui 仅提供 Xray 相关观测 |
| 用户流量与在线地址 | Chiral Core 汇总 | 3x-ui 提供原始计数器和在线集合 |

3x-ui v3 的 client 是可关联多个 inbound 的全局对象，而 Chiral 的安全边界是
`用户 × Profile × 节点`（必要时再含出口）各用一份独立凭证。因此不能把一个 Chiral 用户
映射成一个共享的 3x-ui client。每条 Chiral credential 映射成一个仅属于一个受管 inbound
的 3x-ui client，以现有唯一统计 email 作为外部键；Core 再把这些运行时记录聚合成人类
用户的用量和状态。

受管对象必须带稳定的 Chiral ownership 标识。3x-ui 的数字 ID 只作缓存，不作身份；数据库
恢复后应按 ownership 标识重新发现。Agent 只能更新或删除带有本节点 Chiral 标识的对象，
不得清理无法证明归属的配置。

## API、安全与能力握手

3x-ui API 路径没有版本前缀，OpenAPI 元数据也只标识为 `3.x`，因此不能把“HTTP 200”视为
兼容保证。实现以 [官方 API 文档](https://github.com/MHSanaei/3x-ui/wiki/Configuration#api-documentation)
和目标实例实时提供的 OpenAPI 为准，社区 Postman collection 只作辅助参考。每个 Agent 在
接管运行时前必须完成能力握手：

1. 读取 3x-ui 版本和 OpenAPI 文档，记录其摘要。
2. 确认所需路径、方法、认证方式以及关键请求和响应字段均存在。
3. 在隔离对象上执行无损语义探测，包括创建、读取、更新、删除和 Xray 配置读取。
4. 核对运行中 Xray 版本，并用该节点实际运行的内核验证渲染结果。
5. 未通过握手时保持原 provider，不发送成功确认，也不尝试“尽力兼容”。

第一阶段的只读 `SHADOW` 为避免碰触生产状态，只完成第 1 步和第 2 步中的**精确路径/方法
形状检查**，并额外读取一次最终 config 来验证 token 跨过最低只读权限边界；它不验证全部
请求/响应 schema、写路由权限或写入语义，也**不会**调用增删改接口。控制台的`契约就绪`
只表示实时状态、所需路由形状和这次安全读取均成功，不表示写入语义、计费或数据面已经等价，
更不允许据此进入 `ACTIVE`。认证/schema 深检及第 3、4 步只能在后续隔离对象和金丝雀阶段完成。
实时 OpenAPI 每五分钟重新读取；体积更大的最终 config 只在 Agent 启动或 3x-ui 版本变化时
重读，避免把只读观测本身变成节点的持续负载。

进入 ACTIVE 前，CI 必须针对每个受支持的 3x-ui 版本启动官方镜像并运行完整契约测试；该
官方镜像测试矩阵**尚未在第一阶段实现**。升级先经过契约测试与单节点
金丝雀；发现未知 OpenAPI 摘要或缺失能力时拒绝接管。Chiral 不在源码中固定 Xray 版本，
但每次实际安装必须记录解析后的 Xray 版本、3x-ui 版本与镜像 digest，确保该次动作可审计、
可复现、可回滚。

优先使用最小权限 API token。当前 3x-ui 的 `node-sync` scope 不覆盖实时 OpenAPI、完整 Xray
模板、最终 config 读取和 Xray 安装等能力；在上游提供更窄的 Chiral scope 前，可使用仅存于节点的
admin token，但必须同时满足：

- 3x-ui 管理监听只绑定 `127.0.0.1`，不得由 Core 或公网直接访问；
- token 通过显式挂载的普通文件提供，不得授予 group/other 权限（通常用 `0400` 或 `0600`），
  不必位于 Agent 状态目录；
  token 不上报 Core、不写日志、不进入错误正文；
- Agent 使用显式端点白名单和请求体校验，绝不提供通用 API 转发；
- bootstrap 使用随机管理员凭证和随机 base path，接管成功后轮换一次 token；
- 禁用 3x-ui 自带订阅服务和节点间同步，避免第二条管理链路；
- 不需要 IP 封禁时禁用 fail2ban，并移除 `NET_ADMIN` / `NET_RAW` 能力。

Agent 与 3x-ui 必须共享可访问的回环通道。当前隔离验证使用 3x-ui 的 `network_mode: none`，
实验 Core 与 Agent 使用 `network_mode: service:3x-ui`，不发布宿主机端口，也不使用生产网络。
未来接管节点动态 inbound 时可以评估 host network，但必须先完成端口归属核对与迁移评审，
不能将其用于要求不中断生产连接的并行实验。3x-ui 数据库、证书以及 Xray / geofile 目录均应
使用独立持久化卷；容器重建前记录镜像 digest 和实际内核版本，不能依赖浮动的 `latest` 恢复现场。

## 调和与功能等价门槛

一个完整配置会拆成 3x-ui 的基础 Xray 模板、多个 inbound 和多个 client API 操作，这些调用
不是数据库事务。Agent 必须按节点串行执行声明式调和，并携带 Chiral 期望版本和规范化摘要：

1. 读取当前受管对象并保存 3x-ui 数据库和最终 Xray config 快照。
2. 计算计划，只操作有 ownership 标识的对象；先创建依赖，再切换引用，最后删除旧对象。
3. 读取 3x-ui 生成的最终 config，进行规范化对比，并用实际内核执行 `xray -test`。
4. 用与订阅相同模板和真实凭证执行端到端数据探测；进程存活或 API 可响应不等于可用。
5. 全部通过后才发送 `ConfigAck(applied=true)`；中途失败执行补偿恢复，并上报明确失败阶段。

迁移不得降低以下能力，全部通过才可把节点标为等价：

- 任意骨架、inbound、outbound 与 routing 的表达力，包括 xhttp 上下行分离、后量子密钥、
  REALITY、中转线路、外部出口和受限目的地规则；
- 每个 `用户 × Profile × 节点 × 出口` 的独立凭证、启停、到期、配额与自动续期；
- xray JSON、v2rayN、mihomo 和 Stash 的所有订阅输出逐字节不变；
- 节点、用户、inbound、outbound 和出口维度的流量统计与倍率计费只计算一次；
- 在线地址继续按绝对快照上报，即使无人在线也发送 `complete=true` 的空集合；
- 配置版本、审计、告警、金丝雀升级与真实数据路径探测保留原有语义。

3x-ui 的流量值按累计计数读取，Agent 不调用会改变配额语义的 reset API。只读取本节点的
`inbounds/list` 中 `clientStats`，不使用可能叠加主面板汇总的用户列表流量；同一统计对象
去重，inbound / outbound 仅作独立遥测，不再次计入用户配额。`total` 为配额，不是流量。

Agent 为每个外部键持久化 watermark 与 runtime epoch。普通轮次取累计差；任一方向下降时
视作新计数周期，两方向均从当前值起算。已知数据库恢复或实例身份变化时重新建立基线，明确
上报连续性不确定，不能把历史累计值直接再计费。仅凭累计计数无法识别所有数据库回退，也
无法恢复上游在两次采样之间清除的字节；可靠投递不能被描述为解决了这些采样缺口。

每次采样的全部 delta 与新 watermark 原子落盘，并按协议限制分批。Agent 在 Core 明确发送
`StatsPolicy` 后才投递带 `reporter_id` / `sequence` 的批次；每批收到精确 `StatsAck` 前内容
不变、重试身份不变。Core 将凭证计数、用户倍率用量、图表、inbound / outbound 遥测和去重
收据放在同一事务中，提交后才确认。ACK 丢失时重放不重复计费；超过边界或跨节点凭证整批
拒绝。旧格式保留兼容，但不能绕过所属节点检查。共享上限为每批 4096 条、名称 512 字节，
确保最大消息仍小于默认 gRPC 接收限制。旧 Core 未协商时不读取新 provider 的待投递账本。

新旧计费路径均按每条凭证增量、十进制倍率乘积向上取整一次。倍率沿用现有 SQLite REAL /
JSON number 存储，以该值可往返还原的最短十进制表示参与精确有理数运算；不使用浮点乘法
或 epsilon 修补。这样 `100 × 1.1` 精确计为 `110` 字节，1 倍倍率也不会丢失超过 `2^53`
的整数精度。计费和原始累计计数溢出时事务失败，不允许 SQLite 将整数累计提升成浮点值。

连续性不确定不是一条可丢弃的日志：它作为不含计费 entries 的独立批次参与摘要和确认，
Core 将固定原因与批次身份写入 `stats_continuity_gaps`，与回执同事务提交。后续普通批次推进
回执水位也不会删除历史缺口；事件队列满、ACK 后断线不能让缺口证据消失。

迁移 `0030` 尚未发布，开发阶段曾应用较早内容的临时数据库应重新创建，不能复用迁移号
来推断 schema 已完整。旧实验账本中已经持久化且超限的 pending 批次也不会被静默改写：
读取明确失败并保留原文件，避免以相同 delivery ID 发送不同内容。这些限制不涉及已部署生产库。

切换前必须让原 provider 完成最后一次增量上报，再以 3x-ui 当前值建立基线，并验证用户
删除、计数重置和实例恢复的连续性。当前接口与投递基础设施尚未接入 ACTIVE 3x-ui provider，
不能据此宣称完整生产计费已经验证。

2026-09-29 的 [本机官方 HTTP 写入与数据面验证](../deploy/3x-ui-lab/validation-write-20260929.md)
已确认基础模板读写及后续请求计费可用，但冷启动首次真实请求未计入 HTTP 用户计数，受管
client 的合法 `level` 字段也未保留。整体契约测试为 FAIL，尚不具备生产接管条件；不得用
预热后的计费通过掩盖首次采样损失，也不得将单一受管 client 路径失败夸大为所有官方 HTTP
配置途径均不可用。

### 原版 3.8.5 的剩余能力差异

除上述实测外，已只读核对官方提交
[`7ef22f9`](https://github.com/MHSanaei/3x-ui/tree/7ef22f94c950ff09f0870e2295fa65ad5968742c)。
以下是该版本的源码证据，不是所有未来版本的永久结论；本轮未执行现场升级或数据库恢复。

| 原有要求 | 官方现有能力 | 尚不能证明的等价语义 |
| --- | --- | --- |
| 当前在线地址的完整绝对快照 | 在线 email 缓存、历史 IP 查询 | 在线缓存有约 20 秒宽限；IP 记录保留约 30 分钟，不能据此判断某个地址当前仍在线；关闭 fail2ban 时 IP 任务在持久化前返回 |
| 仅预安装、不激活 | `installXray/:version` 指定版本安装 | 停止现有进程后才下载并覆盖二进制，没有独立预安装或激活接口 |
| 下载失败保持服务、离线回退旧内核 | 重启及重新安装旧版本 | 下载失败路径不自动重启旧内核；重装旧版本仍需联网，没有 HTTP 可用的旧二进制保留槽位 |
| 实际节点内核完整配置预校验 | 模板 JSON 校验、最终配置组装、配置应用 | 未发现完整配置的 HTTP dry-run；`getConfigJson` 不是运行进程已接受配置的证明 |
| 真实代理路径探测 | `testOutbound` 的 real 模式 | 可以发送真实 HTTP，但应单独核对 HTTP 状态；不能选择预安装二进制，也不返回被测二进制摘要 |

在线证据见官方
[`process.go`](https://github.com/MHSanaei/3x-ui/blob/7ef22f94c950ff09f0870e2295fa65ad5968742c/internal/xray/process.go#L463)、
[`inbound_node_ips.go`](https://github.com/MHSanaei/3x-ui/blob/7ef22f94c950ff09f0870e2295fa65ad5968742c/internal/web/service/inbound_node_ips.go#L21)
和 [`check_client_ip_job.go`](https://github.com/MHSanaei/3x-ui/blob/7ef22f94c950ff09f0870e2295fa65ad5968742c/internal/web/job/check_client_ip_job.go#L57)。
安装证据见官方
[`server.go`](https://github.com/MHSanaei/3x-ui/blob/7ef22f94c950ff09f0870e2295fa65ad5968742c/internal/web/service/server.go#L1027)，
真实探测见
[`probe_http.go`](https://github.com/MHSanaei/3x-ui/blob/7ef22f94c950ff09f0870e2295fa65ad5968742c/internal/web/service/outbound/probe_http.go#L280)。

这些差异不能由 Chiral 的 HTTP 重试、累计计数账本或数据库备份自动补齐。节点级迁移回退到
direct-Xray 仍是另一条保留路径，不等同于 3x-ui provider 自身完成离线内核回滚。继续保持
ACTIVE 关闭；在当前授权边界下，不能声称已经具备完整目标状态。

## 分阶段迁移

### 1. 基础设施

- 引入 provider 接口，原生 provider 仍为默认值。
- 实现窄类型的 3x-ui client、能力握手、契约测试和敏感信息脱敏。
- 以增量协议字段上报 provider、3x-ui 版本、OpenAPI 摘要、Xray 已安装/运行版本和兼容状态；
  旧 Core 与旧 Agent 必须仍可互通。

### 2. 影子验证

- 使用独立 Core、Agent 和 3x-ui 进行隔离验证，入口见
  [`deploy/3x-ui-lab/README.md`](../deploy/3x-ui-lab/README.md)。实验使用新数据卷和凭证，
  不重建生产容器、不复用生产节点身份，也不接管生产 inbound。
- 将同一 Chiral 期望状态投影到 3x-ui，读取最终 config，与原生 provider 的配置做语义对比。
- 用真实配置语料覆盖全部传输、密钥、线路和路由组合，并比较所有用户、所有客户端格式的
  订阅输出。

### 3. 单节点金丝雀

- 用 SQLite `.backup` 备份 Chiral 数据库；停止写入后备份并验证恢复 3x-ui 数据库。
- 记录原 provider、配置版本与摘要、Xray 版本/二进制、3x-ui 镜像 digest、token id、端口和
  防火墙状态。
- 刷新原 provider 的最后一批流量，建立 3x-ui watermark 基线。
- **先停止原生 Xray，再启动 3x-ui 管理的 Xray**，避免同端口双实例；保持原凭证和端口。
- 要求配置读取、内核测试、真实订阅者数据路径、Core 健康检查和 Agent ack 都给出正向成功
  信号，并再次逐字节比较全部订阅输出。

### 4. 扩大与收尾

逐节点推进，每一批都保留观测窗口。原生 provider 和旧状态至少保留一个完整发布与回滚周期；
删除原生实现、改变默认 provider 或开放 3x-ui 管理入口都必须另行评审，不能夹带在首个集成
变更中。

## 回滚

任何能力握手失败、配置差异、计费异常、真实数据路径失败或 Agent 未确认都触发节点级回滚：

1. 阻止新的调和操作，保存 3x-ui 数据库、最终 config 和日志供诊断。
2. 停止 3x-ui 管理的 Xray，确认监听端口已经释放。
3. 把节点 provider 切回原生，恢复记录的旧 config、Xray 二进制和 Agent 状态。
4. 启动原生 Xray，重推旧配置内容作为一个新的 Chiral 配置版本。
5. 用真实凭证完成数据路径探测，确认 Agent ack、流量连续性和全部订阅输出。

绝不允许两个 provider 同时管理同一组端口，也不以恢复 3x-ui 数字 ID 作为回滚成功条件。
回滚的完成标准是客户流量恢复、Core 收到明确确认且计费与订阅保持连续。
