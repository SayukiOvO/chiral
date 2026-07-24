# agent — 节点守护进程（Go）

跑在每台节点主机上，主动连回 Core，管理本机 **Xray-core**。

## 布局

- `cmd/agent/` — 入口（main）
- `internal/client/` — 到 Core 的 gRPC 双向流
- `internal/xray/` — Xray-core 进程管理 + config 应用
- `internal/collector/` — 系统与流量指标采集

## 构建

```bash
go build -o ../bin/chiral-agent ./cmd/agent
```

**状态**：待实现（M1 起）。
