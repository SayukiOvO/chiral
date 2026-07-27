# agent — 节点守护进程（Go）

跑在每台节点主机上，主动外连 Core（gRPC 双向流），管理本机 **Xray-core** 子进程：应用下发的 config.json、在线增删用户、上报心跳 / 流量 / 在线地址。

## 布局

- `cmd/agent/` — 入口（main）
- `internal/client/` — 到 Core 的 gRPC 双向流：注册、重连、帧收发
- `internal/xray/` — Xray-core 进程与 config 管理，以及经 `xray api` 子命令驱动的管理 API（增删用户、流量、在线地址）
- `internal/collector/` — 宿主机系统指标（gopsutil）

## 构建

```bash
go build -o ../bin/chiral-agent ./cmd/agent
```

## 关键决策

- **不存历史**：节点侧只持有 `state.json`（node_id + 长期凭证）与最后一份 `config.json`。版本历史在 Core 侧，回滚 = 把旧内容当作新的 `ConfigPush` 重推。
- **进程内不 import xray-core**：管理 API 一律 fork 它所监管的那个二进制（`xray api ...`），线格式天然与运行中的内核同版本。
- **先起内核再连 Core**：启动时磁盘上若有 `config.json` 就立刻拉起 Xray，不让代理在重连期间掉线。
- **凭证被拒不重试**：Core 回 `PermissionDenied` / `Unauthenticated` 意味着节点已删或凭证已吊销，重连不会自愈，直接退出让运维（或容器重启策略）看见。
