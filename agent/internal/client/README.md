# client — 到 Core 的长连接

建立并维持 gRPC 双向流：注册换凭证、心跳保活、断线指数退避重连；接收 `ConfigPush` / `UserOp` / `Command`；上报 `Heartbeat` / `StatsReport` / `ConfigAck` / `Event`。

**状态**：待实现（M1）。
