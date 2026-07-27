# agent/internal — 节点侧内部包

| 包 | 职责 |
|----|------|
| `client/` | 到 Core 的 gRPC 双向流：注册换凭证、指数退避重连、上报心跳 / 流量 / 在线，接收 `ConfigPush` / `UserOp` / `Command` / `OnlinePolicy` |
| `xray/` | Xray-core 子进程生命周期、config.json 落盘 / `xray -test` / 重启；以及经 `xray api` 子进程驱动的管理 API：在线增删用户、流量计数、在线地址 |
| `collector/` | 仅宿主机系统指标（gopsutil：CPU / 内存 / 磁盘 / 网卡吞吐） |

流量统计**不在** `collector/`：它来自 `xray/api.go` 的 `xray api statsquery -reset`。两者节奏也不同——系统指标随心跳（默认 10s），流量单独一条更慢的 ticker（默认 60s），因为每次读取都会清零内核计数器，间隔即计费粒度。

共享只通过 `proto/`。
