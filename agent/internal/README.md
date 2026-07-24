# agent/internal — 节点侧内部包

| 包 | 职责 |
|----|------|
| `client/` | 到 Core 的 gRPC 双向流：注册、心跳、收配置 / 指令、上报 |
| `xray/` | Xray-core 子进程生命周期、config.json 落盘 / reload / `xray -test` |
| `collector/` | gopsutil 系统指标 + Xray-core gRPC stats 流量 |

共享只通过 `proto/`。
