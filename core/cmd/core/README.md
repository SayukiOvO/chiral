# cmd/core — Core 入口

`main` 包。读配置 → 打开 SQLite → 装配各 `internal` 服务 → 启动 gRPC server（对 Agent）与 HTTP server（对前端 / 订阅）。

**状态**：待实现（M1）。
