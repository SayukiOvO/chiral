# core — Panel 后端（Go）

Panel 的后端二进制，承载全部 API 与业务逻辑，是唯一持久化真相来源（SQLite）。术语上 "core" 专指 Panel 后端，不是 Xray-core。

一个进程同时开两个监听：gRPC（对 Agent，默认 `:8443`）与 HTTP（对前端 / 订阅 / 门户，默认 `:8080`）。

## 布局

- `cmd/core/` — 入口（main）：装配、启动、60 秒清扫、优雅退出
- `internal/` — 职责包：`alert` `api` `auth` `mail` `node` `online` `passkey` `portal` `profile` `secret` `store` `subscription` `template` `user`
- `migrations/` — SQLite 迁移，编译进二进制（`embed`），启动时按文件名顺序自动应用

## 构建

```bash
go build -o ../bin/chiral-core ./cmd/core
```

## 关键决策

- **管理台与端用户门户是两类主体，边界靠构造而非小心**：门户 handler 拿到的是作用域化的 `portal.View`，拿不到 `*store.Store`；两边的 handler 签名不通用，所以接错守卫是编译错误。详见 [`internal/portal`](internal/portal)。
- **前端静态文件由 Core 可选伺服**（`CHIRAL_WEB_DIR`）：不配就一个文件都不伺服，开发期走 vite、生产也可以交给 nginx / Caddy。
- **面板自带一份 Xray 二进制**：下发前 `xray -test` 校验，以及 Go 实现不了的密钥派生。没有它则降级（跳过面板侧校验）并告警。

详见 [`../docs/architecture.md`](../docs/architecture.md)。
