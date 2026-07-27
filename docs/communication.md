# Core ↔ Agent 通信协议（方案 A）

## 选型：方案 A（已定）

**Agent 主动外连 Core，保持长连接（gRPC 双向流 over TLS）。**

理由：节点**只需出站网络**，不开放任何入站端口，不暴露公网，NAT / CGNAT 后的机器也能当节点；配置推送式下发、实时性最好；只需 Core 一侧有证书。

被否方案（记录备查）：
- **方案 B（Core 主动连 Agent）**：请求-响应直观、Core 无状态，但每个节点要暴露公网端口 + mTLS，NAT 后的机器当不了节点，攻击面变大。
- **方案 C（Agent 轮询）**：最简单，但实时性差，配置变更与封禁生效有延迟。

## 连接生命周期

1. **注册**：Agent 用一次性 join token 调用注册接口，换取本节点长期凭证（每节点独立），token 随即作废。
2. **建流**：Agent 用长期凭证建立到 Core 的 gRPC 双向流。
3. **保活**：周期心跳；断线后指数退避重连。
4. **在线判定**：Core 侧以心跳超时判定节点离线并告警。

## RPC 形态（已定稿，见 `proto/chiral/v1/agent.proto`）

- **`Register`（unary RPC）**：一次性 join token 换长期凭证。token 换凭证是一次性请求-响应，不放在流上。
- **`Channel`（双向流）**：长期凭证放 gRPC metadata（`x-chiral-credential`），一条流承载以下所有帧；建流后第一帧必须是 `Hello`。

## 消息类型（双向流上的帧）

**Agent → Core**
- `Hello`：建流后第一帧——节点标识、Agent 版本、Xray-core 版本、公网 IP。
- `Heartbeat`：轻量指标（CPU / 内存 / 磁盘 / 上下行网速）、存活标记。
- `StatsReport`：**增量**流量，按 inbound / outbound / user（email）维度。
- `ConfigAck`：config 版本号、`xray -test` 结果、是否已生效。
- `Event`：Xray-core 崩溃 / 重启、错误摘要等。
- `OnlineReport`（M5）：当前每个凭证的在线来源地址集合。**绝对快照，是对上面「增量」规则的明确豁免**——流量计数器因 Xray 重启归零所以必须报增量，而观测集合归零的含义相反：它意味着「没在观测」，绝不能读成「零设备」。即使没人在线也每轮发一个空的 `complete=true` 帧，让沉默保持有歧义这件事不发生。

**Core → Agent**
- `ConfigPush`：完整 config.json + 版本号（Core 已在推送前 `xray -test` 通过）。
- `UserOp`：在线 `AddUser` / `RemoveUser`（走 Xray-core HandlerService，不重启）。
- `Command`：重启 Xray-core、立即上报一次指标等。**回滚不是独立指令**：版本历史存在 Core 侧，回滚 = Core 把旧版本内容作为新的 `ConfigPush` 重推，Agent 无需理解回滚概念、不存本地历史。
- `OnlinePolicy`（M5）：在线地址轮询的开关与间隔。Agent 启动时不轮询，每次建流由 Core 重新下发——所以关掉这个功能后 Agent 会在下一次重连时停下，而不是等到进程退出。刻意**不**携带「受限用户名单」：统计 email 的形状是 `name.userID@profileID.nodeID`，把名单发给每台租来的 VPS 等于交出它并不服务的订阅者的用户 id 与 profile id。

## 安全

- 传输 TLS（Core 侧证书；建议 ACME 自动签发）。开发期 Agent 支持 `--insecure` 明文直连，生产默认强制 TLS。
- join token 一次性、短期有效。
- 长期凭证 = 每节点独立的随机 opaque token，Core 只存哈希，可单独吊销。不用 mTLS 客户端证书（复杂度不值当）。

## proto

定义在 [`../proto/chiral/v1/agent.proto`](../proto/chiral/v1/agent.proto)，Core 与 Agent 共享同一份生成代码。工具链 **buf**（remote 插件），**生成代码提交入库**——克隆即可 `go build`，无需本地装 protoc；CI 校验生成物与 proto 同步。
