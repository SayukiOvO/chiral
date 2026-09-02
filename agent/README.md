# agent — 节点守护进程（Go）

跑在每台节点主机上，主动外连 Core（gRPC 双向流），通过运行时 provider 管理本机的
Xray 执行后端：应用下发的 config.json、在线增删用户、上报心跳 / 流量 / 在线地址。
`direct-xray` 仍是默认且完整的 provider；第一阶段的 `3x-ui-shadow` 只读观测节点本地
3x-ui 的状态与 API 能力，不接管任何写操作。

## 布局

- `cmd/agent/` — 入口（main）
- `internal/client/` — 到 Core 的 gRPC 双向流：注册、重连、帧收发
- `internal/runtimeprovider/` — Core ↔ Agent 运行时语义与 provider 接口
- `internal/xray/` — Xray-core 进程与 config 管理，以及经 `xray api` 子命令驱动的管理 API（增删用户、流量、在线地址）
- `internal/threexui/` — 节点本地 3x-ui 的窄类型只读 API client、状态/最终 config 读取与 OpenAPI 能力发现
- `internal/collector/` — 宿主机系统指标（gopsutil）

## 构建

```bash
go build -o ../bin/chiral-agent ./cmd/agent
```

## 关键决策

- **不存历史**：节点侧只持有 `state.json`（node_id + 长期凭证）与最后一份 `config.json`。版本历史在 Core 侧，回滚 = 把旧内容当作新的 `ConfigPush` 重推。
- **进程内不 import xray-core**：`direct-xray` 的管理 API 一律 fork 它所监管的那个二进制（`xray api ...`），线格式天然与运行中的内核同版本。
- **先起内核再连 Core**：使用 `direct-xray`（包括第一阶段 `3x-ui-shadow` 的写入路径）时，启动时磁盘上若有 `config.json` 就立刻拉起 Xray，不让代理在重连期间掉线。
- **凭证被拒不重试**：Core 回 `PermissionDenied` / `Unauthenticated` 意味着节点已删或凭证已吊销，重连不会自愈，直接退出让运维（或容器重启策略）看见。

## 3x-ui 第一阶段：只读 SHADOW

`CHIRAL_RUNTIME_PROVIDER=3x-ui-shadow` 只增加对 3x-ui 的只读观察：Agent 读取运行状态、版本、
最终 config 权限和实时 OpenAPI 能力，并把兼容状态报告给 Core。`ConfigPush`、`UserOp`、重启、流量与在线快照、
内核安装及回滚等现有路径仍全部由 `direct-xray` 执行。该模式不能视为迁移完成，也不得设为
默认值或用于宣称 3x-ui 已接管生产流量。

进入 `ACTIVE` 前，必须实现 3x-ui 写入调和，并通过
[`docs/3x-ui-integration.md`](../docs/3x-ui-integration.md) 规定的全部功能等价门槛，包括最终配置
回读、在线绝对快照、增量计费、真实数据面探活、安装回滚和所有订阅输出逐字节不变。任何一项
未通过，都应继续使用 `direct-xray`。
