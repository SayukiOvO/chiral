# proto — Core↔Agent gRPC 定义

Core 与 Agent **共享**的 protobuf 定义与生成代码。是两个组件间唯一的共享面（不互相 import 对方 `internal/`）。协议语义见 [`../docs/communication.md`](../docs/communication.md)。

## 服务

定义在 [`chiral/v1/agent.proto`](chiral/v1/agent.proto)：

- **`Register`（unary）**：一次性 join token 换每节点长期凭证，注册成功即作废该 token。
- **`Channel`（双向流）**：凭证放 metadata `x-chiral-credential`，一条流承载所有帧。
  - Agent → Core：`Hello`（首帧）、`Heartbeat`、`StatsReport`、`ConfigAck`、`Event`、`OnlineReport`、`RecentOnlineReport`
  - Core → Agent：`ConfigPush`、`UserOp`（AddUser / RemoveUser）、`Command`（重启 / 立即上报）、`OnlinePolicy`

`Heartbeat.runtime` 报告节点侧运行时提供者、模式、版本、实际探测到的能力、OpenAPI 摘要、契约实际读取时间、候选后端的 Xray 状态/版本及显式健康状态（`READY / INCOMPATIBLE / UNREACHABLE`）。迁移阶段的 `SHADOW` 只做 3x-ui 契约/健康观测，所有写操作仍走旧提供者；候选 Xray 字段不能覆盖 Heartbeat 顶层的 ACTIVE Xray 状态。只有经 Core 支持的 `ACTIVE` 才表示该提供者实际接收配置、用户与生命周期指令；当前 Core 明确拒绝 3x-ui 自报 ACTIVE。旧 Agent 在**收到心跳且完全缺少此字段时**，Core 才按其历史事实显示为 `direct-xray / ACTIVE`；畸形空字段、离线或尚未注册的节点均显示 `UNKNOWN`，不能凭空推断为已激活。

## 两条方向相反的上报规则

`StatsReport` 报**增量**：Xray 重启会把计数器归零，报绝对值会把总量算歪。

`OnlineReport` 报**绝对快照**，是对上面那条规则的刻意豁免。每轮都有明确的完整性标记；能完整采集的 provider 即使没人在线也发送空的 `complete=true` 帧。`complete=false` 表示这轮未枚举完整，Core 保留实际观测到的地址并标为部分结果，不能将下界当作全量。该通道只接受当前连接观测，不能承载 3x-ui 缓存用户或已存储 IP。

`RecentOnlineReport` 单独承载 3x-ui 的近似在线信息，没有 `complete` 字段。`observed_online`
只表示出现在上游在线缓存中，`ips` 是上游记录的地址；false、空记录、读取失败均不能证明
用户已经离线。`retrieved_age_seconds` 包含 HTTP 读取和本地发送排队时间，Core 用接收时钟
估算新鲜度；无法测量其后的网络传输延迟，也不代表来源地址的连接时间。近似记录不计入旧的实时地址数，也不写入
`user_devices` 的真实观测历史。

Agent 必须先在本条流收到 `OnlinePolicy.enabled=true` 和 `recent_observations=true`
才采集／发送近似报告；旧 Core 未协商时保持静默，绝不退回 `OnlineReport`。读取失败发送
`read_ok=false` 且用户列表为空。每帧最多 4096 个用户、512 字节凭证键及合计 8192 个地址，
共享限制与序列化尺寸测试位于 `chiral/v1/recent_limits*`。

口径也别记错：一个用户的在线数是**不同来源地址**的个数而非会话数（同一地址三条并发连接算一个），且 Xray 完全不统计 loopback 来源。

`OnlinePolicy` 默认关闭，且**故意不带要观测的用户名单**：名单发下去等于把不由该节点服务的订阅者信息交给每台租来的 VPS，正好抵消每节点独立凭证买来的隔离。Agent 只看得见自己的在线用户，过滤在 Core 侧做。轮询间隔本身就是采样误差——两轮之间开合的会话看不见，Xray 也在断开约两秒内就把地址移出集合。

## 生成（已定：buf + 生成物入库）

```bash
buf lint && buf generate
```

- 工具链 **buf**（根目录 `buf.yaml` / `buf.gen.yaml`，remote 插件，无需本地 protoc）。
- 生成的 `*.pb.go` **提交入库**：克隆即可 `go build`。改动 proto 后必须重新生成并一起提交，CI 校验两者同步。
