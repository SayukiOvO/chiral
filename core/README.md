# core — Panel 后端（Go）

Panel 的后端二进制，承载全部 API 与业务逻辑，是唯一持久化真相来源（SQLite）。术语上 "core" 专指 Panel 后端，不是 Xray-core。

## 布局

- `cmd/core/` — 入口（main）
- `internal/` — 职责包：`api` `auth` `node` `template` `user` `stats` `subscription` `store`
- `migrations/` — SQLite 迁移

## 构建

```bash
go build -o ../bin/chiral-core ./cmd/core
```

**状态**：待实现（M1 起）。详见 [`../docs/architecture.md`](../docs/architecture.md)。
