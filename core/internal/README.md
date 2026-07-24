# core/internal — 后端内部包

按职责拆分，遵循 Go `internal` 不对外导出规则。Core 与 Agent **不互相 import 对方 internal**，共享只通过 `proto/`。

| 包 | 职责 |
|----|------|
| `api/` | 对前端的 REST + WebSocket handler |
| `auth/` | 鉴权、RBAC、节点 join token |
| `node/` | 节点注册表、Agent 长连接管理、指标接收 |
| `template/` | 模板引擎 + 变量池渲染 |
| `user/` | 用户、clients 数组、强隔离凭证 |
| `stats/` | 流量聚合与配额判定 |
| `subscription/` | 多客户端订阅渲染 |
| `store/` | SQLite 持久化 |
