# proto — Core↔Agent gRPC 定义

Core 与 Agent **共享**的 protobuf 定义与生成代码。是两个组件间唯一的共享面（不互相 import 对方 `internal/`）。协议语义见 [`../docs/communication.md`](../docs/communication.md)。

## 服务

定义在 [`chiral/v1/agent.proto`](chiral/v1/agent.proto)：

- **`Register`（unary）**：一次性 join token 换每节点长期凭证，注册成功即作废该 token。
- **`Channel`（双向流）**：凭证放 metadata `x-chiral-credential`，一条流承载所有帧。
  - Agent → Core：`Hello`（首帧）、`Heartbeat`、`StatsReport`、`ConfigAck`、`Event`、`OnlineReport`
  - Core → Agent：`ConfigPush`、`UserOp`（AddUser / RemoveUser）、`Command`（重启 / 立即上报）、`OnlinePolicy`

## 两条方向相反的上报规则

`StatsReport` 报**增量**：Xray 重启会把计数器归零，报绝对值会把总量算歪。

`OnlineReport` 报**绝对快照**，是对上面那条规则的刻意豁免。两者的失效方式正好相反——观测集合变空的含义是「我们没在观测」，绝不是「零设备，放开限制」。每轮发一次完整集合，Core 才分得清这两件事；**即使没人在线也照发一个空的 `complete=true` 帧**，让沉默保持有歧义这件事不发生。`complete=false` 表示这轮没枚举完，Core 必须整轮丢弃，而不是把截断的集合读成「在用的地址就这些」。

口径也别记错：一个用户的在线数是**不同来源地址**的个数而非会话数（同一地址三条并发连接算一个），且 Xray 完全不统计 loopback 来源。

`OnlinePolicy` 默认关闭，且**故意不带要观测的用户名单**：名单发下去等于把不由该节点服务的订阅者信息交给每台租来的 VPS，正好抵消每节点独立凭证买来的隔离。Agent 只看得见自己的在线用户，过滤在 Core 侧做。轮询间隔本身就是采样误差——两轮之间开合的会话看不见，Xray 也在断开约两秒内就把地址移出集合。

## 生成（已定：buf + 生成物入库）

```bash
buf lint && buf generate
```

- 工具链 **buf**（根目录 `buf.yaml` / `buf.gen.yaml`，remote 插件，无需本地 protoc）。
- 生成的 `*.pb.go` **提交入库**：克隆即可 `go build`。改动 proto 后必须重新生成并一起提交，CI 校验两者同步。
