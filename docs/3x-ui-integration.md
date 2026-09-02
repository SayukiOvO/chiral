# 3x-ui 集成设计

## 目标状态与当前边界

完成全部功能等价验证后的**目标状态**是：Chiral 不再自行承担 Xray-core 的进程、版本与
运行时 API 适配，而把这些职责交给每台节点上的 3x-ui。**Panel、Core 与 Agent 仍然保留**：
Agent 继续主动连接 Core，并在本机调用 3x-ui API。这样不要求节点开放管理端口，也不改变
现有的 NAT / CGNAT 连接模型。

当前第一阶段尚未切换执行权：`direct-xray` 仍是唯一接收写入并服务订阅者的 ACTIVE provider，
`3x-ui-shadow` 只在旁路读取状态和契约。两者可以同时运行，但一次只能有一个 ACTIVE owner；
本阶段没有任何配置、用户、流量或生命周期操作发给 3x-ui。

3x-ui 是节点侧的执行后端，不是第二个控制面。日常管理仍在 Chiral 完成；不把用户跳转到
3x-ui，也不把 3x-ui 页面嵌入 Chiral。破窗排障只能通过 SSH 端口转发访问只监听本机的
3x-ui，手工修改产生的漂移必须显式采纳或由下一轮调和覆盖。

迁移期保留 Agent 的原生 Xray 后端，新增 `3x-ui` 候选 provider。最终两种 provider 共用
Core ↔ Agent 协议所表达的配置版本、流量、在线快照、事件和确认语义；原生 provider 是
节点级回滚路径，不在首个集成版本中删除。

## 上游约束

3x-ui 采用 [GPL-3.0](https://github.com/MHSanaei/3x-ui/blob/main/LICENSE)。Chiral 只通过
HTTP API 调用未修改的独立进程或容器，不复制、链接或复刻 3x-ui 内部实现。若将来必须维护
fork，应把它保留为独立的 GPLv3 仓库和镜像，并履行对应源码提供义务；这是一条工程合规
边界，不替代法律意见。

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

节点采用 Docker 时，动态 inbound 端口使 `network_mode: host` 比逐项发布端口可靠。Agent 与
3x-ui 必须共享可访问的本机回环通道。3x-ui 数据库、证书以及 Xray / geofile 目录都要持久化；
容器重建前记录镜像 digest 和实际内核版本，不能依赖浮动的 `latest` 恢复现场。

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

3x-ui 的流量值按累计计数读取，Agent 不调用会改变配额语义的 reset API。Agent 为每个外部键
持久化 watermark 与 runtime epoch，每轮上报 `max(current - previous, 0)`；计数下降、数据库
恢复或运行时更换时开启新 epoch，不产生负数。切换前先让原 provider 完成最后一次增量上报，
再以 3x-ui 当前值建立基线，避免重算或漏算。

## 分阶段迁移

### 1. 基础设施

- 引入 provider 接口，原生 provider 仍为默认值。
- 实现窄类型的 3x-ui client、能力握手、契约测试和敏感信息脱敏。
- 以增量协议字段上报 provider、3x-ui 版本、OpenAPI 摘要、Xray 已安装/运行版本和兼容状态；
  旧 Core 与旧 Agent 必须仍可互通。

### 2. 影子验证

- 在测试环境或独立端口启动 3x-ui，不接管生产 inbound。
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
